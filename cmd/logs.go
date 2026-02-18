package cmd

import (
	"context"
	"os"
	"os/signal"

	"github.com/joegoldin/claude-container/internal/docker"
	"github.com/spf13/cobra"
)

var logsFollow bool

var logsCmd = &cobra.Command{
	Use:               "logs <session>",
	Short:             "Stream logs from a session",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()

		c := docker.Logs(ctx, args[0], logsFollow)
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		return c.Run()
	},
}

func init() {
	logsCmd.Flags().BoolVarP(&logsFollow, "follow", "f", false, "Stream logs continuously")
	rootCmd.AddCommand(logsCmd)
}
