package docker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/sandbox"
)

// skipIfDockerUnavailable skips the test when Docker is not available or the
// claude-code:nix image has not been loaded.
func skipIfDockerUnavailable(t *testing.T) {
	t.Helper()

	cmd := exec.Command("docker", "info")
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		t.Skip("docker not available: ", err)
	}

	if !ImageExists() {
		t.Skipf("docker image %q not loaded", ImageTag())
	}
}

// makeConfigDir creates a temporary config directory and registers a cleanup
// that fixes permissions before removal. The entrypoint runs as root and may
// create files (e.g. .claude.json) that the test user cannot delete without
// first widening permissions.
func makeConfigDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "claude-integration-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// The entrypoint may have created root-owned files. Use chmod to
		// make them deletable, then remove the tree.
		exec.Command("chmod", "-R", "u+rwX", dir).Run()
		os.RemoveAll(dir)
	})
	return dir
}

// containerResult holds stdout and stderr from a container run.
type containerResult struct {
	Stdout string
	Stderr string
}

// runContainerOpts configures a single ephemeral container run.
type runContainerOpts struct {
	Workspace       string   // primary workspace mount
	ExtraWorkspaces []string // extra workspace mounts
	ConfigDir       string   // config dir mount at /claude
	UID             int
	GID             int
	Command         []string // command to run instead of "claude"
}

// runContainer runs an ephemeral (--rm) container with diagnostic commands.
// It builds the args manually (not via RunArgs) because integration tests
// need --rm, no -it, no --name, and custom commands.
func runContainer(t *testing.T, opts runContainerOpts) containerResult {
	t.Helper()

	args := []string{"run", "--rm"}

	if opts.Workspace != "" {
		args = append(args, "-v", opts.Workspace+":/workspace")
	}

	for _, ws := range opts.ExtraWorkspaces {
		base := filepath.Base(ws)
		args = append(args, "-v", ws+":/workspace/"+base)
	}

	if opts.ConfigDir != "" {
		args = append(args, "-v", opts.ConfigDir+":/claude")
	}

	args = append(args,
		"-e", "CLAUDE_CONFIG_DIR=/claude",
		"-e", fmt.Sprintf("USER_UID=%d", opts.UID),
		"-e", fmt.Sprintf("USER_GID=%d", opts.GID),
	)

	args = append(args, ImageTag())
	args = append(args, opts.Command...)

	cmd := exec.Command("docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("docker run failed: %v\nstdout: %s\nstderr: %s\nargs: %v",
			err, stdout.String(), stderr.String(), args)
	}

	return containerResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}
}

// ---------------------------------------------------------------------------
// Entrypoint behavior tests
// ---------------------------------------------------------------------------

func TestIntegrationEntrypointUIDGID(t *testing.T) {
	skipIfDockerUnavailable(t)

	uid := os.Getuid()
	gid := os.Getgid()

	// When running as root the entrypoint short-circuits (exec "$@" directly).
	if uid == 0 {
		t.Log("running as root – entrypoint skips user mapping")
	}

	configDir := makeConfigDir(t)
	result := runContainer(t, runContainerOpts{
		ConfigDir: configDir,
		UID:       uid,
		GID:       gid,
		Command:   []string{"id"},
	})

	out := strings.TrimSpace(result.Stdout)
	wantUID := fmt.Sprintf("uid=%d", uid)
	wantGID := fmt.Sprintf("gid=%d", gid)

	if !strings.Contains(out, wantUID) {
		t.Errorf("id output %q does not contain %q", out, wantUID)
	}
	if !strings.Contains(out, wantGID) {
		t.Errorf("id output %q does not contain %q", out, wantGID)
	}
}

func TestIntegrationEntrypointWorkingDir(t *testing.T) {
	skipIfDockerUnavailable(t)

	configDir := makeConfigDir(t)
	result := runContainer(t, runContainerOpts{
		ConfigDir: configDir,
		UID:       os.Getuid(),
		GID:       os.Getgid(),
		Command:   []string{"pwd"},
	})

	got := strings.TrimSpace(result.Stdout)
	if got != "/workspace" {
		t.Errorf("pwd = %q, want /workspace", got)
	}
}

func TestIntegrationEntrypointHomeSymlink(t *testing.T) {
	skipIfDockerUnavailable(t)

	uid := os.Getuid()
	gid := os.Getgid()

	// UID 0 causes the entrypoint to exec directly, skipping symlink setup.
	if uid == 0 {
		t.Skip("running as root – entrypoint skips symlink setup")
	}

	configDir := makeConfigDir(t)
	// Use sh -c so that $HOME is resolved inside the container after the
	// entrypoint has set it up.
	result := runContainer(t, runContainerOpts{
		ConfigDir: configDir,
		UID:       uid,
		GID:       gid,
		Command:   []string{"sh", "-c", "readlink -f \"$HOME/.claude\""},
	})

	got := strings.TrimSpace(result.Stdout)
	if got != "/claude" {
		t.Errorf("readlink $HOME/.claude = %q, want /claude", got)
	}
}

func TestIntegrationBypassPermissionsAccepted(t *testing.T) {
	skipIfDockerUnavailable(t)

	configDir := makeConfigDir(t)
	result := runContainer(t, runContainerOpts{
		ConfigDir: configDir,
		UID:       os.Getuid(),
		GID:       os.Getgid(),
		Command:   []string{"cat", "/claude/.claude.json"},
	})

	var data map[string]any
	if err := json.Unmarshal([]byte(result.Stdout), &data); err != nil {
		t.Fatalf("invalid JSON in .claude.json: %v\nraw: %s", err, result.Stdout)
	}

	accepted, ok := data["bypassPermissionsModeAccepted"].(bool)
	if !ok || !accepted {
		t.Errorf("bypassPermissionsModeAccepted = %v, want true", data["bypassPermissionsModeAccepted"])
	}
}

func TestIntegrationConfigDirPermissions(t *testing.T) {
	skipIfDockerUnavailable(t)

	uid := os.Getuid()
	gid := os.Getgid()
	configDir := makeConfigDir(t)

	result := runContainer(t, runContainerOpts{
		ConfigDir: configDir,
		UID:       uid,
		GID:       gid,
		Command:   []string{"stat", "-c", "%u:%g", "/claude"},
	})

	got := strings.TrimSpace(result.Stdout)
	want := fmt.Sprintf("%d:%d", uid, gid)
	if got != want {
		t.Errorf("stat /claude = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// Workspace mount tests
// ---------------------------------------------------------------------------

func TestIntegrationWorkspaceMount(t *testing.T) {
	skipIfDockerUnavailable(t)

	workspace := t.TempDir()
	configDir := makeConfigDir(t)

	if err := os.WriteFile(filepath.Join(workspace, "sentinel.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := runContainer(t, runContainerOpts{
		Workspace: workspace,
		ConfigDir: configDir,
		UID:       os.Getuid(),
		GID:       os.Getgid(),
		Command:   []string{"ls", "/workspace"},
	})

	if !strings.Contains(result.Stdout, "sentinel.txt") {
		t.Errorf("ls /workspace = %q, want sentinel.txt present", result.Stdout)
	}
}

func TestIntegrationExtraWorkspaces(t *testing.T) {
	skipIfDockerUnavailable(t)

	// Create predictable-named directories under a parent temp dir
	// since t.TempDir() names are unpredictable.
	parent := t.TempDir()
	repoA := filepath.Join(parent, "repo-a")
	repoB := filepath.Join(parent, "repo-b")
	os.MkdirAll(repoA, 0o755)
	os.MkdirAll(repoB, 0o755)
	os.WriteFile(filepath.Join(repoA, "a.txt"), []byte("aaa"), 0o644)
	os.WriteFile(filepath.Join(repoB, "b.txt"), []byte("bbb"), 0o644)

	configDir := makeConfigDir(t)

	// Verify repo-a mount.
	resultA := runContainer(t, runContainerOpts{
		ExtraWorkspaces: []string{repoA, repoB},
		ConfigDir:       configDir,
		UID:             os.Getuid(),
		GID:             os.Getgid(),
		Command:         []string{"ls", "/workspace/repo-a"},
	})
	if !strings.Contains(resultA.Stdout, "a.txt") {
		t.Errorf("ls /workspace/repo-a = %q, want a.txt", resultA.Stdout)
	}

	// Verify repo-b mount.
	resultB := runContainer(t, runContainerOpts{
		ExtraWorkspaces: []string{repoA, repoB},
		ConfigDir:       configDir,
		UID:             os.Getuid(),
		GID:             os.Getgid(),
		Command:         []string{"ls", "/workspace/repo-b"},
	})
	if !strings.Contains(resultB.Stdout, "b.txt") {
		t.Errorf("ls /workspace/repo-b = %q, want b.txt", resultB.Stdout)
	}
}

func TestIntegrationExtraWorkspacesWithPrimary(t *testing.T) {
	skipIfDockerUnavailable(t)

	primary := t.TempDir()
	os.WriteFile(filepath.Join(primary, "main.go"), []byte("package main"), 0o644)

	parent := t.TempDir()
	libShared := filepath.Join(parent, "lib-shared")
	os.MkdirAll(libShared, 0o755)
	os.WriteFile(filepath.Join(libShared, "lib.go"), []byte("package lib"), 0o644)

	configDir := makeConfigDir(t)

	// Primary workspace files visible at /workspace root.
	resultPrimary := runContainer(t, runContainerOpts{
		Workspace:       primary,
		ExtraWorkspaces: []string{libShared},
		ConfigDir:       configDir,
		UID:             os.Getuid(),
		GID:             os.Getgid(),
		Command:         []string{"ls", "/workspace/main.go"},
	})
	if !strings.Contains(resultPrimary.Stdout, "main.go") {
		t.Errorf("primary workspace: ls /workspace/main.go = %q, want main.go", resultPrimary.Stdout)
	}

	// Extra workspace file at /workspace/lib-shared.
	resultExtra := runContainer(t, runContainerOpts{
		Workspace:       primary,
		ExtraWorkspaces: []string{libShared},
		ConfigDir:       configDir,
		UID:             os.Getuid(),
		GID:             os.Getgid(),
		Command:         []string{"ls", "/workspace/lib-shared/lib.go"},
	})
	if !strings.Contains(resultExtra.Stdout, "lib.go") {
		t.Errorf("extra workspace: ls /workspace/lib-shared/lib.go = %q, want lib.go", resultExtra.Stdout)
	}
}

// ---------------------------------------------------------------------------
// Profile / settings tests
// ---------------------------------------------------------------------------

// writeManagedSettings writes a managed-settings.json file for the given
// profile into the config directory.
func writeManagedSettings(t *testing.T, configDir, profileName string) {
	t.Helper()

	p, err := sandbox.GetProfile(profileName)
	if err != nil {
		t.Fatal(err)
	}
	settings := p.ManagedSettings(nil, nil)
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "managed-settings.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationManagedSettingsReadable(t *testing.T) {
	skipIfDockerUnavailable(t)

	configDir := makeConfigDir(t)
	writeManagedSettings(t, configDir, "med")

	result := runContainer(t, runContainerOpts{
		ConfigDir: configDir,
		UID:       os.Getuid(),
		GID:       os.Getgid(),
		Command:   []string{"cat", "/claude/managed-settings.json"},
	})

	var settings map[string]any
	if err := json.Unmarshal([]byte(result.Stdout), &settings); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, result.Stdout)
	}

	sb, ok := settings["sandbox"].(map[string]any)
	if !ok {
		t.Fatal("missing sandbox key in managed-settings.json")
	}
	enabled, _ := sb["enabled"].(bool)
	if !enabled {
		t.Error("med profile: sandbox.enabled should be true")
	}
}

func TestIntegrationProfileSettingsLow(t *testing.T) {
	skipIfDockerUnavailable(t)

	configDir := makeConfigDir(t)
	writeManagedSettings(t, configDir, "low")

	result := runContainer(t, runContainerOpts{
		ConfigDir: configDir,
		UID:       os.Getuid(),
		GID:       os.Getgid(),
		Command:   []string{"cat", "/claude/managed-settings.json"},
	})

	var settings map[string]any
	if err := json.Unmarshal([]byte(result.Stdout), &settings); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, result.Stdout)
	}

	sb := settings["sandbox"].(map[string]any)
	enabled, _ := sb["enabled"].(bool)
	if enabled {
		t.Error("low profile: sandbox.enabled should be false")
	}
}

func TestIntegrationProfileSettingsHigh(t *testing.T) {
	skipIfDockerUnavailable(t)

	configDir := makeConfigDir(t)
	writeManagedSettings(t, configDir, "high")

	result := runContainer(t, runContainerOpts{
		ConfigDir: configDir,
		UID:       os.Getuid(),
		GID:       os.Getgid(),
		Command:   []string{"cat", "/claude/managed-settings.json"},
	})

	var settings map[string]any
	if err := json.Unmarshal([]byte(result.Stdout), &settings); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, result.Stdout)
	}

	sb := settings["sandbox"].(map[string]any)
	network := sb["network"].(map[string]any)
	domainsRaw := network["allowedDomains"].([]any)

	domains := make([]string, len(domainsRaw))
	for i, d := range domainsRaw {
		domains[i] = d.(string)
	}

	if len(domains) != 1 || domains[0] != "api.anthropic.com" {
		t.Errorf("high profile allowedDomains = %v, want [api.anthropic.com]", domains)
	}

	perms, ok := settings["permissions"].(map[string]any)
	if !ok {
		t.Fatal("high profile: missing permissions key")
	}
	denyRaw, ok := perms["deny"].([]any)
	if !ok || len(denyRaw) == 0 {
		t.Error("high profile: deny paths should be non-empty")
	}
}

// ---------------------------------------------------------------------------
// Non-Docker integration tests (always run)
// ---------------------------------------------------------------------------

func TestIntegrationWorkspaceStoreCLIFlow(t *testing.T) {
	dir := t.TempDir()
	ws := config.NewWorkspaceStore(dir)

	if err := ws.Add("myproject", []string{"/home/user/code/api", "/home/user/code/frontend"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	names := ws.List()
	if !slices.Contains(names, "myproject") {
		t.Errorf("List = %v, want myproject present", names)
	}

	paths, err := ws.Get("myproject")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("Get: got %d paths, want 2", len(paths))
	}

	primary := paths[0]
	extras := paths[1:]
	args := RunArgs(RunOpts{
		Name:            "myproject-session",
		Workspace:       primary,
		ConfigDir:       filepath.Join(dir, "claude-config"),
		UID:             1000,
		GID:             1000,
		ExtraWorkspaces: extras,
	}, false)

	joined := strings.Join(args, " ")

	if !strings.Contains(joined, "/home/user/code/api:/workspace") {
		t.Errorf("RunArgs missing primary workspace in %v", args)
	}
	if !strings.Contains(joined, "/home/user/code/frontend:/workspace/frontend") {
		t.Errorf("RunArgs missing extra workspace in %v", args)
	}

	if err := ws.Remove("myproject"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := ws.Get("myproject"); err == nil {
		t.Error("Get after Remove should error")
	}
}

func TestIntegrationProfileYoloEquivalence(t *testing.T) {
	p, err := sandbox.GetProfile("low")
	if err != nil {
		t.Fatal(err)
	}

	settings := p.ManagedSettings(nil, nil)
	sb := settings["sandbox"].(map[string]any)
	if enabled, _ := sb["enabled"].(bool); enabled {
		t.Error("low profile sandbox.enabled should be false")
	}

	args := RunArgs(RunOpts{
		Name:      "yolo-test",
		Workspace: "/tmp/project",
		ConfigDir: "/tmp/config",
		UID:       1000,
		GID:       1000,
		Yolo:      true,
	}, false)

	if !slices.Contains(args, "--dangerously-skip-permissions") {
		t.Errorf("yolo RunArgs missing --dangerously-skip-permissions in %v", args)
	}

	argsNoYolo := RunArgs(RunOpts{
		Name:      "no-yolo",
		Workspace: "/tmp/project",
		ConfigDir: "/tmp/config",
		UID:       1000,
		GID:       1000,
		Yolo:      false,
	}, false)

	if slices.Contains(argsNoYolo, "--dangerously-skip-permissions") {
		t.Errorf("non-yolo RunArgs should not have --dangerously-skip-permissions in %v", argsNoYolo)
	}
}
