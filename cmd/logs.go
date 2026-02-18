package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var logsFollow bool

var logsCmd = &cobra.Command{
	Use:               "logs <session>",
	Short:             "Stream logs from a session",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSessionNames,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("logs %s: not yet implemented\n", args[0])
	},
}

func init() {
	logsCmd.Flags().BoolVarP(&logsFollow, "follow", "f", false, "Stream logs continuously")
	rootCmd.AddCommand(logsCmd)
}
