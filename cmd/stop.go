package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var stopCmd = &cobra.Command{
	Use:               "stop <session>",
	Short:             "Stop a session (keep worktree)",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSessionNames,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("stop %s: not yet implemented\n", args[0])
	},
}

func init() {
	rootCmd.AddCommand(stopCmd)
}
