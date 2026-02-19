package cmd

import (
	"fmt"
	"os"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/docker"
	gitpkg "github.com/joegoldin/claude-container/internal/git"
	"github.com/spf13/cobra"
)

var rmCmd = &cobra.Command{
	Use:               "rm <session>",
	Short:             "Remove a session (stop + delete worktree + branch)",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		store := config.NewStore(config.DefaultDir())
		removeSession(store, name)
		fmt.Println("Session removed.")
		return nil
	},
}

// removeSession tears down a session: stops and removes the docker container,
// removes the worktree, and deletes the session record. Errors are printed as warnings.
func removeSession(store *config.Store, name string) {
	sess, _ := store.Get(name)

	if docker.Exists(name) {
		if err := docker.Remove(name); err != nil {
			fmt.Fprintf(os.Stderr, "warning: remove container: %v\n", err)
		}
	}
	if sess != nil && sess.Branch != "" && sess.RepoPath != "" {
		if err := gitpkg.RemoveWorktree(sess.RepoPath, sess.WorktreePath, sess.Branch); err != nil {
			fmt.Fprintf(os.Stderr, "warning: remove worktree: %v\n", err)
		}
	}
	if err := store.Delete(name); err != nil {
		fmt.Fprintf(os.Stderr, "warning: delete session: %v\n", err)
	}
}

func init() {
	rootCmd.AddCommand(rmCmd)
}
