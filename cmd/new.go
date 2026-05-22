package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/joegoldin/claude-container/internal/config"
	gitpkg "github.com/joegoldin/claude-container/internal/git"
	"github.com/joegoldin/claude-container/internal/session"
	"github.com/joegoldin/claude-container/internal/tui"
	"github.com/spf13/cobra"
)

var (
	newName          string
	newWorktree      string
	newFrom          string
	newNoWorktree    bool
	newYolo          bool
	newPrompt        string
	newContinue      bool
	newBackground    bool
	newAutoRemove    bool
	newMounts        []string
	newWorkspaceName string
	newProfile       string
	newAllowDomains  []string
	newDenyPaths     []string
	newProxyPreset   string
	newProxyPort     int
	newResume        string
	newAllowCommands []string
	newDenyCommands  []string
	newAllowPerms    []string
	newDenyPerms     []string
	newPackages      []string
)

// createOpts holds the resolved options for creating a new session.
type createOpts struct {
	name            string
	worktree        string
	from            string
	noWorktree      bool
	yolo            bool
	prompt          string
	resume          string
	cont            bool
	background      bool
	autoRemove      bool
	mounts          []string
	workspace       string
	profile         string
	allowDomains    []string
	denyPaths       []string
	allowCommands   []string
	denyCommands    []string
	allowPerms      []string
	denyPerms       []string
	proxySeedPreset string
	proxyPort       int
	packages        []string
}

var newCmd = &cobra.Command{
	Use:   "new",
	Short: "Create a new Claude Code session",
	Long:  `Create a new session with an interactive wizard, or use flags to skip the wizard.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		// If no identifying flags were provided, launch the interactive wizard.
		if newName == "" && newWorktree == "" && !newNoWorktree {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("getwd: %w", err)
			}
			repoPath, _ := gitpkg.RepoRoot(cwd)

			wiz := tui.NewWizard(repoPath, cwd)
			wp := tea.NewProgram(wiz, tea.WithAltScreen())
			wResult, err := wp.Run()
			if err != nil {
				return fmt.Errorf("wizard error: %w", err)
			}
			res := wResult.(tui.WizardModel).Result()
			if res.Cancelled {
				return nil
			}
			// Prefer wizard selections; fall back to CLI flags.
			resolvedProfile := res.Profile
			if resolvedProfile == "" {
				resolvedProfile = newProfile
			}
			resolvedWorkspace := res.Workspace
			if resolvedWorkspace == "" {
				resolvedWorkspace = newWorkspaceName
			}
			resolvedPackages := splitCSV([]string{res.Packages})
			if len(newPackages) > 0 {
				resolvedPackages = newPackages
			}
			resolvedAllowPerms := splitCSV([]string{res.AllowPerms})
			if len(newAllowPerms) > 0 {
				resolvedAllowPerms = newAllowPerms
			}
			resolvedDenyPerms := splitCSV([]string{res.DenyPerms})
			if len(newDenyPerms) > 0 {
				resolvedDenyPerms = newDenyPerms
			}
			return createSession(ctx, createOpts{
				name:            res.Name,
				worktree:        res.Worktree,
				from:            res.From,
				noWorktree:      res.NoWorktree,
				yolo:            res.Yolo,
				prompt:          res.Prompt,
				background:      res.Background,
				mounts:          newMounts,
				workspace:       resolvedWorkspace,
				profile:         resolvedProfile,
				allowDomains:    newAllowDomains,
				denyPaths:       newDenyPaths,
				allowCommands:   newAllowCommands,
				denyCommands:    newDenyCommands,
				allowPerms:      resolvedAllowPerms,
				denyPerms:       resolvedDenyPerms,
				proxySeedPreset: newProxyPreset,
				proxyPort:       newProxyPort,
				packages:        resolvedPackages,
			})
		}

		return createSession(ctx, createOpts{
			name:            newName,
			worktree:        newWorktree,
			from:            newFrom,
			noWorktree:      newNoWorktree,
			yolo:            newYolo,
			prompt:          newPrompt,
			resume:          newResume,
			cont:            newContinue,
			background:      newBackground,
			autoRemove:      newAutoRemove,
			mounts:          newMounts,
			workspace:       newWorkspaceName,
			profile:         newProfile,
			allowDomains:    newAllowDomains,
			denyPaths:       newDenyPaths,
			allowCommands:   newAllowCommands,
			denyCommands:    newDenyCommands,
			allowPerms:      newAllowPerms,
			denyPerms:       newDenyPerms,
			proxySeedPreset: newProxyPreset,
			proxyPort:       newProxyPort,
			packages:        newPackages,
		})
	},
}

func init() {
	newCmd.Flags().StringVar(&newName, "name", "", "Session name")
	newCmd.Flags().StringVar(&newWorktree, "worktree", "", "Create worktree on new branch")
	newCmd.Flags().StringVar(&newFrom, "from", "", "Base branch for worktree (default: current HEAD)")
	newCmd.Flags().BoolVar(&newNoWorktree, "no-worktree", false, "Use current directory directly")
	newCmd.Flags().BoolVar(&newYolo, "yolo", false, "Skip permission prompts")
	newCmd.Flags().StringVarP(&newPrompt, "prompt", "p", "", "Initial prompt to send to Claude")
	newCmd.Flags().StringVar(&newResume, "resume", "", "Resume a previous conversation (pass ID or empty for picker)")
	newCmd.Flags().Lookup("resume").NoOptDefVal = "__picker__"
	newCmd.Flags().BoolVarP(&newContinue, "continue", "c", false, "Resume previous conversation")
	newCmd.Flags().BoolVarP(&newBackground, "background", "b", false, "Don't attach after creation")
	newCmd.Flags().BoolVar(&newAutoRemove, "rm", false, "Auto-remove session when it exits")
	newCmd.Flags().StringArrayVarP(&newMounts, "mount", "w", nil, "Additional folders to mount (repeatable)")
	newCmd.Flags().StringVarP(&newWorkspaceName, "workspace", "W", "", "Named workspace from workspaces.json")
	newCmd.Flags().StringVar(&newProfile, "profile", "", "Sandbox profile: low, default, med, high (default \"default\")")
	newCmd.Flags().StringArrayVar(&newAllowDomains, "allow-domain", nil, "Add domain to proxy allowlist")
	newCmd.Flags().StringArrayVar(&newDenyPaths, "deny-path", nil, "Add path to permissions deny list")
	newCmd.Flags().StringArrayVar(&newAllowCommands, "allow-command", nil, "Add command pattern to allow list (e.g., 'docker *')")
	newCmd.Flags().StringArrayVar(&newDenyCommands, "deny-command", nil, "Add command pattern to deny list (e.g., 'rm -rf *')")
	newCmd.Flags().StringArrayVar(&newAllowPerms, "allow-perm", nil, "Add raw permission allow rule (e.g., 'Bash(docker *)', 'Read')")
	newCmd.Flags().StringArrayVar(&newDenyPerms, "deny-perm", nil, "Add raw permission deny rule (e.g., 'Read(/etc/**)')")
	newCmd.Flags().StringVar(&newProxyPreset, "preset", "",
		"Seed the proxy with rules from a saved preset name")
	newCmd.Flags().IntVar(&newProxyPort, "proxy-port", 0,
		"Dashboard port on host (0 = auto-assign)")
	newCmd.Flags().StringSliceVar(&newPackages, "packages", nil, "Comma-separated nixpkgs to install (e.g., rustup,bun)")
	rootCmd.AddCommand(newCmd)
}

// createSession builds session.Opts from createOpts, runs the launcher,
// and dispatches to AttachTTY or RunBackground.
//
// It performs the command-layer concerns (resume+continue conflict, yolo
// profile compatibility, name derivation from --worktree, MigrateToPerRepo
// one-shot, env-var-driven extra allow commands) before delegating the
// container/proxy/config wiring to session.Launch.
func createSession(ctx context.Context, opts createOpts) error {
	if opts.resume != "" && opts.cont {
		return fmt.Errorf("--resume and --continue cannot be used together")
	}

	profile := opts.profile
	if opts.yolo && profile != "" && profile != "low" && profile != "default" {
		return fmt.Errorf("--yolo and --profile=%s conflict; --yolo implies a yolo profile (low or default)", profile)
	}

	// Derive name from --worktree when --name is missing (matches the old
	// "name = SanitizeName(worktree)" fallback).
	name := opts.name
	if name == "" && opts.worktree != "" {
		name = config.SanitizeName(opts.worktree)
	}

	store := config.NewStore(config.DefaultDir())

	// One-time migration of shared conversation history to per-repo storage.
	if n, err := config.MigrateToPerRepo(store); err != nil {
		fmt.Fprintf(os.Stderr, "warning: migration failed: %v\n", err)
	} else if n > 0 {
		fmt.Fprintf(os.Stderr, "Migrated %d conversations to per-repo storage\n", n)
	}

	if name != "" {
		if _, err := store.Get(name); err == nil {
			return fmt.Errorf("session %q already exists", name)
		}
	}

	if err := requireAuth(store); err != nil {
		return err
	}

	// Determine WorktreeMode. --worktree without --no-worktree means
	// explicit worktree (use worktree string as branch name); --no-worktree
	// forces pwd; otherwise let Auto decide (used by `new` defaults).
	worktreeMode := session.WorktreeAuto
	if opts.noWorktree {
		worktreeMode = session.WorktreeNever
	} else if opts.worktree != "" {
		worktreeMode = session.WorktreeAlways
	}

	// Merge env-var allow commands when profile permits (skipped for high).
	allowCommands := opts.allowCommands
	if profile != "high" {
		if envCmds := envExtraAllowCommands(); len(envCmds) > 0 {
			allowCommands = append(append([]string(nil), envCmds...), allowCommands...)
		}
	}

	sessOpts := session.Opts{
		Name:            name,
		Mode:            session.ModeTTY,
		WorktreeMode:    worktreeMode,
		NoWorktree:      opts.noWorktree,
		From:            opts.from,
		WorktreeName:    opts.worktree,
		Profile:         profile,
		Yolo:            opts.yolo,
		AllowDomains:    opts.allowDomains,
		DenyPaths:       opts.denyPaths,
		AllowCommands:   allowCommands,
		DenyCommands:    opts.denyCommands,
		AllowPerms:      opts.allowPerms,
		DenyPerms:       opts.denyPerms,
		Mounts:          opts.mounts,
		WorkspaceName:   opts.workspace,
		AutoRemove:      opts.autoRemove,
		Background:      opts.background,
		Prompt:          opts.prompt,
		Resume:          opts.resume,
		Continue:        opts.cont,
		Packages:        opts.packages,
		ProxySeedPreset: opts.proxySeedPreset,
		ProxyPort:       opts.proxyPort,
	}

	h, err := session.Launch(ctx, store, sessOpts)
	if err != nil {
		return err
	}

	if opts.background {
		fmt.Printf("Session %q created (background).\n", h.Name)
		fmt.Printf("  Attach: claude-container attach %s\n", h.Name)
		return h.RunBackground()
	}
	fmt.Printf("Session %q created.\n", h.Name)
	attachErr := h.AttachTTY()
	saveResumeID(store, h.Name)
	return attachErr
}

// envExtraAllowCommands reads command patterns from the
// CLAUDE_CONTAINER_EXTRA_ALLOW_COMMANDS environment variable (JSON string array).
// Returns nil when the variable is unset or empty.
func envExtraAllowCommands() []string {
	raw := os.Getenv("CLAUDE_CONTAINER_EXTRA_ALLOW_COMMANDS")
	if raw == "" {
		return nil
	}
	var cmds []string
	if err := json.Unmarshal([]byte(raw), &cmds); err != nil {
		return nil
	}
	return cmds
}

// wrapCommandPerms wraps bare command patterns as Bash() permission rules.
// Example: "docker *" → "Bash(docker *)". Still used by cmd/attach.go for
// the recreate-on-missing-container path.
func wrapCommandPerms(commands []string) []string {
	if len(commands) == 0 {
		return nil
	}
	perms := make([]string, len(commands))
	for i, cmd := range commands {
		perms[i] = fmt.Sprintf("Bash(%s)", cmd)
	}
	return perms
}

