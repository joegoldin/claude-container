package cmd

import (
	"os"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/docker"
	"github.com/joegoldin/claude-container/internal/proxy"
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
		claudeConfigDir := store.ClaudeConfigDir()
		if err := os.MkdirAll(claudeConfigDir, 0o755); err != nil {
			return err
		}

		shellArgs := docker.ShellArgs(ws, claudeConfigDir, config.HostClaudeDir(), config.HostClaudeJSON(), os.Getuid(), os.Getgid())
		return proxy.Run(proxy.Opts{
			DockerArgs: shellArgs,
			StatusBar:  proxy.StatusBarInfo{Name: "_shell"},
		})
	},
}

func init() {
	rootCmd.AddCommand(shellCmd)
}
