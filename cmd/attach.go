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

		containerName := docker.ContainerName(name)

		switch {
		case docker.IsRunning(name):
			fmt.Println("Attaching...")
		case docker.Exists(name):
			fmt.Println("Restarting stopped container...")
			if err := docker.Start(name); err != nil {
				return fmt.Errorf("start container: %w", err)
			}
		default:
			// Container is gone — recreate it. Use --resume if we have a
			// saved session ID, otherwise fall back to --continue.
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
				Continue:       sess.ResumeID == "", // only if no resume ID
			}, true)
			startCmd := exec.Command("docker", detachedArgs...)
			startCmd.Stderr = os.Stderr
			if err := startCmd.Run(); err != nil {
				return fmt.Errorf("recreate container: %w", err)
			}
		}

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

func init() {
	rootCmd.AddCommand(attachCmd)
}

// completeSessionNames provides tab completion for session names.
func completeSessionNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	store := config.NewStore(config.DefaultDir())
	return store.Names(), cobra.ShellCompDirectiveNoFileComp
}
