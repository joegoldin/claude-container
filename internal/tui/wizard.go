package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/joegoldin/claude-container/internal/config"
	gitpkg "github.com/joegoldin/claude-container/internal/git"
	"github.com/joegoldin/claude-container/internal/sandbox"
)

// Wizard steps.
const (
	stepName      = iota
	stepWorktree  // choose: new branch / from existing / no worktree
	stepBranch    // pick base branch (only if "from existing")
	stepProfile   // select sandbox profile
	stepWorkspace // select workspace (skip if none defined)
	stepPackages    // optional packages input
	stepPermissions // optional custom permission rules
	stepPrompt      // optional initial prompt
	stepReview    // summary + confirm
	stepDone
)

const branchListMaxVisible = 15

// WizardResult holds the final values collected by the wizard.
type WizardResult struct {
	Name       string
	Worktree   string
	From       string
	Profile    string
	Workspace  string
	Packages   string // comma-separated package names
	AllowPerms string // comma-separated allow permission rules
	DenyPerms  string // comma-separated deny permission rules
	Prompt     string
	NoWorktree bool
	Yolo       bool
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

	profiles       []sandbox.Profile
	workspaceNames []string
	workspaceMap   map[string][]string

	// saved cursor positions for back navigation
	savedCursors map[int]int
}

// NewWizard creates a WizardModel. If repoPath is non-empty, branches are
// loaded for the "from existing branch" option. A random readable name is
// generated from dir and pre-filled as the default session name. Profiles
// and workspaces are loaded from their respective stores.
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

	profiles := sandbox.ListProfiles()

	ws := config.NewWorkspaceStore(config.DefaultDir())
	workspaceNames := ws.List()
	workspaceMap := ws.ListAll()

	return WizardModel{
		step:           stepName,
		textInput:      ti,
		repoPath:       repoPath,
		branches:       branches,
		defaultName:    generated,
		profiles:       profiles,
		workspaceNames: workspaceNames,
		workspaceMap:   workspaceMap,
		savedCursors:   make(map[int]int),
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
		if key == "ctrl+c" {
			m.result.Cancelled = true
			return m, tea.Quit
		}

		// Esc cancels except on review step where it goes back.
		if key == "esc" {
			if m.step == stepReview {
				return m.goBack()
			}
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
		case stepProfile:
			return m.updateProfile(key)
		case stepWorkspace:
			return m.updateWorkspace(key)
		case stepPackages:
			return m.updatePackages(msg)
		case stepPermissions:
			return m.updatePermissions(msg)
		case stepPrompt:
			return m.updatePrompt(msg)
		case stepReview:
			return m.updateReview(key)
		}
	}

	// Forward to text input when active.
	if m.step == stepName || m.step == stepPackages || m.step == stepPermissions || m.step == stepPrompt {
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
			m.step = stepWorktree
			m.cursor = 0
		} else {
			// Not in a repo -- skip worktree step entirely.
			m.result.NoWorktree = true
			m.step = stepProfile
			m.setupProfileStep()
		}
		return m, nil
	}

	var cmd tea.Cmd
	m.textInput, cmd = m.textInput.Update(msg)
	return m, cmd
}

func (m WizardModel) updateWorktree(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "left":
		return m.goBack()
	case "j", "down":
		if m.cursor < len(m.choices)-1 {
			m.cursor++
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "enter":
		m.savedCursors[stepWorktree] = m.cursor
		switch m.cursor {
		case 0: // New branch
			m.result.Worktree = m.result.Name
			m.result.From = ""
			m.step = stepProfile
			m.setupProfileStep()
		case 1: // From existing branch
			if len(m.branches) == 0 {
				// No branches available -- treat as new branch.
				m.result.Worktree = m.result.Name
				m.result.From = ""
				m.step = stepProfile
				m.setupProfileStep()
			} else {
				m.step = stepBranch
				m.choices = m.branches
				m.cursor = 0
				m.scroll = 0
			}
		case 2: // No worktree
			m.result.NoWorktree = true
			m.result.Worktree = ""
			m.result.From = ""
			m.step = stepProfile
			m.setupProfileStep()
		}
	}
	return m, nil
}

func (m WizardModel) updateBranch(key string) (tea.Model, tea.Cmd) {
	maxVisible := branchListMaxVisible
	switch key {
	case "left":
		return m.goBack()
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
		m.savedCursors[stepBranch] = m.cursor
		m.result.From = m.choices[m.cursor]
		m.result.Worktree = m.result.Name
		m.step = stepProfile
		m.setupProfileStep()
	}
	return m, nil
}

func (m WizardModel) updateProfile(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "left":
		return m.goBack()
	case "j", "down":
		if m.cursor < len(m.choices)-1 {
			m.cursor++
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "enter":
		m.savedCursors[stepProfile] = m.cursor
		p := m.profiles[m.cursor]
		m.result.Profile = p.Name
		m.result.Yolo = p.Yolo

		if len(m.workspaceNames) > 0 {
			m.step = stepWorkspace
			m.setupWorkspaceStep()
		} else {
			m.step = stepPackages
			m.setupPackagesStep()
			return m, textinput.Blink
		}
	}
	return m, nil
}

func (m WizardModel) updateWorkspace(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "left":
		return m.goBack()
	case "j", "down":
		if m.cursor < len(m.choices)-1 {
			m.cursor++
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "enter":
		m.savedCursors[stepWorkspace] = m.cursor
		if m.cursor == 0 {
			m.result.Workspace = "" // none selected
		} else {
			m.result.Workspace = m.workspaceNames[m.cursor-1]
		}
		m.step = stepPackages
		m.setupPackagesStep()
		return m, textinput.Blink
	}
	return m, nil
}

func (m WizardModel) updatePackages(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "left":
		return m.goBack()
	case "enter":
		m.result.Packages = strings.TrimSpace(m.textInput.Value())
		m.step = stepPermissions
		m.setupPermissionsStep()
		return m, textinput.Blink
	}

	var cmd tea.Cmd
	m.textInput, cmd = m.textInput.Update(msg)
	return m, cmd
}

func (m WizardModel) updatePermissions(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "left":
		return m.goBack()
	case "enter":
		m.result.AllowPerms = strings.TrimSpace(m.textInput.Value())
		m.step = stepPrompt
		m.setupPromptStep()
		return m, textinput.Blink
	}

	var cmd tea.Cmd
	m.textInput, cmd = m.textInput.Update(msg)
	return m, cmd
}

func (m WizardModel) updatePrompt(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		m.result.Prompt = strings.TrimSpace(m.textInput.Value())
		m.step = stepReview
		return m, nil
	}

	var cmd tea.Cmd
	m.textInput, cmd = m.textInput.Update(msg)
	return m, cmd
}

func (m WizardModel) updateReview(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "left":
		return m.goBack()
	case "enter":
		m.step = stepDone
		return m, tea.Quit
	case "ctrl+b":
		m.result.Background = true
		m.step = stepDone
		return m, tea.Quit
	}
	return m, nil
}

// ---------- step setup helpers ----------

func (m *WizardModel) setupProfileStep() {
	m.choices = make([]string, len(m.profiles))
	for i, p := range m.profiles {
		m.choices[i] = p.Name
	}
	// Default cursor to the "default" profile if present.
	if saved, ok := m.savedCursors[stepProfile]; ok {
		m.cursor = saved
	} else {
		m.cursor = 0
		for i, p := range m.profiles {
			if p.Name == "default" {
				m.cursor = i
				break
			}
		}
	}
}

func (m *WizardModel) setupWorkspaceStep() {
	m.choices = make([]string, 0, len(m.workspaceNames)+1)
	m.choices = append(m.choices, "(none \u2014 current directory)")
	m.choices = append(m.choices, m.workspaceNames...)
	if saved, ok := m.savedCursors[stepWorkspace]; ok {
		m.cursor = saved
	} else {
		m.cursor = 0
	}
}

func (m *WizardModel) setupPackagesStep() {
	m.textInput = textinput.New()
	m.textInput.Placeholder = "(optional) e.g., rust,nodejs,python3"
	m.textInput.Focus()
	m.textInput.CharLimit = 500
	if m.result.Packages != "" {
		m.textInput.SetValue(m.result.Packages)
	}
}

func (m *WizardModel) setupPermissionsStep() {
	m.textInput = textinput.New()
	m.textInput.Placeholder = "(optional) e.g., Bash(docker *),Read(/etc/**)"
	m.textInput.Focus()
	m.textInput.CharLimit = 1000
	if m.result.AllowPerms != "" {
		m.textInput.SetValue(m.result.AllowPerms)
	}
}

func (m *WizardModel) setupPromptStep() {
	m.textInput = textinput.New()
	m.textInput.Placeholder = "(optional) initial prompt for Claude"
	m.textInput.Focus()
	m.textInput.CharLimit = 2000
	// Restore previous prompt text if going back and forward.
	if m.result.Prompt != "" {
		m.textInput.SetValue(m.result.Prompt)
	}
}

// ---------- back navigation ----------

func (m WizardModel) goBack() (tea.Model, tea.Cmd) {
	switch m.step {
	case stepWorktree:
		// Go back to stepName.
		m.step = stepName
		ti := textinput.New()
		ti.Placeholder = m.defaultName
		ti.Focus()
		ti.CharLimit = 80
		ti.SetValue(m.result.Name)
		m.textInput = ti
		return m, textinput.Blink

	case stepBranch:
		// Go back to stepWorktree.
		m.step = stepWorktree
		m.choices = []string{
			"New branch",
			"From existing branch",
			"No worktree (use current dir)",
		}
		if saved, ok := m.savedCursors[stepWorktree]; ok {
			m.cursor = saved
		} else {
			m.cursor = 0
		}
		return m, nil

	case stepProfile:
		if m.repoPath != "" {
			// Check if we came from branch step or worktree step.
			if m.result.From != "" {
				// Came via branch selection -> go back to branch.
				m.step = stepBranch
				m.choices = m.branches
				if saved, ok := m.savedCursors[stepBranch]; ok {
					m.cursor = saved
				} else {
					m.cursor = 0
				}
				m.scroll = 0
				// Adjust scroll to keep cursor visible.
				if m.cursor >= branchListMaxVisible {
					m.scroll = m.cursor - branchListMaxVisible + 1
				}
			} else {
				// Came from worktree step directly.
				m.step = stepWorktree
				m.choices = []string{
					"New branch",
					"From existing branch",
					"No worktree (use current dir)",
				}
				if saved, ok := m.savedCursors[stepWorktree]; ok {
					m.cursor = saved
				} else {
					m.cursor = 0
				}
			}
		} else {
			// No repo: go back to name.
			m.step = stepName
			ti := textinput.New()
			ti.Placeholder = m.defaultName
			ti.Focus()
			ti.CharLimit = 80
			ti.SetValue(m.result.Name)
			m.textInput = ti
			return m, textinput.Blink
		}
		return m, nil

	case stepWorkspace:
		// Go back to stepProfile.
		m.step = stepProfile
		m.setupProfileStep()
		return m, nil

	case stepPackages:
		if len(m.workspaceNames) > 0 {
			m.step = stepWorkspace
			m.setupWorkspaceStep()
		} else {
			m.step = stepProfile
			m.setupProfileStep()
		}
		return m, nil

	case stepPermissions:
		// Go back to stepPackages.
		m.step = stepPackages
		m.setupPackagesStep()
		return m, textinput.Blink

	case stepPrompt:
		// Go back to stepPermissions.
		m.step = stepPermissions
		m.setupPermissionsStep()
		return m, textinput.Blink

	case stepReview:
		// Go back to stepPrompt.
		m.step = stepPrompt
		m.setupPromptStep()
		return m, textinput.Blink
	}

	return m, nil
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
		b.WriteString(dimStyle.Render("j/k navigate  enter select  \u2190 back  esc cancel"))

	case stepBranch:
		b.WriteString(titleStyle.Render("Base Branch"))
		b.WriteString("\n\n")
		b.WriteString("Select branch to base the worktree on:\n\n")
		m.renderBranchList(&b)
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("j/k navigate  enter select  \u2190 back  esc cancel"))

	case stepProfile:
		b.WriteString(titleStyle.Render("Sandbox Profile"))
		b.WriteString("\n\n")
		b.WriteString("Select security profile:\n\n")
		b.WriteString(m.renderProfileStep())
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("j/k navigate  enter select  \u2190 back  esc cancel"))

	case stepWorkspace:
		b.WriteString(titleStyle.Render("Workspace"))
		b.WriteString("\n\n")
		b.WriteString("Select workspace (additional directory mounts):\n\n")
		b.WriteString(m.renderWorkspaceStep())
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("j/k navigate  enter select  \u2190 back  esc cancel"))

	case stepPackages:
		b.WriteString(titleStyle.Render("Packages"))
		b.WriteString("\n\n")
		b.WriteString("Extra packages to install (comma-separated nixpkgs names):\n")
		b.WriteString(m.textInput.View())
		b.WriteString("\n\n")
		b.WriteString(dimStyle.Render("enter confirm  left back"))

	case stepPermissions:
		b.WriteString(titleStyle.Render("Extra Permissions"))
		b.WriteString("\n\n")
		b.WriteString("Additional allow permission rules (comma-separated):\n")
		b.WriteString(m.textInput.View())
		b.WriteString("\n\n")
		b.WriteString(dimStyle.Render("e.g., Bash(docker *), Read(/secrets/**)"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("enter confirm  left back"))

	case stepPrompt:
		b.WriteString(titleStyle.Render("Initial Prompt"))
		b.WriteString("\n\n")
		b.WriteString("Prompt to send to Claude:\n")
		b.WriteString(m.textInput.View())
		b.WriteString("\n\n")
		b.WriteString(dimStyle.Render("enter confirm  esc cancel"))

	case stepReview:
		b.WriteString(m.renderReview())
	}

	return b.String()
}

// ---------- render helpers ----------

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
	maxVisible := branchListMaxVisible
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

// renderProfileStep renders the profile selection with a preview panel.
func (m WizardModel) renderProfileStep() string {
	if len(m.profiles) == 0 {
		return "(no profiles configured)"
	}

	// Build left column: profile list.
	var left strings.Builder
	for i, p := range m.profiles {
		if i == m.cursor {
			left.WriteString(fmt.Sprintf("  %s %s\n", selectedStyle.Render(">"), selectedStyle.Render(p.Name)))
		} else {
			left.WriteString(fmt.Sprintf("    %s\n", p.Name))
		}
	}

	// Build right column: preview of selected profile.
	preview := m.buildProfilePreview(m.profiles[m.cursor])

	if m.width > 0 && m.width < 80 {
		// Narrow terminal: preview below.
		return left.String() + "\n" + preview
	}

	// Wide terminal: side-by-side layout.
	return lipgloss.JoinHorizontal(lipgloss.Top, left.String(), "  ", preview)
}

// buildProfilePreview builds the preview panel for a profile.
func (m WizardModel) buildProfilePreview(p sandbox.Profile) string {
	var content strings.Builder

	content.WriteString(previewTitleStyle.Render(p.Name))
	content.WriteString("\n\n")

	content.WriteString(fmt.Sprintf("%-12s %s\n", "Description:", p.Description))

	if p.Yolo {
		content.WriteString(fmt.Sprintf("%-12s %s\n", "Mode:", "yolo (skip permission prompts)"))
	} else {
		content.WriteString(fmt.Sprintf("%-12s %s\n", "Mode:", "interactive (permission prompts)"))
	}

	// Network policy.
	if len(p.AllowedDomains) == 0 {
		content.WriteString(fmt.Sprintf("%-12s %s\n", "Network:", "unrestricted"))
	} else {
		// Summarize: show count and first few.
		if len(p.AllowedDomains) <= 3 {
			content.WriteString(fmt.Sprintf("%-12s %s\n", "Network:", strings.Join(p.AllowedDomains, ", ")))
		} else {
			content.WriteString(fmt.Sprintf("%-12s %s + %d more\n", "Network:", strings.Join(p.AllowedDomains[:3], ", "), len(p.AllowedDomains)-3))
		}
	}

	// Command restrictions.
	if len(p.AllowPerms) == 0 && len(p.DenyPerms) == 0 {
		content.WriteString(fmt.Sprintf("%-12s %s", "Commands:", "none"))
	} else {
		parts := make([]string, 0, 2)
		if len(p.AllowPerms) > 0 {
			parts = append(parts, fmt.Sprintf("%d allow rules", len(p.AllowPerms)))
		}
		if len(p.DenyPerms) > 0 {
			parts = append(parts, fmt.Sprintf("%d deny rules", len(p.DenyPerms)))
		}
		content.WriteString(fmt.Sprintf("%-12s %s", "Commands:", strings.Join(parts, ", ")))
	}

	return previewBoxStyle.Render(content.String())
}

// renderWorkspaceStep renders the workspace selection with a preview panel.
func (m WizardModel) renderWorkspaceStep() string {
	// Build left column: workspace list.
	var left strings.Builder
	for i, item := range m.choices {
		if i == m.cursor {
			left.WriteString(fmt.Sprintf("  %s %s\n", selectedStyle.Render(">"), selectedStyle.Render(item)))
		} else {
			left.WriteString(fmt.Sprintf("    %s\n", item))
		}
	}

	// Build right column: preview of selected workspace paths.
	preview := m.buildWorkspacePreview()

	if m.width > 0 && m.width < 80 {
		// Narrow terminal: preview below.
		return left.String() + "\n" + preview
	}

	// Wide terminal: side-by-side layout.
	return lipgloss.JoinHorizontal(lipgloss.Top, left.String(), "  ", preview)
}

// buildWorkspacePreview builds the preview panel for the selected workspace.
func (m WizardModel) buildWorkspacePreview() string {
	var content strings.Builder

	if m.cursor == 0 {
		content.WriteString(previewTitleStyle.Render("Current Directory"))
		content.WriteString("\n\n")
		content.WriteString("No additional directories will be mounted.")
	} else {
		wsName := m.workspaceNames[m.cursor-1]
		content.WriteString(previewTitleStyle.Render(wsName))
		content.WriteString("\n\n")
		paths := m.workspaceMap[wsName]
		if len(paths) == 0 {
			content.WriteString("No paths configured.")
		} else {
			content.WriteString("Paths:\n")
			for _, p := range paths {
				content.WriteString(fmt.Sprintf("  %s\n", p))
			}
		}
	}

	return previewBoxStyle.Render(content.String())
}

// renderReview renders the final review screen with summary and CLI equivalent.
func (m WizardModel) renderReview() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render("Review"))
	b.WriteString("\n\n")

	// Session type description.
	sessionType := "run (no worktree)"
	if m.result.Worktree != "" {
		if m.result.From != "" {
			sessionType = fmt.Sprintf("work (worktree from %s)", m.result.From)
		} else {
			sessionType = "work (new worktree)"
		}
	}

	b.WriteString(fmt.Sprintf("  %-12s %s\n", "Session:", m.result.Name))
	b.WriteString(fmt.Sprintf("  %-12s %s\n", "Type:", sessionType))
	b.WriteString(fmt.Sprintf("  %-12s %s\n", "Profile:", m.result.Profile))

	if m.result.Workspace != "" {
		b.WriteString(fmt.Sprintf("  %-12s %s\n", "Workspace:", m.result.Workspace))
	} else {
		b.WriteString(fmt.Sprintf("  %-12s %s\n", "Workspace:", "(current directory)"))
	}

	if m.result.Packages != "" {
		b.WriteString(fmt.Sprintf("  %-12s %s\n", "Packages:", m.result.Packages))
	}

	if m.result.AllowPerms != "" {
		b.WriteString(fmt.Sprintf("  %-12s %s\n", "Allow:", m.result.AllowPerms))
	}

	if m.result.DenyPerms != "" {
		b.WriteString(fmt.Sprintf("  %-12s %s\n", "Deny:", m.result.DenyPerms))
	}

	if m.result.Prompt != "" {
		// Truncate long prompts for display.
		prompt := m.result.Prompt
		if len(prompt) > 60 {
			prompt = prompt[:57] + "..."
		}
		b.WriteString(fmt.Sprintf("  %-12s %q\n", "Prompt:", prompt))
	} else {
		b.WriteString(fmt.Sprintf("  %-12s %s\n", "Prompt:", "(none)"))
	}

	b.WriteString("\n")
	b.WriteString("  CLI equivalent:\n")
	b.WriteString("    ")
	b.WriteString(cliCommandStyle.Render(m.buildCLICommand()))
	b.WriteString("\n\n")

	b.WriteString(dimStyle.Render("enter launch  ctrl+b background  \u2190 back  esc cancel"))

	return b.String()
}

// ---------- CLI command builder ----------

// buildCLICommand constructs the equivalent CLI command string from the result.
func (m WizardModel) buildCLICommand() string {
	var parts []string
	parts = append(parts, "claude-container", "new")

	parts = append(parts, "--name", m.result.Name)

	if m.result.NoWorktree {
		parts = append(parts, "--no-worktree")
	} else if m.result.Worktree != "" {
		parts = append(parts, "--worktree")
		if m.result.From != "" {
			parts = append(parts, "--from", m.result.From)
		}
	}

	// Only include profile if non-default.
	if m.result.Profile != "" && m.result.Profile != "default" {
		parts = append(parts, "--profile", m.result.Profile)
	}

	if m.result.Workspace != "" {
		parts = append(parts, "--workspace", m.result.Workspace)
	}

	if m.result.Packages != "" {
		parts = append(parts, fmt.Sprintf("--packages %s", m.result.Packages))
	}

	if m.result.AllowPerms != "" {
		for _, perm := range strings.Split(m.result.AllowPerms, ",") {
			perm = strings.TrimSpace(perm)
			if perm != "" {
				parts = append(parts, "--allow-perm", perm)
			}
		}
	}

	if m.result.DenyPerms != "" {
		for _, perm := range strings.Split(m.result.DenyPerms, ",") {
			perm = strings.TrimSpace(perm)
			if perm != "" {
				parts = append(parts, "--deny-perm", perm)
			}
		}
	}

	if m.result.Prompt != "" {
		escaped := strings.ReplaceAll(m.result.Prompt, `"`, `\"`)
		parts = append(parts, "-p", `"`+escaped+`"`)
	}

	return strings.Join(parts, " ")
}
