package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/docker"
	"github.com/joegoldin/claude-container/internal/tmux"
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
		sess, err := store.Get(name)
		if err != nil {
			return fmt.Errorf("session %q not found", name)
		}

		// If tmux session doesn't exist (stopped state), restart it.
		if !tmux.Exists(name) {
			// Remove old docker container if it exists.
			if docker.Exists(name) {
				if err := docker.Remove(name); err != nil {
					return fmt.Errorf("remove old container: %w", err)
				}
			}

			// Rebuild docker run command using stored session metadata.
			// Always use --continue for resume.
			containerConfigDir := store.ContainerConfigDir(name)
			dockerArgs := docker.RunArgs(docker.RunOpts{
				Name:            name,
				Workspace:       sess.WorktreePath,
				ConfigDir:       containerConfigDir,
				CredentialsFile: config.CredentialsFile(),
				UID:             os.Getuid(),
				GID:             os.Getgid(),
				Yolo:            sess.Yolo,
				Continue:        true,
			})
			fullCmd := append([]string{"docker"}, dockerArgs...)

			if err := tmux.Create(name, sess.WorktreePath, fullCmd); err != nil {
				return fmt.Errorf("restart tmux session: %w", err)
			}
			fmt.Println("Restarted session with --continue")
		}

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()

		fmt.Println("Attaching (Ctrl+Q to detach)...")
		attachErr := tmux.Attach(ctx, name)

		// Auto-remove if session was created with --rm and has ended.
		if sess.AutoRemove && !tmux.Exists(name) {
			removeSession(store, name)
		}

		return attachErr
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
