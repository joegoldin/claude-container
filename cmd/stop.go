package cmd

import (
	"fmt"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/docker"
	"github.com/spf13/cobra"
)

var stopCmd = &cobra.Command{
	Use:               "stop <session>",
	Short:             "Stop a session (keep worktree)",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		store := config.NewStore(config.DefaultDir())
		sess, err := store.Get(name)
		if err != nil {
			return fmt.Errorf("session %q not found", name)
		}

		if docker.IsRunning(name) {
			if err := docker.Stop(name); err != nil {
				return fmt.Errorf("stop container: %w", err)
			}
		}

		// Clean up proxy if this was the last session using it.
		if sess.ProxyProfile != "" &&
			(sess.NetworkSandbox == "proxy" || sess.NetworkSandbox == "both") {
			proxyCleanupIfUnused(store, sess.ProxyProfile, name)
		}

		fmt.Println("Session stopped. Worktree preserved.")
		fmt.Printf("  Resume: claude-container attach %s\n", name)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(stopCmd)
}
