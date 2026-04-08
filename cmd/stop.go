package cmd

import (
	"fmt"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/docker"
	"github.com/joegoldin/claude-container/internal/httpproxy"
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
		if _, err := store.Get(name); err != nil {
			return fmt.Errorf("session %q not found", name)
		}

		if docker.IsRunning(name) {
			if err := docker.Stop(name); err != nil {
				return fmt.Errorf("stop container: %w", err)
			}
		}

		// Each session owns its own proxy sidecar — tear it down too.
		// Best-effort: failing to remove the network shouldn't block the
		// stop, but we surface it.
		if err := httpproxy.Stop(name); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: stop proxy: %v\n", err)
		}

		fmt.Println("Session stopped. Worktree preserved.")
		fmt.Printf("  Resume: claude-container attach %s\n", name)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(stopCmd)
}
