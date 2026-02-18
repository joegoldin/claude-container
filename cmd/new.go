package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	newName       string
	newWorktree   string
	newFrom       string
	newNoWorktree bool
	newYolo       bool
	newPrompt     string
	newContinue   bool
)

var newCmd = &cobra.Command{
	Use:   "new",
	Short: "Create a new Claude Code session",
	Long:  `Create a new session with an interactive wizard, or use flags to skip the wizard.`,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("new: not yet implemented")
	},
}

func init() {
	newCmd.Flags().StringVar(&newName, "name", "", "Session name")
	newCmd.Flags().StringVar(&newWorktree, "worktree", "", "Create worktree on new branch")
	newCmd.Flags().StringVar(&newFrom, "from", "", "Base branch for worktree (default: current HEAD)")
	newCmd.Flags().BoolVar(&newNoWorktree, "no-worktree", false, "Use current directory directly")
	newCmd.Flags().BoolVar(&newYolo, "yolo", false, "Skip permission prompts")
	newCmd.Flags().StringVarP(&newPrompt, "prompt", "p", "", "Initial prompt to send to Claude")
	newCmd.Flags().BoolVarP(&newContinue, "continue", "c", false, "Resume previous conversation")
	rootCmd.AddCommand(newCmd)
}
