package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/spf13/cobra"
)

var workspaceCmd = &cobra.Command{
	Use:   "workspace",
	Short: "Manage named workspace definitions",
}

var workspaceAddCmd = &cobra.Command{
	Use:   "add <name> <path>...",
	Short: "Create or append paths to a named workspace",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		paths := make([]string, 0, len(args)-1)
		for _, p := range args[1:] {
			abs, err := filepath.Abs(p)
			if err != nil {
				return fmt.Errorf("resolve path %q: %w", p, err)
			}
			if _, err := os.Stat(abs); err != nil {
				return fmt.Errorf("path %q does not exist", abs)
			}
			paths = append(paths, abs)
		}
		ws := config.NewWorkspaceStore(config.DefaultDir())
		if err := ws.Add(name, paths); err != nil {
			return err
		}
		fmt.Printf("Workspace %q updated (%d paths).\n", name, len(paths))
		return nil
	},
}

var workspaceListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all workspace names",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ws := config.NewWorkspaceStore(config.DefaultDir())
		names := ws.List()
		if len(names) == 0 {
			fmt.Println("No workspaces defined.")
			return nil
		}
		for _, name := range names {
			fmt.Println(name)
		}
		return nil
	},
}

var workspaceShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show paths in a workspace",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws := config.NewWorkspaceStore(config.DefaultDir())
		paths, err := ws.Get(args[0])
		if err != nil {
			return err
		}
		for _, p := range paths {
			fmt.Println(p)
		}
		return nil
	},
}

var workspaceRmCmd = &cobra.Command{
	Use:   "rm <name>",
	Short: "Remove a workspace definition",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws := config.NewWorkspaceStore(config.DefaultDir())
		if err := ws.Remove(args[0]); err != nil {
			return err
		}
		fmt.Printf("Workspace %q removed.\n", args[0])
		return nil
	},
}

func init() {
	workspaceCmd.AddCommand(workspaceAddCmd)
	workspaceCmd.AddCommand(workspaceListCmd)
	workspaceCmd.AddCommand(workspaceShowCmd)
	workspaceCmd.AddCommand(workspaceRmCmd)
	rootCmd.AddCommand(workspaceCmd)
}
