package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/docker"
	gitpkg "github.com/joegoldin/claude-container/internal/git"
	"github.com/joegoldin/claude-container/internal/httpproxy"
	sandboxPkg "github.com/joegoldin/claude-container/internal/sandbox"
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
		return runTask()
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

func runTask() error {
	startTime := time.Now()
	stderrIsTTY := term.IsTerminal(int(os.Stderr.Fd()))

	// --- Session setup (mirrors createSession) ---
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}

	repoRoot, _ := gitpkg.RepoRoot(cwd)

	extraWorkspaces, err := resolveWorkspaces(taskWorkspace, taskMounts)
	if err != nil {
		return err
	}

	profile := taskProfile
	if profile == "" {
		profile = "default"
	}

	name := taskName
	if name == "" {
		name = config.GenerateName(cwd)
	}

	store := config.NewStore(config.DefaultDir())
	if _, err := store.Get(name); err == nil {
		return fmt.Errorf("session %q already exists", name)
	}

	if err := docker.EnsureImage(config.DefaultDir()); err != nil {
		return err
	}

	// Proxy setup.
	proxyProfile := taskProxyProfile
	if proxyProfile == "" {
		proxyProfile = "default"
	}
	if !httpproxy.ImageExists() {
		tarball := os.Getenv("CLAUDE_PROXY_IMAGE_TARBALL")
		if tarball != "" {
			loadCmd := exec.Command("docker", "load", "-i", tarball)
			loadCmd.Stdout = os.Stderr
			loadCmd.Stderr = os.Stderr
			if err := loadCmd.Run(); err != nil {
				return fmt.Errorf("load proxy image: %w", err)
			}
		} else {
			return fmt.Errorf("proxy image %q not found; set CLAUDE_PROXY_IMAGE_TARBALL", httpproxy.ImageTag())
		}
	}

	prof, err := sandboxPkg.GetProfile(profile)
	if err != nil {
		return err
	}
	proxyRules := prof.ProxyRules(taskAllowDomains)
	rulesPath := httpproxy.ProfileRulesPath(config.DefaultDir(), proxyProfile)
	if err := os.MkdirAll(filepath.Dir(rulesPath), 0o755); err != nil {
		return fmt.Errorf("create proxy rules dir: %w", err)
	}
	rulesJSON, err := json.MarshalIndent(proxyRules, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal proxy rules: %w", err)
	}
	if err := os.WriteFile(rulesPath, rulesJSON, 0o644); err != nil {
		return fmt.Errorf("write proxy rules: %w", err)
	}

	_, resolvedPort, err := httpproxy.EnsureRunning(httpproxy.ProxyOpts{
		Profile:       proxyProfile,
		ConfigDir:     config.DefaultDir(),
		DashboardPort: taskProxyPort,
	})
	if err != nil {
		return fmt.Errorf("start proxy: %w", err)
	}
	if err := httpproxy.WaitForCACert(config.DefaultDir()); err != nil {
		return err
	}

	workspace := cwd
	if len(extraWorkspaces) > 0 {
		workspace = ""
	}

	claudeConfigDir := store.ClaudeConfigDir()
	if err := os.MkdirAll(claudeConfigDir, 0o755); err != nil {
		return fmt.Errorf("create claude config dir: %w", err)
	}
	if err := requireAuth(store); err != nil {
		return err
	}

	// Managed settings.
	var extraAllowPerms []string
	if profile != "high" {
		extraAllowPerms = append(extraAllowPerms, wrapCommandPerms(envExtraAllowCommands())...)
	}
	extraAllowPerms = append(extraAllowPerms, wrapCommandPerms(taskAllowCommands)...)
	var extraDenyPerms []string
	for _, p := range taskDenyPaths {
		extraDenyPerms = append(extraDenyPerms, fmt.Sprintf("Read(%s)", p))
	}
	extraDenyPerms = append(extraDenyPerms, wrapCommandPerms(taskDenyCommands)...)
	settingsJSON, err := json.MarshalIndent(
		prof.ManagedSettingsForProxy(8080, extraAllowPerms, extraDenyPerms), "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(claudeConfigDir, "managed-settings.json"), settingsJSON, 0o644); err != nil {
		return fmt.Errorf("write managed settings: %w", err)
	}

	runOpts := docker.RunOpts{
		Name:            name,
		Workspace:       workspace,
		ConfigDir:       claudeConfigDir,
		HostClaudeDir:   config.HostClaudeDir(),
		HostClaudeJSON:  config.HostClaudeJSON(),
		UID:             docker.ContainerUID(),
		GID:             docker.ContainerGID(),
		Prompt:          taskPrompt,
		ExtraWorkspaces: extraWorkspaces,
		ProxyProfile:    proxyProfile,
		ProxyCACertDir:  httpproxy.CACertDir(config.DefaultDir()),
	}

	// Save session.
	sess := &config.Session{
		Name:            name,
		WorktreePath:    workspace,
		RepoPath:        repoRoot,
		ContainerName:   docker.ContainerName(name),
		Yolo:            true,
		AutoRemove:      !taskKeep,
		CreatedAt:       time.Now(),
		Profile:         profile,
		ExtraWorkspaces: extraWorkspaces,
		AllowDomains:    taskAllowDomains,
		DenyPaths:       taskDenyPaths,
		AllowCommands:   taskAllowCommands,
		DenyCommands:    taskDenyCommands,
		ProxyProfile:    proxyProfile,
		ProxyPort:       resolvedPort,
	}
	if err := store.Save(sess); err != nil {
		return fmt.Errorf("save session: %w", err)
	}

	// --- Start container (detached, non-interactive) ---
	dockerArgs := docker.TaskRunArgs(runOpts, taskModel, taskMaxTurns)
	startCmd := exec.Command("docker", dockerArgs...)
	startCmd.Stderr = os.Stderr
	if err := startCmd.Run(); err != nil {
		return fmt.Errorf("start task container: %w", err)
	}

	// --- Stream logs and parse ---
	containerName := docker.ContainerName(name)

	// Start spinner on stderr (TTY only).
	var stopSpinner func()
	if stderrIsTTY {
		stopSpinner = startSpinner(startTime)
	}

	logsCmd := exec.Command("docker", "logs", "--follow", containerName)
	logsPipe, err := logsCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("logs pipe: %w", err)
	}
	logsCmd.Stderr = nil // discard docker logs stderr
	if err := logsCmd.Start(); err != nil {
		return fmt.Errorf("start logs: %w", err)
	}

	result := parseStreamEvents(logsPipe)
	logsCmd.Wait()

	if stopSpinner != nil {
		stopSpinner()
	}

	// --- Wait for container exit code ---
	exitCode, _ := docker.WaitExitCode(name)

	// --- Get changed files via docker exec ---
	var changedFiles string
	diffCmd := docker.ExecGitDiff(name)
	if diffOut, err := diffCmd.Output(); err == nil {
		changedFiles = strings.TrimSpace(string(diffOut))
	}

	// --- Output ---
	// stdout: final assistant text
	if result.FinalText != "" {
		fmt.Print(result.FinalText)
		if !strings.HasSuffix(result.FinalText, "\n") {
			fmt.Println()
		}
	}

	// stderr: summary
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
		fmt.Fprintf(os.Stderr, "Session:  %s  (kept)\n", name)
	}

	// --- Cleanup ---
	if !taskKeep {
		removeSession(store, name)
	}

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
