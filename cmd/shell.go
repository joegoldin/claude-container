package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var shellCmd = &cobra.Command{
	Use:   "shell [workspace]",
	Short: "Drop into a bash shell in a container",
	Args:  cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("shell: not yet implemented")
	},
}

func init() {
	rootCmd.AddCommand(shellCmd)
}
