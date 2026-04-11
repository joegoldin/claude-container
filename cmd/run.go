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
	runProxyPreset string
	runProxyPort   int
	runResume         string
	runAllowCommands  []string
	runDenyCommands   []string
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
			name:          name,
			noWorktree:    true,
			yolo:          runYolo,
			prompt:        runPrompt,
			resume:        runResume,
			background:    runBackground,
			autoRemove:    runAutoRemove,
			mounts:        runMounts,
			workspace:     runWorkspace,
			profile:       runProfile,
			allowDomains:  runAllowDomains,
			denyPaths:     runDenyPaths,
			allowCommands: runAllowCommands,
			denyCommands:  runDenyCommands,
			proxySeedPreset: runProxyPreset,
			proxyPort:       runProxyPort,
		})
	},
}

func init() {
	runCmd.Flags().BoolVar(&runYolo, "yolo", false, "Skip permission prompts")
	runCmd.Flags().StringVar(&runResume, "resume", "", "Resume a previous conversation (pass ID or empty for picker)")
	runCmd.Flags().Lookup("resume").NoOptDefVal = "__picker__"
	runCmd.Flags().StringVarP(&runPrompt, "prompt", "p", "", "Initial prompt to send to Claude")
	runCmd.Flags().StringVar(&runName, "name", "", "Session name (auto-generated if omitted)")
	runCmd.Flags().BoolVarP(&runBackground, "background", "b", false, "Don't attach after creation")
	runCmd.Flags().BoolVar(&runAutoRemove, "rm", false, "Auto-remove session when it exits")
	runCmd.Flags().StringArrayVarP(&runMounts, "mount", "w", nil, "Additional folders to mount (repeatable)")
	runCmd.Flags().StringVarP(&runWorkspace, "workspace", "W", "", "Named workspace from workspaces.json")
	runCmd.Flags().StringVar(&runProfile, "profile", "", "Sandbox profile: low, default, med, high (default \"default\")")
	runCmd.Flags().StringArrayVar(&runAllowDomains, "allow-domain", nil, "Add domain to proxy allowlist")
	runCmd.Flags().StringArrayVar(&runDenyPaths, "deny-path", nil, "Add path to permissions deny list")
	runCmd.Flags().StringArrayVar(&runAllowCommands, "allow-command", nil, "Add command pattern to allow list (e.g., 'docker *')")
	runCmd.Flags().StringArrayVar(&runDenyCommands, "deny-command", nil, "Add command pattern to deny list (e.g., 'rm -rf *')")
	runCmd.Flags().StringVar(&runProxyPreset, "preset", "",
		"Seed the proxy with rules from a saved preset name")
	runCmd.Flags().IntVar(&runProxyPort, "proxy-port", 0,
		"Dashboard port on host (0 = auto-assign)")
	rootCmd.AddCommand(runCmd)
}
