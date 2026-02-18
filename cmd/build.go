package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var buildCmd = &cobra.Command{
	Use:   "build",
	Short: "Build the Claude Code container image",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("build: not yet implemented")
	},
}

func init() {
	rootCmd.AddCommand(buildCmd)
}
