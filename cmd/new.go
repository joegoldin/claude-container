package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	newName            string
	newWorktree        string
	newFrom            string
	newNoWorktree      bool
	newYolo            bool
	newPrompt          string
	newContinue        bool
	newBackground      bool
	newAutoRemove      bool
	newMounts          []string
	newWorkspaceName   string
	newProfile         string
	newAllowDomains    []string
	newDenyPaths       []string
	newNetworkSandbox  string
	newProxyProfile    string
	newProxyPort       int
)

// createOpts holds the resolved options for creating a new session.
type createOpts struct {
	name           string
	worktree       string
	from           string
	noWorktree     bool
	yolo           bool
	prompt         string
	cont           bool // "continue" is a keyword
	background     bool // don't auto-attach after creation
	autoRemove     bool // clean up session when it stops
	mounts         []string // -w flag: ad-hoc folder paths
	workspace      string   // -W flag: named workspace
	profile        string   // --profile flag
	allowDomains   []string // --allow-domain flag
	denyPaths      []string // --deny-path flag
	networkSandbox string
	proxyProfile   string
	proxyPort      int
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
			return createSession(createOpts{
				name:           res.Name,
				worktree:       res.Worktree,
				from:           res.From,
				noWorktree:     res.NoWorktree,
				yolo:           res.Yolo,
				prompt:         res.Prompt,
				background:     res.Background,
				mounts:         newMounts,
				workspace:      newWorkspaceName,
				profile:        newProfile,
				allowDomains:   newAllowDomains,
				denyPaths:      newDenyPaths,
				networkSandbox: newNetworkSandbox,
				proxyProfile:   newProxyProfile,
				proxyPort:      newProxyPort,
			})
		}

		return createSession(createOpts{
			name:           newName,
			worktree:       newWorktree,
			from:           newFrom,
			noWorktree:     newNoWorktree,
			yolo:           newYolo,
			prompt:         newPrompt,
			cont:           newContinue,
			background:     newBackground,
			autoRemove:     newAutoRemove,
			mounts:         newMounts,
			workspace:      newWorkspaceName,
			profile:        newProfile,
			allowDomains:   newAllowDomains,
			denyPaths:      newDenyPaths,
			networkSandbox: newNetworkSandbox,
			proxyProfile:   newProxyProfile,
			proxyPort:      newProxyPort,
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
	newCmd.Flags().StringVar(&newProfile, "profile", "", "Sandbox profile: low, med, high (default \"med\")")
	newCmd.Flags().StringArrayVar(&newAllowDomains, "allow-domain", nil, "Add domain to sandbox allowlist")
	newCmd.Flags().StringArrayVar(&newDenyPaths, "deny-path", nil, "Add path to sandbox deny list")
	newCmd.Flags().StringVar(&newNetworkSandbox, "network-sandbox", "claude",
		"Network enforcement: proxy, claude, both, none")
	newCmd.Flags().StringVar(&newProxyProfile, "proxy-profile", "default",
		"Proxy rule profile name")
	newCmd.Flags().IntVar(&newProxyPort, "proxy-port", 0,
		"Dashboard port on host (0 = auto-assign)")
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
	if opts.yolo && profile != "" && profile != "low" {
		return fmt.Errorf("--yolo and --profile=%s conflict; --yolo is equivalent to --profile=low", profile)
	}
	if opts.yolo {
		profile = "low"
	}
	if profile == "" {
		profile = "med"
	}

	// Resolve network sandbox mode.
	networkSandbox := opts.networkSandbox
	if networkSandbox == "" {
		networkSandbox = "claude"
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

	// Start proxy sidecar if needed.
	proxyProfile := opts.proxyProfile
	if proxyProfile == "" {
		proxyProfile = "default"
	}
	useProxy := networkSandbox == "proxy" || networkSandbox == "both"
	var resolvedPort int
	if useProxy {
		if !httpproxy.ImageExists() {
			// Try loading from tarball
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
		// Wait for CA cert
		if err := httpproxy.WaitForCACert(config.DefaultDir()); err != nil {
			return err
		}
	}

	// e. Resolve workspace directory.
	workspace := cwd
	branch := opts.worktree

	if opts.worktree != "" && !opts.noWorktree {
		worktreeDir := filepath.Join(store.WorktreeDir(), name)

		if opts.from != "" {
			if err := gitpkg.CreateWorktreeFromBranch(repoRoot, worktreeDir, opts.worktree, opts.from); err != nil {
				return fmt.Errorf("create worktree from branch: %w", err)
			}
		} else {
			if err := gitpkg.CreateWorktree(repoRoot, worktreeDir, opts.worktree); err != nil {
				return fmt.Errorf("create worktree: %w", err)
			}
		}

		workspace = worktreeDir
	}

	// For no-worktree mode, try to get the current branch name.
	if opts.noWorktree && repoErr == nil {
		if b, err := gitpkg.CurrentBranch(cwd); err == nil {
			branch = b
		}
	}

	// When extra workspaces are provided, don't mount cwd as primary workspace.
	if len(extraWorkspaces) > 0 {
		workspace = ""
	}

	// f. Ensure shared Claude config dir exists.
	claudeConfigDir := store.ClaudeConfigDir()
	if err := os.MkdirAll(claudeConfigDir, 0o755); err != nil {
		return fmt.Errorf("create claude config dir: %w", err)
	}

	if err := requireAuth(store); err != nil {
		return err
	}

	// Generate managed settings from profile.
	prof, err := sandboxPkg.GetProfile(profile)
	if err != nil {
		return err
	}
	var settingsJSON []byte
	switch networkSandbox {
	case "proxy", "none":
		// Unrestrict Claude's network — proxy (or nothing) handles it.
		settingsJSON, err = json.MarshalIndent(
			prof.ManagedSettingsUnrestricted(opts.denyPaths), "", "  ")
	case "claude", "both":
		// Claude sandbox manages network.
		settingsJSON, err = json.MarshalIndent(
			prof.ManagedSettings(opts.allowDomains, opts.denyPaths), "", "  ")
	default:
		return fmt.Errorf("invalid --network-sandbox value %q (valid: proxy, claude, both, none)", networkSandbox)
	}
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
		UID:             os.Getuid(),
		GID:             os.Getgid(),
		Yolo:            profile == "low",
		Prompt:          opts.prompt,
		Continue:        opts.cont,
		ExtraWorkspaces: extraWorkspaces,
		ProxyProfile: func() string {
			if useProxy {
				return proxyProfile
			}
			return ""
		}(),
		ProxyCACertDir: func() string {
			if useProxy {
				return httpproxy.CACertDir(config.DefaultDir())
			}
			return ""
		}(),
	}

	// g. Save session to store before running so it's tracked even if
	// the user detaches quickly.
	sess := &config.Session{
		Name:            name,
		Branch:          branch,
		WorktreePath:    workspace,
		RepoPath:        repoRoot,
		ContainerName:   docker.ContainerName(name),
		Yolo:            profile == "low",
		AutoRemove:      opts.autoRemove,
		CreatedAt:       time.Now(),
		Profile:         profile,
		ExtraWorkspaces: extraWorkspaces,
		AllowDomains:    opts.allowDomains,
		DenyPaths:       opts.denyPaths,
		NetworkSandbox:  networkSandbox,
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
		StatusBar: proxy.StatusBarInfo{Name: name, Branch: branch, Yolo: profile == "low", ProxyPort: resolvedPort},
		AutoRemove:    opts.autoRemove,
		Cleanup:       func(_ string) { removeSession(store, name) },
	})
	// Save resume ID from container logs so future reattach uses --resume.
	saveResumeID(store, name)
	return proxyErr
}
