package cmd

import (
	"fmt"
	"os"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/spf13/cobra"
)

var (
	runYolo       bool
	runPrompt     string
	runName       string
	runBackground bool
	runAutoRemove bool
)

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Quick-start a session in the current directory",
	Long:  `Create a session without a worktree, using the current directory. Name is auto-generated unless --name is provided.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := runName
		if name == "" {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("getwd: %w", err)
			}
			name = config.GenerateName(cwd)
		}

		return createSession(createOpts{
			name:       name,
			noWorktree: true,
			yolo:       runYolo,
			prompt:     runPrompt,
			background: runBackground,
			autoRemove: runAutoRemove,
		})
	},
}

func init() {
	runCmd.Flags().BoolVar(&runYolo, "yolo", false, "Skip permission prompts")
	runCmd.Flags().StringVarP(&runPrompt, "prompt", "p", "", "Initial prompt to send to Claude")
	runCmd.Flags().StringVar(&runName, "name", "", "Session name (auto-generated if omitted)")
	runCmd.Flags().BoolVarP(&runBackground, "background", "b", false, "Don't attach after creation")
	runCmd.Flags().BoolVar(&runAutoRemove, "rm", false, "Auto-remove session when it exits")
	rootCmd.AddCommand(runCmd)
}
