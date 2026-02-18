package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/tmux"
	"github.com/joegoldin/claude-container/internal/tui"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "claude-container",
	Short: "Run multiple Claude Code instances in isolated containers",
	Long:  `A CLI tool for running multiple Claude Code instances in isolated, sandboxed Docker containers with git worktree separation and a TUI dashboard.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		store := config.NewStore(config.DefaultDir())

		for {
			m := tui.NewDashboard(store)
			p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseAllMotion())

			result, err := p.Run()
			if err != nil {
				return fmt.Errorf("TUI error: %w", err)
			}

			dm, ok := result.(tui.DashboardModel)
			if !ok {
				return nil
			}

			if dm.Attached() != "" {
				ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
				_ = tmux.Attach(ctx, dm.Attached())
				stop()
				continue // return to dashboard after detach
			}

			if dm.Creating() {
				fmt.Println("Use 'claude-container new' to create a session (wizard coming soon).")
				return nil
			}

			// User quit.
			return nil
		}
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
