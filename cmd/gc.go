package cmd

import (
	"fmt"
	"os"
	"os/exec"

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
			if err := removeClaudeConfig(dir); err != nil {
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

// removeClaudeConfig removes the shared claude config directory. Files inside
// may be owned by the container's UID, so we first try os.RemoveAll and fall
// back to running a Docker container as root to delete them.
func removeClaudeConfig(dir string) error {
	if err := os.RemoveAll(dir); err == nil {
		return nil
	}
	// Files created by the container may have different ownership.
	// Use a Docker container to remove the contents as root.
	// We can't rm the mount point itself (busy), so delete contents with shell glob.
	c := exec.Command("docker", "run", "--rm",
		"-v", dir+":/cleanup",
		"alpine", "sh", "-c", "rm -rf /cleanup/* /cleanup/.[!.]* /cleanup/..?*")
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("docker rm cleanup: %w", err)
	}
	// Directory itself is now empty; try to remove but ignore errors
	// (it may still be owned by container UID, and gets recreated on next auth).
	os.Remove(dir)
	return nil
}

func init() {
	gcCmd.Flags().BoolVar(&gcAll, "all", false, "Also remove worktrees, branches, and session records")
	gcCmd.Flags().BoolVar(&gcAuth, "auth", false, "Remove shared Claude config directory (logs you out)")
	rootCmd.AddCommand(gcCmd)
}
