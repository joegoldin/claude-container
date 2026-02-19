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

func TestBuildArgs(t *testing.T) {
	args := BuildArgs("/path/to/context")

	if len(args) == 0 {
		t.Fatal("BuildArgs returned empty slice")
	}
	if args[0] != "build" {
		t.Errorf("args[0] = %q, want %q", args[0], "build")
	}

	// The image name should appear as -t <ImageName>.
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, ImageName) {
		t.Errorf("BuildArgs missing image name %q in %v", ImageName, args)
	}
	if !strings.Contains(joined, "/path/to/context") {
		t.Errorf("BuildArgs missing context dir in %v", args)
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

	// Image name should be present.
	if !slices.Contains(args, ImageName) {
		t.Errorf("RunArgs missing image name %q in %v", ImageName, args)
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

	if !slices.Contains(args, "--dangerously-skip-permissions") {
		t.Errorf("RunArgs with Yolo=true missing --dangerously-skip-permissions in %v", args)
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

	// Find -p flag and its value.
	foundP := false
	for i, arg := range args {
		if arg == "-p" && i+1 < len(args) {
			foundP = true
			if args[i+1] != "fix the tests" {
				t.Errorf("prompt value = %q, want %q", args[i+1], "fix the tests")
			}
			break
		}
	}
	if !foundP {
		t.Errorf("RunArgs with Prompt missing -p flag in %v", args)
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
	if volumeCount != 2 {
		t.Errorf("RunArgs has %d volume mounts, want 2", volumeCount)
	}

	// Verify both specific mounts are present.
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "/projects/myapp:/workspace") {
		t.Errorf("RunArgs missing workspace volume mount in %v", args)
	}
	if !strings.Contains(joined, "/home/user/.claude-config:/claude") {
		t.Errorf("RunArgs missing config volume mount in %v", args)
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
	args := ShellArgs("/tmp/ws", "/tmp/cfg", 1000, 1000)

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
	args := ShellArgs("/tmp/ws", "/tmp/cfg", 1000, 1000)

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
	args := ShellArgs("/home/user/project", "/home/user/.config/claude", 1000, 1000)

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
