package cmd

import (
	"fmt"
	"os"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/docker"
	"github.com/joegoldin/claude-container/internal/proxy"
	"github.com/spf13/cobra"
)

var attachCmd = &cobra.Command{
	Use:               "attach <session>",
	Short:             "Attach to a running session",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		store := config.NewStore(config.DefaultDir())
		if err := requireAuth(store); err != nil {
			return err
		}
		sess, err := store.Get(name)
		if err != nil {
			return fmt.Errorf("session %q not found", name)
		}

		var dockerArgs []string
		switch {
		case docker.IsRunning(name):
			fmt.Println("Attaching...")
			dockerArgs = []string{"attach", docker.ContainerName(name)}
		case docker.Exists(name):
			fmt.Println("Restarting stopped container...")
			dockerArgs = []string{"start", "-ai", docker.ContainerName(name)}
		default:
			fmt.Println("Recreating container with --continue...")
			dockerArgs = docker.RunArgs(docker.RunOpts{
				Name:      name,
				Workspace: sess.WorktreePath,
				ConfigDir: store.ClaudeConfigDir(),
				UID:       os.Getuid(),
				GID:       os.Getgid(),
				Yolo:      sess.Yolo,
				Continue:  true,
			}, false)
		}

		return proxy.Run(proxy.Opts{
			DockerArgs: dockerArgs,
			StatusBar:  proxy.StatusBarInfo{Name: name, Branch: sess.Branch, Yolo: sess.Yolo},
			AutoRemove: sess.AutoRemove,
			Cleanup:    func(_ string) { removeSession(store, name) },
		})
	},
}

func init() {
	rootCmd.AddCommand(attachCmd)
}

// completeSessionNames provides tab completion for session names.
func completeSessionNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	store := config.NewStore(config.DefaultDir())
	return store.Names(), cobra.ShellCompDirectiveNoFileComp
}
