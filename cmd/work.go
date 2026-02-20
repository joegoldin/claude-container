package cmd

import (
	"fmt"
	"os"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/spf13/cobra"
)

var (
	workYolo            bool
	workPrompt          string
	workName            string
	workFrom            string
	workBackground      bool
	workAutoRemove      bool
	workMounts          []string
	workWorkspace       string
	workProfile         string
	workAllowDomains    []string
	workDenyPaths       []string
	workNetworkSandbox  string
	workProxyProfile    string
	workProxyPort       int
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
			name:           name,
			worktree:       name,
			from:           workFrom,
			yolo:           workYolo,
			prompt:         workPrompt,
			background:     workBackground,
			autoRemove:     workAutoRemove,
			mounts:         workMounts,
			workspace:      workWorkspace,
			profile:        workProfile,
			allowDomains:   workAllowDomains,
			denyPaths:      workDenyPaths,
			networkSandbox: workNetworkSandbox,
			proxyProfile:   workProxyProfile,
			proxyPort:      workProxyPort,
		})
	},
}

func init() {
	workCmd.Flags().BoolVar(&workYolo, "yolo", false, "Skip permission prompts")
	workCmd.Flags().StringVarP(&workPrompt, "prompt", "p", "", "Initial prompt to send to Claude")
	workCmd.Flags().StringVar(&workName, "name", "", "Session name (auto-generated if omitted)")
	workCmd.Flags().StringVar(&workFrom, "from", "", "Base branch for worktree (default: current HEAD)")
	workCmd.Flags().BoolVarP(&workBackground, "background", "b", false, "Don't attach after creation")
	workCmd.Flags().BoolVar(&workAutoRemove, "rm", false, "Auto-remove session when it exits")
	workCmd.Flags().StringArrayVarP(&workMounts, "mount", "w", nil, "Additional folders to mount (repeatable)")
	workCmd.Flags().StringVarP(&workWorkspace, "workspace", "W", "", "Named workspace from workspaces.json")
	workCmd.Flags().StringVar(&workProfile, "profile", "", "Sandbox profile: low, med, high (default \"med\")")
	workCmd.Flags().StringArrayVar(&workAllowDomains, "allow-domain", nil, "Add domain to sandbox allowlist")
	workCmd.Flags().StringArrayVar(&workDenyPaths, "deny-path", nil, "Add path to sandbox deny list")
	workCmd.Flags().StringVar(&workNetworkSandbox, "network-sandbox", "claude",
		"Network enforcement: proxy, claude, both, none")
	workCmd.Flags().StringVar(&workProxyProfile, "proxy-profile", "default",
		"Proxy rule profile name")
	workCmd.Flags().IntVar(&workProxyPort, "proxy-port", 8081,
		"Dashboard port on host")
	rootCmd.AddCommand(workCmd)
}
