package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/docker"
	"github.com/joegoldin/claude-container/internal/session"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var (
	taskPrompt        string
	taskName          string
	taskKeep          bool
	taskProfile       string
	taskAllowDomains  []string
	taskDenyPaths     []string
	taskAllowCommands []string
	taskDenyCommands  []string
	taskMounts        []string
	taskWorkspace     string
	taskProxyProfile  string
	taskProxyPort     int
	taskModel         string
	taskMaxTurns      int
)

var taskCmd = &cobra.Command{
	Use:   "task",
	Short: "Run a task and print the result to stdout",
	Long:  `Run Claude non-interactively. Final output goes to stdout (pipeable). Summary (changed files, duration, tokens) goes to stderr.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if taskPrompt == "" {
			return fmt.Errorf("--prompt is required")
		}
		return runTask(cmd)
	},
}

func init() {
	taskCmd.Flags().StringVarP(&taskPrompt, "prompt", "p", "", "Task prompt (required)")
	taskCmd.Flags().StringVar(&taskName, "name", "", "Session name (auto-generated if omitted)")
	taskCmd.Flags().BoolVar(&taskKeep, "keep", false, "Keep session after completion (default: ephemeral)")
	taskCmd.Flags().StringVar(&taskProfile, "profile", "", "Sandbox profile: low, default, med, high (default \"default\")")
	taskCmd.Flags().StringArrayVar(&taskAllowDomains, "allow-domain", nil, "Add domain to proxy allowlist")
	taskCmd.Flags().StringArrayVar(&taskDenyPaths, "deny-path", nil, "Add path to permissions deny list")
	taskCmd.Flags().StringArrayVar(&taskAllowCommands, "allow-command", nil, "Add command pattern to allow list")
	taskCmd.Flags().StringArrayVar(&taskDenyCommands, "deny-command", nil, "Add command pattern to deny list")
	taskCmd.Flags().StringArrayVarP(&taskMounts, "mount", "w", nil, "Additional folders to mount")
	taskCmd.Flags().StringVarP(&taskWorkspace, "workspace", "W", "", "Named workspace from workspaces.json")
	taskCmd.Flags().StringVar(&taskProxyProfile, "proxy-profile", "default", "Proxy rule profile name")
	taskCmd.Flags().IntVar(&taskProxyPort, "proxy-port", 0, "Dashboard port on host (0 = auto-assign)")
	taskCmd.Flags().StringVar(&taskModel, "model", "", "Model to use (passed to Claude CLI)")
	taskCmd.Flags().IntVar(&taskMaxTurns, "max-turns", 0, "Max agentic turns (passed to Claude CLI)")
	rootCmd.AddCommand(taskCmd)
}

func runTask(cmd *cobra.Command) error {
	startTime := time.Now()
	stderrIsTTY := term.IsTerminal(int(os.Stderr.Fd()))

	store := config.NewStore(config.DefaultDir())
	if err := requireAuth(store); err != nil {
		return err
	}

	opts := session.Opts{
		Name:            taskName,
		Mode:            session.ModeTask,
		Profile:         taskProfile,
		AllowDomains:    taskAllowDomains,
		DenyPaths:       taskDenyPaths,
		AllowCommands:   taskAllowCommands,
		DenyCommands:    taskDenyCommands,
		Mounts:          taskMounts,
		WorkspaceName:   taskWorkspace,
		AutoRemove:      !taskKeep,
		Prompt:          taskPrompt,
		ProxySeedPreset: taskProxyProfile,
		ProxyPort:       taskProxyPort,
		Model:           taskModel,
		MaxTurns:        taskMaxTurns,
	}

	h, err := session.Launch(cmd.Context(), store, opts)
	if err != nil {
		return err
	}

	// Spinner shows "Working... Ns" on stderr while the task runs (TTY only).
	var stopSpinner func()
	if stderrIsTTY {
		stopSpinner = startSpinner(startTime)
	}

	// Stream logs in parallel with the running container so the spinner can
	// keep ticking. parseStreamEvents reads until the container exits.
	logsCmd := exec.Command("docker", "logs", "--follow", h.Container)
	logsPipe, err := logsCmd.StdoutPipe()
	if err != nil {
		h.Cleanup()
		return fmt.Errorf("logs pipe: %w", err)
	}
	logsCmd.Stderr = nil
	if err := logsCmd.Start(); err != nil {
		h.Cleanup()
		return fmt.Errorf("start logs: %w", err)
	}

	result := parseStreamEvents(logsPipe)
	logsCmd.Wait()

	if stopSpinner != nil {
		stopSpinner()
	}

	exitCode, _ := docker.WaitExitCode(h.Name)

	// Changed files via docker exec — container must still exist, so this
	// happens BEFORE h.Cleanup() runs the auto-remove path.
	var changedFiles string
	diffCmd := docker.ExecGitDiff(h.Name)
	if diffOut, err := diffCmd.Output(); err == nil {
		changedFiles = strings.TrimSpace(string(diffOut))
	}

	// stdout: final assistant text.
	if result.FinalText != "" {
		fmt.Print(result.FinalText)
		if !strings.HasSuffix(result.FinalText, "\n") {
			fmt.Println()
		}
	}

	// stderr: summary.
	duration := time.Since(startTime)
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "--- Task Complete ---")
	if changedFiles != "" {
		fmt.Fprintln(os.Stderr, "Changed files:")
		for _, line := range strings.Split(changedFiles, "\n") {
			if line != "" {
				fmt.Fprintf(os.Stderr, "  %s\n", line)
			}
		}
	}
	fmt.Fprintf(os.Stderr, "Duration: %s\n", formatDuration(duration))
	if result.InputTokens > 0 || result.OutputTokens > 0 {
		fmt.Fprintf(os.Stderr, "Tokens:   %s in / %s out\n",
			formatNumber(result.InputTokens), formatNumber(result.OutputTokens))
	}
	if taskKeep {
		fmt.Fprintf(os.Stderr, "Session:  %s  (kept)\n", h.Name)
	}

	// Cleanup runs the auto-remove block (docker stop/rm + proxy teardown +
	// session record delete) only when opts.AutoRemove is true.
	h.Cleanup()

	if exitCode != 0 {
		os.Exit(exitCode)
	}
	return nil
}

// startSpinner shows a "Working... Xs" line on stderr, updating every second.
// Returns a stop function that clears the spinner line.
func startSpinner(start time.Time) func() {
	done := make(chan struct{})
	go func() {
		frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		i := 0
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				elapsed := time.Since(start).Truncate(time.Second)
				fmt.Fprintf(os.Stderr, "\r%s Working... %s", frames[i%len(frames)], elapsed)
				i++
			}
		}
	}()
	return func() {
		close(done)
		fmt.Fprintf(os.Stderr, "\r\033[K") // clear spinner line
	}
}

// formatDuration formats a duration as "Xm Ys" or "Xs".
func formatDuration(d time.Duration) string {
	d = d.Truncate(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%dm %ds", m, s)
}

// formatNumber formats an integer with comma separators.
func formatNumber(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	parts = append([]string{s}, parts...)
	return strings.Join(parts, ",")
}
