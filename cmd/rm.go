package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var rmCmd = &cobra.Command{
	Use:               "rm <session>",
	Short:             "Remove a session (stop + delete worktree + branch)",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSessionNames,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("rm %s: not yet implemented\n", args[0])
	},
}

func init() {
	rootCmd.AddCommand(rmCmd)
}
