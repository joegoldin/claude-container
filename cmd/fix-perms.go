package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/spf13/cobra"
)

var fixPermsCmd = &cobra.Command{
	Use:               "fix-perms <session>",
	Short:             "Fix workspace ownership after container UID remapping",
	Long:              `Runs sudo chown to restore workspace ownership to the current user. Useful when Docker user namespace remapping causes files to be owned by a remapped UID.`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		store := config.NewStore(config.DefaultDir())
		sess, err := store.Get(name)
		if err != nil {
			return fmt.Errorf("session %q not found", name)
		}

		workspace := sess.WorktreePath
		if workspace == "" {
			return fmt.Errorf("session %q has no workspace path", name)
		}

		uid := os.Getuid()
		gid := os.Getgid()
		chownArg := fmt.Sprintf("%d:%d", uid, gid)

		fmt.Printf("Fixing ownership of %s to %s...\n", workspace, chownArg)
		chown := exec.Command("sudo", "chown", "-R", chownArg, workspace)
		chown.Stdout = os.Stdout
		chown.Stderr = os.Stderr
		if err := chown.Run(); err != nil {
			return fmt.Errorf("chown failed: %w", err)
		}
		fmt.Println("Done.")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(fixPermsCmd)
}
