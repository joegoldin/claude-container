package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/docker"
	gitpkg "github.com/joegoldin/claude-container/internal/git"
	"github.com/joegoldin/claude-container/internal/tmux"
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
}

var newCmd = &cobra.Command{
	Use:   "new",
	Short: "Create a new Claude Code session",
	Long:  `Create a new session with an interactive wizard, or use flags to skip the wizard.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return createSession(createOpts{
			name:       newName,
			worktree:   newWorktree,
			from:       newFrom,
			noWorktree: newNoWorktree,
			yolo:       newYolo,
			prompt:     newPrompt,
			cont:       newContinue,
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

	// f. Build docker run args.
	configDir := config.DefaultDir()
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	dockerArgs := docker.RunArgs(docker.RunOpts{
		Name:      name,
		Workspace: workspace,
		ConfigDir: configDir,
		UID:       os.Getuid(),
		GID:       os.Getgid(),
		Yolo:      opts.yolo,
		Prompt:    opts.prompt,
		Continue:  opts.cont,
	})

	// g. Create tmux session running docker.
	fullCmd := append([]string{"docker"}, dockerArgs...)
	if err := tmux.Create(name, workspace, fullCmd); err != nil {
		return fmt.Errorf("create tmux session: %w", err)
	}

	// h. Save session to store.
	sess := &config.Session{
		Name:          name,
		Branch:        branch,
		WorktreePath:  workspace,
		RepoPath:      repoRoot,
		ContainerName: docker.ContainerName(name),
		TmuxSession:   tmux.SessionName(name),
		Yolo:          opts.yolo,
		CreatedAt:     time.Now(),
	}
	if err := store.Save(sess); err != nil {
		return fmt.Errorf("save session: %w", err)
	}

	// i. Print success.
	fmt.Printf("Session %q created.\n", name)
	fmt.Printf("  Attach: claude-container attach %s\n", name)
	return nil
}
