package cmd

import (
	"fmt"
	"os"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/spf13/cobra"
)

var (
	workYolo   bool
	workPrompt string
	workName   string
	workFrom   string
)

var workCmd = &cobra.Command{
	Use:   "work",
	Short: "Quick-start an isolated worktree session",
	Long:  `Create a session with its own git worktree for isolation. Name and branch are auto-generated unless --name is provided.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := workName
		if name == "" {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("getwd: %w", err)
			}
			name = config.GenerateName(cwd)
		}

		return createSession(createOpts{
			name:     name,
			worktree: name,
			from:     workFrom,
			yolo:     workYolo,
			prompt:   workPrompt,
		})
	},
}

func init() {
	workCmd.Flags().BoolVar(&workYolo, "yolo", false, "Skip permission prompts")
	workCmd.Flags().StringVarP(&workPrompt, "prompt", "p", "", "Initial prompt to send to Claude")
	workCmd.Flags().StringVar(&workName, "name", "", "Session name (auto-generated if omitted)")
	workCmd.Flags().StringVar(&workFrom, "from", "", "Base branch for worktree (default: current HEAD)")
	rootCmd.AddCommand(workCmd)
}
