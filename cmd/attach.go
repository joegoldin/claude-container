package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/docker"
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

		// If container is running, attach directly.
		if docker.IsRunning(name) {
			return attachToSession(store, sess)
		}

		// If container exists but stopped, start it and attach.
		if docker.Exists(name) {
			if err := docker.Start(name); err != nil {
				return fmt.Errorf("start container: %w", err)
			}
			fmt.Println("Restarted stopped container")
			return attachToSession(store, sess)
		}

		// Container doesn't exist — recreate with --continue.
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

		if err := docker.Run(dockerArgs); err != nil {
			return fmt.Errorf("recreate container: %w", err)
		}
		fmt.Println("Recreated container with --continue")
		return attachToSession(store, sess)
	},
}

func attachToSession(store *config.Store, sess *config.Session) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	fmt.Println("Attaching (Ctrl+P,Ctrl+Q to detach)...")
	attachErr := docker.Attach(ctx, sess.Name)

	// Auto-remove if session was created with --rm and container has exited.
	if sess.AutoRemove && !docker.IsRunning(sess.Name) {
		removeSession(store, sess.Name)
	}

	return attachErr
}

func init() {
	rootCmd.AddCommand(attachCmd)
}

// completeSessionNames provides tab completion for session names.
func completeSessionNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	store := config.NewStore(config.DefaultDir())
	return store.Names(), cobra.ShellCompDirectiveNoFileComp
}
