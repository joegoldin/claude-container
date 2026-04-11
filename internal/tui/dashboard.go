package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/docker"
	gitpkg "github.com/joegoldin/claude-container/internal/git"
)

// sessionInfo pairs a persisted session with its live status.
type sessionInfo struct {
	session *config.Session
	status  string // "running", "stopped", "removed"
}

// DashboardModel is the Bubble Tea model for the TUI dashboard.
type DashboardModel struct {
	store       *config.Store
	sessions    []sessionInfo
	cursor      int
	width       int
	height      int
	quitting    bool
	attached    string
	creating    bool
	creatingDir string // directory chosen for new session
	dirInput    textinput.Model
	pickingDir  bool // true when directory input is active
	confirming        bool // true when awaiting y/n confirmation for remove
	confirmName       string // session name pending removal
	showConversations bool
	convosModel       ConversationsModel
}

// Internal message types.
type refreshMsg struct {
	sessions []sessionInfo
}

type tickMsg time.Time

// NewDashboard creates a new DashboardModel backed by the given store.
func NewDashboard(store *config.Store) DashboardModel {
	return DashboardModel{
		store: store,
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

// CreatingDir returns the directory chosen for the new session, or "" for cwd.
func (m DashboardModel) CreatingDir() string {
	return m.creatingDir
}

// tick returns a command that sends a tickMsg after 2s.
func (m DashboardModel) tick() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// refreshSessions loads sessions from the store and checks live status.
func (m DashboardModel) refreshSessions() tea.Cmd {
	return func() tea.Msg {
		list := m.store.List()
		infos := make([]sessionInfo, 0, len(list))
		for _, sess := range list {
			var status string
			switch {
			case docker.IsRunning(sess.Name):
				status = "running"
			case docker.Exists(sess.Name):
				status = "stopped"
			default:
				status = "removed"
			}
			infos = append(infos, sessionInfo{session: sess, status: status})
		}
		return refreshMsg{sessions: infos}
	}
}

// Update processes messages and returns the updated model and any commands.
func (m DashboardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Delegate to conversations view when active.
	if m.showConversations {
		updated, cmd := m.convosModel.Update(msg)
		m.convosModel = updated.(ConversationsModel)
		if m.convosModel.GoBack() {
			m.showConversations = false
			m.convosModel = ConversationsModel{}
			return m, m.refreshSessions()
		}
		return m, cmd
	}

	switch msg := msg.(type) {

	case tea.KeyMsg:
		// Handle confirmation dialog.
		if m.confirming {
			switch msg.String() {
			case "y", "Y":
				m.confirming = false
				// Stop container first (ignore error if already stopped).
				_ = docker.Stop(m.confirmName)
				_ = docker.Remove(m.confirmName)
				// Clean up worktree/branch if applicable.
				for _, si := range m.sessions {
					if si.session.Name == m.confirmName {
						if si.session.Branch != "" && si.session.RepoPath != "" {
							_ = gitpkg.RemoveWorktree(si.session.RepoPath, si.session.WorktreePath, si.session.Branch)
						}
						break
					}
				}
				_ = m.store.Delete(m.confirmName)
				if m.cursor > 0 && m.cursor >= len(m.sessions)-1 {
					m.cursor--
				}
				return m, m.refreshSessions()
			case "n", "N", "esc":
				m.confirming = false
				return m, nil
			case "ctrl+c":
				m.quitting = true
				return m, tea.Quit
			}
			return m, nil
		}

		// Handle directory input mode.
		if m.pickingDir {
			switch msg.String() {
			case "enter":
				dir := strings.TrimSpace(m.dirInput.Value())
				if dir == "" {
					dir, _ = os.Getwd()
				}
				m.creatingDir = dir
				m.creating = true
				m.quitting = true
				return m, tea.Quit
			case "esc":
				m.pickingDir = false
				return m, nil
			case "ctrl+c":
				m.quitting = true
				return m, tea.Quit
			}
			var cmd tea.Cmd
			m.dirInput, cmd = m.dirInput.Update(msg)
			return m, cmd
		}

		switch msg.String() {
		case "q", "ctrl+c":
			m.quitting = true
			return m, tea.Quit

		case "j", "down":
			if m.cursor < len(m.sessions)-1 {
				m.cursor++
			}
			return m, nil

		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
			return m, nil

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
			if m.pickingDir {
				break
			}
			cwd, _ := os.Getwd()
			ti := textinput.New()
			ti.Placeholder = cwd
			ti.SetValue(cwd)
			ti.Focus()
			ti.CharLimit = 256
			ti.CursorEnd()
			m.dirInput = ti
			m.pickingDir = true
			return m, textinput.Blink

		case "d":
			if len(m.sessions) > 0 {
				idx := m.cursor
				if idx >= len(m.sessions) {
					idx = len(m.sessions) - 1
				}
				si := m.sessions[idx]
				_ = docker.Stop(si.session.Name)
			}
			return m, m.refreshSessions()

		case "c":
			m.convosModel = NewConversations(m.store)
			m.showConversations = true
			return m, m.convosModel.Init()

		case "x":
			if len(m.sessions) > 0 {
				idx := m.cursor
				if idx >= len(m.sessions) {
					idx = len(m.sessions) - 1
				}
				m.confirming = true
				m.confirmName = m.sessions[idx].session.Name
			}
			return m, nil
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case refreshMsg:
		m.sessions = msg.sessions
		if m.cursor >= len(m.sessions) && len(m.sessions) > 0 {
			m.cursor = len(m.sessions) - 1
		}
		return m, nil

	case tickMsg:
		return m, tea.Batch(m.refreshSessions(), m.tick())
	}

	if m.pickingDir {
		var cmd tea.Cmd
		m.dirInput, cmd = m.dirInput.Update(msg)
		return m, cmd
	}

	return m, nil
}

// View renders the dashboard.
func (m DashboardModel) View() string {
	if m.quitting {
		return ""
	}

	if m.showConversations {
		return m.convosModel.View()
	}

	contentW := m.width - 4 // border padding
	if contentW < 20 {
		contentW = 20
	}
	contentH := m.height - 4
	if contentH < 3 {
		contentH = 3
	}

	var lines []string
	lines = append(lines, titleStyle.Render("Sessions"))
	lines = append(lines, "")

	if len(m.sessions) == 0 {
		lines = append(lines, dimStyle.Render("  No sessions yet."))
		lines = append(lines, dimStyle.Render("  Press n to create one."))
	} else {
		rowW := contentW - 2
		if rowW < 10 {
			rowW = 10
		}

		for i, si := range m.sessions {
			dot := "●"
			sess := si.session

			// Header line: dot + name + status tag.
			var statusTag string
			switch si.status {
			case "running":
				uptime := formatUptime(time.Since(sess.CreatedAt))
				statusTag = statusRunning.Render(fmt.Sprintf(" [running %s]", uptime))
			case "stopped":
				statusTag = statusStopped.Render(" [stopped]")
			default:
				statusTag = dimStyle.Render(" [removed]")
			}

			if i == m.cursor {
				// Build a plain status label for the highlighted row.
				var statusLabel string
				switch si.status {
				case "running":
					statusLabel = fmt.Sprintf(" [running %s]", formatUptime(time.Since(sess.CreatedAt)))
				case "stopped":
					statusLabel = " [stopped]"
				default:
					statusLabel = " [removed]"
				}
				row := fmt.Sprintf(" %s %s%s", dot, sess.Name, statusLabel)
				if len(row) < rowW {
					row += strings.Repeat(" ", rowW-len(row))
				}
				lines = append(lines, selectedStyle.Render(row))
			} else {
				var indicator string
				switch si.status {
				case "running":
					indicator = statusRunning.Render(dot)
				case "stopped":
					indicator = statusStopped.Render(dot)
				default:
					indicator = dimStyle.Render(dot)
				}
				lines = append(lines, fmt.Sprintf(" %s %s%s", indicator, sess.Name, statusTag))
			}

			// Detail lines with session info.
			if sess.Branch != "" {
				lines = append(lines, dimStyle.Render(fmt.Sprintf("     branch: %s", sess.Branch)))
			}
			if sess.RepoPath != "" {
				lines = append(lines, dimStyle.Render(fmt.Sprintf("     repo:   %s", shortenHome(sess.RepoPath))))
			}
			if sess.WorktreePath != "" && sess.WorktreePath != sess.RepoPath {
				lines = append(lines, dimStyle.Render(fmt.Sprintf("     work:   %s", shortenHome(sess.WorktreePath))))
			}

			// Flags line.
			var flags []string
			if sess.Yolo {
				flags = append(flags, "yolo")
			}
			if sess.AutoRemove {
				flags = append(flags, "auto-remove")
			}
			if len(flags) > 0 {
				lines = append(lines, dimStyle.Render(fmt.Sprintf("     flags:  %s", strings.Join(flags, ", "))))
			}

			lines = append(lines, dimStyle.Render(fmt.Sprintf("     created: %s", sess.CreatedAt.Format("2006-01-02 15:04"))))

			// Blank line between sessions.
			if i < len(m.sessions)-1 {
				lines = append(lines, "")
			}
		}
	}

	content := strings.Join(lines, "\n")
	panel := borderStyle.
		Width(contentW).
		Height(contentH).
		Render(content)

	var bottom string
	if m.confirming {
		bottom = fmt.Sprintf("  %s  %s",
			statusStopped.Render(fmt.Sprintf("Remove %s?", m.confirmName)),
			dimStyle.Render("y confirm  n cancel"),
		)
	} else if m.pickingDir {
		bottom = fmt.Sprintf("  %s %s\n  %s",
			titleStyle.Render("New session directory:"),
			m.dirInput.View(),
			dimStyle.Render("enter confirm  esc cancel"),
		)
	} else {
		bottom = helpStyle.Render(
			"  ↑/↓ navigate  enter attach  n new  d stop  x remove  c convos  q quit",
		)
	}

	return panel + "\n" + bottom
}

// formatUptime returns a human-readable duration string.
func formatUptime(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		return fmt.Sprintf("%dh%dm", h, m)
	default:
		days := int(d.Hours()) / 24
		h := int(d.Hours()) % 24
		return fmt.Sprintf("%dd%dh", days, h)
	}
}

// shortenHome replaces the user's home directory prefix with ~.
func shortenHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if len(path) >= len(home) && path[:len(home)] == home {
		return "~" + path[len(home):]
	}
	return path
}

