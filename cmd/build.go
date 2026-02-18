package cmd

import (
	"fmt"
	"os"

	"github.com/joegoldin/claude-container/internal/docker"
	"github.com/spf13/cobra"
)

var buildCmd = &cobra.Command{
	Use:   "build",
	Short: "Build the Claude Code container image",
	RunE: func(cmd *cobra.Command, args []string) error {
		contextDir := os.Getenv("CLAUDE_CONTAINER_DOCKER_CONTEXT")
		if contextDir == "" {
			return fmt.Errorf("CLAUDE_CONTAINER_DOCKER_CONTEXT is not set; point it at the directory containing your Dockerfile")
		}

		fmt.Println("Building Claude Code container...")
		return docker.Build(contextDir).Run()
	},
}

func init() {
	rootCmd.AddCommand(buildCmd)
}
