package cmd

import (
	"fmt"
	"os/exec"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/docker"
	"github.com/spf13/cobra"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check system health and configuration",
	RunE: func(cmd *cobra.Command, args []string) error {
		ok := true

		// 1. Docker available?
		if _, err := exec.LookPath("docker"); err != nil {
			fmt.Println("  [FAIL] Docker not found in PATH")
			ok = false
		} else {
			fmt.Println("  [ OK ] Docker found")
		}

		// 2. Docker daemon running?
		ping := exec.Command("docker", "info")
		ping.Stdout = nil
		ping.Stderr = nil
		if err := ping.Run(); err != nil {
			fmt.Println("  [FAIL] Docker daemon not running")
			ok = false
		} else {
			fmt.Println("  [ OK ] Docker daemon running")
		}

		// 3. Image built?
		if docker.ImageExists() {
			fmt.Println("  [ OK ] Docker image '" + docker.ImageName + "' found")
		} else {
			fmt.Println("  [FAIL] Docker image '" + docker.ImageName + "' not found (run 'claude-container build')")
			ok = false
		}

		// 4. Authenticated?
		store := config.NewStore(config.DefaultDir())
		if store.IsAuthenticated() {
			fmt.Println("  [ OK ] Authenticated")
		} else {
			fmt.Println("  [WARN] Not authenticated (run 'claude-container auth')")
		}

		// 5. Info.
		fmt.Println("  [INFO] Config dir: " + config.DefaultDir())
		fmt.Println("  [INFO] Claude config: " + store.ClaudeConfigDir())

		if !ok {
			return fmt.Errorf("doctor found issues")
		}
		fmt.Println("\nAll checks passed.")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(doctorCmd)
}
