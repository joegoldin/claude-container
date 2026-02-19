package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/docker"
	"github.com/joegoldin/claude-container/internal/proxy"
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
		if err := ensureRunning(store, name, sess); err != nil {
			return err
		}

		if attachBackground {
			fmt.Printf("Session %q is running (background).\n", name)
			return nil
		}

		if attachDashboard {
			return rootCmd.RunE(cmd, nil)
		}

		containerName := docker.ContainerName(name)
		proxyErr := proxy.Run(proxy.Opts{
			DockerArgs:    []string{"attach", containerName},
			ContainerName: containerName,
			StatusBar:     proxy.StatusBarInfo{Name: name, Branch: sess.Branch, Yolo: sess.Yolo},
			AutoRemove:    sess.AutoRemove,
			Cleanup:       func(_ string) { removeSession(store, name) },
		})
		saveResumeID(store, name)
		return proxyErr
	},
}

// ensureRunning makes sure the container for the given session is running,
// starting or recreating it as needed.
func ensureRunning(store *config.Store, name string, sess *config.Session) error {
	switch {
	case docker.IsRunning(name):
		return nil
	case docker.Exists(name):
		fmt.Println("Restarting stopped container...")
		if err := docker.Start(name); err != nil {
			return fmt.Errorf("start container: %w", err)
		}
		return nil
	default:
		if sess.ResumeID != "" {
			fmt.Printf("Recreating container with --resume %s...\n", sess.ResumeID)
		} else {
			fmt.Println("Recreating container with --continue...")
		}
		detachedArgs := docker.RunArgs(docker.RunOpts{
			Name:           name,
			Workspace:      sess.WorktreePath,
			ConfigDir:      store.ClaudeConfigDir(),
			HostClaudeDir:  config.HostClaudeDir(),
			HostClaudeJSON: config.HostClaudeJSON(),
			UID:            os.Getuid(),
			GID:            os.Getgid(),
			Yolo:           sess.Yolo,
			Resume:         sess.ResumeID,
			Continue:       sess.ResumeID == "",
		}, true)
		startCmd := exec.Command("docker", detachedArgs...)
		startCmd.Stderr = os.Stderr
		if err := startCmd.Run(); err != nil {
			return fmt.Errorf("recreate container: %w", err)
		}
		return nil
	}
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
