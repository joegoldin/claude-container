package cmd

import (
	"fmt"
	"os"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "claude-container",
	Short: "Run multiple Claude Code instances in isolated containers",
	Long:  `A CLI tool for running multiple Claude Code instances in isolated, sandboxed Docker containers with git worktree separation and a TUI dashboard.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("bare-invoke not yet wired up; use 'claude-container tui' for the dashboard")
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// requireAuth returns an error if the user has not authenticated yet.
func requireAuth(store *config.Store) error {
	if !store.IsAuthenticated() {
		return fmt.Errorf("not authenticated; run 'claude-container auth' first")
	}
	return nil
}
