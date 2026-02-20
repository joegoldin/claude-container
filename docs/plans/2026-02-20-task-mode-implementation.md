# Task Mode Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a `claude-container task` subcommand that runs Claude non-interactively, emitting final output to stdout and a summary to stderr.

**Architecture:** Container runs detached with `claude -p --output-format stream-json`. Go code streams `docker logs -f`, parses NDJSON events, extracts final assistant text for stdout and metadata for a stderr summary. Container is ephemeral by default.

**Tech Stack:** Go, cobra CLI, Docker API via exec, NDJSON parsing

---

### Task 1: Add `TaskRunArgs()` to docker.go

**Files:**
- Modify: `internal/docker/docker.go`
- Test: `internal/docker/docker_test.go`

**Step 1: Write the failing tests**

Add to `internal/docker/docker_test.go`:

```go
func TestTaskRunArgs(t *testing.T) {
	opts := RunOpts{
		Name:      "task-test",
		Workspace: "/home/user/project",
		ConfigDir: "/home/user/.config/claude",
		UID:       1000,
		GID:       1000,
		Prompt:    "fix the tests",
	}
	args := TaskRunArgs(opts)
	joined := strings.Join(args, " ")

	// Must have -d (detached, no TTY).
	if !slices.Contains(args, "-d") {
		t.Errorf("TaskRunArgs missing -d in %v", args)
	}
	// Must NOT have -it or -dit.
	for _, a := range args {
		if a == "-it" || a == "-dit" {
			t.Errorf("TaskRunArgs should not have %s in %v", a, args)
		}
	}
	// Must have -p flag for print mode.
	if !slices.Contains(args, "-p") {
		t.Errorf("TaskRunArgs missing -p in %v", args)
	}
	// Must have --output-format stream-json.
	if !strings.Contains(joined, "--output-format stream-json") {
		t.Errorf("TaskRunArgs missing --output-format stream-json in %v", args)
	}
	// Must have --dangerously-skip-permissions (task mode always uses yolo).
	if !slices.Contains(args, "--dangerously-skip-permissions") {
		t.Errorf("TaskRunArgs missing --dangerously-skip-permissions in %v", args)
	}
	// Prompt should be last.
	if args[len(args)-1] != "fix the tests" {
		t.Errorf("TaskRunArgs last arg = %q, want prompt", args[len(args)-1])
	}
}

func TestTaskRunArgsModel(t *testing.T) {
	opts := RunOpts{
		Name:      "model-test",
		Workspace: "/tmp/ws",
		ConfigDir: "/tmp/cfg",
		UID:       1000,
		GID:       1000,
		Prompt:    "hello",
	}
	args := TaskRunArgs(opts, "sonnet", 0)
	joined := strings.Join(args, " ")

	if !strings.Contains(joined, "--model sonnet") {
		t.Errorf("TaskRunArgs missing --model sonnet in %v", args)
	}
}

func TestTaskRunArgsMaxTurns(t *testing.T) {
	opts := RunOpts{
		Name:      "turns-test",
		Workspace: "/tmp/ws",
		ConfigDir: "/tmp/cfg",
		UID:       1000,
		GID:       1000,
		Prompt:    "hello",
	}
	args := TaskRunArgs(opts, "", 5)
	joined := strings.Join(args, " ")

	if !strings.Contains(joined, "--max-turns 5") {
		t.Errorf("TaskRunArgs missing --max-turns 5 in %v", args)
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `nix develop --command go test ./internal/docker/ -run 'TestTaskRunArgs' -v -count=1`
Expected: FAIL — `TaskRunArgs` undefined.

**Step 3: Write `TaskRunArgs()` implementation**

Add to `internal/docker/docker.go` after `RunArgs()`:

```go
// TaskRunArgs returns docker run arguments for a non-interactive task container.
// The container runs detached (-d, no TTY) with claude in print mode (-p) and
// stream-json output. The model and maxTurns parameters are passed through to
// Claude CLI when non-empty/non-zero.
func TaskRunArgs(opts RunOpts, model string, maxTurns int) []string {
	name := ContainerName(opts.Name)

	args := []string{
		"run",
		"--name", name,
		"-d",
	}

	if opts.Workspace != "" {
		args = append(args, "-v", opts.Workspace+":/workspace")
	}
	for _, ws := range opts.ExtraWorkspaces {
		base := filepath.Base(ws)
		args = append(args, "-v", ws+":/workspace/"+base)
	}

	if opts.ProxyProfile != "" {
		proxyContainer := "claude-proxy_" + opts.ProxyProfile
		network := "claude-proxy-net_" + opts.ProxyProfile
		args = append(args,
			"--network", network,
			"-e", fmt.Sprintf("HTTP_PROXY=http://%s:8080", proxyContainer),
			"-e", fmt.Sprintf("HTTPS_PROXY=http://%s:8080", proxyContainer),
		)
		if opts.ProxyCACertDir != "" {
			args = append(args,
				"-v", opts.ProxyCACertDir+":/proxy-ca:ro",
				"-e", "SSL_CERT_FILE=/proxy-ca/mitmproxy-ca-cert.pem",
				"-e", "NIX_SSL_CERT_FILE=/proxy-ca/mitmproxy-ca-cert.pem",
				"-e", "NODE_EXTRA_CA_CERTS=/proxy-ca/mitmproxy-ca-cert.pem",
			)
		}
	}

	args = append(args,
		"-v", opts.ConfigDir+":/claude",
		"-e", "CLAUDE_CONFIG_DIR=/claude",
		"-e", fmt.Sprintf("USER_UID=%d", opts.UID),
		"-e", fmt.Sprintf("USER_GID=%d", opts.GID),
	)

	if opts.HostClaudeDir != "" {
		args = append(args, "-v", opts.HostClaudeDir+":/mnt/claude-host:ro")
	}
	if opts.HostClaudeJSON != "" {
		args = append(args, "-v", opts.HostClaudeJSON+":/mnt/claude-host-json:ro")
	}

	args = append(args, ImageTag(), "claude",
		"-p",
		"--output-format", "stream-json",
		"--dangerously-skip-permissions",
	)

	if model != "" {
		args = append(args, "--model", model)
	}
	if maxTurns > 0 {
		args = append(args, "--max-turns", fmt.Sprintf("%d", maxTurns))
	}
	if opts.Prompt != "" {
		args = append(args, opts.Prompt)
	}

	return args
}
```

**Step 4: Run tests to verify they pass**

Run: `nix develop --command go test ./internal/docker/ -run 'TestTaskRunArgs' -v -count=1`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/docker/docker.go internal/docker/docker_test.go
git commit -m "feat(docker): add TaskRunArgs for non-interactive task containers"
```

---

### Task 2: Add `ExecGitDiff()` to docker.go

**Files:**
- Modify: `internal/docker/docker.go`
- Test: `internal/docker/docker_test.go`

**Step 1: Write the failing test**

Add to `internal/docker/docker_test.go`:

```go
func TestExecGitDiffArgs(t *testing.T) {
	// We can't test actual docker exec, but we can test the command construction.
	cmd := ExecGitDiff("test-session")
	args := cmd.Args

	// Should be: docker exec <container> git diff --name-status HEAD
	if args[0] != "docker" {
		t.Errorf("first arg = %q, want docker", args[0])
	}
	if args[1] != "exec" {
		t.Errorf("second arg = %q, want exec", args[1])
	}
	if args[2] != ContainerName("test-session") {
		t.Errorf("third arg = %q, want container name", args[2])
	}
	joined := strings.Join(args[3:], " ")
	if joined != "git diff --name-status HEAD" {
		t.Errorf("git command = %q, want 'git diff --name-status HEAD'", joined)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `nix develop --command go test ./internal/docker/ -run TestExecGitDiffArgs -v -count=1`
Expected: FAIL — `ExecGitDiff` undefined.

**Step 3: Write implementation**

Add to `internal/docker/docker.go`:

```go
// ExecGitDiff returns a prepared *exec.Cmd that runs git diff --name-status HEAD
// inside the container. The caller runs it and reads stdout for changed files.
func ExecGitDiff(session string) *exec.Cmd {
	name := ContainerName(session)
	return exec.Command("docker", "exec", name, "git", "diff", "--name-status", "HEAD")
}

// WaitExitCode runs docker wait and returns the container exit code.
func WaitExitCode(session string) (int, error) {
	name := ContainerName(session)
	cmd := exec.Command("docker", "wait", name)
	out, err := cmd.Output()
	if err != nil {
		return 1, err
	}
	code := 0
	fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &code)
	return code, nil
}
```

**Step 4: Run test to verify it passes**

Run: `nix develop --command go test ./internal/docker/ -run TestExecGitDiffArgs -v -count=1`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/docker/docker.go internal/docker/docker_test.go
git commit -m "feat(docker): add ExecGitDiff and WaitExitCode helpers"
```

---

### Task 3: Create NDJSON stream parser

**Files:**
- Create: `cmd/task_parser.go`
- Create: `cmd/task_parser_test.go`

**Step 1: Write the failing tests**

Create `cmd/task_parser_test.go`:

```go
package cmd

import (
	"strings"
	"testing"
)

func TestParseStreamEvents(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"abc-123"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"I'll fix the tests now."}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Done. Here's what I changed."}]}}`,
		`{"type":"result","duration_ms":5000,"usage":{"input_tokens":1200,"output_tokens":450}}`,
	}, "\n")

	result := parseStreamEvents(strings.NewReader(input))

	if result.SessionID != "abc-123" {
		t.Errorf("SessionID = %q, want abc-123", result.SessionID)
	}
	if result.FinalText != "Done. Here's what I changed." {
		t.Errorf("FinalText = %q, want last assistant message", result.FinalText)
	}
	if result.InputTokens != 1200 {
		t.Errorf("InputTokens = %d, want 1200", result.InputTokens)
	}
	if result.OutputTokens != 450 {
		t.Errorf("OutputTokens = %d, want 450", result.OutputTokens)
	}
}

func TestParseStreamEventsEmpty(t *testing.T) {
	result := parseStreamEvents(strings.NewReader(""))
	if result.FinalText != "" {
		t.Errorf("FinalText = %q, want empty", result.FinalText)
	}
}

func TestParseStreamEventsMultiContent(t *testing.T) {
	// Assistant message with multiple content blocks — only text blocks matter.
	input := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit"},{"type":"text","text":"All done."}]}}`

	result := parseStreamEvents(strings.NewReader(input))
	if result.FinalText != "All done." {
		t.Errorf("FinalText = %q, want 'All done.'", result.FinalText)
	}
}

func TestParseStreamEventsMalformedLines(t *testing.T) {
	input := strings.Join([]string{
		`not json`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"works"}]}}`,
		`{"broken`,
	}, "\n")

	result := parseStreamEvents(strings.NewReader(input))
	if result.FinalText != "works" {
		t.Errorf("FinalText = %q, want 'works'", result.FinalText)
	}
}

func TestParseStreamEventsResultTokensNested(t *testing.T) {
	// Result event may have usage at top level or nested under message.
	input := `{"type":"result","usage":{"input_tokens":500,"output_tokens":200}}`
	result := parseStreamEvents(strings.NewReader(input))

	if result.InputTokens != 500 {
		t.Errorf("InputTokens = %d, want 500", result.InputTokens)
	}
	if result.OutputTokens != 200 {
		t.Errorf("OutputTokens = %d, want 200", result.OutputTokens)
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `nix develop --command go test ./cmd/ -run TestParseStream -v -count=1`
Expected: FAIL — `parseStreamEvents` undefined.

**Step 3: Write the parser implementation**

Create `cmd/task_parser.go`:

```go
package cmd

import (
	"bufio"
	"encoding/json"
	"io"
)

// taskResult holds parsed data from a Claude stream-json session.
type taskResult struct {
	SessionID    string
	FinalText    string
	InputTokens  int
	OutputTokens int
}

// parseStreamEvents reads NDJSON lines from r and extracts the final
// assistant text, session ID, and token usage.
func parseStreamEvents(r io.Reader) taskResult {
	var res taskResult

	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var event struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`

			// system init event
			SessionID string `json:"session_id"`

			// assistant event
			Message struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`

			// result event
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}

		if err := json.Unmarshal(line, &event); err != nil {
			continue // skip malformed lines
		}

		switch event.Type {
		case "system":
			if event.Subtype == "init" && event.SessionID != "" {
				res.SessionID = event.SessionID
			}
		case "assistant":
			// Extract last text content block from this message.
			for i := len(event.Message.Content) - 1; i >= 0; i-- {
				if event.Message.Content[i].Type == "text" {
					res.FinalText = event.Message.Content[i].Text
					break
				}
			}
		case "result":
			if event.Usage.InputTokens > 0 {
				res.InputTokens = event.Usage.InputTokens
			}
			if event.Usage.OutputTokens > 0 {
				res.OutputTokens = event.Usage.OutputTokens
			}
		}
	}

	return res
}
```

**Step 4: Run tests to verify they pass**

Run: `nix develop --command go test ./cmd/ -run TestParseStream -v -count=1`
Expected: PASS

**Step 5: Commit**

```bash
git add cmd/task_parser.go cmd/task_parser_test.go
git commit -m "feat: add NDJSON stream parser for task mode"
```

---

### Task 4: Create `cmd/task.go` subcommand

**Files:**
- Create: `cmd/task.go`

This is the main orchestration. It reuses `createSession`'s setup logic but replaces the attach/background tail with NDJSON streaming and summary output.

**Step 1: Write `cmd/task.go`**

```go
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
	taskPrompt       string
	taskName         string
	taskKeep         bool
	taskProfile      string
	taskAllowDomains []string
	taskDenyPaths    []string
	taskAllowCommands []string
	taskDenyCommands  []string
	taskMounts       []string
	taskWorkspace    string
	taskProxyProfile string
	taskProxyPort    int
	taskModel        string
	taskMaxTurns     int
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
		UID:             os.Getuid(),
		GID:             os.Getgid(),
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
```

**Step 2: Verify build compiles**

Run: `nix develop --command go build ./...`
Expected: Success (no errors).

**Step 3: Commit**

```bash
git add cmd/task.go
git commit -m "feat: add task subcommand for non-interactive headless mode"
```

---

### Task 5: Add unit tests for task helpers

**Files:**
- Create or append: `cmd/task_parser_test.go`

**Step 1: Write the tests**

Append to `cmd/task_parser_test.go`:

```go
func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{5 * time.Second, "5s"},
		{45 * time.Second, "45s"},
		{90 * time.Second, "1m 30s"},
		{125 * time.Second, "2m 5s"},
	}
	for _, tc := range tests {
		got := formatDuration(tc.d)
		if got != tc.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestFormatNumber(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{42, "42"},
		{999, "999"},
		{1000, "1,000"},
		{12450, "12,450"},
		{1234567, "1,234,567"},
	}
	for _, tc := range tests {
		got := formatNumber(tc.n)
		if got != tc.want {
			t.Errorf("formatNumber(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}
```

**Step 2: Run tests**

Run: `nix develop --command go test ./cmd/ -run 'TestFormatDuration|TestFormatNumber' -v -count=1`
Expected: PASS

**Step 3: Commit**

```bash
git add cmd/task_parser_test.go
git commit -m "test: add unit tests for task helpers (formatDuration, formatNumber)"
```

---

### Task 6: Full build verification

**Step 1: Run all existing tests**

Run: `nix develop --command go test ./internal/sandbox/ -v -count=1`
Expected: All PASS (existing tests unaffected).

Run: `nix develop --command go test ./internal/docker/ -run 'TestContainerName|TestRunArgs|TestShellArgs|TestImageTag|TestEnsureImage|TestTaskRunArgs|TestExecGitDiff' -v -count=1`
Expected: All PASS.

Run: `nix develop --command go test ./cmd/ -run 'TestParseStream|TestFormat' -v -count=1`
Expected: All PASS.

**Step 2: Verify full build**

Run: `nix develop --command go build ./...`
Expected: Success.

**Step 3: Verify CLI help**

Run: `nix develop --command go run . task --help`
Expected: Shows task subcommand help with all flags.

**Step 4: Commit (if any fixups needed)**

```bash
git add -A
git commit -m "fix: address any build issues from task mode integration"
```

Only if there were issues to fix. If everything passes clean, skip this commit.
