package cmd

import (
	"fmt"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/docker"
	gitpkg "github.com/joegoldin/claude-container/internal/git"
	"github.com/joegoldin/claude-container/internal/tmux"
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
		sess, err := store.Get(name)
		if err != nil {
			return fmt.Errorf("session %q not found", name)
		}

		// Force remove docker container if it exists.
		if docker.Exists(name) {
			if err := docker.Remove(name); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: remove container: %v\n", err)
			}
		}

		// Kill tmux session if it exists.
		if tmux.Exists(name) {
			if err := tmux.Kill(name); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: kill tmux session: %v\n", err)
			}
		}

		// Remove worktree and branch if session has them.
		if sess.Branch != "" && sess.RepoPath != "" {
			if err := gitpkg.RemoveWorktree(sess.RepoPath, sess.WorktreePath, sess.Branch); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: remove worktree: %v\n", err)
			}
		}

		// Delete session from store.
		if err := store.Delete(name); err != nil {
			return fmt.Errorf("delete session: %w", err)
		}

		fmt.Println("Session removed.")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(rmCmd)
}
