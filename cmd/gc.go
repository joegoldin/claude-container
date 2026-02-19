package cmd

import (
	"fmt"
	"os"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/docker"
	"github.com/spf13/cobra"
)

var (
	gcAll  bool
	gcAuth bool
)

var gcCmd = &cobra.Command{
	Use:   "gc",
	Short: "Clean up stopped containers and stale sessions",
	Long: `Remove stopped Docker containers for tracked sessions.

By default, only stopped containers are removed. Use --all to also
remove worktrees, branches, and session records for stopped sessions.
Use --auth to remove the shared Claude config directory (logs you out).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		store := config.NewStore(config.DefaultDir())

		if gcAuth {
			dir := store.ClaudeConfigDir()
			if err := os.RemoveAll(dir); err != nil {
				return fmt.Errorf("remove claude config dir: %w", err)
			}
			fmt.Printf("Removed claude config: %s\n", dir)
			if !gcAll {
				return nil
			}
		}

		sessions := store.List()

		if len(sessions) == 0 {
			fmt.Println("No sessions found.")
			return nil
		}

		cleaned := 0
		for _, sess := range sessions {
			if docker.IsRunning(sess.Name) {
				continue
			}

			if docker.Exists(sess.Name) {
				if err := docker.Remove(sess.Name); err != nil {
					fmt.Fprintf(os.Stderr, "warning: remove container %s: %v\n", sess.Name, err)
					continue
				}
				fmt.Printf("Removed container: %s\n", sess.Name)
				cleaned++
			}

			if gcAll {
				removeSession(store, sess.Name)
				fmt.Printf("Removed session: %s\n", sess.Name)
			}
		}

		if cleaned == 0 && !gcAll {
			fmt.Println("Nothing to clean up.")
		}
		return nil
	},
}

func init() {
	gcCmd.Flags().BoolVar(&gcAll, "all", false, "Also remove worktrees, branches, and session records")
	gcCmd.Flags().BoolVar(&gcAuth, "auth", false, "Remove shared Claude config directory (logs you out)")
	rootCmd.AddCommand(gcCmd)
}
