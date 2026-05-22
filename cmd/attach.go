package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/docker"
	"github.com/joegoldin/claude-container/internal/httpproxy"
	"github.com/joegoldin/claude-container/internal/proxy"
	"github.com/joegoldin/claude-container/internal/session"
	"github.com/spf13/cobra"
)

var (
	attachBackground bool
	attachDashboard  bool
)

var attachCmd = &cobra.Command{
	Use:               "attach <session>",
	Short:             "Attach to a running session",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		name := args[0]

		store := config.NewStore(config.DefaultDir())
		if err := requireAuth(store); err != nil {
			return err
		}
		sess, err := store.Get(name)
		if err != nil {
			return fmt.Errorf("session %q not found", name)
		}

		// Ensure the container is running (start/recreate as needed).
		if err := ensureRunning(ctx, store, name, sess); err != nil {
			return err
		}

		if attachBackground {
			fmt.Printf("Session %q is running (background).\n", name)
			return nil
		}

		if attachDashboard {
			return runTUI(cmd.Context())
		}

		containerName := docker.ContainerName(name)
		proxyErr := proxy.Run(proxy.Opts{
			DockerArgs:    []string{"attach", containerName},
			ContainerName: containerName,
			StatusBar:     proxy.StatusBarInfo{Name: name, Branch: sess.Branch, Yolo: sess.Yolo, ProxyPort: sess.ProxyPort},
			AutoRemove:    sess.AutoRemove,
			Cleanup:       func(_ string) { removeSession(store, name) },
		})
		saveResumeID(store, name)
		if sess.RepoPath != "" {
			if err := store.SaveNewConversations(name, sess.RepoPath); err != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to save conversations: %v\n", err)
			}
		}
		return proxyErr
	},
}

// ensureRunning makes sure the container for the given session is running,
// starting it (cheap) or recreating it via session.Launch (expensive) as
// needed. The proxy is always ensured.
func ensureRunning(ctx context.Context, store *config.Store, name string, sess *config.Session) error {
	// Always ensure the per-session proxy is running. Rules file is only
	// seeded if it doesn't already exist; user-added rules accumulated in
	// a previous attach are preserved.
	if err := httpproxy.EnsureSessionRules(config.DefaultDir(), name, sess.ProxySeedPreset); err != nil {
		return fmt.Errorf("seed proxy rules: %w", err)
	}
	_, resolvedPort, err := httpproxy.EnsureRunning(httpproxy.ProxyOpts{
		Session:       name,
		ConfigDir:     config.DefaultDir(),
		DashboardPort: sess.ProxyPort,
	})
	if err != nil {
		return fmt.Errorf("start proxy: %w", err)
	}
	if resolvedPort > 0 {
		sess.ProxyPort = resolvedPort
	}

	switch {
	case docker.IsRunning(name):
		return nil
	case docker.Exists(name):
		fmt.Println("Restarting stopped container...")
		if err := docker.Start(name); err != nil {
			return fmt.Errorf("start container: %w", err)
		}
		return nil
	}

	// Container is missing — recreate via session.Launch. We pass the
	// stored worktree path as Cwd and force WorktreeNever so Launch mounts
	// it directly rather than trying to (re-)create a worktree the
	// entrypoint already provisioned.
	cwd := sess.WorktreePath
	if cwd == "" {
		cwd = sess.RepoPath
	}
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("getwd: %w", err)
		}
	}

	if sess.ResumeID != "" {
		fmt.Printf("Recreating container with --resume %s...\n", sess.ResumeID)
	} else {
		fmt.Println("Recreating container with --continue...")
	}

	opts := session.Opts{
		Name:            name,
		Mode:            session.ModeTTY,
		Cwd:             cwd,
		WorktreeMode:    session.WorktreeNever,
		NoWorktree:      true,
		Profile:         sess.Profile,
		Yolo:            sess.Yolo,
		AllowDomains:    sess.AllowDomains,
		DenyPaths:       sess.DenyPaths,
		AllowCommands:   sess.AllowCommands,
		DenyCommands:    sess.DenyCommands,
		AllowPerms:      sess.AllowPerms,
		DenyPerms:       sess.DenyPerms,
		Mounts:          sess.ExtraWorkspaces,
		Resume:          sess.ResumeID,
		Continue:        sess.ResumeID == "",
		Packages:        sess.Packages,
		AutoRemove:      sess.AutoRemove,
		ProxySeedPreset: sess.ProxySeedPreset,
		ProxyPort:       sess.ProxyPort,
	}

	// session.Launch starts the container detached but also returns a
	// Handle whose cleanup closure would tear the container down on exit.
	// For ensureRunning we just want the container started — discard the
	// Handle (no Cleanup call). The container is then attached to by the
	// caller via proxy.Run.
	if _, err := session.Launch(ctx, store, opts); err != nil {
		return fmt.Errorf("recreate container: %w", err)
	}
	return nil
}

func init() {
	attachCmd.Flags().BoolVarP(&attachBackground, "background", "b", false, "Start container in background without attaching")
	attachCmd.Flags().BoolVarP(&attachDashboard, "dashboard", "d", false, "Start container then open the TUI dashboard")
	rootCmd.AddCommand(attachCmd)
}

// completeSessionNames provides tab completion for session names.
func completeSessionNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	store := config.NewStore(config.DefaultDir())
	return store.Names(), cobra.ShellCompDirectiveNoFileComp
}
