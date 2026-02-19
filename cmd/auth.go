package cmd

import (
	"fmt"
	"os"
	"os/exec"
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
		if hostDir := config.HostClaudeDir(); hostDir != "" {
			if _, err := os.Stat(hostDir + "/.credentials.json"); err == nil {
				fmt.Printf("Authenticated (host credentials: %s)\n", hostDir)
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

func authLoginRun(cmd *cobra.Command, args []string) error {
	store := config.NewStore(config.DefaultDir())

	// Check if host already has credentials — no container auth needed.
	if hostDir := config.HostClaudeDir(); hostDir != "" {
		if _, err := os.Stat(hostDir + "/.credentials.json"); err == nil {
			fmt.Printf("Host credentials found at %s/.credentials.json\n", hostDir)
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
		"-e", fmt.Sprintf("USER_UID=%d", os.Getuid()),
		"-e", fmt.Sprintf("USER_GID=%d", os.Getgid()),
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
	rootCmd.AddCommand(authCmd)
}
