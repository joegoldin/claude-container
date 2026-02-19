package cmd

import (
	"os"
	"os/exec"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/docker"
	"github.com/spf13/cobra"
)

var shellCmd = &cobra.Command{
	Use:   "shell [workspace]",
	Short: "Drop into a bash shell in a container",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, err := os.Getwd()
		if err != nil {
			return err
		}
		if len(args) > 0 {
			ws = args[0]
		}

		store := config.NewStore(config.DefaultDir())
		shellConfigDir := store.ContainerConfigDir("_shell")
		if err := os.MkdirAll(shellConfigDir, 0o755); err != nil {
			return err
		}

		shellArgs := docker.ShellArgs(ws, shellConfigDir, os.Getuid(), os.Getgid())
		c := exec.Command("docker", shellArgs...)
		c.Stdin = os.Stdin
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		return c.Run()
	},
}

func init() {
	rootCmd.AddCommand(shellCmd)
}
