package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/docker"
	gitpkg "github.com/joegoldin/claude-container/internal/git"
	"github.com/joegoldin/claude-container/internal/proxy"
	"github.com/joegoldin/claude-container/internal/tui"
	"github.com/spf13/cobra"
)

var (
	newName       string
	newWorktree   string
	newFrom       string
	newNoWorktree bool
	newYolo       bool
	newPrompt     string
	newContinue   bool
	newBackground  bool
	newAutoRemove  bool
)

// createOpts holds the resolved options for creating a new session.
type createOpts struct {
	name       string
	worktree   string
	from       string
	noWorktree bool
	yolo       bool
	prompt     string
	cont       bool // "continue" is a keyword
	background bool // don't auto-attach after creation
	autoRemove bool // clean up session when it stops
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
				name:       res.Name,
				worktree:   res.Worktree,
				from:       res.From,
				noWorktree: res.NoWorktree,
				yolo:       res.Yolo,
				prompt:     res.Prompt,
				background: res.Background,
			})
		}

		return createSession(createOpts{
			name:       newName,
			worktree:   newWorktree,
			from:       newFrom,
			noWorktree: newNoWorktree,
			yolo:       newYolo,
			prompt:     newPrompt,
			cont:       newContinue,
			background: newBackground,
			autoRemove: newAutoRemove,
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
	rootCmd.AddCommand(newCmd)
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

	// d. Check docker image exists; auto-build if possible.
	if !docker.ImageExists() {
		contextDir := os.Getenv("CLAUDE_CONTAINER_DOCKER_CONTEXT")
		if contextDir == "" {
			return fmt.Errorf("docker image %q not found; run 'claude-container build' first or set CLAUDE_CONTAINER_DOCKER_CONTEXT", docker.ImageName)
		}
		fmt.Println("Docker image not found, building automatically...")
		if err := docker.Build(contextDir).Run(); err != nil {
			return fmt.Errorf("auto-build failed: %w", err)
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

	// f. Ensure shared Claude config dir exists.
	claudeConfigDir := store.ClaudeConfigDir()
	if err := os.MkdirAll(claudeConfigDir, 0o755); err != nil {
		return fmt.Errorf("create claude config dir: %w", err)
	}

	if err := requireAuth(store); err != nil {
		return err
	}

	runOpts := docker.RunOpts{
		Name:           name,
		Workspace:      workspace,
		ConfigDir:      claudeConfigDir,
		HostClaudeDir:  config.HostClaudeDir(),
		HostClaudeJSON: config.HostClaudeJSON(),
		UID:            os.Getuid(),
		GID:            os.Getgid(),
		Yolo:           opts.yolo,
		Prompt:         opts.prompt,
		Continue:       opts.cont,
	}

	// g. Save session to store before running so it's tracked even if
	// the user detaches quickly.
	sess := &config.Session{
		Name:          name,
		Branch:        branch,
		WorktreePath:  workspace,
		RepoPath:      repoRoot,
		ContainerName: docker.ContainerName(name),
		Yolo:          opts.yolo,
		AutoRemove:    opts.autoRemove,
		CreatedAt:     time.Now(),
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
	return proxy.Run(proxy.Opts{
		DockerArgs:    []string{"attach", containerName},
		ContainerName: containerName,
		StatusBar:     proxy.StatusBarInfo{Name: name, Branch: branch, Yolo: opts.yolo},
		AutoRemove:    opts.autoRemove,
		Cleanup:       func(_ string) { removeSession(store, name) },
	})
}
