package session

import "fmt"

// Mode identifies what kind of output adapter the caller wants.
type Mode string

const (
	ModeTTY        Mode = "tty"
	ModeACP        Mode = "acp"
	ModeTask       Mode = "task"
	ModeBackground Mode = "background"
)

// WorktreeMode selects how ResolveWorkspace handles git repos.
type WorktreeMode int

const (
	// WorktreeAuto creates a worktree in <repo>/.worktrees/<name>/ when in a
	// git repo, otherwise pwd passthrough. Used by bare invoke.
	WorktreeAuto WorktreeMode = iota
	// WorktreeAlways creates a worktree even if cwd is the repo root. Used by `work`.
	WorktreeAlways
	// WorktreeNever forces pwd passthrough. Used by `run` and `acp`.
	WorktreeNever
)

// Opts holds everything Launch needs to start a session.
type Opts struct {
	Name string // session name; auto-generated if empty
	Mode Mode

	// Workspace controls.
	Cwd          string
	WorktreeMode WorktreeMode
	NoWorktree   bool   // legacy alias used by --no-worktree flag; if true forces WorktreeNever
	From         string // base ref for worktree branch
	WorktreeName string // explicit branch name; empty = use Name

	// Sandbox profile and overrides.
	Profile       string
	Yolo          bool
	AllowDomains  []string
	DenyPaths     []string
	AllowCommands []string
	DenyCommands  []string
	AllowPerms    []string
	DenyPerms     []string

	// Mounts.
	Mounts        []string // -w (ad-hoc paths)
	WorkspaceName string   // -W (named workspace)

	// Container behavior.
	AutoRemove bool
	Background bool

	// Claude Code controls.
	Prompt   string
	Resume   string
	Continue bool

	// Packages and proxy.
	Packages        []string
	ProxySeedPreset string
	ProxyPort       int
}

// ApplyDefaults fills in per-mode defaults for fields the caller did not set.
// Fields explicitly set by the caller are preserved.
func (o *Opts) ApplyDefaults() {
	if o.NoWorktree {
		o.WorktreeMode = WorktreeNever
	}
	switch o.Mode {
	case ModeACP:
		// ACP is always pwd passthrough and always ephemeral.
		o.WorktreeMode = WorktreeNever
		o.NoWorktree = true
		if !o.AutoRemove {
			o.AutoRemove = true
		}
		if o.Profile == "" {
			o.Profile = "med"
		}
	case ModeTask:
		if !o.AutoRemove {
			o.AutoRemove = true
		}
		if o.Profile == "" {
			o.Profile = "default"
		}
	case ModeBackground, ModeTTY, "":
		if o.Profile == "" {
			o.Profile = "default"
		}
	}
}

// Validate returns an error when Opts is internally inconsistent.
func (o *Opts) Validate() error {
	if o.Resume != "" && o.Continue {
		return fmt.Errorf("resume and continue cannot both be set")
	}
	switch o.Mode {
	case ModeTTY, ModeACP, ModeTask, ModeBackground, "":
		// ok
	default:
		return fmt.Errorf("unknown mode %q", o.Mode)
	}
	return nil
}
