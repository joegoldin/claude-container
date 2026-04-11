package tui

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/transcript"
)

// repoRow pairs a repo entry with conversation metadata for display.
type repoRow struct {
	id      string
	entry   config.RepoEntry
	count   int
	lastMod time.Time
}

// ConversationsModel is the Bubble Tea model for browsing conversations.
type ConversationsModel struct {
	store       *config.Store
	repos       []repoRow
	convos      []transcript.ConversationInfo
	repoCursor  int
	convoCursor int
	inRepo      bool   // true = viewing conversations within a repo
	width       int
	height      int
	goBack      bool   // signal to parent to switch back to dashboard
	confirming  bool   // delete confirmation
	confirmID   string // conversation ID pending deletion
}

// reposLoadedMsg is sent after scanning repos and their conversation counts.
type reposLoadedMsg struct {
	repos []repoRow
}

// convosLoadedMsg is sent after scanning conversations for a repo.
type convosLoadedMsg struct {
	convos []transcript.ConversationInfo
}

// NewConversations creates a new ConversationsModel.
func NewConversations(store *config.Store) ConversationsModel {
	return ConversationsModel{
		store: store,
	}
}

// Init loads repos and scans conversation counts.
func (m ConversationsModel) Init() tea.Cmd {
	return m.loadRepos()
}

// GoBack returns true when the user wants to return to the dashboard.
func (m ConversationsModel) GoBack() bool {
	return m.goBack
}

// loadRepos scans all repos and counts their conversations.
func (m ConversationsModel) loadRepos() tea.Cmd {
	return func() tea.Msg {
		repoMap, err := m.store.ListRepos()
		if err != nil {
			return reposLoadedMsg{}
		}

		var rows []repoRow
		for id, entry := range repoMap {
			configDir := m.store.RepoConfigDir(entry.Path)
			convos, _ := transcript.ScanConversations(configDir)
			row := repoRow{
				id:    id,
				entry: entry,
				count: len(convos),
			}
			if len(convos) > 0 {
				row.lastMod = convos[0].ModTime // already sorted newest first
			}
			rows = append(rows, row)
		}

		// Sort by last modified descending, then by name.
		sort.Slice(rows, func(i, j int) bool {
			if !rows[i].lastMod.Equal(rows[j].lastMod) {
				return rows[i].lastMod.After(rows[j].lastMod)
			}
			return rows[i].entry.Name < rows[j].entry.Name
		})

		return reposLoadedMsg{repos: rows}
	}
}

// loadConvos scans conversations for the selected repo.
func (m ConversationsModel) loadConvos(repoPath string) tea.Cmd {
	return func() tea.Msg {
		configDir := m.store.RepoConfigDir(repoPath)
		convos, _ := transcript.ScanConversations(configDir)
		return convosLoadedMsg{convos: convos}
	}
}

// Update processes messages.
func (m ConversationsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.KeyMsg:
		// Handle delete confirmation.
		if m.confirming {
			switch msg.String() {
			case "y", "Y":
				m.confirming = false
				// Find and delete the file.
				for _, c := range m.convos {
					if c.ID == m.confirmID {
						_ = os.Remove(c.Path)
						break
					}
				}
				// Reload conversations.
				if m.repoCursor < len(m.repos) {
					return m, m.loadConvos(m.repos[m.repoCursor].entry.Path)
				}
				return m, nil
			case "n", "N", "esc":
				m.confirming = false
				return m, nil
			case "ctrl+c":
				m.goBack = true
				return m, nil
			}
			return m, nil
		}

		switch msg.String() {
		case "q", "ctrl+c":
			m.goBack = true
			return m, nil

		case "j", "down":
			if m.inRepo {
				if m.convoCursor < len(m.convos)-1 {
					m.convoCursor++
				}
			} else {
				if m.repoCursor < len(m.repos)-1 {
					m.repoCursor++
				}
			}
			return m, nil

		case "k", "up":
			if m.inRepo {
				if m.convoCursor > 0 {
					m.convoCursor--
				}
			} else {
				if m.repoCursor > 0 {
					m.repoCursor--
				}
			}
			return m, nil

		case "enter":
			if !m.inRepo && len(m.repos) > 0 {
				idx := m.repoCursor
				if idx >= len(m.repos) {
					idx = len(m.repos) - 1
				}
				m.inRepo = true
				m.convoCursor = 0
				return m, m.loadConvos(m.repos[idx].entry.Path)
			}
			return m, nil

		case "d":
			if m.inRepo && len(m.convos) > 0 {
				idx := m.convoCursor
				if idx >= len(m.convos) {
					idx = len(m.convos) - 1
				}
				m.confirming = true
				m.confirmID = m.convos[idx].ID
			}
			return m, nil

		case "esc", "backspace":
			if m.inRepo {
				m.inRepo = false
				m.convos = nil
				m.convoCursor = 0
				return m, m.loadRepos()
			}
			m.goBack = true
			return m, nil
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case reposLoadedMsg:
		m.repos = msg.repos
		if m.repoCursor >= len(m.repos) && len(m.repos) > 0 {
			m.repoCursor = len(m.repos) - 1
		}
		return m, nil

	case convosLoadedMsg:
		m.convos = msg.convos
		if m.convoCursor >= len(m.convos) && len(m.convos) > 0 {
			m.convoCursor = len(m.convos) - 1
		}
		return m, nil
	}

	return m, nil
}

// View renders the conversations view.
func (m ConversationsModel) View() string {
	contentW := m.width - 4
	if contentW < 20 {
		contentW = 20
	}
	contentH := m.height - 4
	if contentH < 3 {
		contentH = 3
	}

	var lines []string
	lines = append(lines, titleStyle.Render("Conversations"))
	lines = append(lines, "")

	if m.inRepo {
		lines = append(lines, m.viewConversations(contentW)...)
	} else {
		lines = append(lines, m.viewRepos(contentW)...)
	}

	content := strings.Join(lines, "\n")
	panel := borderStyle.
		Width(contentW).
		Height(contentH).
		Render(content)

	var bottom string
	if m.confirming {
		bottom = fmt.Sprintf("  %s  %s",
			statusStopped.Render("Delete conversation?"),
			dimStyle.Render("y confirm  n cancel"),
		)
	} else if m.inRepo {
		bottom = helpStyle.Render(
			"  ↑/↓ navigate  d delete  esc back  q quit",
		)
	} else {
		bottom = helpStyle.Render(
			"  ↑/↓ navigate  enter expand  esc back  q quit",
		)
	}

	return panel + "\n" + bottom
}

// viewRepos renders the repo list.
func (m ConversationsModel) viewRepos(rowW int) []string {
	var lines []string

	if len(m.repos) == 0 {
		lines = append(lines, dimStyle.Render("  No repositories with conversations."))
		return lines
	}

	for i, r := range m.repos {
		name := r.entry.Name
		path := shortenHome(r.entry.Path)
		countStr := fmt.Sprintf("%d conversations", r.count)
		if r.count == 1 {
			countStr = "1 conversation"
		}

		var dateStr string
		if !r.lastMod.IsZero() {
			dateStr = r.lastMod.Format("2006-01-02 15:04")
		}

		if i == m.repoCursor {
			row := fmt.Sprintf(" %s  %s", name, countStr)
			if len(row) < rowW-2 {
				row += strings.Repeat(" ", rowW-2-len(row))
			}
			lines = append(lines, selectedStyle.Render(row))
		} else {
			lines = append(lines, fmt.Sprintf(" %s  %s",
				name,
				dimStyle.Render(countStr),
			))
		}

		lines = append(lines, dimStyle.Render(fmt.Sprintf("     %s", path)))
		if dateStr != "" {
			lines = append(lines, dimStyle.Render(fmt.Sprintf("     last: %s", dateStr)))
		}

		if i < len(m.repos)-1 {
			lines = append(lines, "")
		}
	}

	return lines
}

// viewConversations renders the conversation list for the selected repo.
func (m ConversationsModel) viewConversations(rowW int) []string {
	var lines []string

	if m.repoCursor < len(m.repos) {
		repo := m.repos[m.repoCursor]
		lines = append(lines, dimStyle.Render(fmt.Sprintf("  %s — %s",
			repo.entry.Name, shortenHome(repo.entry.Path))))
		lines = append(lines, "")
	}

	if len(m.convos) == 0 {
		lines = append(lines, dimStyle.Render("  No conversations found."))
		return lines
	}

	for i, c := range m.convos {
		date := c.ModTime.Format("2006-01-02 15:04")
		size := formatSize(c.Size)

		if i == m.convoCursor {
			row := fmt.Sprintf(" %s  %s", date, size)
			if len(row) < rowW-2 {
				row += strings.Repeat(" ", rowW-2-len(row))
			}
			lines = append(lines, selectedStyle.Render(row))
		} else {
			lines = append(lines, fmt.Sprintf(" %s  %s", date, dimStyle.Render(size)))
		}
		lines = append(lines, dimStyle.Render(fmt.Sprintf("     %s", c.Preview)))

		if i < len(m.convos)-1 {
			lines = append(lines, "")
		}
	}

	return lines
}

// formatSize returns a human-readable file size.
func formatSize(bytes int64) string {
	switch {
	case bytes < 1024:
		return fmt.Sprintf("%dB", bytes)
	case bytes < 1024*1024:
		return fmt.Sprintf("%.1fKB", float64(bytes)/1024)
	default:
		return fmt.Sprintf("%.1fMB", float64(bytes)/(1024*1024))
	}
}
