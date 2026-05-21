package cmd

import (
	"fmt"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/session"
	"github.com/spf13/cobra"
)

var (
	workYolo          bool
	workPrompt        string
	workName          string
	workFrom          string
	workBackground    bool
	workAutoRemove    bool
	workMounts        []string
	workWorkspace     string
	workProfile       string
	workAllowDomains  []string
	workDenyPaths     []string
	workProxyPreset   string
	workProxyPort     int
	workResume        string
	workAllowCommands []string
	workDenyCommands  []string
)

var workCmd = &cobra.Command{
	Use:   "work",
	Short: "Quick-start an isolated worktree session",
	Long:  `Create a session with its own git worktree for isolation. Name and branch are auto-generated unless --name is provided.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		store := config.NewStore(config.DefaultDir())
		if err := requireAuth(store); err != nil {
			return err
		}

		opts := session.Opts{
			Name:            workName,
			Mode:            session.ModeTTY,
			WorktreeMode:    session.WorktreeAlways,
			From:            workFrom,
			Profile:         workProfile,
			Yolo:            workYolo,
			AllowDomains:    workAllowDomains,
			DenyPaths:       workDenyPaths,
			AllowCommands:   workAllowCommands,
			DenyCommands:    workDenyCommands,
			Mounts:          workMounts,
			WorkspaceName:   workWorkspace,
			AutoRemove:      workAutoRemove,
			Background:      workBackground,
			Prompt:          workPrompt,
			Resume:          workResume,
			ProxySeedPreset: workProxyPreset,
			ProxyPort:       workProxyPort,
		}

		h, err := session.Launch(ctx, store, opts)
		if err != nil {
			return err
		}

		if workBackground {
			fmt.Printf("Session %q created (background).\n", h.Name)
			fmt.Printf("  Attach: claude-container attach %s\n", h.Name)
			return h.RunBackground()
		}
		fmt.Printf("Session %q created on branch %q.\n", h.Name, h.Branch)
		err = h.AttachTTY()
		saveResumeID(store, h.Name)
		return err
	},
}

func init() {
	workCmd.Flags().BoolVar(&workYolo, "yolo", false, "Skip permission prompts")
	workCmd.Flags().StringVar(&workResume, "resume", "", "Resume a previous conversation (pass ID or empty for picker)")
	workCmd.Flags().Lookup("resume").NoOptDefVal = "__picker__"
	workCmd.Flags().StringVarP(&workPrompt, "prompt", "p", "", "Initial prompt to send to Claude")
	workCmd.Flags().StringVar(&workName, "name", "", "Session name (auto-generated if omitted)")
	workCmd.Flags().StringVar(&workFrom, "from", "", "Base branch for worktree (default: current HEAD)")
	workCmd.Flags().BoolVarP(&workBackground, "background", "b", false, "Don't attach after creation")
	workCmd.Flags().BoolVar(&workAutoRemove, "rm", false, "Auto-remove session when it exits")
	workCmd.Flags().StringArrayVarP(&workMounts, "mount", "w", nil, "Additional folders to mount (repeatable)")
	workCmd.Flags().StringVarP(&workWorkspace, "workspace", "W", "", "Named workspace from workspaces.json")
	workCmd.Flags().StringVar(&workProfile, "profile", "", "Sandbox profile: low, default, med, high (default \"default\")")
	workCmd.Flags().StringArrayVar(&workAllowDomains, "allow-domain", nil, "Add domain to proxy allowlist")
	workCmd.Flags().StringArrayVar(&workDenyPaths, "deny-path", nil, "Add path to permissions deny list")
	workCmd.Flags().StringArrayVar(&workAllowCommands, "allow-command", nil, "Add command pattern to allow list (e.g., 'docker *')")
	workCmd.Flags().StringArrayVar(&workDenyCommands, "deny-command", nil, "Add command pattern to deny list (e.g., 'rm -rf *')")
	workCmd.Flags().StringVar(&workProxyPreset, "preset", "",
		"Seed the proxy with rules from a saved preset name")
	workCmd.Flags().IntVar(&workProxyPort, "proxy-port", 0,
		"Dashboard port on host (0 = auto-assign)")
	rootCmd.AddCommand(workCmd)
}
