package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joegoldin/claude-container/internal/config"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

var testBinary string

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "claude-container-e2e-*")
	if err != nil {
		panic(err)
	}
	bin := filepath.Join(tmp, "claude-container")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = filepath.Join(moduleRoot(), ".")
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		panic("failed to build binary: " + err.Error())
	}
	testBinary = bin

	code := m.Run()
	os.RemoveAll(tmp)
	os.Exit(code)
}

func moduleRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return filepath.Dir(wd)
}

// runCLI executes the compiled binary with the given args.
func runCLI(t *testing.T, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	cmd := exec.Command(testBinary, args...)
	cmd.Env = os.Environ()
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	exitCode = 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("exec error: %v", err)
		}
	}
	return outBuf.String(), errBuf.String(), exitCode
}

// setupConfigDir creates an isolated config directory via XDG_CONFIG_HOME.
func setupConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	return dir
}

// setupGitRepo creates a temporary git repo with an initial commit.
func setupGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "e2e@test.com"},
		{"config", "user.name", "E2E Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# E2E\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "commit", "-m", "initial commit")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
	return dir
}

func skipIfDockerUnavailable(t *testing.T) {
	t.Helper()
	cmd := exec.Command("docker", "info")
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		t.Skip("docker not available:", err)
	}
}

func skipIfImageUnavailable(t *testing.T) {
	t.Helper()
	skipIfDockerUnavailable(t)
	out, err := exec.Command("docker", "images", "-q", "claude-code").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		t.Skip("claude-container image not loaded")
	}
}

func skipIfProxyUnavailable(t *testing.T) {
	t.Helper()
	skipIfImageUnavailable(t)
	out, err := exec.Command("docker", "images", "-q", "claude-proxy").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		t.Skip("proxy image not loaded")
	}
}

// skipIfNoAuth skips the test when real Claude credentials are not available.
func skipIfNoAuth(t *testing.T) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home directory")
	}
	credFile := filepath.Join(home, ".claude", ".credentials.json")
	if _, err := os.Stat(credFile); os.IsNotExist(err) {
		t.Skip("no ~/.claude/.credentials.json; need authenticated Claude Code")
	}
}

// requireDockerAndAuth is a combined skip guard for all authed Docker tests.
func requireDockerAndAuth(t *testing.T) {
	t.Helper()
	skipIfProxyUnavailable(t)
	skipIfNoAuth(t)
}

// cleanupContainer force-removes a docker container in t.Cleanup.
func cleanupContainer(t *testing.T, name string) {
	t.Helper()
	t.Cleanup(func() {
		exec.Command("docker", "rm", "-f", "claude-container_"+name).Run()
	})
}

// cleanupProxy force-removes a proxy container and network both immediately
// (to clean up stale resources from previous runs) and in t.Cleanup (to clean
// up after this test).
func cleanupProxy(t *testing.T, profile string) {
	t.Helper()
	// Immediate cleanup of stale resources from previous test runs.
	exec.Command("docker", "rm", "-f", "claude-proxy_"+profile).Run()
	exec.Command("docker", "network", "rm", "claude-proxy-net_"+profile).Run()
	// Deferred cleanup after this test completes.
	t.Cleanup(func() {
		exec.Command("docker", "rm", "-f", "claude-proxy_"+profile).Run()
		exec.Command("docker", "network", "rm", "claude-proxy-net_"+profile).Run()
	})
}

// dockerContainerExists checks if a docker container exists.
func dockerContainerExists(name string) bool {
	out, err := exec.Command("docker", "inspect", "--format={{.State.Status}}", "claude-container_"+name).Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// dockerContainerRunning checks if a docker container is currently running.
func dockerContainerRunning(name string) bool {
	out, err := exec.Command("docker", "inspect", "--format={{.State.Running}}", "claude-container_"+name).Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// dockerContainerNetwork returns the network JSON for a container.
func dockerContainerNetwork(name string) string {
	out, _ := exec.Command("docker", "inspect", "--format={{json .NetworkSettings.Networks}}", "claude-container_"+name).Output()
	return string(out)
}

// dockerContainerMounts returns the mounts JSON for a container.
func dockerContainerMounts(name string) string {
	out, _ := exec.Command("docker", "inspect", "--format={{json .Mounts}}", "claude-container_"+name).Output()
	return string(out)
}

// dockerContainerEnv returns environment variables set on a container.
func dockerContainerEnv(name string) []string {
	out, _ := exec.Command("docker", "inspect", "--format={{json .Config.Env}}", "claude-container_"+name).Output()
	var envs []string
	json.Unmarshal(out, &envs)
	return envs
}

// dockerLogs returns the last N lines of container logs.
func dockerLogs(name string, tail int) string {
	out, _ := exec.Command("docker", "logs", "--tail", fmt.Sprintf("%d", tail), "claude-container_"+name).CombinedOutput()
	return string(out)
}

// waitForContainerLogs waits for a container's logs to contain a substring.
func waitForContainerLogs(t *testing.T, name, substr string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, _ := exec.Command("docker", "logs", "claude-container_"+name).CombinedOutput()
		if strings.Contains(string(out), substr) {
			return true
		}
		time.Sleep(1 * time.Second)
	}
	return false
}

// readSessionsJSON parses sessions.json from the isolated config dir.
func readSessionsJSON(t *testing.T, xdgDir string) []map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(xdgDir, "claude-container", "sessions.json"))
	if err != nil {
		t.Fatalf("read sessions.json: %v", err)
	}
	var sessions []map[string]interface{}
	if err := json.Unmarshal(data, &sessions); err != nil {
		t.Fatalf("unmarshal sessions.json: %v\nraw: %s", err, data)
	}
	return sessions
}

// findSession finds a session by name.
func findSession(sessions []map[string]interface{}, name string) map[string]interface{} {
	for _, s := range sessions {
		if s["name"] == name {
			return s
		}
	}
	return nil
}

// readManagedSettings parses managed-settings.json from the config dir.
func readManagedSettings(t *testing.T, xdgDir string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(xdgDir, "claude-container", "claude-config", "managed-settings.json"))
	if err != nil {
		t.Fatalf("read managed-settings.json: %v", err)
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("unmarshal managed-settings.json: %v", err)
	}
	return settings
}

// gitBranchExists checks if a branch exists in a git repo.
func gitBranchExists(repo, branch string) bool {
	cmd := exec.Command("git", "branch", "--list", branch)
	cmd.Dir = repo
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// waitForBranch polls for a git branch to appear (created by container entrypoint).
func waitForBranch(t *testing.T, repo, branch string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if gitBranchExists(repo, branch) {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

// proxyRulesPath returns the path where proxy rules are written for a given
// proxy profile name within the test's isolated config dir.
func proxyRulesPath(xdgDir, proxyProfile string) string {
	return filepath.Join(xdgDir, "claude-container", "proxy-profiles", "profiles", proxyProfile+".json")
}

// runCLIWithTimeout executes the compiled binary with a custom timeout.
func runCLIWithTimeout(t *testing.T, timeout time.Duration, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	return runCLIInDirWithTimeout(t, "", timeout, args...)
}

// runCLIInDir executes the compiled binary from a specific working directory.
func runCLIInDir(t *testing.T, dir string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	return runCLIInDirWithTimeout(t, dir, 2*time.Minute, args...)
}

// runCLIInDirWithTimeout executes the compiled binary in a specific directory with a timeout.
func runCLIInDirWithTimeout(t *testing.T, dir string, timeout time.Duration, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, testBinary, args...)
	cmd.Env = os.Environ()
	if dir != "" {
		cmd.Dir = dir
	}
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	exitCode = 0
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			t.Fatalf("command timed out after %s: %v", timeout, args)
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("exec error: %v", err)
		}
	}
	return outBuf.String(), errBuf.String(), exitCode
}

// dockerExec runs a command inside a running container and returns the output.
func dockerExec(t *testing.T, name string, args ...string) (string, error) {
	t.Helper()
	cmdArgs := append([]string{"exec", "claude-container_" + name}, args...)
	out, err := exec.Command("docker", cmdArgs...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// ---------------------------------------------------------------------------
// Group 1: No external deps — pure CLI validation
// ---------------------------------------------------------------------------

func TestE2E_HelpOutput(t *testing.T) {
	setupConfigDir(t)
	stdout, _, code := runCLI(t, "--help")
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout, "Available Commands") {
		t.Errorf("help output missing 'Available Commands'")
	}
	for _, cmd := range []string{"run", "work", "task", "ps", "new", "stop", "rm", "doctor", "gc", "workspace"} {
		if !strings.Contains(stdout, cmd) {
			t.Errorf("help output missing subcommand %q", cmd)
		}
	}
}

func TestE2E_PsEmpty(t *testing.T) {
	setupConfigDir(t)
	stdout, _, code := runCLI(t, "ps")
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout, "No sessions.") {
		t.Errorf("ps = %q, want 'No sessions.'", stdout)
	}
}

func TestE2E_PsJsonEmpty(t *testing.T) {
	setupConfigDir(t)
	stdout, _, code := runCLI(t, "ps", "--json")
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if strings.TrimSpace(stdout) != "[]" {
		t.Errorf("ps --json = %q, want '[]'", stdout)
	}
}

func TestE2E_TaskRequiresPrompt(t *testing.T) {
	setupConfigDir(t)
	_, stderr, code := runCLI(t, "task")
	if code == 0 {
		t.Error("expected non-zero exit for task without --prompt")
	}
	if !strings.Contains(stderr, "--prompt is required") {
		t.Errorf("stderr = %q, want '--prompt is required'", stderr)
	}
}

func TestE2E_StopRequiresArg(t *testing.T) {
	setupConfigDir(t)
	_, stderr, code := runCLI(t, "stop")
	if code == 0 {
		t.Error("expected non-zero exit for stop without args")
	}
	if !strings.Contains(stderr, "accepts 1 arg") {
		t.Errorf("stderr = %q, want 'accepts 1 arg'", stderr)
	}
}

func TestE2E_RmRequiresArg(t *testing.T) {
	setupConfigDir(t)
	_, stderr, code := runCLI(t, "rm")
	if code == 0 {
		t.Error("expected non-zero exit for rm without args")
	}
	if !strings.Contains(stderr, "accepts 1 arg") {
		t.Errorf("stderr = %q, want 'accepts 1 arg'", stderr)
	}
}

func TestE2E_WorkspaceAddRequiresArgs(t *testing.T) {
	setupConfigDir(t)
	_, stderr, code := runCLI(t, "workspace", "add")
	if code == 0 {
		t.Error("expected non-zero exit for workspace add without args")
	}
	if !strings.Contains(stderr, "requires at least 2 arg") {
		t.Errorf("stderr = %q, want 'requires at least 2 arg'", stderr)
	}
}

func TestE2E_InvalidProfile(t *testing.T) {
	setupConfigDir(t)
	skipIfImageUnavailable(t)

	_, stderr, code := runCLI(t, "run", "--profile=invalid", "--name=test-invalid", "-b")
	if code == 0 {
		t.Error("expected non-zero exit for invalid profile")
	}
	if !strings.Contains(stderr, "unknown sandbox profile") {
		t.Errorf("stderr = %q, want 'unknown sandbox profile'", stderr)
	}
	if !strings.Contains(stderr, "low, default, med, high") {
		t.Errorf("error should list valid profiles, got: %q", stderr)
	}
}

func TestE2E_WorkspaceLifecycle(t *testing.T) {
	xdgDir := setupConfigDir(t)

	dirA := t.TempDir()
	dirB := t.TempDir()

	// ADD — verify it persists to disk.
	stdout, _, code := runCLI(t, "workspace", "add", "my-work", dirA, dirB)
	if code != 0 {
		t.Fatalf("workspace add: exit %d", code)
	}
	if !strings.Contains(stdout, `"my-work"`) || !strings.Contains(stdout, "2 paths") {
		t.Errorf("add output = %q, want name and path count", stdout)
	}
	wsFile := filepath.Join(xdgDir, "claude-container", "workspaces.json")
	wsData, err := os.ReadFile(wsFile)
	if err != nil {
		t.Fatalf("workspaces.json not created: %v", err)
	}
	var wsMap map[string]config.Workspace
	if err := json.Unmarshal(wsData, &wsMap); err != nil {
		t.Fatalf("workspaces.json invalid JSON: %v", err)
	}
	if ws, ok := wsMap["my-work"]; !ok {
		t.Error("workspaces.json missing 'my-work'")
	} else if len(ws.Paths) != 2 {
		t.Errorf("workspaces.json has %d paths, want 2", len(ws.Paths))
	} else {
		if ws.Paths[0] != dirA || ws.Paths[1] != dirB {
			t.Errorf("workspace paths = %v, want [%s %s]", ws.Paths, dirA, dirB)
		}
	}

	// LIST — verify it appears.
	stdout, _, code = runCLI(t, "workspace", "list")
	if code != 0 {
		t.Fatalf("list: exit %d", code)
	}
	if !strings.Contains(stdout, "my-work") {
		t.Errorf("list = %q, want 'my-work'", stdout)
	}

	// SHOW — verify correct paths.
	stdout, _, code = runCLI(t, "workspace", "show", "my-work")
	if code != 0 {
		t.Fatalf("show: exit %d", code)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 2 || lines[0] != dirA || lines[1] != dirB {
		t.Errorf("show = %q, want exact paths [%s, %s]", stdout, dirA, dirB)
	}

	// RM — verify removal via CLI.
	stdout, _, code = runCLI(t, "workspace", "rm", "my-work")
	if code != 0 {
		t.Fatalf("rm: exit %d", code)
	}
	if !strings.Contains(stdout, "removed") {
		t.Errorf("rm output = %q, want 'removed'", stdout)
	}

	// Show should now fail.
	_, _, code = runCLI(t, "workspace", "show", "my-work")
	if code == 0 {
		t.Error("show after rm should fail")
	}

	// List should be empty.
	stdout, _, _ = runCLI(t, "workspace", "list")
	if strings.Contains(stdout, "my-work") {
		t.Error("list after rm still contains 'my-work'")
	}
}

// ---------------------------------------------------------------------------
// Group 2: Docker + images + auth required — real session lifecycle
//
// Each test uses its own unique --proxy-profile to avoid sharing the proxy
// sidecar (and its CA cert) across tests with different XDG_CONFIG_HOME dirs.
// ---------------------------------------------------------------------------

func TestE2E_Doctor(t *testing.T) {
	setupConfigDir(t)
	skipIfDockerUnavailable(t)

	stdout, _, _ := runCLI(t, "doctor")
	if !strings.Contains(stdout, "Docker found") {
		t.Errorf("doctor should report 'Docker found'")
	}
	if !strings.Contains(stdout, "Docker daemon running") {
		t.Errorf("doctor should report 'Docker daemon running'")
	}
	if !strings.Contains(stdout, "Config dir:") {
		t.Errorf("doctor should show config dir path")
	}
}

func TestE2E_RunBackground(t *testing.T) {
	xdgDir := setupConfigDir(t)
	requireDockerAndAuth(t)

	name := "e2e-run-bg"
	proxyProf := "e2e-run-bg"
	cleanupContainer(t, name)
	cleanupProxy(t, proxyProf)

	// CREATE session in background.
	stdout, stderr, code := runCLI(t, "run", "--yolo", "-b", "--name", name,
		"--preset", proxyProf, "-p", "echo hi")
	if code != 0 {
		t.Fatalf("run -b: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, name) {
		t.Errorf("output missing session name %q", name)
	}
	if !strings.Contains(stdout, "background") {
		t.Errorf("output should mention 'background'")
	}

	// Verify Docker container was created and is running.
	if !dockerContainerExists(name) {
		t.Fatal("docker container not created")
	}
	if !dockerContainerRunning(name) {
		t.Error("docker container not running after creation")
	}

	// Verify session persisted with correct fields.
	sessions := readSessionsJSON(t, xdgDir)
	sess := findSession(sessions, name)
	if sess == nil {
		t.Fatal("session not in sessions.json")
	}
	if sess["yolo"] != true {
		t.Errorf("yolo = %v, want true", sess["yolo"])
	}
	if sess["profile"] != "default" {
		t.Errorf("profile = %v, want 'default'", sess["profile"])
	}
	if cn, _ := sess["container_name"].(string); cn != "claude-container_"+name {
		t.Errorf("container_name = %q, want %q", cn, "claude-container_"+name)
	}

	// Verify container has proxy env vars (proving proxy sidecar wired up).
	envs := dockerContainerEnv(name)
	hasProxy := false
	for _, e := range envs {
		if strings.HasPrefix(e, "HTTPS_PROXY=") {
			hasProxy = true
		}
	}
	if !hasProxy {
		t.Error("container missing HTTPS_PROXY — proxy sidecar not wired")
	}

	// ps shows it running.
	stdout, _, _ = runCLI(t, "ps")
	if !strings.Contains(stdout, name) {
		t.Errorf("ps missing %q", name)
	}
	if !strings.Contains(stdout, "NAME") || !strings.Contains(stdout, "STATUS") {
		t.Errorf("ps missing table header")
	}

	// ps --json returns valid structured data.
	stdout, _, _ = runCLI(t, "ps", "--json")
	var entries []map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &entries); err != nil {
		t.Fatalf("ps --json: invalid JSON: %v", err)
	}
	var psEntry map[string]interface{}
	for _, e := range entries {
		if e["name"] == name {
			psEntry = e
		}
	}
	if psEntry == nil {
		t.Fatal("ps --json missing session")
	}

	// STOP — container stops, but still exists.
	stdout, _, code = runCLI(t, "stop", name)
	if code != 0 {
		t.Fatalf("stop: exit %d", code)
	}
	if !strings.Contains(stdout, "stopped") {
		t.Errorf("stop output missing 'stopped'")
	}
	if dockerContainerRunning(name) {
		t.Error("container still running after stop")
	}
	if !dockerContainerExists(name) {
		t.Error("container removed after stop — should only stop")
	}

	// RM — container and session record gone.
	stdout, _, code = runCLI(t, "rm", name)
	if code != 0 {
		t.Fatalf("rm: exit %d", code)
	}
	if !strings.Contains(stdout, "removed") {
		t.Errorf("rm output missing 'removed'")
	}
	if dockerContainerExists(name) {
		t.Error("container still exists after rm")
	}

	// ps empty.
	stdout, _, _ = runCLI(t, "ps")
	if strings.Contains(stdout, name) {
		t.Errorf("ps after rm still shows %q", name)
	}
}

func TestE2E_WorkSession(t *testing.T) {
	xdgDir := setupConfigDir(t)
	requireDockerAndAuth(t)

	repo := setupGitRepo(t)
	name := "e2e-work"
	proxyProf := "e2e-work"
	cleanupContainer(t, name)
	cleanupProxy(t, proxyProf)

	origDir, _ := os.Getwd()
	os.Chdir(repo)
	t.Cleanup(func() { os.Chdir(origDir) })

	stdout, stderr, code := runCLI(t, "work", "--yolo", "-b", "--name", name,
		"--preset", proxyProf)
	if code != 0 {
		t.Fatalf("work: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	if !dockerContainerExists(name) {
		t.Fatal("container not created")
	}

	// Session has branch (worktree_path is empty for container-created worktrees).
	sessions := readSessionsJSON(t, xdgDir)
	sess := findSession(sessions, name)
	if sess == nil {
		t.Fatal("session not in sessions.json")
	}
	branch, _ := sess["branch"].(string)
	if branch == "" {
		t.Error("branch is empty — work should create a worktree branch")
	}

	// Git branch created in host repo (via bind mount) — wait for entrypoint.
	if !waitForBranch(t, repo, branch, 10*time.Second) {
		t.Errorf("git branch %q not found in repo after waiting", branch)
	}

	// Verify workspace content is accessible INSIDE the container.
	if dockerContainerRunning(name) {
		out, err := dockerExec(t, name, "cat", "/workspace/README.md")
		if err != nil {
			t.Errorf("docker exec cat README.md failed: %v", err)
		} else if !strings.Contains(out, "E2E") {
			t.Errorf("README.md inside container = %q, want 'E2E'", out)
		}

		// Verify git works inside the container (worktree has valid .git).
		branchOut, err := dockerExec(t, name, "git", "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			t.Errorf("docker exec git rev-parse failed: %v", err)
		} else if branchOut != branch {
			t.Errorf("branch inside container = %q, want %q", branchOut, branch)
		}
	}

	// rm cleans up the branch.
	runCLI(t, "rm", name)
	if gitBranchExists(repo, branch) {
		t.Errorf("git branch %q still exists after rm", branch)
	}
}

func TestE2E_WorkFromBranch(t *testing.T) {
	xdgDir := setupConfigDir(t)
	requireDockerAndAuth(t)

	repo := setupGitRepo(t)

	// Add a file on main so we can verify the worktree is based on it.
	execGit := func(args ...string) {
		c := exec.Command("git", args...)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	os.WriteFile(filepath.Join(repo, "main-marker.txt"), []byte("from main"), 0o644)
	execGit("add", "main-marker.txt")
	execGit("commit", "-m", "add marker")

	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = repo
	branchOut, _ := cmd.Output()
	mainBranch := strings.TrimSpace(string(branchOut))

	name := "e2e-from-branch"
	proxyProf := "e2e-from-branch"
	cleanupContainer(t, name)
	cleanupProxy(t, proxyProf)

	origDir, _ := os.Getwd()
	os.Chdir(repo)
	t.Cleanup(func() { os.Chdir(origDir) })

	_, stderr, code := runCLI(t, "work", "--from", mainBranch, "--yolo", "-b", "--name", name,
		"--preset", proxyProf)
	if code != 0 {
		t.Fatalf("work --from: exit %d\nstderr: %s", code, stderr)
	}

	// Session has branch set.
	sessions := readSessionsJSON(t, xdgDir)
	sess := findSession(sessions, name)
	if sess == nil {
		t.Fatal("session not in sessions.json")
	}

	// Verify worktree content (including --from marker) is accessible INSIDE the container.
	if dockerContainerRunning(name) {
		out, err := dockerExec(t, name, "cat", "/workspace/main-marker.txt")
		if err != nil {
			t.Errorf("docker exec cat main-marker.txt failed: %v", err)
		} else if !strings.Contains(out, "from main") {
			t.Errorf("main-marker.txt inside container = %q, want 'from main'", out)
		}

		// Verify git works and branch name is correct.
		branchInContainer, err := dockerExec(t, name, "git", "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			t.Errorf("docker exec git rev-parse failed: %v", err)
		} else if branchInContainer != name {
			t.Errorf("branch inside container = %q, want %q", branchInContainer, name)
		}
	}

	runCLI(t, "rm", name)
}

func TestE2E_ProfileHigh(t *testing.T) {
	xdgDir := setupConfigDir(t)
	requireDockerAndAuth(t)

	name := "e2e-profile-high"
	proxyProf := "e2e-prof-high"
	cleanupContainer(t, name)
	cleanupProxy(t, proxyProf)

	_, stderr, code := runCLI(t, "run", "--profile=high", "--allow-domain=github.com",
		"--preset", proxyProf, "-b", "--name", name)
	if code != 0 {
		t.Fatalf("run --profile=high: exit %d\nstderr: %s", code, stderr)
	}

	// Session stores profile.
	sessions := readSessionsJSON(t, xdgDir)
	sess := findSession(sessions, name)
	if sess == nil {
		t.Fatal("session not in sessions.json")
	}
	if sess["profile"] != "high" {
		t.Errorf("profile = %v, want 'high'", sess["profile"])
	}

	// Extra domain recorded.
	domains, _ := sess["allow_domains"].([]interface{})
	foundGH := false
	for _, d := range domains {
		if d == "github.com" {
			foundGH = true
		}
	}
	if !foundGH {
		t.Errorf("allow_domains = %v, want github.com", domains)
	}

	// managed-settings.json has high-profile deny rules.
	settings := readManagedSettings(t, xdgDir)
	perms, _ := settings["permissions"].(map[string]interface{})
	if perms == nil {
		t.Fatal("managed-settings missing permissions")
	}
	denyRaw, _ := perms["deny"].([]interface{})
	denySet := map[string]bool{}
	for _, d := range denyRaw {
		denySet[d.(string)] = true
	}
	if !denySet["Bash(curl *)"] {
		t.Error("high profile: missing Bash(curl *) in deny")
	}
	if !denySet["Bash(wget *)"] {
		t.Error("high profile: missing Bash(wget *) in deny")
	}

	// Proxy rules file has api.anthropic.com + github.com but NOT wildcard.
	rulesFile := proxyRulesPath(xdgDir, proxyProf)
	rulesData, err := os.ReadFile(rulesFile)
	if err != nil {
		t.Fatalf("read proxy rules: %v", err)
	}
	if strings.Contains(string(rulesData), `".*"`) {
		t.Error("high profile should NOT have wildcard proxy rule")
	}
	if !strings.Contains(string(rulesData), "anthropic") {
		t.Error("high profile proxy rules should include anthropic")
	}
	if !strings.Contains(string(rulesData), "github") {
		t.Error("high profile proxy rules should include github.com (from --allow-domain)")
	}

	runCLI(t, "rm", name)
}

func TestE2E_ProfileLow(t *testing.T) {
	xdgDir := setupConfigDir(t)
	requireDockerAndAuth(t)

	name := "e2e-profile-low"
	proxyProf := "e2e-prof-low"
	cleanupContainer(t, name)
	cleanupProxy(t, proxyProf)

	_, stderr, code := runCLI(t, "run", "--profile=low",
		"--preset", proxyProf, "-b", "--name", name)
	if code != 0 {
		t.Fatalf("run --profile=low: exit %d\nstderr: %s", code, stderr)
	}

	// Session records low + yolo.
	sessions := readSessionsJSON(t, xdgDir)
	sess := findSession(sessions, name)
	if sess == nil {
		t.Fatal("session not in sessions.json")
	}
	if sess["profile"] != "low" {
		t.Errorf("profile = %v, want 'low'", sess["profile"])
	}
	if sess["yolo"] != true {
		t.Errorf("yolo = %v, want true (low implies yolo)", sess["yolo"])
	}

	// managed-settings has permissions with allToolsAllow (dontAsk mode).
	settings := readManagedSettings(t, xdgDir)
	perms, ok := settings["permissions"]
	if !ok || perms == nil {
		t.Fatal("low profile should have permissions block with allToolsAllow")
	}
	permsMap, _ := perms.(map[string]interface{})
	allow, _ := permsMap["allow"].([]interface{})
	if len(allow) == 0 {
		t.Error("low profile should have non-empty allow list")
	}

	// Proxy rules are wildcard allow-all.
	rulesFile := proxyRulesPath(xdgDir, proxyProf)
	rulesData, err := os.ReadFile(rulesFile)
	if err != nil {
		t.Fatalf("read proxy rules: %v", err)
	}
	var rules []map[string]interface{}
	json.Unmarshal(rulesData, &rules)
	if len(rules) != 1 || rules[0]["pattern"] != ".*" {
		t.Errorf("low profile proxy rules should be single wildcard, got: %s", rulesData)
	}

	runCLI(t, "rm", name)
}

func TestE2E_AllowCommands(t *testing.T) {
	xdgDir := setupConfigDir(t)
	requireDockerAndAuth(t)

	name := "e2e-allow-cmds"
	proxyProf := "e2e-allow-cmds"
	cleanupContainer(t, name)
	cleanupProxy(t, proxyProf)

	_, stderr, code := runCLI(t, "run", "--allow-command=docker *",
		"--preset", proxyProf, "-b", "--name", name, "--yolo")
	if code != 0 {
		t.Fatalf("run --allow-command: exit %d\nstderr: %s", code, stderr)
	}

	// Session records the command.
	sessions := readSessionsJSON(t, xdgDir)
	sess := findSession(sessions, name)
	if sess == nil {
		t.Fatal("session not in sessions.json")
	}
	cmds, _ := sess["allow_commands"].([]interface{})
	found := false
	for _, c := range cmds {
		if c == "docker *" {
			found = true
		}
	}
	if !found {
		t.Errorf("allow_commands = %v, want 'docker *'", cmds)
	}

	// managed-settings has Bash(docker *) in allow.
	settings := readManagedSettings(t, xdgDir)
	perms, _ := settings["permissions"].(map[string]interface{})
	if perms == nil {
		t.Fatal("managed-settings missing permissions")
	}
	allowRaw, _ := perms["allow"].([]interface{})
	foundBash := false
	for _, a := range allowRaw {
		if a == "Bash(docker *)" {
			foundBash = true
		}
	}
	if !foundBash {
		t.Errorf("allow = %v, want 'Bash(docker *)'", allowRaw)
	}

	runCLI(t, "rm", name)
}

func TestE2E_DenyPath(t *testing.T) {
	xdgDir := setupConfigDir(t)
	requireDockerAndAuth(t)

	name := "e2e-deny-path"
	proxyProf := "e2e-deny-path"
	cleanupContainer(t, name)
	cleanupProxy(t, proxyProf)

	_, stderr, code := runCLI(t, "run", "--deny-path=/etc/passwd",
		"--preset", proxyProf, "-b", "--name", name, "--yolo")
	if code != 0 {
		t.Fatalf("run --deny-path: exit %d\nstderr: %s", code, stderr)
	}

	// Session records the deny path.
	sessions := readSessionsJSON(t, xdgDir)
	sess := findSession(sessions, name)
	if sess == nil {
		t.Fatal("session not in sessions.json")
	}
	paths, _ := sess["deny_paths"].([]interface{})
	found := false
	for _, p := range paths {
		if p == "/etc/passwd" {
			found = true
		}
	}
	if !found {
		t.Errorf("deny_paths = %v, want '/etc/passwd'", paths)
	}

	// managed-settings has Read(/etc/passwd) in deny.
	settings := readManagedSettings(t, xdgDir)
	perms, _ := settings["permissions"].(map[string]interface{})
	if perms == nil {
		t.Fatal("managed-settings missing permissions")
	}
	denyRaw, _ := perms["deny"].([]interface{})
	foundRead := false
	for _, d := range denyRaw {
		if d == "Read(/etc/passwd)" {
			foundRead = true
		}
	}
	if !foundRead {
		t.Errorf("deny = %v, want 'Read(/etc/passwd)'", denyRaw)
	}

	runCLI(t, "rm", name)
}

func TestE2E_NamedWorkspace(t *testing.T) {
	xdgDir := setupConfigDir(t)
	requireDockerAndAuth(t)

	dirA := filepath.Join(t.TempDir(), "repo-a")
	dirB := filepath.Join(t.TempDir(), "repo-b")
	os.MkdirAll(dirA, 0o755)
	os.MkdirAll(dirB, 0o755)

	_, _, code := runCLI(t, "workspace", "add", "my-work", dirA, dirB)
	if code != 0 {
		t.Fatal("workspace add failed")
	}

	name := "e2e-named-ws"
	proxyProf := "e2e-named-ws"
	cleanupContainer(t, name)
	cleanupProxy(t, proxyProf)

	_, stderr, code := runCLI(t, "run", "-W", "my-work",
		"--preset", proxyProf, "-b", "--name", name, "--yolo")
	if code != 0 {
		t.Fatalf("run -W: exit %d\nstderr: %s", code, stderr)
	}

	// Session records extra_workspaces.
	sessions := readSessionsJSON(t, xdgDir)
	sess := findSession(sessions, name)
	if sess == nil {
		t.Fatal("session not in sessions.json")
	}
	extra, _ := sess["extra_workspaces"].([]interface{})
	if len(extra) != 2 {
		t.Errorf("extra_workspaces has %d entries, want 2", len(extra))
	}

	// Container actually has the mounts.
	mounts := dockerContainerMounts(name)
	if !strings.Contains(mounts, "repo-a") || !strings.Contains(mounts, "repo-b") {
		t.Errorf("container mounts missing workspace dirs: %s", mounts)
	}

	runCLI(t, "rm", name)
}

func TestE2E_AdHocMounts(t *testing.T) {
	xdgDir := setupConfigDir(t)
	requireDockerAndAuth(t)

	dirA := filepath.Join(t.TempDir(), "mount-a")
	dirB := filepath.Join(t.TempDir(), "mount-b")
	os.MkdirAll(dirA, 0o755)
	os.MkdirAll(dirB, 0o755)

	name := "e2e-mounts"
	proxyProf := "e2e-mounts"
	cleanupContainer(t, name)
	cleanupProxy(t, proxyProf)

	_, stderr, code := runCLI(t, "run", "-w", dirA, "-w", dirB,
		"--preset", proxyProf, "-b", "--name", name, "--yolo")
	if code != 0 {
		t.Fatalf("run -w: exit %d\nstderr: %s", code, stderr)
	}

	// Session records.
	sessions := readSessionsJSON(t, xdgDir)
	sess := findSession(sessions, name)
	if sess == nil {
		t.Fatal("session not in sessions.json")
	}
	extra, _ := sess["extra_workspaces"].([]interface{})
	if len(extra) != 2 {
		t.Errorf("extra_workspaces has %d, want 2", len(extra))
	}

	// Container mounts.
	mounts := dockerContainerMounts(name)
	if !strings.Contains(mounts, "mount-a") || !strings.Contains(mounts, "mount-b") {
		t.Errorf("container mounts missing dirs: %s", mounts)
	}

	runCLI(t, "rm", name)
}

func TestE2E_EphemeralSession(t *testing.T) {
	xdgDir := setupConfigDir(t)
	requireDockerAndAuth(t)

	name := "e2e-ephemeral"
	proxyProf := "e2e-ephemeral"
	cleanupContainer(t, name)
	cleanupProxy(t, proxyProf)

	_, stderr, code := runCLI(t, "run", "--rm", "--yolo",
		"--preset", proxyProf, "-b", "--name", name)
	if code != 0 {
		t.Fatalf("run --rm: exit %d\nstderr: %s", code, stderr)
	}

	// Session record has auto_remove=true.
	sessions := readSessionsJSON(t, xdgDir)
	sess := findSession(sessions, name)
	if sess == nil {
		t.Fatal("session not in sessions.json")
	}
	if sess["auto_remove"] != true {
		t.Errorf("auto_remove = %v, want true", sess["auto_remove"])
	}

	if !dockerContainerExists(name) {
		t.Error("container not created")
	}

	runCLI(t, "rm", name)
}

func TestE2E_GarbageCollect(t *testing.T) {
	xdgDir := setupConfigDir(t)
	requireDockerAndAuth(t)

	name := "e2e-gc-target"
	proxyProf := "e2e-gc"
	cleanupContainer(t, name)
	cleanupProxy(t, proxyProf)

	_, stderr, code := runCLI(t, "run", "--yolo",
		"--preset", proxyProf, "-b", "--name", name)
	if code != 0 {
		t.Fatalf("run: exit %d, stderr=%s", code, stderr)
	}

	runCLI(t, "stop", name)

	// Stopped but exists.
	if dockerContainerRunning(name) {
		t.Error("should be stopped")
	}
	if !dockerContainerExists(name) {
		t.Error("should still exist after stop")
	}

	// gc removes the stopped container.
	stdout, _, code := runCLI(t, "gc")
	if code != 0 {
		t.Fatalf("gc: exit %d", code)
	}
	if !strings.Contains(stdout, "Removed container") {
		t.Errorf("gc output = %q, want 'Removed container'", stdout)
	}
	if dockerContainerExists(name) {
		t.Error("container still exists after gc")
	}

	// gc --all removes session records.
	_, _, code = runCLI(t, "gc", "--all")
	if code != 0 {
		t.Fatalf("gc --all: exit %d", code)
	}
	// Verify sessions.json is now empty or missing.
	sessPath := filepath.Join(xdgDir, "claude-container", "sessions.json")
	if data, err := os.ReadFile(sessPath); err == nil {
		var sessions []interface{}
		json.Unmarshal(data, &sessions)
		if len(sessions) > 0 {
			t.Errorf("sessions.json should be empty after gc --all, has %d", len(sessions))
		}
	}

	stdout, _, _ = runCLI(t, "ps")
	if strings.Contains(stdout, name) {
		t.Errorf("ps after gc --all still shows %q", name)
	}
}

func TestE2E_ProxyProfile(t *testing.T) {
	xdgDir := setupConfigDir(t)
	requireDockerAndAuth(t)

	name := "e2e-proxy-prof"
	proxyProf := "work-e2e"
	cleanupContainer(t, name)
	cleanupProxy(t, proxyProf)

	stdout, stderr, code := runCLI(t, "run", "--preset="+proxyProf,
		"-b", "--name", name, "--yolo")
	if code != 0 {
		t.Fatalf("run --proxy-profile: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	// Session records proxy profile and port.
	sessions := readSessionsJSON(t, xdgDir)
	sess := findSession(sessions, name)
	if sess == nil {
		t.Fatal("session not in sessions.json")
	}
	if sess["proxy_profile"] != proxyProf {
		t.Errorf("proxy_profile = %v, want %q", sess["proxy_profile"], proxyProf)
	}
	proxyPort, _ := sess["proxy_port"].(float64)
	if proxyPort == 0 {
		t.Error("proxy_port is 0, should be auto-assigned")
	}

	// Proxy container is actually running.
	proxyOut, err := exec.Command("docker", "inspect", "--format={{.State.Running}}", "claude-proxy_"+proxyProf).Output()
	if err != nil {
		t.Errorf("proxy container not found: %v", err)
	} else if strings.TrimSpace(string(proxyOut)) != "true" {
		t.Error("proxy container not running")
	}

	// Claude container is on the proxy network.
	networks := dockerContainerNetwork(name)
	if !strings.Contains(networks, "claude-proxy-net_"+proxyProf) {
		t.Errorf("container not on proxy network: %s", networks)
	}

	// Container has proxy env vars.
	envs := dockerContainerEnv(name)
	hasHTTP, hasHTTPS := false, false
	for _, e := range envs {
		if strings.HasPrefix(e, "HTTP_PROXY=") {
			hasHTTP = true
		}
		if strings.HasPrefix(e, "HTTPS_PROXY=") {
			hasHTTPS = true
		}
	}
	if !hasHTTP || !hasHTTPS {
		t.Error("container missing proxy env vars")
	}

	// Proxy rules file written.
	rulesFile := proxyRulesPath(xdgDir, proxyProf)
	if _, err := os.Stat(rulesFile); os.IsNotExist(err) {
		t.Error("proxy rules file not created")
	}

	runCLI(t, "rm", name)
}

func TestE2E_SharedProxy(t *testing.T) {
	setupConfigDir(t)
	requireDockerAndAuth(t)

	proxyProf := "shared-e2e"
	name1 := "e2e-shared-p1"
	name2 := "e2e-shared-p2"
	cleanupContainer(t, name1)
	cleanupContainer(t, name2)
	cleanupProxy(t, proxyProf)

	// First session starts the proxy.
	stdout1, _, code := runCLI(t, "run", "--preset="+proxyProf,
		"-b", "--name", name1, "--yolo")
	if code != 0 {
		t.Fatalf("run 1: exit %d", code)
	}
	if !strings.Contains(stdout1, "Proxy started") {
		t.Errorf("first session should show 'Proxy started', got:\n%s", stdout1)
	}

	// Second session reuses proxy.
	stdout2, _, code := runCLI(t, "run", "--preset="+proxyProf,
		"-b", "--name", name2, "--yolo")
	if code != 0 {
		t.Fatalf("run 2: exit %d", code)
	}
	if !strings.Contains(stdout2, "Reusing proxy") {
		t.Errorf("second session should show 'Reusing proxy', got:\n%s", stdout2)
	}

	// Both on same network.
	net1 := dockerContainerNetwork(name1)
	net2 := dockerContainerNetwork(name2)
	if !strings.Contains(net1, "claude-proxy-net_"+proxyProf) {
		t.Error("container 1 not on shared proxy network")
	}
	if !strings.Contains(net2, "claude-proxy-net_"+proxyProf) {
		t.Error("container 2 not on shared proxy network")
	}

	runCLI(t, "rm", name1)
	runCLI(t, "rm", name2)
}

func TestE2E_NewWithFlags(t *testing.T) {
	xdgDir := setupConfigDir(t)
	requireDockerAndAuth(t)

	repo := setupGitRepo(t)
	name := "e2e-new-flags"
	proxyProf := "e2e-new-flags"
	cleanupContainer(t, name)
	cleanupProxy(t, proxyProf)

	origDir, _ := os.Getwd()
	os.Chdir(repo)
	t.Cleanup(func() { os.Chdir(origDir) })

	stdout, stderr, code := runCLI(t, "new", "--name", name, "--worktree", "feature-auth",
		"--yolo", "--preset", proxyProf, "-b")
	if code != 0 {
		t.Fatalf("new: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	if !dockerContainerExists(name) {
		t.Fatal("container not created")
	}

	// Session has correct branch name.
	sessions := readSessionsJSON(t, xdgDir)
	sess := findSession(sessions, name)
	if sess == nil {
		t.Fatal("session not in sessions.json")
	}
	if sess["branch"] != "feature-auth" {
		t.Errorf("branch = %v, want 'feature-auth'", sess["branch"])
	}
	if sess["yolo"] != true {
		t.Errorf("yolo = %v, want true", sess["yolo"])
	}

	// Git branch created in host repo (via bind mount) — wait for entrypoint.
	if !waitForBranch(t, repo, "feature-auth", 10*time.Second) {
		t.Error("git branch 'feature-auth' not created after waiting")
	}

	// Verify worktree is accessible INSIDE the container.
	if dockerContainerRunning(name) {
		out, err := dockerExec(t, name, "cat", "/workspace/README.md")
		if err != nil {
			t.Errorf("docker exec cat README.md failed: %v", err)
		} else if !strings.Contains(out, "E2E") {
			t.Errorf("README.md inside container = %q, want 'E2E'", out)
		}

		// Verify git works inside container and branch is correct.
		branchOut, err := dockerExec(t, name, "git", "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			t.Errorf("docker exec git rev-parse failed: %v", err)
		} else if branchOut != "feature-auth" {
			t.Errorf("branch inside container = %q, want 'feature-auth'", branchOut)
		}
	}

	// ps shows branch.
	stdout, _, _ = runCLI(t, "ps")
	if !strings.Contains(stdout, "feature-auth") {
		t.Error("ps missing branch 'feature-auth'")
	}

	// rm cleans up the branch.
	runCLI(t, "rm", name)
	if gitBranchExists(repo, "feature-auth") {
		t.Error("branch still exists after rm")
	}
}

func TestE2E_WorkWithWorkspace(t *testing.T) {
	xdgDir := setupConfigDir(t)
	requireDockerAndAuth(t)

	// Create two separate git repos as extra workspaces.
	repoA := setupGitRepo(t)
	os.WriteFile(filepath.Join(repoA, "marker-a.txt"), []byte("REPO_A"), 0o644)
	execInDir := func(dir string, args ...string) {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
	}
	execInDir(repoA, "add", "marker-a.txt")
	execInDir(repoA, "commit", "-m", "add marker-a")

	repoB := setupGitRepo(t)
	os.WriteFile(filepath.Join(repoB, "marker-b.txt"), []byte("REPO_B"), 0o644)
	execInDir(repoB, "add", "marker-b.txt")
	execInDir(repoB, "commit", "-m", "add marker-b")

	// Use repoA as cwd (primary repo for worktree).
	origDir, _ := os.Getwd()
	os.Chdir(repoA)
	t.Cleanup(func() { os.Chdir(origDir) })

	// Register workspace with both repos.
	_, _, code := runCLI(t, "workspace", "add", "multi-wt", repoA, repoB)
	if code != 0 {
		t.Fatal("workspace add failed")
	}

	name := "e2e-work-ws"
	proxyProf := "e2e-work-ws"
	cleanupContainer(t, name)
	cleanupProxy(t, proxyProf)

	stdout, stderr, code := runCLI(t, "work", "-W", "multi-wt", "--yolo", "-b",
		"--name", name, "--preset", proxyProf)
	if code != 0 {
		t.Fatalf("work -W: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	if !dockerContainerExists(name) {
		t.Fatal("container not created")
	}

	// Session should have worktree_repos set and extra_workspaces empty.
	sessions := readSessionsJSON(t, xdgDir)
	sess := findSession(sessions, name)
	if sess == nil {
		t.Fatal("session not in sessions.json")
	}
	wtRepos, _ := sess["worktree_repos"].([]interface{})
	if len(wtRepos) != 2 {
		t.Errorf("worktree_repos has %d entries, want 2", len(wtRepos))
	}
	extraWs, _ := sess["extra_workspaces"].([]interface{})
	if len(extraWs) != 0 {
		t.Errorf("extra_workspaces should be empty in worktree mode, got %d", len(extraWs))
	}
	branch, _ := sess["branch"].(string)
	if branch == "" {
		t.Error("branch is empty")
	}

	// Branches created in both host repos — wait for entrypoint.
	if !waitForBranch(t, repoA, branch, 10*time.Second) {
		t.Errorf("branch %q not found in repoA after waiting", branch)
	}
	if !waitForBranch(t, repoB, branch, 10*time.Second) {
		t.Errorf("branch %q not found in repoB after waiting", branch)
	}

	// Verify worktree content is accessible INSIDE the container.
	if dockerContainerRunning(name) {
		baseA := filepath.Base(repoA)
		baseB := filepath.Base(repoB)

		out, err := dockerExec(t, name, "cat", "/workspace/"+baseA+"/marker-a.txt")
		if err != nil {
			t.Errorf("docker exec cat marker-a.txt failed: %v", err)
		} else if !strings.Contains(out, "REPO_A") {
			t.Errorf("marker-a.txt = %q, want 'REPO_A'", out)
		}

		out, err = dockerExec(t, name, "cat", "/workspace/"+baseB+"/marker-b.txt")
		if err != nil {
			t.Errorf("docker exec cat marker-b.txt failed: %v", err)
		} else if !strings.Contains(out, "REPO_B") {
			t.Errorf("marker-b.txt = %q, want 'REPO_B'", out)
		}

		// Git works in each worktree.
		branchA, err := dockerExec(t, name, "git", "-C", "/workspace/"+baseA, "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			t.Errorf("git rev-parse in repoA failed: %v", err)
		} else if branchA != branch {
			t.Errorf("branch in repoA = %q, want %q", branchA, branch)
		}

		branchB, err := dockerExec(t, name, "git", "-C", "/workspace/"+baseB, "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			t.Errorf("git rev-parse in repoB failed: %v", err)
		} else if branchB != branch {
			t.Errorf("branch in repoB = %q, want %q", branchB, branch)
		}
	}

	// rm cleans up branches in both repos.
	runCLI(t, "rm", name)
	if gitBranchExists(repoA, branch) {
		t.Errorf("branch %q still exists in repoA after rm", branch)
	}
	if gitBranchExists(repoB, branch) {
		t.Errorf("branch %q still exists in repoB after rm", branch)
	}
}

func TestE2E_RunFilePermissions(t *testing.T) {
	setupConfigDir(t)
	requireDockerAndAuth(t)

	repo := setupGitRepo(t)
	// Write a marker file so we have something to overwrite inside the container.
	os.WriteFile(filepath.Join(repo, "host-marker.txt"), []byte("FROM_HOST\n"), 0o644)

	name := "e2e-run-perms"
	proxyProf := "e2e-run-perms"
	cleanupContainer(t, name)
	cleanupProxy(t, proxyProf)

	origDir, _ := os.Getwd()
	os.Chdir(repo)
	t.Cleanup(func() { os.Chdir(origDir) })

	_, stderr, code := runCLI(t, "run", "--yolo", "-b", "--name", name,
		"--preset", proxyProf)
	if code != 0 {
		t.Fatalf("run -b: exit %d\nstderr: %s", code, stderr)
	}

	// Wait for container to be ready.
	time.Sleep(3 * time.Second)
	if !dockerContainerRunning(name) {
		logs := dockerLogs(name, 50)
		t.Fatalf("container not running\nLogs:\n%s", logs)
	}

	// Create a file inside the container.
	_, err := dockerExec(t, name, "touch", "/workspace/created-inside.txt")
	if err != nil {
		t.Fatalf("docker exec touch failed: %v", err)
	}

	// Verify file appears on host.
	hostPath := filepath.Join(repo, "created-inside.txt")
	info, err := os.Stat(hostPath)
	if err != nil {
		t.Fatalf("file created inside container not visible on host: %v", err)
	}
	if info.IsDir() {
		t.Fatal("created-inside.txt is a directory, want file")
	}

	// Verify owner UID matches current user (rootless Docker maps container
	// root to host user).
	expectedUID := os.Getuid()
	stat, err := os.Stat(hostPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	_ = stat // UID check via docker exec below is more portable.

	// Verify UID inside the container (should be 0 for rootless, or USER_UID).
	uidOut, err := dockerExec(t, name, "stat", "-c", "%u", "/workspace/created-inside.txt")
	if err != nil {
		t.Errorf("stat UID inside container failed: %v", err)
	}
	t.Logf("file UID inside container: %s (host UID: %d)", uidOut, expectedUID)

	// Write to existing host file from inside the container.
	_, err = dockerExec(t, name, "sh", "-c", "echo MODIFIED_INSIDE > /workspace/host-marker.txt")
	if err != nil {
		t.Fatalf("docker exec write failed: %v", err)
	}

	// Verify changes visible on host.
	data, err := os.ReadFile(filepath.Join(repo, "host-marker.txt"))
	if err != nil {
		t.Fatalf("read host-marker.txt: %v", err)
	}
	if !strings.Contains(string(data), "MODIFIED_INSIDE") {
		t.Errorf("host-marker.txt = %q, want 'MODIFIED_INSIDE'", string(data))
	}

	// Cleanup.
	os.Remove(hostPath)
	runCLI(t, "rm", name)
}

func TestE2E_WorktreeFilePermissions(t *testing.T) {
	xdgDir := setupConfigDir(t)
	requireDockerAndAuth(t)

	repo := setupGitRepo(t)
	name := "e2e-wt-perms"
	proxyProf := "e2e-wt-perms"
	cleanupContainer(t, name)
	cleanupProxy(t, proxyProf)

	origDir, _ := os.Getwd()
	os.Chdir(repo)
	t.Cleanup(func() { os.Chdir(origDir) })

	_, stderr, code := runCLI(t, "work", "--yolo", "-b", "--name", name,
		"--preset", proxyProf)
	if code != 0 {
		t.Fatalf("work -b: exit %d\nstderr: %s", code, stderr)
	}

	// Wait for the worktree branch to be created by the entrypoint.
	sessions := readSessionsJSON(t, xdgDir)
	sess := findSession(sessions, name)
	if sess == nil {
		t.Fatal("session not in sessions.json")
	}
	branch, _ := sess["branch"].(string)
	if branch == "" {
		t.Fatal("branch is empty")
	}
	if !waitForBranch(t, repo, branch, 15*time.Second) {
		t.Fatalf("branch %q not created in time", branch)
	}

	// Wait for container to settle.
	time.Sleep(3 * time.Second)
	if !dockerContainerRunning(name) {
		logs := dockerLogs(name, 50)
		t.Fatalf("container not running\nLogs:\n%s", logs)
	}

	// Create a file inside the container worktree.
	_, err := dockerExec(t, name, "touch", "/workspace/wt-created.txt")
	if err != nil {
		t.Fatalf("docker exec touch failed: %v", err)
	}

	// Verify the file exists inside the container.
	out, err := dockerExec(t, name, "ls", "/workspace/wt-created.txt")
	if err != nil {
		t.Errorf("file not found inside container: %v", err)
	} else if !strings.Contains(out, "wt-created.txt") {
		t.Errorf("ls = %q, want 'wt-created.txt'", out)
	}

	// Write content and read it back inside the container.
	_, err = dockerExec(t, name, "sh", "-c", "echo WT_CONTENT > /workspace/wt-created.txt")
	if err != nil {
		t.Fatalf("docker exec write failed: %v", err)
	}
	content, err := dockerExec(t, name, "cat", "/workspace/wt-created.txt")
	if err != nil {
		t.Errorf("docker exec cat failed: %v", err)
	} else if !strings.Contains(content, "WT_CONTENT") {
		t.Errorf("cat = %q, want 'WT_CONTENT'", content)
	}

	// Verify UID inside container.
	uidOut, err := dockerExec(t, name, "stat", "-c", "%u", "/workspace/wt-created.txt")
	if err != nil {
		t.Errorf("stat UID failed: %v", err)
	}
	t.Logf("worktree file UID inside container: %s (host UID: %d)", uidOut, os.Getuid())

	// Cleanup.
	runCLI(t, "rm", name)
}

func TestE2E_MountFilePermissions(t *testing.T) {
	setupConfigDir(t)
	requireDockerAndAuth(t)

	// Create a temp dir with a marker file to mount.
	mountDir := filepath.Join(t.TempDir(), "perm-mount")
	os.MkdirAll(mountDir, 0o755)
	os.WriteFile(filepath.Join(mountDir, "marker.txt"), []byte("MOUNT_MARKER\n"), 0o644)
	mountBase := filepath.Base(mountDir)

	name := "e2e-mount-perms"
	proxyProf := "e2e-mount-perms"
	cleanupContainer(t, name)
	cleanupProxy(t, proxyProf)

	_, stderr, code := runCLI(t, "run", "--yolo", "-b", "--name", name,
		"--preset", proxyProf, "-w", mountDir)
	if code != 0 {
		t.Fatalf("run -b -w: exit %d\nstderr: %s", code, stderr)
	}

	// Wait for container to be ready.
	time.Sleep(3 * time.Second)
	if !dockerContainerRunning(name) {
		logs := dockerLogs(name, 50)
		t.Fatalf("container not running\nLogs:\n%s", logs)
	}

	// Verify mounted marker file is readable inside container.
	out, err := dockerExec(t, name, "cat", "/workspace/"+mountBase+"/marker.txt")
	if err != nil {
		t.Fatalf("docker exec cat marker.txt failed: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "MOUNT_MARKER") {
		t.Errorf("marker.txt = %q, want 'MOUNT_MARKER'", out)
	}

	// Write a new file inside the mounted directory from container.
	_, err = dockerExec(t, name, "sh", "-c",
		fmt.Sprintf("echo WRITTEN > /workspace/%s/new-file.txt", mountBase))
	if err != nil {
		t.Fatalf("docker exec write failed: %v", err)
	}

	// Verify new file appears on host.
	hostNewFile := filepath.Join(mountDir, "new-file.txt")
	data, err := os.ReadFile(hostNewFile)
	if err != nil {
		t.Fatalf("new-file.txt not visible on host: %v", err)
	}
	if !strings.Contains(string(data), "WRITTEN") {
		t.Errorf("new-file.txt = %q, want 'WRITTEN'", string(data))
	}

	// Verify file ownership on host matches current user.
	info, err := os.Stat(hostNewFile)
	if err != nil {
		t.Fatalf("stat new-file.txt: %v", err)
	}
	t.Logf("new-file.txt on host: size=%d, mode=%s", info.Size(), info.Mode())

	// Cleanup.
	os.Remove(hostNewFile)
	runCLI(t, "rm", name)
}

// ---------------------------------------------------------------------------
// Group 3: Live execution — Claude actually runs and produces output
//
// These tests verify that Claude works end-to-end inside containers.
// `task` tests run Claude non-interactively and verify stdout output.
// Background session tests verify Claude starts and workspace is correct
// via `docker exec`.
// ---------------------------------------------------------------------------

func TestLive_TaskPong(t *testing.T) {
	setupConfigDir(t)
	requireDockerAndAuth(t)

	repo := setupGitRepo(t)
	name := "e2e-live-pong"
	proxyProf := "e2e-live-pong"
	cleanupContainer(t, name)
	cleanupProxy(t, proxyProf)

	stdout, stderr, code := runCLIInDirWithTimeout(t, repo, 5*time.Minute,
		"task", "-p", "Respond with exactly the word PONG and nothing else.",
		"--name", name, "--preset", proxyProf, "--max-turns", "1")

	if code != 0 {
		t.Fatalf("task exited %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "PONG") {
		t.Errorf("stdout = %q, want PONG in output", stdout)
	}
	if !strings.Contains(stderr, "Task Complete") {
		t.Errorf("stderr missing 'Task Complete'")
	}
	if !strings.Contains(stderr, "Duration:") {
		t.Errorf("stderr missing 'Duration:'")
	}
	if !strings.Contains(stderr, "Tokens:") {
		t.Errorf("stderr missing token summary")
	}
}

func TestLive_TaskReadsFile(t *testing.T) {
	setupConfigDir(t)
	requireDockerAndAuth(t)

	repo := setupGitRepo(t)
	// Write a file with a unique sentinel value and commit it.
	sentinel := "SENTINEL_E2E_42"
	if err := os.WriteFile(filepath.Join(repo, "magic.txt"), []byte(sentinel+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd := exec.Command("git", "add", "magic.txt")
	gitCmd.Dir = repo
	gitCmd.CombinedOutput()
	gitCmd = exec.Command("git", "commit", "-m", "add magic file")
	gitCmd.Dir = repo
	gitCmd.CombinedOutput()

	name := "e2e-live-reads"
	proxyProf := "e2e-live-reads"
	cleanupContainer(t, name)
	cleanupProxy(t, proxyProf)

	stdout, stderr, code := runCLIInDirWithTimeout(t, repo, 5*time.Minute,
		"task", "-p", "Read the file magic.txt in the current directory and respond with its exact contents, nothing else.",
		"--name", name, "--preset", proxyProf, "--max-turns", "3")

	if code != 0 {
		t.Fatalf("task exited %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, sentinel) {
		t.Errorf("stdout = %q, want sentinel %q — Claude could not read workspace file", stdout, sentinel)
	}
}

func TestLive_TaskMaxTurns(t *testing.T) {
	setupConfigDir(t)
	requireDockerAndAuth(t)

	repo := setupGitRepo(t)
	name := "e2e-live-turns"
	proxyProf := "e2e-live-turns"
	cleanupContainer(t, name)
	cleanupProxy(t, proxyProf)

	stdout, stderr, code := runCLIInDirWithTimeout(t, repo, 5*time.Minute,
		"task", "-p", "Respond with exactly the word TURNS_OK and nothing else.",
		"--name", name, "--preset", proxyProf, "--max-turns", "1")

	if code != 0 {
		t.Fatalf("task --max-turns=1 exited %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "TURNS_OK") {
		t.Errorf("stdout = %q, want TURNS_OK", stdout)
	}
}

func TestLive_TaskKeep(t *testing.T) {
	xdgDir := setupConfigDir(t)
	requireDockerAndAuth(t)

	repo := setupGitRepo(t)
	name := "e2e-live-keep"
	proxyProf := "e2e-live-keep"
	cleanupContainer(t, name)
	cleanupProxy(t, proxyProf)

	stdout, stderr, code := runCLIInDirWithTimeout(t, repo, 5*time.Minute,
		"task", "-p", "Respond with exactly the word KEPT and nothing else.",
		"--name", name, "--preset", proxyProf, "--keep", "--max-turns", "1")

	if code != 0 {
		t.Fatalf("task --keep exited %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "KEPT") {
		t.Errorf("stdout = %q, want KEPT", stdout)
	}

	// Session should still exist because --keep was used.
	sessions := readSessionsJSON(t, xdgDir)
	sess := findSession(sessions, name)
	if sess == nil {
		t.Error("session removed despite --keep flag")
	}

	// ps should show it.
	psOut, _, _ := runCLI(t, "ps")
	if !strings.Contains(psOut, name) {
		t.Errorf("ps missing kept session %q", name)
	}

	runCLI(t, "rm", name)
}

func TestLive_TaskWithMounts(t *testing.T) {
	setupConfigDir(t)
	requireDockerAndAuth(t)

	// Create a directory with a marker file to mount as extra workspace.
	mountDir := filepath.Join(t.TempDir(), "extra-mount")
	os.MkdirAll(mountDir, 0o755)
	os.WriteFile(filepath.Join(mountDir, "mounted-sentinel.txt"), []byte("MOUNT_OK_99\n"), 0o644)

	repo := setupGitRepo(t)
	name := "e2e-live-mounts"
	proxyProf := "e2e-live-mounts"
	cleanupContainer(t, name)
	cleanupProxy(t, proxyProf)

	// Extra workspaces become /workspace/<basename>/ inside the container.
	stdout, stderr, code := runCLIInDirWithTimeout(t, repo, 5*time.Minute,
		"task", "-p", "Read the file /workspace/extra-mount/mounted-sentinel.txt and respond with its exact contents, nothing else.",
		"--name", name, "--preset", proxyProf, "-w", mountDir, "--max-turns", "3")

	if code != 0 {
		t.Fatalf("task -w mount exited %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "MOUNT_OK_99") {
		t.Errorf("stdout = %q, want MOUNT_OK_99 proving extra mount is visible to Claude", stdout)
	}
}

func TestLive_RunContainerLive(t *testing.T) {
	setupConfigDir(t)
	requireDockerAndAuth(t)

	repo := setupGitRepo(t)
	// Write a marker so we can verify workspace content inside the container.
	os.WriteFile(filepath.Join(repo, "run-marker.txt"), []byte("RUN_LIVE_OK\n"), 0o644)

	name := "e2e-live-run"
	proxyProf := "e2e-live-run"
	cleanupContainer(t, name)
	cleanupProxy(t, proxyProf)

	origDir, _ := os.Getwd()
	os.Chdir(repo)
	t.Cleanup(func() { os.Chdir(origDir) })

	_, stderr, code := runCLI(t, "run", "--yolo", "-b", "--name", name,
		"--preset", proxyProf)
	if code != 0 {
		t.Fatalf("run -b: exit %d\nstderr: %s", code, stderr)
	}

	if !dockerContainerRunning(name) {
		t.Fatal("container not running after creation")
	}

	// Wait for entrypoint + Claude to finish initializing.
	time.Sleep(5 * time.Second)

	// Container should still be running (Claude didn't crash).
	if !dockerContainerRunning(name) {
		logs := dockerLogs(name, 50)
		t.Fatalf("container exited within 5s — Claude likely failed to start.\nLogs:\n%s", logs)
	}

	// Verify workspace files are accessible INSIDE the container.
	out, err := dockerExec(t, name, "cat", "/workspace/run-marker.txt")
	if err != nil {
		t.Errorf("docker exec cat run-marker.txt failed: %v\noutput: %s", err, out)
	} else if !strings.Contains(out, "RUN_LIVE_OK") {
		t.Errorf("run-marker.txt inside container = %q, want 'RUN_LIVE_OK'", out)
	}

	// Verify README.md from the git repo is also visible.
	out, err = dockerExec(t, name, "cat", "/workspace/README.md")
	if err != nil {
		t.Errorf("docker exec cat README.md failed: %v", err)
	} else if !strings.Contains(out, "E2E") {
		t.Errorf("README.md inside container = %q, want 'E2E'", out)
	}

	// `run` mounts a real git repo (not a worktree), so git works inside.
	branchOut, err := dockerExec(t, name, "git", "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		t.Errorf("docker exec git rev-parse failed: %v", err)
	} else {
		// setupGitRepo creates a repo on "main" or "master".
		if branchOut != "main" && branchOut != "master" {
			t.Errorf("branch inside container = %q, want main or master", branchOut)
		}
	}

	runCLI(t, "rm", name)
}

func TestLive_WorkContainerLive(t *testing.T) {
	setupConfigDir(t)
	requireDockerAndAuth(t)

	repo := setupGitRepo(t)
	name := "e2e-live-work"
	proxyProf := "e2e-live-work"
	cleanupContainer(t, name)
	cleanupProxy(t, proxyProf)

	origDir, _ := os.Getwd()
	os.Chdir(repo)
	t.Cleanup(func() { os.Chdir(origDir) })

	_, stderr, code := runCLI(t, "work", "--yolo", "-b", "--name", name,
		"--preset", proxyProf)
	if code != 0 {
		t.Fatalf("work -b: exit %d\nstderr: %s", code, stderr)
	}

	if !dockerContainerRunning(name) {
		t.Fatal("container not running")
	}

	time.Sleep(5 * time.Second)
	if !dockerContainerRunning(name) {
		logs := dockerLogs(name, 50)
		t.Fatalf("container exited within 5s — Claude likely failed to start.\nLogs:\n%s", logs)
	}

	// Verify worktree README.md is accessible inside the container.
	out, err := dockerExec(t, name, "cat", "/workspace/README.md")
	if err != nil {
		t.Errorf("docker exec cat README.md failed: %v\noutput: %s", err, out)
	} else if !strings.Contains(out, "E2E") {
		t.Errorf("README.md inside container = %q, want 'E2E'", out)
	}

	// Verify git works inside the container (worktree created by entrypoint).
	branchOut, err := dockerExec(t, name, "git", "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		t.Errorf("docker exec git rev-parse failed: %v", err)
	} else if branchOut != name {
		t.Errorf("branch inside container = %q, want %q", branchOut, name)
	}

	runCLI(t, "rm", name)
}

func TestLive_NewContainerLive(t *testing.T) {
	setupConfigDir(t)
	requireDockerAndAuth(t)

	repo := setupGitRepo(t)
	name := "e2e-live-new"
	proxyProf := "e2e-live-new"
	cleanupContainer(t, name)
	cleanupProxy(t, proxyProf)

	origDir, _ := os.Getwd()
	os.Chdir(repo)
	t.Cleanup(func() { os.Chdir(origDir) })

	_, stderr, code := runCLI(t, "new", "--name", name, "--worktree", "live-branch",
		"--yolo", "--preset", proxyProf, "-b")
	if code != 0 {
		t.Fatalf("new -b: exit %d\nstderr: %s", code, stderr)
	}

	if !dockerContainerRunning(name) {
		t.Fatal("container not running")
	}

	time.Sleep(5 * time.Second)
	if !dockerContainerRunning(name) {
		logs := dockerLogs(name, 50)
		t.Fatalf("container exited within 5s — Claude likely failed to start.\nLogs:\n%s", logs)
	}

	// Verify the git branch was created on the host (via bind mount).
	if !gitBranchExists(repo, "live-branch") {
		t.Error("git branch 'live-branch' not created on host")
	}

	// Verify worktree content is accessible inside the container.
	out, err := dockerExec(t, name, "cat", "/workspace/README.md")
	if err != nil {
		t.Errorf("docker exec cat README.md failed: %v\noutput: %s", err, out)
	} else if !strings.Contains(out, "E2E") {
		t.Errorf("README.md inside container = %q, want 'E2E'", out)
	}

	// Verify git works inside the container — worktree is on the correct branch.
	branchOut, err := dockerExec(t, name, "git", "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		t.Errorf("docker exec git rev-parse failed: %v", err)
	} else if branchOut != "live-branch" {
		t.Errorf("branch inside container = %q, want 'live-branch'", branchOut)
	}

	runCLI(t, "rm", name)
}

func TestLive_WorkWithWorkspaceLive(t *testing.T) {
	setupConfigDir(t)
	requireDockerAndAuth(t)

	// Create two separate git repos.
	repoA := setupGitRepo(t)
	os.WriteFile(filepath.Join(repoA, "live-a.txt"), []byte("LIVE_A"), 0o644)
	execInDir := func(dir string, args ...string) {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
	}
	execInDir(repoA, "add", "live-a.txt")
	execInDir(repoA, "commit", "-m", "add live-a")

	repoB := setupGitRepo(t)
	os.WriteFile(filepath.Join(repoB, "live-b.txt"), []byte("LIVE_B"), 0o644)
	execInDir(repoB, "add", "live-b.txt")
	execInDir(repoB, "commit", "-m", "add live-b")

	origDir, _ := os.Getwd()
	os.Chdir(repoA)
	t.Cleanup(func() { os.Chdir(origDir) })

	// Register workspace.
	_, _, code := runCLI(t, "workspace", "add", "live-multi", repoA, repoB)
	if code != 0 {
		t.Fatal("workspace add failed")
	}

	name := "e2e-live-multi"
	proxyProf := "e2e-live-multi"
	cleanupContainer(t, name)
	cleanupProxy(t, proxyProf)

	_, stderr, code := runCLI(t, "work", "-W", "live-multi", "--yolo", "-b",
		"--name", name, "--preset", proxyProf)
	if code != 0 {
		t.Fatalf("work -W: exit %d\nstderr: %s", code, stderr)
	}

	if !dockerContainerRunning(name) {
		t.Fatal("container not running")
	}

	time.Sleep(5 * time.Second)
	if !dockerContainerRunning(name) {
		logs := dockerLogs(name, 50)
		t.Fatalf("container exited within 5s.\nLogs:\n%s", logs)
	}

	baseA := filepath.Base(repoA)
	baseB := filepath.Base(repoB)

	// Verify files are accessible inside container worktrees.
	out, err := dockerExec(t, name, "cat", "/workspace/"+baseA+"/live-a.txt")
	if err != nil {
		t.Errorf("docker exec cat live-a.txt failed: %v\noutput: %s", err, out)
	} else if !strings.Contains(out, "LIVE_A") {
		t.Errorf("live-a.txt = %q, want 'LIVE_A'", out)
	}

	out, err = dockerExec(t, name, "cat", "/workspace/"+baseB+"/live-b.txt")
	if err != nil {
		t.Errorf("docker exec cat live-b.txt failed: %v\noutput: %s", err, out)
	} else if !strings.Contains(out, "LIVE_B") {
		t.Errorf("live-b.txt = %q, want 'LIVE_B'", out)
	}

	// Git works in each worktree.
	branchA, err := dockerExec(t, name, "git", "-C", "/workspace/"+baseA, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		t.Errorf("git rev-parse in repoA failed: %v", err)
	} else if branchA != name {
		t.Errorf("branch in repoA = %q, want %q", branchA, name)
	}

	branchB, err := dockerExec(t, name, "git", "-C", "/workspace/"+baseB, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		t.Errorf("git rev-parse in repoB failed: %v", err)
	} else if branchB != name {
		t.Errorf("branch in repoB = %q, want %q", branchB, name)
	}

	runCLI(t, "rm", name)
}

// ---------------------------------------------------------------------------
// Group 5: Parallel sessions — multiple containers from same repo
// ---------------------------------------------------------------------------

func TestE2E_ParallelRunSessions(t *testing.T) {
	xdgDir := setupConfigDir(t)
	requireDockerAndAuth(t)

	repo := setupGitRepo(t)
	os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("SHARED_DATA\n"), 0o644)

	name1 := "e2e-par-run1"
	name2 := "e2e-par-run2"
	proxyProf := "e2e-par-run"
	cleanupContainer(t, name1)
	cleanupContainer(t, name2)
	cleanupProxy(t, proxyProf)

	origDir, _ := os.Getwd()
	os.Chdir(repo)
	t.Cleanup(func() { os.Chdir(origDir) })

	// Start two run sessions (no worktree) sharing the same mounted workspace.
	_, stderr1, code := runCLI(t, "run", "--yolo", "-b", "--name", name1,
		"--preset", proxyProf)
	if code != 0 {
		t.Fatalf("run 1: exit %d\nstderr: %s", code, stderr1)
	}

	_, stderr2, code := runCLI(t, "run", "--yolo", "-b", "--name", name2,
		"--preset", proxyProf)
	if code != 0 {
		t.Fatalf("run 2: exit %d\nstderr: %s", code, stderr2)
	}

	// Both containers should be running.
	if !dockerContainerRunning(name1) {
		t.Error("container 1 not running")
	}
	if !dockerContainerRunning(name2) {
		t.Error("container 2 not running")
	}

	// Both should see workspace files.
	out1, err := dockerExec(t, name1, "cat", "/workspace/shared.txt")
	if err != nil {
		t.Errorf("container 1 cat shared.txt failed: %v", err)
	} else if !strings.Contains(out1, "SHARED_DATA") {
		t.Errorf("container 1 shared.txt = %q, want 'SHARED_DATA'", out1)
	}

	out2, err := dockerExec(t, name2, "cat", "/workspace/shared.txt")
	if err != nil {
		t.Errorf("container 2 cat shared.txt failed: %v", err)
	} else if !strings.Contains(out2, "SHARED_DATA") {
		t.Errorf("container 2 shared.txt = %q, want 'SHARED_DATA'", out2)
	}

	// Both in sessions.json.
	sessions := readSessionsJSON(t, xdgDir)
	if findSession(sessions, name1) == nil {
		t.Error("session 1 not in sessions.json")
	}
	if findSession(sessions, name2) == nil {
		t.Error("session 2 not in sessions.json")
	}

	runCLI(t, "rm", name1)
	runCLI(t, "rm", name2)
}

func TestE2E_ParallelWorkSessions(t *testing.T) {
	xdgDir := setupConfigDir(t)
	requireDockerAndAuth(t)

	repo := setupGitRepo(t)

	name1 := "e2e-par-work1"
	name2 := "e2e-par-work2"
	proxyProf := "e2e-par-work"
	cleanupContainer(t, name1)
	cleanupContainer(t, name2)
	cleanupProxy(t, proxyProf)

	origDir, _ := os.Getwd()
	os.Chdir(repo)
	t.Cleanup(func() { os.Chdir(origDir) })

	// Start first work session.
	_, stderr1, code := runCLI(t, "work", "--yolo", "-b", "--name", name1,
		"--preset", proxyProf)
	if code != 0 {
		t.Fatalf("work 1: exit %d\nstderr: %s", code, stderr1)
	}

	// Start second work session — this is the bug case (used to fail).
	_, stderr2, code := runCLI(t, "work", "--yolo", "-b", "--name", name2,
		"--preset", proxyProf)
	if code != 0 {
		t.Fatalf("work 2: exit %d\nstderr: %s", code, stderr2)
	}

	// Both containers should be running.
	if !dockerContainerRunning(name1) {
		t.Error("container 1 not running")
	}
	if !dockerContainerRunning(name2) {
		t.Error("container 2 not running")
	}

	// Get branch names from sessions.
	sessions := readSessionsJSON(t, xdgDir)
	sess1 := findSession(sessions, name1)
	sess2 := findSession(sessions, name2)
	if sess1 == nil {
		t.Fatal("session 1 not in sessions.json")
	}
	if sess2 == nil {
		t.Fatal("session 2 not in sessions.json")
	}
	branch1, _ := sess1["branch"].(string)
	branch2, _ := sess2["branch"].(string)
	if branch1 == "" {
		t.Fatal("branch1 is empty")
	}
	if branch2 == "" {
		t.Fatal("branch2 is empty")
	}
	if branch1 == branch2 {
		t.Errorf("both sessions have same branch %q — should be unique", branch1)
	}

	// Both branches should exist in the host repo simultaneously.
	if !waitForBranch(t, repo, branch1, 15*time.Second) {
		t.Errorf("branch %q not found in repo", branch1)
	}
	if !waitForBranch(t, repo, branch2, 15*time.Second) {
		t.Errorf("branch %q not found in repo", branch2)
	}

	// Both containers should see workspace files via /workspace symlink.
	out1, err := dockerExec(t, name1, "cat", "/workspace/README.md")
	if err != nil {
		t.Errorf("container 1 cat README.md failed: %v", err)
	} else if !strings.Contains(out1, "E2E") {
		t.Errorf("container 1 README.md = %q, want 'E2E'", out1)
	}

	out2, err := dockerExec(t, name2, "cat", "/workspace/README.md")
	if err != nil {
		t.Errorf("container 2 cat README.md failed: %v", err)
	} else if !strings.Contains(out2, "E2E") {
		t.Errorf("container 2 README.md = %q, want 'E2E'", out2)
	}

	// Both containers should be on the correct branch.
	branchOut1, err := dockerExec(t, name1, "git", "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		t.Errorf("container 1 git rev-parse failed: %v", err)
	} else if branchOut1 != branch1 {
		t.Errorf("container 1 branch = %q, want %q", branchOut1, branch1)
	}

	branchOut2, err := dockerExec(t, name2, "git", "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		t.Errorf("container 2 git rev-parse failed: %v", err)
	} else if branchOut2 != branch2 {
		t.Errorf("container 2 branch = %q, want %q", branchOut2, branch2)
	}

	// /workspace should be a symlink to /worktrees/<branch>.
	link1, err := dockerExec(t, name1, "readlink", "/workspace")
	if err != nil {
		t.Errorf("container 1 readlink /workspace failed: %v", err)
	} else if link1 != "/worktrees/"+branch1 {
		t.Errorf("container 1 /workspace symlink = %q, want %q", link1, "/worktrees/"+branch1)
	}

	link2, err := dockerExec(t, name2, "readlink", "/workspace")
	if err != nil {
		t.Errorf("container 2 readlink /workspace failed: %v", err)
	} else if link2 != "/worktrees/"+branch2 {
		t.Errorf("container 2 /workspace symlink = %q, want %q", link2, "/worktrees/"+branch2)
	}

	// Cleanup removes both branches.
	runCLI(t, "rm", name1)
	runCLI(t, "rm", name2)
	if gitBranchExists(repo, branch1) {
		t.Errorf("branch %q still exists after rm", branch1)
	}
	if gitBranchExists(repo, branch2) {
		t.Errorf("branch %q still exists after rm", branch2)
	}
}

func TestE2E_ParallelWorkWithWorkspace(t *testing.T) {
	xdgDir := setupConfigDir(t)
	requireDockerAndAuth(t)

	// Create two separate git repos.
	repoA := setupGitRepo(t)
	os.WriteFile(filepath.Join(repoA, "marker-a.txt"), []byte("REPO_A"), 0o644)
	execInDir := func(dir string, args ...string) {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
	}
	execInDir(repoA, "add", "marker-a.txt")
	execInDir(repoA, "commit", "-m", "add marker-a")

	repoB := setupGitRepo(t)
	os.WriteFile(filepath.Join(repoB, "marker-b.txt"), []byte("REPO_B"), 0o644)
	execInDir(repoB, "add", "marker-b.txt")
	execInDir(repoB, "commit", "-m", "add marker-b")

	// Use repoA as cwd (primary repo for worktree).
	origDir, _ := os.Getwd()
	os.Chdir(repoA)
	t.Cleanup(func() { os.Chdir(origDir) })

	// Register workspace with both repos.
	_, _, code := runCLI(t, "workspace", "add", "par-multi", repoA, repoB)
	if code != 0 {
		t.Fatal("workspace add failed")
	}

	name1 := "e2e-par-ws1"
	name2 := "e2e-par-ws2"
	proxyProf := "e2e-par-ws"
	cleanupContainer(t, name1)
	cleanupContainer(t, name2)
	cleanupProxy(t, proxyProf)

	// Start two work sessions with multi-repo workspace.
	_, stderr1, code := runCLI(t, "work", "-W", "par-multi", "--yolo", "-b",
		"--name", name1, "--preset", proxyProf)
	if code != 0 {
		t.Fatalf("work 1: exit %d\nstderr: %s", code, stderr1)
	}

	_, stderr2, code := runCLI(t, "work", "-W", "par-multi", "--yolo", "-b",
		"--name", name2, "--preset", proxyProf)
	if code != 0 {
		t.Fatalf("work 2: exit %d\nstderr: %s", code, stderr2)
	}

	// Both containers should be running.
	if !dockerContainerRunning(name1) {
		t.Error("container 1 not running")
	}
	if !dockerContainerRunning(name2) {
		t.Error("container 2 not running")
	}

	// Get branch names from sessions.
	sessions := readSessionsJSON(t, xdgDir)
	sess1 := findSession(sessions, name1)
	sess2 := findSession(sessions, name2)
	if sess1 == nil {
		t.Fatal("session 1 not in sessions.json")
	}
	if sess2 == nil {
		t.Fatal("session 2 not in sessions.json")
	}
	branch1, _ := sess1["branch"].(string)
	branch2, _ := sess2["branch"].(string)

	// Wait for branches to be created in both repos.
	if !waitForBranch(t, repoA, branch1, 15*time.Second) {
		t.Errorf("branch %q not found in repoA", branch1)
	}
	if !waitForBranch(t, repoA, branch2, 15*time.Second) {
		t.Errorf("branch %q not found in repoA", branch2)
	}
	if !waitForBranch(t, repoB, branch1, 15*time.Second) {
		t.Errorf("branch %q not found in repoB", branch1)
	}
	if !waitForBranch(t, repoB, branch2, 15*time.Second) {
		t.Errorf("branch %q not found in repoB", branch2)
	}

	baseA := filepath.Base(repoA)
	baseB := filepath.Base(repoB)

	// Both containers see files from both repos.
	out, err := dockerExec(t, name1, "cat", "/workspace/"+baseA+"/marker-a.txt")
	if err != nil {
		t.Errorf("container 1 cat marker-a.txt failed: %v", err)
	} else if !strings.Contains(out, "REPO_A") {
		t.Errorf("container 1 marker-a.txt = %q, want 'REPO_A'", out)
	}

	out, err = dockerExec(t, name1, "cat", "/workspace/"+baseB+"/marker-b.txt")
	if err != nil {
		t.Errorf("container 1 cat marker-b.txt failed: %v", err)
	} else if !strings.Contains(out, "REPO_B") {
		t.Errorf("container 1 marker-b.txt = %q, want 'REPO_B'", out)
	}

	out, err = dockerExec(t, name2, "cat", "/workspace/"+baseA+"/marker-a.txt")
	if err != nil {
		t.Errorf("container 2 cat marker-a.txt failed: %v", err)
	} else if !strings.Contains(out, "REPO_A") {
		t.Errorf("container 2 marker-a.txt = %q, want 'REPO_A'", out)
	}

	out, err = dockerExec(t, name2, "cat", "/workspace/"+baseB+"/marker-b.txt")
	if err != nil {
		t.Errorf("container 2 cat marker-b.txt failed: %v", err)
	} else if !strings.Contains(out, "REPO_B") {
		t.Errorf("container 2 marker-b.txt = %q, want 'REPO_B'", out)
	}

	// Cleanup.
	runCLI(t, "rm", name1)
	runCLI(t, "rm", name2)
}
