package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/joegoldin/claude-container/internal/config"
	gitpkg "github.com/joegoldin/claude-container/internal/git"
)

// Wizard steps.
const (
	stepName     = iota
	stepWorktree // choose: new branch / from existing / no worktree
	stepBranch   // pick base branch (only if "from existing")
	stepMode     // normal vs yolo
	stepPrompt   // optional initial prompt
	stepDone
)

// WizardResult holds the final values collected by the wizard.
type WizardResult struct {
	Name       string
	Worktree   string
	From       string
	NoWorktree bool
	Yolo       bool
	Prompt     string
	Cancelled  bool
	Background bool // create in background without attaching
}

// WizardModel is the Bubble Tea model for the interactive new-session wizard.
type WizardModel struct {
	step        int
	textInput   textinput.Model
	result      WizardResult
	choices     []string
	cursor      int
	repoPath    string
	branches    []string
	scroll      int // scroll offset for branch list
	width       int
	height      int
	defaultName string // auto-generated name shown as default
}

// NewWizard creates a WizardModel. If repoPath is non-empty, branches are
// loaded for the "from existing branch" option. A random readable name is
// generated from dir and pre-filled as the default session name.
func NewWizard(repoPath string, dir string) WizardModel {
	generated := config.GenerateName(dir)

	ti := textinput.New()
	ti.Placeholder = generated
	ti.Focus()
	ti.CharLimit = 80

	var branches []string
	if repoPath != "" {
		branches, _ = gitpkg.ListBranches(repoPath)
	}

	return WizardModel{
		step:        stepName,
		textInput:   ti,
		repoPath:    repoPath,
		branches:    branches,
		defaultName: generated,
	}
}

// Result returns the collected wizard values.
func (m WizardModel) Result() WizardResult {
	return m.result
}

// Init returns the initial command (text input blink cursor).
func (m WizardModel) Init() tea.Cmd {
	return textinput.Blink
}

// Update handles messages for the current wizard step.
func (m WizardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		key := msg.String()

		// Global cancel.
		if key == "ctrl+c" || key == "esc" {
			m.result.Cancelled = true
			return m, tea.Quit
		}

		switch m.step {
		case stepName:
			return m.updateName(msg)
		case stepWorktree:
			return m.updateWorktree(key)
		case stepBranch:
			return m.updateBranch(key)
		case stepMode:
			return m.updateMode(key)
		case stepPrompt:
			return m.updatePrompt(msg)
		}
	}

	// Forward to text input when active.
	if m.step == stepName || m.step == stepPrompt {
		var cmd tea.Cmd
		m.textInput, cmd = m.textInput.Update(msg)
		return m, cmd
	}

	return m, nil
}

// ---------- step handlers ----------

func (m WizardModel) updateName(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "enter" {
		val := strings.TrimSpace(m.textInput.Value())
		if val == "" {
			val = m.defaultName // use generated name
		}
		m.result.Name = val

		// Prepare worktree choices.
		if m.repoPath != "" {
			m.choices = []string{
				"New branch",
				"From existing branch",
				"No worktree (use current dir)",
			}
		} else {
			// Not in a repo -- skip worktree step entirely.
			m.result.NoWorktree = true
			m.step = stepMode
			m.choices = []string{"Normal", "Yolo (skip permissions)"}
			m.cursor = 0
			return m, nil
		}

		m.step = stepWorktree
		m.cursor = 0
		return m, nil
	}

	var cmd tea.Cmd
	m.textInput, cmd = m.textInput.Update(msg)
	return m, cmd
}

func (m WizardModel) updateWorktree(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "j", "down":
		if m.cursor < len(m.choices)-1 {
			m.cursor++
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "enter":
		switch m.cursor {
		case 0: // New branch
			m.result.Worktree = m.result.Name
			m.step = stepMode
			m.choices = []string{"Normal", "Yolo (skip permissions)"}
			m.cursor = 0
		case 1: // From existing branch
			if len(m.branches) == 0 {
				// No branches available -- treat as new branch.
				m.result.Worktree = m.result.Name
				m.step = stepMode
				m.choices = []string{"Normal", "Yolo (skip permissions)"}
				m.cursor = 0
			} else {
				m.step = stepBranch
				m.choices = m.branches
				m.cursor = 0
				m.scroll = 0
			}
		case 2: // No worktree
			m.result.NoWorktree = true
			m.step = stepMode
			m.choices = []string{"Normal", "Yolo (skip permissions)"}
			m.cursor = 0
		}
	}
	return m, nil
}

func (m WizardModel) updateBranch(key string) (tea.Model, tea.Cmd) {
	maxVisible := 15
	switch key {
	case "j", "down":
		if m.cursor < len(m.choices)-1 {
			m.cursor++
			// Scroll down if cursor goes below visible window.
			if m.cursor >= m.scroll+maxVisible {
				m.scroll = m.cursor - maxVisible + 1
			}
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
			// Scroll up if cursor goes above visible window.
			if m.cursor < m.scroll {
				m.scroll = m.cursor
			}
		}
	case "enter":
		m.result.From = m.choices[m.cursor]
		m.result.Worktree = m.result.Name
		m.step = stepMode
		m.choices = []string{"Normal", "Yolo (skip permissions)"}
		m.cursor = 0
	}
	return m, nil
}

func (m WizardModel) updateMode(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "j", "down":
		if m.cursor < len(m.choices)-1 {
			m.cursor++
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "enter":
		m.result.Yolo = (m.cursor == 1)
		// Switch to prompt step.
		m.step = stepPrompt
		m.textInput = textinput.New()
		m.textInput.Placeholder = "(optional) initial prompt for Claude"
		m.textInput.Focus()
		m.textInput.CharLimit = 2000
		return m, textinput.Blink
	}
	return m, nil
}

func (m WizardModel) updatePrompt(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		m.result.Prompt = strings.TrimSpace(m.textInput.Value())
		m.step = stepDone
		return m, tea.Quit
	case "ctrl+b":
		m.result.Prompt = strings.TrimSpace(m.textInput.Value())
		m.result.Background = true
		m.step = stepDone
		return m, tea.Quit
	}

	var cmd tea.Cmd
	m.textInput, cmd = m.textInput.Update(msg)
	return m, cmd
}

// ---------- View ----------

// View renders the current wizard step.
func (m WizardModel) View() string {
	if m.step == stepDone {
		return ""
	}

	var b strings.Builder

	switch m.step {
	case stepName:
		b.WriteString(titleStyle.Render("New Session"))
		b.WriteString("\n\n")
		b.WriteString("Session name:\n")
		b.WriteString(m.textInput.View())
		b.WriteString("\n\n")
		b.WriteString(dimStyle.Render("enter confirm (empty = " + m.defaultName + ")  esc cancel"))

	case stepWorktree:
		b.WriteString(titleStyle.Render("Worktree Setup"))
		b.WriteString("\n\n")
		b.WriteString("How should the worktree be created?\n\n")
		m.renderChoices(&b, m.choices, m.cursor)
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("j/k navigate  enter select  esc cancel"))

	case stepBranch:
		b.WriteString(titleStyle.Render("Base Branch"))
		b.WriteString("\n\n")
		b.WriteString("Select branch to base the worktree on:\n\n")
		m.renderBranchList(&b)
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("j/k navigate  enter select  esc cancel"))

	case stepMode:
		b.WriteString(titleStyle.Render("Mode"))
		b.WriteString("\n\n")
		b.WriteString("Select Claude execution mode:\n\n")
		m.renderChoices(&b, m.choices, m.cursor)
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("j/k navigate  enter select  esc cancel"))

	case stepPrompt:
		b.WriteString(titleStyle.Render("Initial Prompt"))
		b.WriteString("\n\n")
		b.WriteString("Prompt to send to Claude:\n")
		b.WriteString(m.textInput.View())
		b.WriteString("\n\n")
		b.WriteString(dimStyle.Render("enter confirm  ctrl+b background  esc cancel"))
	}

	return b.String()
}

// renderChoices renders a simple selectable list.
func (m WizardModel) renderChoices(b *strings.Builder, items []string, cursor int) {
	for i, item := range items {
		if i == cursor {
			b.WriteString(fmt.Sprintf("  %s %s\n", selectedStyle.Render(">"), selectedStyle.Render(item)))
		} else {
			b.WriteString(fmt.Sprintf("    %s\n", item))
		}
	}
}

// renderBranchList renders the branch list with scroll support (max 15 visible).
func (m WizardModel) renderBranchList(b *strings.Builder) {
	maxVisible := 15
	start := m.scroll
	end := start + maxVisible
	if end > len(m.choices) {
		end = len(m.choices)
	}

	if start > 0 {
		b.WriteString(dimStyle.Render("  ... (scroll up)"))
		b.WriteString("\n")
	}

	for i := start; i < end; i++ {
		item := m.choices[i]
		if i == m.cursor {
			b.WriteString(fmt.Sprintf("  %s %s\n", selectedStyle.Render(">"), selectedStyle.Render(item)))
		} else {
			b.WriteString(fmt.Sprintf("    %s\n", item))
		}
	}

	if end < len(m.choices) {
		b.WriteString(dimStyle.Render("  ... (scroll down)"))
		b.WriteString("\n")
	}
}
