package cmd

import (
	"fmt"
	"os"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/spf13/cobra"
)

var (
	runYolo         bool
	runPrompt       string
	runName         string
	runBackground   bool
	runAutoRemove   bool
	runMounts       []string
	runWorkspace    string
	runProfile      string
	runAllowDomains []string
	runDenyPaths    []string
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
			name:         name,
			noWorktree:   true,
			yolo:         runYolo,
			prompt:       runPrompt,
			background:   runBackground,
			autoRemove:   runAutoRemove,
			mounts:       runMounts,
			workspace:    runWorkspace,
			profile:      runProfile,
			allowDomains: runAllowDomains,
			denyPaths:    runDenyPaths,
		})
	},
}

func init() {
	runCmd.Flags().BoolVar(&runYolo, "yolo", false, "Skip permission prompts")
	runCmd.Flags().StringVarP(&runPrompt, "prompt", "p", "", "Initial prompt to send to Claude")
	runCmd.Flags().StringVar(&runName, "name", "", "Session name (auto-generated if omitted)")
	runCmd.Flags().BoolVarP(&runBackground, "background", "b", false, "Don't attach after creation")
	runCmd.Flags().BoolVar(&runAutoRemove, "rm", false, "Auto-remove session when it exits")
	runCmd.Flags().StringArrayVarP(&runMounts, "mount", "w", nil, "Additional folders to mount (repeatable)")
	runCmd.Flags().StringVarP(&runWorkspace, "workspace", "W", "", "Named workspace from workspaces.json")
	runCmd.Flags().StringVar(&runProfile, "profile", "", "Sandbox profile: low, med, high (default \"med\")")
	runCmd.Flags().StringArrayVar(&runAllowDomains, "allow-domain", nil, "Add domain to sandbox allowlist")
	runCmd.Flags().StringArrayVar(&runDenyPaths, "deny-path", nil, "Add path to sandbox deny list")
	rootCmd.AddCommand(runCmd)
}
