package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/docker"
	"github.com/spf13/cobra"
)

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Authenticate Claude Code inside a container",
	Long: `Log in to Claude Code by running an interactive authentication session inside a container.

If you have already authenticated Claude Code on the host (credentials in ~/.claude/),
those credentials are automatically mounted into containers — no separate auth step needed.

Use 'claude-container auth status' to check authentication state.
Use 'claude-container gc --auth' to remove stored credentials.`,
	RunE: authLoginRun,
}

var authStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check authentication status",
	RunE: func(cmd *cobra.Command, args []string) error {
		store := config.NewStore(config.DefaultDir())
		for _, f := range config.HostClaudeCredentialFiles() {
			if filepath.Base(f) == ".credentials.json" {
				fmt.Printf("Authenticated (host credentials: %s)\n", f)
				return nil
			}
		}
		if store.IsAuthenticated() {
			fmt.Printf("Authenticated (config: %s)\n", store.ClaudeConfigDir())
		} else {
			fmt.Println("Not authenticated. Run 'claude-container auth' to log in.")
		}
		return nil
	},
}

var authRefreshCmd = &cobra.Command{
	Use:   "refresh",
	Short: "Re-copy host credentials into running containers",
	Long:  `Re-copies host credentials from ~/.claude/ into all running containers. Use after re-authenticating on the host.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		store := config.NewStore(config.DefaultDir())

		sessions := store.List()
		if len(sessions) == 0 {
			fmt.Println("No sessions found.")
			return nil
		}

		refreshed := 0
		for _, sess := range sessions {
			if !docker.IsRunning(sess.Name) {
				continue
			}
			c := docker.RefreshAuthCmd(sess.Name)
			c.Stderr = os.Stderr
			if err := c.Run(); err != nil {
				fmt.Printf("  %s: failed (%v)\n", sess.Name, err)
				continue
			}
			fmt.Printf("  %s: refreshed\n", sess.Name)
			refreshed++
		}

		if refreshed == 0 {
			fmt.Println("No running containers found.")
		} else {
			fmt.Printf("Refreshed credentials in %d container(s).\n", refreshed)
		}
		return nil
	},
}

func authLoginRun(cmd *cobra.Command, args []string) error {
	store := config.NewStore(config.DefaultDir())

	// Check if host already has credentials — no container auth needed.
	for _, f := range config.HostClaudeCredentialFiles() {
		if filepath.Base(f) == ".credentials.json" {
			fmt.Printf("Host credentials found at %s\n", f)
			fmt.Println("These are automatically mounted into containers. No auth needed.")
			return nil
		}
	}

	// Ensure the shared config directory exists.
	if err := os.MkdirAll(store.ClaudeConfigDir(), 0o755); err != nil {
		return fmt.Errorf("create claude config dir: %w", err)
	}

	// Ensure docker image is loaded.
	if err := docker.EnsureImage(config.DefaultDir()); err != nil {
		return err
	}

	// Run an interactive container so the user can authenticate.
	dockerArgs := []string{
		"run",
		"--rm",
		"-it",
		"-v", store.ClaudeConfigDir() + ":/claude",
		"-e", "CLAUDE_CONFIG_DIR=/claude",
		"-e", fmt.Sprintf("USER_UID=%d", docker.ContainerUID()),
		"-e", fmt.Sprintf("USER_GID=%d", docker.ContainerGID()),
		docker.ImageTag(),
		"claude",
		"--dangerously-skip-permissions",
	}

	c := exec.Command("docker", dockerArgs...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr

	if err := c.Start(); err != nil {
		return fmt.Errorf("docker run: %w", err)
	}

	// Poll for credentials file — auto-exit when authenticated.
	// Wait a few seconds after detecting credentials so Claude finishes
	// writing all config files (.claude.json, settings, etc).
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for range ticker.C {
			if store.IsAuthenticated() {
				// Give Claude time to finish writing settings.
				time.Sleep(3 * time.Second)
				if c.ProcessState == nil {
					c.Process.Signal(os.Interrupt)
				}
				return
			}
			if c.ProcessState != nil {
				return
			}
		}
	}()

	_ = c.Wait()

	// Report auth status.
	if store.IsAuthenticated() {
		fmt.Println("\nAuthentication successful.")
	} else {
		fmt.Println("\nAuthentication was not completed. Run 'claude-container auth' to try again.")
	}

	return nil
}

func init() {
	authCmd.AddCommand(authStatusCmd)
	authCmd.AddCommand(authRefreshCmd)
	rootCmd.AddCommand(authCmd)
}
