package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/docker"
	gitpkg "github.com/joegoldin/claude-container/internal/git"
	"github.com/joegoldin/claude-container/internal/tmux"
)

// sessionInfo pairs a persisted session with its live status.
type sessionInfo struct {
	session *config.Session
	status  string // "running", "stopped", "exited"
}

// DashboardModel is the Bubble Tea model for the TUI dashboard.
type DashboardModel struct {
	store    *config.Store
	sessions []sessionInfo
	cursor   int
	width    int
	height   int
	preview  viewport.Model
	showDiff bool
	quitting bool
	attached string
	creating bool
}

// Internal message types.
type refreshMsg struct {
	sessions []sessionInfo
}

type previewMsg struct {
	content string
}

type tickMsg time.Time

// NewDashboard creates a new DashboardModel backed by the given store.
func NewDashboard(store *config.Store) DashboardModel {
	vp := viewport.New(0, 0)
	return DashboardModel{
		store:   store,
		preview: vp,
	}
}

// Init starts the initial refresh and tick timer.
func (m DashboardModel) Init() tea.Cmd {
	return tea.Batch(m.refreshSessions(), m.tick())
}

// Attached returns the session name the user chose to attach to, or "".
func (m DashboardModel) Attached() string {
	return m.attached
}

// Creating returns true if the user pressed 'n' to create a new session.
func (m DashboardModel) Creating() bool {
	return m.creating
}

// tick returns a command that sends a tickMsg after 500ms.
func (m DashboardModel) tick() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// refreshSessions loads sessions from the store and checks live status.
func (m DashboardModel) refreshSessions() tea.Cmd {
	return func() tea.Msg {
		list := m.store.List()
		infos := make([]sessionInfo, 0, len(list))
		for _, sess := range list {
			status := "stopped"
			if tmux.Exists(sess.Name) {
				if docker.IsRunning(sess.Name) {
					status = "running"
				} else {
					status = "exited"
				}
			}
			infos = append(infos, sessionInfo{session: sess, status: status})
		}
		return refreshMsg{sessions: infos}
	}
}

// fetchPreview returns a command that fetches preview content for the
// currently selected session.
func (m DashboardModel) fetchPreview() tea.Cmd {
	if len(m.sessions) == 0 {
		return nil
	}
	idx := m.cursor
	if idx >= len(m.sessions) {
		idx = len(m.sessions) - 1
	}
	si := m.sessions[idx]

	return func() tea.Msg {
		if si.status == "stopped" {
			return previewMsg{content: "(session stopped)"}
		}

		if m.showDiff {
			// Show git diff + status for the worktree.
			var sb strings.Builder
			st, err := gitpkg.Status(si.session.WorktreePath)
			if err == nil && st != "" {
				sb.WriteString("git status --short:\n")
				sb.WriteString(st)
				sb.WriteString("\n\n")
			}
			diff, err := gitpkg.Diff(si.session.WorktreePath)
			if err == nil && diff != "" {
				sb.WriteString("git diff HEAD:\n")
				sb.WriteString(diff)
			}
			content := sb.String()
			if content == "" {
				content = "(no changes)"
			}
			return previewMsg{content: content}
		}

		// Live tmux pane capture.
		output, err := tmux.CapturePane(si.session.Name)
		if err != nil {
			return previewMsg{content: fmt.Sprintf("(capture error: %v)", err)}
		}
		if strings.TrimSpace(output) == "" {
			return previewMsg{content: "(empty pane)"}
		}
		return previewMsg{content: output}
	}
}

// Update processes messages and returns the updated model and any commands.
func (m DashboardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.quitting = true
			return m, tea.Quit

		case "j", "down":
			if m.cursor < len(m.sessions)-1 {
				m.cursor++
			}
			return m, m.fetchPreview()

		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
			return m, m.fetchPreview()

		case "enter":
			if len(m.sessions) > 0 {
				idx := m.cursor
				if idx >= len(m.sessions) {
					idx = len(m.sessions) - 1
				}
				m.attached = m.sessions[idx].session.Name
				m.quitting = true
				return m, tea.Quit
			}

		case "n":
			m.creating = true
			m.quitting = true
			return m, tea.Quit

		case "d":
			if len(m.sessions) > 0 {
				idx := m.cursor
				if idx >= len(m.sessions) {
					idx = len(m.sessions) - 1
				}
				si := m.sessions[idx]
				_ = docker.Stop(si.session.Name)
				_ = tmux.Kill(si.session.Name)
			}
			return m, m.refreshSessions()

		case "x":
			if len(m.sessions) > 0 {
				idx := m.cursor
				if idx >= len(m.sessions) {
					idx = len(m.sessions) - 1
				}
				si := m.sessions[idx]
				_ = docker.Remove(si.session.Name)
				_ = tmux.Kill(si.session.Name)
				if si.session.Branch != "" && si.session.RepoPath != "" {
					_ = gitpkg.RemoveWorktree(si.session.RepoPath, si.session.WorktreePath, si.session.Branch)
				}
				_ = m.store.Delete(si.session.Name)
				// Adjust cursor if it would go out of bounds.
				if m.cursor > 0 && m.cursor >= len(m.sessions)-1 {
					m.cursor--
				}
			}
			return m, m.refreshSessions()

		case "tab":
			m.showDiff = !m.showDiff
			return m, m.fetchPreview()
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// Right panel gets 2/3 of width minus border overhead.
		previewW := m.width*2/3 - 4
		if previewW < 10 {
			previewW = 10
		}
		previewH := m.height - 6 // leave room for title, help bar, borders
		if previewH < 3 {
			previewH = 3
		}
		m.preview.Width = previewW
		m.preview.Height = previewH
		return m, m.fetchPreview()

	case refreshMsg:
		m.sessions = msg.sessions
		if m.cursor >= len(m.sessions) && len(m.sessions) > 0 {
			m.cursor = len(m.sessions) - 1
		}
		return m, m.fetchPreview()

	case previewMsg:
		m.preview.SetContent(msg.content)
		m.preview.GotoTop()
		return m, nil

	case tickMsg:
		return m, tea.Batch(m.refreshSessions(), m.tick())
	}

	// Pass unhandled messages to the viewport.
	var cmd tea.Cmd
	m.preview, cmd = m.preview.Update(msg)
	return m, cmd
}

// View renders the dashboard.
func (m DashboardModel) View() string {
	if m.quitting {
		return ""
	}

	// Calculate panel widths.
	leftW := m.width / 3
	if leftW < 20 {
		leftW = 20
	}
	rightW := m.width - leftW
	if rightW < 20 {
		rightW = 20
	}

	// Available height for content (subtract title line + help bar + borders).
	contentH := m.height - 4
	if contentH < 3 {
		contentH = 3
	}

	// -- Left panel: session list --
	var leftLines []string
	leftLines = append(leftLines, titleStyle.Render("Sessions"))
	leftLines = append(leftLines, "")

	if len(m.sessions) == 0 {
		leftLines = append(leftLines, dimStyle.Render("  No sessions yet."))
		leftLines = append(leftLines, dimStyle.Render("  Press n to create one."))
	} else {
		for i, si := range m.sessions {
			// Status indicator.
			var indicator string
			switch si.status {
			case "running":
				indicator = statusRunning.Render("●")
			case "stopped":
				indicator = statusStopped.Render("●")
			case "exited":
				indicator = statusExited.Render("●")
			default:
				indicator = dimStyle.Render("●")
			}

			name := si.session.Name
			branch := si.session.Branch
			line := fmt.Sprintf(" %s %s", indicator, name)
			if branch != "" {
				line += dimStyle.Render(fmt.Sprintf(" (%s)", branch))
			}

			if i == m.cursor {
				line = selectedStyle.Render(line)
			}

			leftLines = append(leftLines, line)
		}
	}

	leftContent := strings.Join(leftLines, "\n")
	leftPanel := borderStyle.
		Width(leftW - 2). // subtract border width
		Height(contentH).
		Render(leftContent)

	// -- Right panel: preview or diff --
	var previewTitle string
	if m.showDiff {
		previewTitle = previewTitleStyle.Render("Git Diff")
	} else {
		previewTitle = previewTitleStyle.Render("Live Preview")
	}

	rightContent := previewTitle + "\n" + m.preview.View()
	rightPanel := borderStyle.
		Width(rightW - 2).
		Height(contentH).
		Render(rightContent)

	// Join panels horizontally.
	panels := lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, rightPanel)

	// Help bar.
	help := helpStyle.Render(
		"  j/k navigate  enter attach  n new  d stop  x remove  tab diff/preview  q quit",
	)

	return panels + "\n" + help
}
