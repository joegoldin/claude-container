package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var attachCmd = &cobra.Command{
	Use:               "attach <session>",
	Short:             "Attach to a running session",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSessionNames,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("attach %s: not yet implemented\n", args[0])
	},
}

func init() {
	rootCmd.AddCommand(attachCmd)
}

// completeSessionNames provides tab completion for session names.
func completeSessionNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	// TODO: load sessions from config and return names
	return nil, cobra.ShellCompDirectiveNoFileComp
}
