package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/docker"
	gitpkg "github.com/joegoldin/claude-container/internal/git"
	"github.com/joegoldin/claude-container/internal/httpproxy"
	"github.com/joegoldin/claude-container/internal/proxy"
	sandboxPkg "github.com/joegoldin/claude-container/internal/sandbox"
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
	newProxyProfile   string
	newProxyPort      int
	newAllowCommands  []string
	newDenyCommands   []string
	newPackages       []string
)

// createOpts holds the resolved options for creating a new session.
type createOpts struct {
	name         string
	worktree     string
	from         string
	noWorktree   bool
	yolo         bool
	prompt       string
	cont         bool     // "continue" is a keyword
	background   bool     // don't auto-attach after creation
	autoRemove   bool     // clean up session when it stops
	mounts       []string // -w flag: ad-hoc folder paths
	workspace    string   // -W flag: named workspace
	profile      string   // --profile flag
	allowDomains  []string // --allow-domain flag
	denyPaths     []string // --deny-path flag
	allowCommands []string // --allow-command flag
	denyCommands  []string // --deny-command flag
	proxyProfile  string
	proxyPort     int
	packages      []string // --packages flag
}

var newCmd = &cobra.Command{
	Use:   "new",
	Short: "Create a new Claude Code session",
	Long:  `Create a new session with an interactive wizard, or use flags to skip the wizard.`,
	RunE: func(cmd *cobra.Command, args []string) error {
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
			var wizPackages []string
			if res.Packages != "" {
				for _, p := range strings.Split(res.Packages, ",") {
					p = strings.TrimSpace(p)
					if p != "" {
						wizPackages = append(wizPackages, p)
					}
				}
			}
			resolvedPackages := wizPackages
			if len(newPackages) > 0 {
				resolvedPackages = newPackages
			}
			return createSession(createOpts{
				name:          res.Name,
				worktree:      res.Worktree,
				from:          res.From,
				noWorktree:    res.NoWorktree,
				yolo:          res.Yolo,
				prompt:        res.Prompt,
				background:    res.Background,
				mounts:        newMounts,
				workspace:     resolvedWorkspace,
				profile:       resolvedProfile,
				allowDomains:  newAllowDomains,
				denyPaths:     newDenyPaths,
				allowCommands: newAllowCommands,
				denyCommands:  newDenyCommands,
				proxyProfile:  newProxyProfile,
				proxyPort:     newProxyPort,
				packages:      resolvedPackages,
			})
		}

		return createSession(createOpts{
			name:          newName,
			worktree:      newWorktree,
			from:          newFrom,
			noWorktree:    newNoWorktree,
			yolo:          newYolo,
			prompt:        newPrompt,
			cont:          newContinue,
			background:    newBackground,
			autoRemove:    newAutoRemove,
			mounts:        newMounts,
			workspace:     newWorkspaceName,
			profile:       newProfile,
			allowDomains:  newAllowDomains,
			denyPaths:     newDenyPaths,
			allowCommands: newAllowCommands,
			denyCommands:  newDenyCommands,
			proxyProfile:  newProxyProfile,
			proxyPort:     newProxyPort,
			packages:      newPackages,
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
	newCmd.Flags().StringVar(&newProxyProfile, "proxy-profile", "default",
		"Proxy rule profile name")
	newCmd.Flags().IntVar(&newProxyPort, "proxy-port", 0,
		"Dashboard port on host (0 = auto-assign)")
	newCmd.Flags().StringSliceVar(&newPackages, "packages", nil, "Comma-separated nixpkgs to install (e.g., rust,nodejs)")
	rootCmd.AddCommand(newCmd)
}

// resolveWorkspaces merges named workspace paths with ad-hoc mount paths,
// validates all paths exist, and checks for basename collisions.
func resolveWorkspaces(workspaceName string, mounts []string) ([]string, error) {
	var paths []string

	if workspaceName != "" {
		ws := config.NewWorkspaceStore(config.DefaultDir())
		wsPaths, err := ws.Get(workspaceName)
		if err != nil {
			return nil, err
		}
		paths = append(paths, wsPaths...)
	}

	for _, m := range mounts {
		abs, err := filepath.Abs(m)
		if err != nil {
			return nil, fmt.Errorf("resolve path %q: %w", m, err)
		}
		paths = append(paths, abs)
	}

	if len(paths) == 0 {
		return nil, nil
	}

	seen := make(map[string]string)
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			return nil, fmt.Errorf("workspace path %q does not exist", p)
		}
		base := filepath.Base(p)
		if existing, ok := seen[base]; ok {
			return nil, fmt.Errorf("basename collision: %q and %q both have basename %q", existing, p, base)
		}
		seen[base] = p
	}

	return paths, nil
}

func createSession(opts createOpts) error {
	// a. Get cwd and resolve repo root.
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}

	repoRoot, repoErr := gitpkg.RepoRoot(cwd)
	if repoErr != nil && !opts.noWorktree {
		return fmt.Errorf("not inside a git repository (use --no-worktree to skip worktree creation): %w", repoErr)
	}

	// Resolve extra workspaces from -W and -w flags.
	extraWorkspaces, err := resolveWorkspaces(opts.workspace, opts.mounts)
	if err != nil {
		return err
	}

	// Resolve sandbox profile.
	profile := opts.profile
	if opts.yolo && profile != "" && profile != "low" && profile != "default" {
		return fmt.Errorf("--yolo and --profile=%s conflict; --yolo implies a yolo profile (low or default)", profile)
	}
	if opts.yolo && profile == "" {
		profile = "default"
	}
	if profile == "" {
		profile = "default"
	}

	// b. Determine session name.
	name := opts.name
	if name == "" && opts.worktree != "" {
		name = config.SanitizeName(opts.worktree)
	}
	if name == "" {
		return fmt.Errorf("session name is required: use --name or --worktree")
	}

	// c. Check no existing session with that name.
	store := config.NewStore(config.DefaultDir())
	if _, err := store.Get(name); err == nil {
		return fmt.Errorf("session %q already exists", name)
	}

	// d. Ensure docker image is loaded.
	if err := docker.EnsureImage(config.DefaultDir()); err != nil {
		return err
	}

	// Start proxy sidecar (always).
	proxyProfile := opts.proxyProfile
	if proxyProfile == "" {
		proxyProfile = "default"
	}
	if !httpproxy.ImageExists() {
		tarball := os.Getenv("CLAUDE_PROXY_IMAGE_TARBALL")
		if tarball != "" {
			loadCmd := exec.Command("docker", "load", "-i", tarball)
			loadCmd.Stdout = os.Stdout
			loadCmd.Stderr = os.Stderr
			if err := loadCmd.Run(); err != nil {
				return fmt.Errorf("load proxy image: %w", err)
			}
		} else {
			return fmt.Errorf("proxy image %q not found; set CLAUDE_PROXY_IMAGE_TARBALL", httpproxy.ImageTag())
		}
	}

	// Write proxy rules from profile before starting proxy.
	prof, err := sandboxPkg.GetProfile(profile)
	if err != nil {
		return err
	}
	proxyRules := prof.ProxyRules(opts.allowDomains)
	rulesPath := httpproxy.ProfileRulesPath(config.DefaultDir(), proxyProfile)
	if err := os.MkdirAll(filepath.Dir(rulesPath), 0o755); err != nil {
		return fmt.Errorf("create proxy rules dir: %w", err)
	}
	rulesJSON, err := json.MarshalIndent(proxyRules, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal proxy rules: %w", err)
	}
	if err := os.WriteFile(rulesPath, rulesJSON, 0o644); err != nil {
		return fmt.Errorf("write proxy rules: %w", err)
	}

	var resolvedPort int
	var started bool
	started, resolvedPort, err = httpproxy.EnsureRunning(httpproxy.ProxyOpts{
		Profile:       proxyProfile,
		ConfigDir:     config.DefaultDir(),
		DashboardPort: opts.proxyPort,
	})
	if err != nil {
		return fmt.Errorf("start proxy: %w", err)
	}
	if started {
		fmt.Printf("Proxy started for profile %q — dashboard at %s\n",
			proxyProfile, httpproxy.DashboardURL(resolvedPort))
	} else {
		fmt.Printf("Reusing proxy for profile %q — dashboard at %s\n",
			proxyProfile, httpproxy.DashboardURL(resolvedPort))
	}
	if err := httpproxy.WaitForCACert(config.DefaultDir()); err != nil {
		return err
	}

	// e. Resolve workspace directory.
	workspace := cwd
	branch := opts.worktree

	if opts.worktree != "" && !opts.noWorktree {
		// Don't create worktree on host — the container entrypoint will
		// create it from the mounted repo at /mnt/repo.
		workspace = ""
	}

	// For no-worktree mode, try to get the current branch name.
	if opts.noWorktree && repoErr == nil {
		if b, err := gitpkg.CurrentBranch(cwd); err == nil {
			branch = b
		}
	}

	// When extra workspaces are provided, don't mount cwd as primary workspace.
	// In worktree mode, extra workspaces that are git repos become worktree
	// repos (mounted at /mnt/repos/ with worktrees at /workspace/).
	var worktreeRepos []string
	if len(extraWorkspaces) > 0 {
		workspace = ""
		if opts.worktree != "" && !opts.noWorktree {
			// All extra workspaces become worktree repos.
			for _, ws := range extraWorkspaces {
				if _, err := gitpkg.RepoRoot(ws); err != nil {
					return fmt.Errorf("worktree mode: %q is not a git repository", ws)
				}
				worktreeRepos = append(worktreeRepos, ws)
			}
			extraWorkspaces = nil // don't direct-mount, entrypoint creates worktrees
		}
	}

	// f. Ensure shared Claude config dir exists.
	claudeConfigDir := store.ClaudeConfigDir()
	if err := os.MkdirAll(claudeConfigDir, 0o755); err != nil {
		return fmt.Errorf("create claude config dir: %w", err)
	}

	if err := requireAuth(store); err != nil {
		return err
	}

	// Build extra allow perms: env var commands (skip for high profile) + CLI flags.
	var extraAllowPerms []string
	if profile != "high" {
		extraAllowPerms = append(extraAllowPerms, wrapCommandPerms(envExtraAllowCommands())...)
	}
	extraAllowPerms = append(extraAllowPerms, wrapCommandPerms(opts.allowCommands)...)

	// Build extra deny perms: --deny-path as Read() rules + --deny-command as Bash() rules.
	var extraDenyPerms []string
	for _, p := range opts.denyPaths {
		extraDenyPerms = append(extraDenyPerms, fmt.Sprintf("Read(%s)", p))
	}
	extraDenyPerms = append(extraDenyPerms, wrapCommandPerms(opts.denyCommands)...)

	settingsJSON, err := json.MarshalIndent(
		prof.ManagedSettingsForProxy(8080, extraAllowPerms, extraDenyPerms), "", "  ")
	if err != nil {
		return err
	}
	settingsPath := filepath.Join(claudeConfigDir, "managed-settings.json")
	if err := os.WriteFile(settingsPath, settingsJSON, 0o644); err != nil {
		return fmt.Errorf("write managed settings: %w", err)
	}

	runOpts := docker.RunOpts{
		Name:            name,
		Workspace:       workspace,
		ConfigDir:       claudeConfigDir,
		HostClaudeDir:   config.HostClaudeDir(),
		HostClaudeJSON:  config.HostClaudeJSON(),
		UID:             docker.ContainerUID(),
		GID:             docker.ContainerGID(),
		Yolo:            prof.Yolo,
		Prompt:          opts.prompt,
		Continue:        opts.cont,
		ExtraWorkspaces: extraWorkspaces,
		ProxyProfile:    proxyProfile,
		ProxyCACertDir:  httpproxy.CACertDir(config.DefaultDir()),
		Packages:        opts.packages,
	}

	// When using worktree mode, pass repo info so the container entrypoint
	// creates the worktree inside the container (fixing broken .git refs).
	if opts.worktree != "" && !opts.noWorktree {
		runOpts.WorktreeBranch = branch
		runOpts.WorktreeFrom = opts.from
		if len(worktreeRepos) > 0 {
			// Multi-repo: worktrees at /workspace/<basename> for each repo.
			// Don't mount primary repo at /mnt/repo (would conflict with /workspace/).
			runOpts.WorktreeRepos = worktreeRepos
		} else {
			// Single-repo: worktree at /workspace from cwd repo.
			runOpts.RepoPath = repoRoot
		}
	}

	// g. Save session to store before running so it's tracked even if
	// the user detaches quickly.
	// For container-created worktrees, WorktreePath is empty (no host path).
	worktreePath := workspace
	if opts.worktree != "" && !opts.noWorktree {
		worktreePath = ""
	}
	sess := &config.Session{
		Name:            name,
		Branch:          branch,
		WorktreePath:    worktreePath,
		RepoPath:        repoRoot,
		ContainerName:   docker.ContainerName(name),
		Yolo:            prof.Yolo,
		AutoRemove:      opts.autoRemove,
		CreatedAt:       time.Now(),
		Profile:         profile,
		ExtraWorkspaces: extraWorkspaces,
		WorktreeRepos:   worktreeRepos,
		AllowDomains:    opts.allowDomains,
		DenyPaths:       opts.denyPaths,
		AllowCommands:   opts.allowCommands,
		DenyCommands:    opts.denyCommands,
		Packages:        opts.packages,
		ProxyProfile:    proxyProfile,
		ProxyPort:       resolvedPort,
	}
	if err := store.Save(sess); err != nil {
		return fmt.Errorf("save session: %w", err)
	}

	// h. Background mode: start detached, return immediately.
	if opts.background {
		dockerArgs := docker.RunArgs(runOpts, true)
		if err := docker.RunDetached(dockerArgs); err != nil {
			return fmt.Errorf("create container: %w", err)
		}
		fmt.Printf("Session %q created (background).\n", name)
		fmt.Printf("  Attach: claude-container attach %s\n", name)
		return nil
	}

	// i. Foreground mode: start container detached, then attach via proxy.
	// Starting detached allows Ctrl+B d to detach without killing the container.
	containerName := docker.ContainerName(name)
	detachedArgs := docker.RunArgs(runOpts, true)
	startCmd := exec.Command("docker", detachedArgs...)
	startCmd.Stderr = os.Stderr
	if err := startCmd.Run(); err != nil {
		return fmt.Errorf("start container: %w", err)
	}
	fmt.Printf("Session %q created.\n", name)
	proxyErr := proxy.Run(proxy.Opts{
		DockerArgs:    []string{"attach", containerName},
		ContainerName: containerName,
		StatusBar:     proxy.StatusBarInfo{Name: name, Branch: branch, Yolo: prof.Yolo, ProxyPort: resolvedPort},
		AutoRemove:    opts.autoRemove,
		Cleanup:       func(_ string) { removeSession(store, name) },
	})
	// Save resume ID from container logs so future reattach uses --resume.
	saveResumeID(store, name)
	return proxyErr
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
// Example: "docker *" → "Bash(docker *)".
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
