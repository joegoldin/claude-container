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
Use 'claude-container auth status' to check authentication state.
Use 'claude-container gc --auth' to remove stored credentials.`,
	RunE:  authLoginRun,
}

var authStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check authentication status",
	RunE: func(cmd *cobra.Command, args []string) error {
		store := config.NewStore(config.DefaultDir())
		if store.IsAuthenticated() {
			fmt.Println("Authenticated")
		} else {
			fmt.Println("Not authenticated. Run 'claude-container auth' to log in.")
		}
		return nil
	},
}

func authLoginRun(cmd *cobra.Command, args []string) error {
	store := config.NewStore(config.DefaultDir())

	// Ensure the shared config directory exists.
	if err := os.MkdirAll(store.ClaudeConfigDir(), 0o755); err != nil {
		return fmt.Errorf("create claude config dir: %w", err)
	}

	// Check that the docker image has been built.
	if !docker.ImageExists() {
		return fmt.Errorf("docker image %q not found; run 'claude-container build' first", docker.ImageName)
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
		docker.ImageName,
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
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for range ticker.C {
			if store.IsAuthenticated() {
				c.Process.Signal(os.Interrupt)
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
