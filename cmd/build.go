package cmd

import (
	"github.com/joegoldin/claude-container/internal/docker"
	"github.com/spf13/cobra"
)

var buildCmd = &cobra.Command{
	Use:   "build",
	Short: "Load the Claude Code Docker image",
	Long:  `Load the Docker image from the Nix-built tarball. This happens automatically when creating sessions, but you can use this command to force a reload.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return docker.LoadImage()
	},
}

func init() {
	rootCmd.AddCommand(buildCmd)
}
