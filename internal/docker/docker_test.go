package docker

import (
	"slices"
	"strings"
	"testing"
)

func TestContainerName(t *testing.T) {
	got := ContainerName("my-session")
	want := "claude-container_my-session"
	if got != want {
		t.Errorf("ContainerName(%q) = %q, want %q", "my-session", got, want)
	}
}

func TestRunArgs(t *testing.T) {
	opts := RunOpts{
		Name:      "test-session",
		Workspace: "/home/user/project",
		ConfigDir: "/home/user/.config/claude",
		UID:       1000,
		GID:       1000,
	}
	args := RunArgs(opts, false)

	joined := strings.Join(args, " ")

	// Must have --name.
	if !slices.Contains(args, "--name") {
		t.Errorf("RunArgs missing --name in %v", args)
	}
	if !strings.Contains(joined, "claude-container_test-session") {
		t.Errorf("RunArgs missing container name in %v", args)
	}

	// Must NOT have --rm (persistent containers).
	if slices.Contains(args, "--rm") {
		t.Errorf("RunArgs should not contain --rm, got %v", args)
	}

	// Must have -it (interactive TTY) when not detached.
	if !slices.Contains(args, "-it") {
		t.Errorf("RunArgs missing -it in %v", args)
	}

	// Volume mounts.
	if !strings.Contains(joined, "/home/user/project:/workspace") {
		t.Errorf("RunArgs missing workspace volume mount in %v", args)
	}
	if !strings.Contains(joined, "/home/user/.config/claude:/claude") {
		t.Errorf("RunArgs missing config volume mount in %v", args)
	}

	// Environment variables.
	if !strings.Contains(joined, "CLAUDE_CONFIG_DIR=/claude") {
		t.Errorf("RunArgs missing CLAUDE_CONFIG_DIR env in %v", args)
	}
	if !strings.Contains(joined, "USER_UID=1000") {
		t.Errorf("RunArgs missing USER_UID env in %v", args)
	}
	if !strings.Contains(joined, "USER_GID=1000") {
		t.Errorf("RunArgs missing USER_GID env in %v", args)
	}

	// Image tag should be present.
	if !slices.Contains(args, ImageTag()) {
		t.Errorf("RunArgs missing image tag %q in %v", ImageTag(), args)
	}

	// Should end with "claude" command (no extra flags for base case).
	if args[len(args)-1] != "claude" {
		t.Errorf("RunArgs last arg = %q, want %q", args[len(args)-1], "claude")
	}
}

func TestRunArgsYolo(t *testing.T) {
	opts := RunOpts{
		Name:      "yolo-session",
		Workspace: "/tmp/project",
		ConfigDir: "/tmp/config",
		UID:       1000,
		GID:       1000,
		Yolo:      true,
	}
	args := RunArgs(opts, false)

	if IsRootless() {
		if slices.Contains(args, "--dangerously-skip-permissions") {
			t.Errorf("rootless Docker: RunArgs with Yolo=true should not have --dangerously-skip-permissions in %v", args)
		}
	} else {
		if !slices.Contains(args, "--dangerously-skip-permissions") {
			t.Errorf("RunArgs with Yolo=true missing --dangerously-skip-permissions in %v", args)
		}
	}
}

func TestRunArgsWithPrompt(t *testing.T) {
	opts := RunOpts{
		Name:      "prompt-session",
		Workspace: "/tmp/project",
		ConfigDir: "/tmp/config",
		UID:       1000,
		GID:       1000,
		Prompt:    "fix the tests",
	}
	args := RunArgs(opts, false)

	// Prompt should be the last positional argument (not -p flag).
	last := args[len(args)-1]
	if last != "fix the tests" {
		t.Errorf("RunArgs last arg = %q, want prompt %q", last, "fix the tests")
	}

	// Should NOT contain -p flag (that's non-interactive print mode).
	for _, arg := range args {
		if arg == "-p" {
			t.Errorf("RunArgs should not contain -p flag, got %v", args)
			break
		}
	}
}

func TestRunArgsContinue(t *testing.T) {
	opts := RunOpts{
		Name:      "continue-session",
		Workspace: "/tmp/project",
		ConfigDir: "/tmp/config",
		UID:       1000,
		GID:       1000,
		Continue:  true,
	}
	args := RunArgs(opts, false)

	if !slices.Contains(args, "--continue") {
		t.Errorf("RunArgs with Continue=true missing --continue in %v", args)
	}
}

func TestRunArgsVolumeMounts(t *testing.T) {
	opts := RunOpts{
		Name:      "vol-test",
		Workspace: "/projects/myapp",
		ConfigDir: "/home/user/.claude-config",
		UID:       1000,
		GID:       1000,
	}
	args := RunArgs(opts, false)

	// Count the number of -v flags.
	volumeCount := 0
	for i, arg := range args {
		if arg == "-v" && i+1 < len(args) {
			volumeCount++
		}
	}
	if volumeCount != 3 {
		t.Errorf("RunArgs has %d volume mounts, want 3", volumeCount)
	}

	// Verify specific mounts are present.
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "/projects/myapp:/workspace") {
		t.Errorf("RunArgs missing workspace volume mount in %v", args)
	}
	if !strings.Contains(joined, "/home/user/.claude-config:/claude") {
		t.Errorf("RunArgs missing config volume mount in %v", args)
	}
	if !strings.Contains(joined, "claude-nix-store:/nix/var") {
		t.Errorf("RunArgs missing nix store volume mount in %v", args)
	}
}

func TestRunArgsEnvVars(t *testing.T) {
	opts := RunOpts{
		Name:      "env-test",
		Workspace: "/tmp/project",
		ConfigDir: "/tmp/config",
		UID:       5000,
		GID:       5001,
	}
	args := RunArgs(opts, false)

	joined := strings.Join(args, " ")

	// Verify all three -e environment variables.
	envVars := []string{
		"CLAUDE_CONFIG_DIR=/claude",
		"USER_UID=5000",
		"USER_GID=5001",
	}
	for _, env := range envVars {
		if !strings.Contains(joined, env) {
			t.Errorf("RunArgs missing env var %q in %v", env, args)
		}
	}

	// Count the number of -e flags.
	envCount := 0
	for _, arg := range args {
		if arg == "-e" {
			envCount++
		}
	}
	if envCount != 3 {
		t.Errorf("RunArgs has %d -e flags, want 3", envCount)
	}
}

func TestShellArgsHasRm(t *testing.T) {
	args := ShellArgs("/tmp/ws", "/tmp/cfg", nil, 1000, 1000)

	if !slices.Contains(args, "--rm") {
		t.Errorf("ShellArgs missing --rm flag in %v", args)
	}

	// RunArgs should NOT have --rm, so verify the distinction.
	runArgs := RunArgs(RunOpts{
		Name:      "no-rm",
		Workspace: "/tmp/ws",
		ConfigDir: "/tmp/cfg",
		UID:       1000,
		GID:       1000,
	}, false)
	if slices.Contains(runArgs, "--rm") {
		t.Errorf("RunArgs should not have --rm but ShellArgs should")
	}
}

func TestShellArgsBash(t *testing.T) {
	args := ShellArgs("/tmp/ws", "/tmp/cfg", nil, 1000, 1000)

	if len(args) == 0 {
		t.Fatal("ShellArgs returned empty slice")
	}

	last := args[len(args)-1]
	if last != "/bin/bash" {
		t.Errorf("ShellArgs last arg = %q, want %q", last, "/bin/bash")
	}

	// Verify that RunArgs ends with "claude" (not /bin/bash).
	runArgs := RunArgs(RunOpts{
		Name:      "bash-test",
		Workspace: "/tmp/ws",
		ConfigDir: "/tmp/cfg",
		UID:       1000,
		GID:       1000,
	}, false)
	runLast := runArgs[len(runArgs)-1]
	if runLast != "claude" {
		t.Errorf("RunArgs last arg = %q, want %q", runLast, "claude")
	}
}

func TestContainerNamePrefix(t *testing.T) {
	// Verify ContainerName uses the same prefix as config.Prefix.
	got := ContainerName("test-session")
	wantPrefix := "claude-container_"

	if !strings.HasPrefix(got, wantPrefix) {
		t.Errorf("ContainerName(%q) = %q, should have prefix %q", "test-session", got, wantPrefix)
	}

	// Verify the suffix is the session name.
	suffix := strings.TrimPrefix(got, wantPrefix)
	if suffix != "test-session" {
		t.Errorf("ContainerName suffix = %q, want %q", suffix, "test-session")
	}
}

func TestShellArgs(t *testing.T) {
	args := ShellArgs("/home/user/project", "/home/user/.config/claude", nil, 1000, 1000)

	joined := strings.Join(args, " ")

	// Must have --rm (ephemeral debug shells).
	if !slices.Contains(args, "--rm") {
		t.Errorf("ShellArgs missing --rm in %v", args)
	}

	// Volume mounts.
	if !strings.Contains(joined, "/home/user/project:/workspace") {
		t.Errorf("ShellArgs missing workspace volume mount in %v", args)
	}
	if !strings.Contains(joined, "/home/user/.config/claude:/claude") {
		t.Errorf("ShellArgs missing config volume mount in %v", args)
	}

	// Must end with /bin/bash.
	if args[len(args)-1] != "/bin/bash" {
		t.Errorf("ShellArgs last arg = %q, want %q", args[len(args)-1], "/bin/bash")
	}
}

func TestImageTag(t *testing.T) {
	// Default: returns ImageName when env var not set.
	t.Setenv("CLAUDE_CONTAINER_IMAGE_TAG", "")
	if got := ImageTag(); got != ImageName {
		t.Errorf("ImageTag() = %q, want %q (default)", got, ImageName)
	}

	// With env var set.
	t.Setenv("CLAUDE_CONTAINER_IMAGE_TAG", "claude-code:latest")
	if got := ImageTag(); got != "claude-code:latest" {
		t.Errorf("ImageTag() = %q, want %q", got, "claude-code:latest")
	}
}

func TestRunArgsExtraWorkspaces(t *testing.T) {
	opts := RunOpts{
		Name:            "multi-test",
		Workspace:       "/home/user/main-project",
		ConfigDir:       "/tmp/config",
		UID:             1000,
		GID:             1000,
		ExtraWorkspaces: []string{"/home/user/code/repo-a", "/home/user/code/repo-b"},
	}
	args := RunArgs(opts, false)
	joined := strings.Join(args, " ")

	if !strings.Contains(joined, "/home/user/main-project:/workspace") {
		t.Errorf("RunArgs missing primary workspace mount in %v", args)
	}
	if !strings.Contains(joined, "/home/user/code/repo-a:/workspace/repo-a") {
		t.Errorf("RunArgs missing extra workspace repo-a in %v", args)
	}
	if !strings.Contains(joined, "/home/user/code/repo-b:/workspace/repo-b") {
		t.Errorf("RunArgs missing extra workspace repo-b in %v", args)
	}
}

func TestRunArgsExtraWorkspacesOnly(t *testing.T) {
	opts := RunOpts{
		Name:            "extra-only",
		ConfigDir:       "/tmp/config",
		UID:             1000,
		GID:             1000,
		ExtraWorkspaces: []string{"/home/user/code/repo-a"},
	}
	args := RunArgs(opts, false)
	joined := strings.Join(args, " ")

	if !strings.Contains(joined, "/home/user/code/repo-a:/workspace/repo-a") {
		t.Errorf("RunArgs missing extra workspace in %v", args)
	}
}

func TestEnsureImageMarker(t *testing.T) {
	// When no tarball env and image doesn't exist, should error.
	t.Setenv("CLAUDE_CONTAINER_IMAGE_TARBALL", "")
	t.Setenv("CLAUDE_CONTAINER_IMAGE_TAG", "nonexistent:test")
	err := EnsureImage(t.TempDir())
	if err == nil {
		t.Error("EnsureImage should error when no tarball and image missing")
	}
}

func TestRunArgsProxyNetwork(t *testing.T) {
	args := RunArgs(RunOpts{
		Name:           "test-session",
		Workspace:      "/tmp/ws",
		ConfigDir:      "/tmp/config",
		UID:            1000,
		GID:            1000,
		ProxyEnabled:   true,
		ProxyCACertDir: "/tmp/ca",
	}, false)

	joined := strings.Join(args, " ")

	// The proxy container is named after the SESSION (opts.Name), not a
	// separate profile string — each session owns its own sidecar.
	if !strings.Contains(joined, "--network container:claude-proxy_test-session") {
		t.Errorf("missing shared-netns flag in %v", args)
	}
	if strings.Contains(joined, "HTTP_PROXY") {
		t.Errorf("HTTP_PROXY should not be set with shared netns: %v", args)
	}
	if !strings.Contains(joined, "CLAUDE_PROXY_DASHBOARD_URL=http://127.0.0.1:8081") {
		t.Errorf("dashboard URL should point to loopback in shared netns: %v", args)
	}
	if !strings.Contains(joined, "/tmp/ca:/proxy-ca:ro") {
		t.Errorf("missing CA cert volume in %v", args)
	}
	if !strings.Contains(joined, "SSL_CERT_FILE=/proxy-ca/mitmproxy-ca-cert.pem") {
		t.Errorf("missing SSL_CERT_FILE in %v", args)
	}
	if !strings.Contains(joined, "NODE_EXTRA_CA_CERTS=/proxy-ca/mitmproxy-ca-cert.pem") {
		t.Errorf("missing NODE_EXTRA_CA_CERTS in %v", args)
	}
}

func TestRunArgsNoProxy(t *testing.T) {
	args := RunArgs(RunOpts{
		Name:      "test-session",
		Workspace: "/tmp/ws",
		ConfigDir: "/tmp/config",
		UID:       1000,
		GID:       1000,
	}, false)

	joined := strings.Join(args, " ")
	if strings.Contains(joined, "HTTP_PROXY") {
		t.Errorf("unexpected HTTP_PROXY in non-proxy args: %v", args)
	}
	if strings.Contains(joined, "--network") {
		t.Errorf("unexpected --network in non-proxy args: %v", args)
	}
}

func TestTaskRunArgs(t *testing.T) {
	opts := RunOpts{
		Name:      "task-test",
		Workspace: "/home/user/project",
		ConfigDir: "/home/user/.config/claude",
		UID:       1000,
		GID:       1000,
		Prompt:    "fix the tests",
	}
	args := TaskRunArgs(opts, "", 0)
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
	// Must have --dangerously-skip-permissions unless rootless Docker.
	if IsRootless() {
		if slices.Contains(args, "--dangerously-skip-permissions") {
			t.Errorf("rootless Docker: TaskRunArgs should not have --dangerously-skip-permissions in %v", args)
		}
	} else {
		if !slices.Contains(args, "--dangerously-skip-permissions") {
			t.Errorf("TaskRunArgs missing --dangerously-skip-permissions in %v", args)
		}
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

func TestExecGitDiffArgs(t *testing.T) {
	cmd := ExecGitDiff("test-session")
	args := cmd.Args

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

func TestRefreshAuthArgs(t *testing.T) {
	cmd := RefreshAuthCmd("test-session")
	args := cmd.Args

	if args[0] != "docker" {
		t.Errorf("first arg = %q, want docker", args[0])
	}
	if args[1] != "exec" {
		t.Errorf("second arg = %q, want exec", args[1])
	}
	if args[2] != ContainerName("test-session") {
		t.Errorf("third arg = %q, want container name", args[2])
	}
	if args[3] != "sh" || args[4] != "-c" {
		t.Errorf("args[3:5] = %v, want [sh -c]", args[3:5])
	}

	script := args[5]
	// Must copy the three credential files from /mnt/claude-host/.
	for _, f := range []string{".credentials.json", "settings.json", ".claude.json"} {
		if !strings.Contains(script, f) {
			t.Errorf("refresh script missing %q", f)
		}
	}
	// Must set permissions to 600.
	if !strings.Contains(script, "chmod 600") {
		t.Errorf("refresh script missing chmod 600")
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
