package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/docker"
	"github.com/joegoldin/claude-container/internal/tmux"
	"github.com/spf13/cobra"
)

var psJSON bool

type psEntry struct {
	Name   string `json:"name"`
	Branch string `json:"branch"`
	Status string `json:"status"`
	Uptime string `json:"uptime"`
	Repo   string `json:"repo"`
}

var psCmd = &cobra.Command{
	Use:   "ps",
	Short: "List all sessions",
	RunE: func(cmd *cobra.Command, args []string) error {
		store := config.NewStore(config.DefaultDir())
		sessions := store.List()

		if len(sessions) == 0 {
			if psJSON {
				fmt.Println("[]")
			} else {
				fmt.Println("No sessions.")
			}
			return nil
		}

		entries := make([]psEntry, 0, len(sessions))
		for _, sess := range sessions {
			tmuxAlive := tmux.Exists(sess.Name)
			containerRunning := docker.IsRunning(sess.Name)

			var status, uptime string
			switch {
			case tmuxAlive && containerRunning:
				status = "running"
				uptime = formatUptime(time.Since(sess.CreatedAt))
			case tmuxAlive:
				status = "exited"
				uptime = "-"
			default:
				status = "stopped"
				uptime = "-"
			}

			entries = append(entries, psEntry{
				Name:   sess.Name,
				Branch: sess.Branch,
				Status: status,
				Uptime: uptime,
				Repo:   shortenHome(sess.RepoPath),
			})
		}

		if psJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(entries)
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tBRANCH\tSTATUS\tUPTIME\tREPO")
		for _, e := range entries {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", e.Name, e.Branch, e.Status, e.Uptime, e.Repo)
		}
		return w.Flush()
	},
}

func init() {
	psCmd.Flags().BoolVar(&psJSON, "json", false, "Machine-readable JSON output")
	rootCmd.AddCommand(psCmd)
}

// shortenHome replaces the user's home directory prefix with ~.
func shortenHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if len(path) >= len(home) && path[:len(home)] == home {
		return "~" + path[len(home):]
	}
	return path
}

// formatUptime returns a human-readable duration string.
func formatUptime(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		return fmt.Sprintf("%dh%dm", h, m)
	default:
		days := int(d.Hours()) / 24
		h := int(d.Hours()) % 24
		return fmt.Sprintf("%dd%dh", days, h)
	}
}
