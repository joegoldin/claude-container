package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/httpproxy"
	"github.com/joegoldin/claude-container/internal/sandbox"
)

// skipIfDockerUnavailable skips the test when Docker is not available or the
// claude-code image has not been loaded.
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

// fixContainerPerms runs a container to chmod files that may be owned by
// subordinate UIDs in rootless Docker. The host user cannot chmod/delete
// these files directly, but container root can.
func fixContainerPerms(dir string) {
	exec.Command("docker", "run", "--rm",
		"--entrypoint", "chmod",
		"-v", dir+":/cleanup",
		ImageTag(),
		"-R", "a+rwX", "/cleanup").Run()
}

// makeConfigDir creates a temporary config directory and registers a cleanup
// that fixes permissions before removal. In rootless Docker, the entrypoint
// creates files owned by a subordinate UID that the host user cannot delete.
func makeConfigDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "claude-integration-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		fixContainerPerms(dir)
		os.RemoveAll(dir)
	})
	return dir
}

// makeTempDir creates a temporary directory with container-aware cleanup.
// Use this instead of t.TempDir() for directories mounted into containers,
// since rootless Docker UID mapping can make host-side cleanup fail.
func makeTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "claude-integration-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		fixContainerPerms(dir)
		os.RemoveAll(dir)
	})
	return dir
}

// containerResult holds stdout and stderr from a container run.
type containerResult struct {
	Stdout string
	Stderr string
}

// stripEntrypointLogs removes [entrypoint] log lines from output.
// The entrypoint writes logs to stderr, but older images may write to stdout.
func stripEntrypointLogs(s string) string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if !strings.HasPrefix(line, "[entrypoint]") {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

// runContainerOpts configures a single ephemeral container run.
type runContainerOpts struct {
	Workspace       string   // primary workspace mount
	ExtraWorkspaces []string // extra workspace mounts
	ConfigDir       string   // config dir mount at /claude
	UID             int
	GID             int
	Command         []string // command to run instead of "claude"
	ProxySession    string   // join the proxy container's netns
	ProxyCACertDir  string   // mount CA cert directory at /proxy-ca
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

	// Share the per-session proxy container's network namespace (mirrors
	// RunArgs in shared-netns transparent-proxy mode). HTTP_PROXY env vars
	// are intentionally NOT set; transparent mode REDIRECTs every TCP
	// connection through mitmproxy regardless.
	if opts.ProxySession != "" {
		proxyContainer := "claude-proxy_" + opts.ProxySession
		args = append(args,
			"--network", "container:"+proxyContainer,
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

	uid := ContainerUID()
	gid := ContainerGID()

	if uid == 0 {
		t.Log("rootless Docker – container runs as root (maps to host user)")
	}

	configDir := makeConfigDir(t)
	result := runContainer(t, runContainerOpts{
		ConfigDir: configDir,
		UID:       uid,
		GID:       gid,
		Command:   []string{"id"},
	})

	out := strings.TrimSpace(stripEntrypointLogs(result.Stdout))
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
		UID:       ContainerUID(),
		GID:       ContainerGID(),
		Command:   []string{"pwd"},
	})

	got := strings.TrimSpace(stripEntrypointLogs(result.Stdout))
	if got != "/workspace" {
		t.Errorf("pwd = %q, want /workspace", got)
	}
}

func TestIntegrationEntrypointHomeSymlink(t *testing.T) {
	skipIfDockerUnavailable(t)

	uid := ContainerUID()
	gid := ContainerGID()

	configDir := makeConfigDir(t)
	// Use sh -c so that $HOME is resolved inside the container after the
	// entrypoint has set it up.
	result := runContainer(t, runContainerOpts{
		ConfigDir: configDir,
		UID:       uid,
		GID:       gid,
		Command:   []string{"sh", "-c", "readlink -f \"$HOME/.claude\""},
	})

	got := strings.TrimSpace(stripEntrypointLogs(result.Stdout))
	if got != "/claude" {
		t.Errorf("readlink $HOME/.claude = %q, want /claude", got)
	}
}

func TestIntegrationBypassPermissionsAccepted(t *testing.T) {
	skipIfDockerUnavailable(t)

	configDir := makeConfigDir(t)
	result := runContainer(t, runContainerOpts{
		ConfigDir: configDir,
		UID:       ContainerUID(),
		GID:       ContainerGID(),
		Command:   []string{"cat", "/claude/.claude.json"},
	})

	var data map[string]any
	cleaned := stripEntrypointLogs(result.Stdout)
	if err := json.Unmarshal([]byte(cleaned), &data); err != nil {
		t.Fatalf("invalid JSON in .claude.json: %v\nraw: %s", err, cleaned)
	}

	accepted, ok := data["bypassPermissionsModeAccepted"].(bool)
	if !ok || !accepted {
		t.Errorf("bypassPermissionsModeAccepted = %v, want true", data["bypassPermissionsModeAccepted"])
	}
}

func TestIntegrationConfigDirPermissions(t *testing.T) {
	skipIfDockerUnavailable(t)

	uid := ContainerUID()
	gid := ContainerGID()
	configDir := makeConfigDir(t)

	result := runContainer(t, runContainerOpts{
		ConfigDir: configDir,
		UID:       uid,
		GID:       gid,
		Command:   []string{"stat", "-c", "%u:%g", "/claude"},
	})

	got := strings.TrimSpace(stripEntrypointLogs(result.Stdout))
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

	workspace := makeTempDir(t)
	configDir := makeConfigDir(t)

	if err := os.WriteFile(filepath.Join(workspace, "sentinel.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := runContainer(t, runContainerOpts{
		Workspace: workspace,
		ConfigDir: configDir,
		UID:       ContainerUID(),
		GID:       ContainerGID(),
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
	parent := makeTempDir(t)
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
		UID:             ContainerUID(),
		GID:             ContainerGID(),
		Command:         []string{"ls", "/workspace/repo-a"},
	})
	if !strings.Contains(resultA.Stdout, "a.txt") {
		t.Errorf("ls /workspace/repo-a = %q, want a.txt", resultA.Stdout)
	}

	// Verify repo-b mount.
	resultB := runContainer(t, runContainerOpts{
		ExtraWorkspaces: []string{repoA, repoB},
		ConfigDir:       configDir,
		UID:             ContainerUID(),
		GID:             ContainerGID(),
		Command:         []string{"ls", "/workspace/repo-b"},
	})
	if !strings.Contains(resultB.Stdout, "b.txt") {
		t.Errorf("ls /workspace/repo-b = %q, want b.txt", resultB.Stdout)
	}
}

func TestIntegrationExtraWorkspacesWithPrimary(t *testing.T) {
	skipIfDockerUnavailable(t)

	primary := makeTempDir(t)
	os.WriteFile(filepath.Join(primary, "main.go"), []byte("package main"), 0o644)

	parent := makeTempDir(t)
	libShared := filepath.Join(parent, "lib-shared")
	os.MkdirAll(libShared, 0o755)
	os.WriteFile(filepath.Join(libShared, "lib.go"), []byte("package lib"), 0o644)

	configDir := makeConfigDir(t)

	// Primary workspace files visible at /workspace root.
	resultPrimary := runContainer(t, runContainerOpts{
		Workspace:       primary,
		ExtraWorkspaces: []string{libShared},
		ConfigDir:       configDir,
		UID:             ContainerUID(),
		GID:             ContainerGID(),
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
		UID:             ContainerUID(),
		GID:             ContainerGID(),
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
	settings := p.ManagedSettingsForProxy(8080, nil, nil, nil)
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
		UID:       ContainerUID(),
		GID:       ContainerGID(),
		Command:   []string{"cat", "/claude/managed-settings.json"},
	})

	var settings map[string]any
	cleaned := stripEntrypointLogs(result.Stdout)
	if err := json.Unmarshal([]byte(cleaned), &settings); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, cleaned)
	}

	sb, ok := settings["sandbox"].(map[string]any)
	if !ok {
		t.Fatal("missing sandbox key in managed-settings.json")
	}
	enabled, _ := sb["enabled"].(bool)
	if !enabled {
		t.Error("med profile: sandbox.enabled should be true (weaker nested sandbox)")
	}

	// Verify permissions are present.
	perms, ok := settings["permissions"].(map[string]any)
	if !ok {
		t.Fatal("med profile: missing permissions key")
	}
	if _, hasAllow := perms["allow"]; !hasAllow {
		t.Error("med profile: missing permissions.allow")
	}
	if _, hasDeny := perms["deny"]; !hasDeny {
		t.Error("med profile: missing permissions.deny")
	}
}

func TestIntegrationProfileSettingsLow(t *testing.T) {
	skipIfDockerUnavailable(t)

	configDir := makeConfigDir(t)
	writeManagedSettings(t, configDir, "low")

	result := runContainer(t, runContainerOpts{
		ConfigDir: configDir,
		UID:       ContainerUID(),
		GID:       ContainerGID(),
		Command:   []string{"cat", "/claude/managed-settings.json"},
	})

	var settings map[string]any
	cleaned := stripEntrypointLogs(result.Stdout)
	if err := json.Unmarshal([]byte(cleaned), &settings); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, cleaned)
	}

	sb := settings["sandbox"].(map[string]any)
	enabled, _ := sb["enabled"].(bool)
	if !enabled {
		t.Error("low profile: sandbox.enabled should be true")
	}
}

func TestIntegrationProfileSettingsHigh(t *testing.T) {
	skipIfDockerUnavailable(t)

	configDir := makeConfigDir(t)
	writeManagedSettings(t, configDir, "high")

	result := runContainer(t, runContainerOpts{
		ConfigDir: configDir,
		UID:       ContainerUID(),
		GID:       ContainerGID(),
		Command:   []string{"cat", "/claude/managed-settings.json"},
	})

	var settings map[string]any
	cleaned := stripEntrypointLogs(result.Stdout)
	if err := json.Unmarshal([]byte(cleaned), &settings); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, cleaned)
	}

	sb := settings["sandbox"].(map[string]any)
	enabled, _ := sb["enabled"].(bool)
	if !enabled {
		t.Error("high profile: sandbox.enabled should be true (weaker nested sandbox)")
	}

	perms, ok := settings["permissions"].(map[string]any)
	if !ok {
		t.Fatal("high profile: missing permissions key")
	}
	denyRaw, ok := perms["deny"].([]any)
	if !ok || len(denyRaw) == 0 {
		t.Error("high profile: deny rules should be non-empty")
	}

	// Verify deny rules include Bash(curl *) and Bash(wget *)
	denyStrings := make([]string, len(denyRaw))
	for i, d := range denyRaw {
		denyStrings[i] = d.(string)
	}
	wantDeny := []string{"Bash(curl *)", "Bash(wget *)"}
	for _, want := range wantDeny {
		found := false
		for _, d := range denyStrings {
			if d == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("high profile: missing %q in deny rules: %v", want, denyStrings)
		}
	}

	// Verify allow rules present
	allowRaw, ok := perms["allow"].([]any)
	if !ok || len(allowRaw) == 0 {
		t.Error("high profile: allow rules should be non-empty")
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

	if !p.Yolo {
		t.Error("low profile should have Yolo=true")
	}

	// Default profile should also be yolo.
	dp, err := sandbox.GetProfile("default")
	if err != nil {
		t.Fatal(err)
	}
	if !dp.Yolo {
		t.Error("default profile should have Yolo=true")
	}

	args := RunArgs(RunOpts{
		Name:      "yolo-test",
		Workspace: "/tmp/project",
		ConfigDir: "/tmp/config",
		UID:       1000,
		GID:       1000,
		Yolo:      true,
	}, false)

	if IsRootless() {
		if slices.Contains(args, "--dangerously-skip-permissions") {
			t.Errorf("rootless Docker: yolo RunArgs should not have --dangerously-skip-permissions in %v", args)
		}
	} else {
		if !slices.Contains(args, "--dangerously-skip-permissions") {
			t.Errorf("yolo RunArgs missing --dangerously-skip-permissions in %v", args)
		}
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

// ---------------------------------------------------------------------------
// Proxy E2E tests — Claude container on proxy network
// ---------------------------------------------------------------------------

// skipIfProxyImageUnavailable skips the test when the proxy image is not loaded.
func skipIfProxyImageUnavailable(t *testing.T) {
	t.Helper()
	if !httpproxy.ImageExists() {
		t.Skipf("proxy image %q not loaded", httpproxy.ImageTag())
	}
}

// waitForProxyDashboard polls the proxy dashboard health endpoint.
func waitForProxyDashboard(t *testing.T, port int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	url := fmt.Sprintf("http://localhost:%d/api/health", port)
	for time.Now().Before(deadline) {
		cmd := exec.Command("curl", "-sf", "--max-time", "2", url)
		if out, err := cmd.Output(); err == nil && strings.Contains(string(out), "ok") {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("proxy dashboard at port %d did not become healthy within %v", port, timeout)
}

// addProxyRule adds a rule to the proxy via the dashboard REST API.
func addProxyRule(t *testing.T, port int, ruleType, pattern, label string) {
	t.Helper()
	url := fmt.Sprintf("http://localhost:%d/api/rules", port)
	payload := fmt.Sprintf(`{"type":%q,"pattern":%q,"label":%q}`, ruleType, pattern, label)
	cmd := exec.Command("curl", "-sf", "-X", "POST",
		"-H", "Content-Type: application/json",
		"-d", payload, url)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("addProxyRule: %v\noutput: %s", err, out)
	}
}

// waitForCACert waits for the mitmproxy CA certificate to appear on the host.
func waitForCACert(t *testing.T, configDir string, timeout time.Duration) string {
	t.Helper()
	certDir := httpproxy.CACertDir(configDir)
	certPath := filepath.Join(certDir, "mitmproxy-ca-cert.pem")
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(certPath); err == nil {
			return certDir
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("CA cert not generated within %v at %s", timeout, certPath)
	return ""
}

// TestIntegrationProxyContainerSetup verifies that a Claude container started
// with proxy settings has the correct environment, CA certs, and can route
// traffic through the proxy sidecar.
func TestIntegrationProxyContainerSetup(t *testing.T) {
	skipIfDockerUnavailable(t)
	skipIfProxyImageUnavailable(t)

	profile := "docker-e2e"
	httpproxy.Stop(profile) // clean up stale proxy from previous runs
	configDir := makeConfigDir(t)
	os.MkdirAll(filepath.Join(configDir, "proxy-profiles"), 0o755)

	// Start the proxy sidecar.
	started, port, err := httpproxy.EnsureRunning(httpproxy.ProxyOpts{
		Session:       profile,
		ConfigDir:     configDir,
		DashboardPort: 0,
	})
	if err != nil {
		t.Fatalf("EnsureRunning: %v", err)
	}
	if !started {
		t.Fatal("expected proxy to be freshly started")
	}
	t.Cleanup(func() { httpproxy.Stop(profile) })

	waitForProxyDashboard(t, port, 30*time.Second)
	caCertDir := waitForCACert(t, configDir, 30*time.Second)

	proxyContainer := httpproxy.ContainerName(profile)

	// Add allow rule for the proxy's own dashboard.
	addProxyRule(t, port, "allow",
		fmt.Sprintf(`^http://%s:8081(/.*)?$`, proxyContainer),
		"proxy-dashboard")

	uid := ContainerUID()
	gid := ContainerGID()

	t.Run("ProxyEnvVarsSet", func(t *testing.T) {
		t.Skip("obsolete: HTTP_PROXY env vars are no longer set in shared-netns transparent mode; rewrite to assert traffic flow + presence of nftables redirect instead")
		result := runContainer(t, runContainerOpts{
			ConfigDir:    configDir,
			UID:          uid,
			GID:          gid,
			ProxySession: profile,
			Command:      []string{"sh", "-c", "env | sort"},
		})
		out := result.Stdout
		wantHTTP := fmt.Sprintf("HTTP_PROXY=http://%s:8080", proxyContainer)
		wantHTTPS := fmt.Sprintf("HTTPS_PROXY=http://%s:8080", proxyContainer)
		if !strings.Contains(out, wantHTTP) {
			t.Errorf("missing %s in env output:\n%s", wantHTTP, out)
		}
		if !strings.Contains(out, wantHTTPS) {
			t.Errorf("missing %s in env output:\n%s", wantHTTPS, out)
		}
	})

	t.Run("CACertMounted", func(t *testing.T) {
		result := runContainer(t, runContainerOpts{
			ConfigDir:      configDir,
			UID:            uid,
			GID:            gid,
			ProxySession:   profile,
			ProxyCACertDir: caCertDir,
			Command:        []string{"ls", "/proxy-ca/mitmproxy-ca-cert.pem"},
		})
		if !strings.Contains(result.Stdout, "mitmproxy-ca-cert.pem") {
			t.Errorf("CA cert not found at /proxy-ca/, got: %s", result.Stdout)
		}
	})

	t.Run("SSLCertEnvOverridden", func(t *testing.T) {
		result := runContainer(t, runContainerOpts{
			ConfigDir:      configDir,
			UID:            uid,
			GID:            gid,
			ProxySession:   profile,
			ProxyCACertDir: caCertDir,
			Command:        []string{"sh", "-c", "echo $SSL_CERT_FILE"},
		})
		got := strings.TrimSpace(stripEntrypointLogs(result.Stdout))
		want := "/proxy-ca/mitmproxy-ca-cert.pem"
		if got != want {
			t.Errorf("SSL_CERT_FILE = %q, want %q", got, want)
		}
	})

	t.Run("TrafficRoutedThroughProxy", func(t *testing.T) {
		// Use --proxy flag (not env var) because curl requires lowercase http_proxy.
		dashboardURL := fmt.Sprintf("http://%s:8081/api/health", proxyContainer)
		proxyURL := fmt.Sprintf("http://%s:8080", proxyContainer)

		result := runContainer(t, runContainerOpts{
			ConfigDir:    configDir,
			UID:          uid,
			GID:          gid,
			ProxySession: profile,
			Command: []string{"curl", "-s", "--proxy", proxyURL,
				"--max-time", "15", dashboardURL},
		})
		if !strings.Contains(result.Stdout, `"ok"`) {
			t.Errorf("expected health response with 'ok', got: %s", result.Stdout)
		}
	})

	t.Run("UnmatchedTrafficHeld", func(t *testing.T) {
		t.Skip("obsolete: shared-netns transparent mode no longer uses --proxy URLs; rewrite to dial through the netns directly")
		proxyURL := fmt.Sprintf("http://%s:8080", proxyContainer)

		// Curl should timeout because the proxy holds unmatched requests.
		cmd := exec.Command("docker", "run", "--rm",
			"--network", "claude-proxy-net_"+profile,
			"-e", "CLAUDE_CONFIG_DIR=/claude",
			"-e", fmt.Sprintf("USER_UID=%d", uid),
			"-e", fmt.Sprintf("USER_GID=%d", gid),
			"-v", configDir+":/claude",
			ImageTag(),
			"curl", "-s", "--proxy", proxyURL,
			"--max-time", "5", "http://www.google.com/",
		)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		start := time.Now()
		err := cmd.Run()
		elapsed := time.Since(start)

		if err == nil {
			t.Error("expected timeout for domain with no matching rule")
		}
		if elapsed < 4*time.Second {
			t.Errorf("request finished in %v; expected proxy to hold until timeout (~5s)", elapsed)
		}
	})
}

// ---------------------------------------------------------------------------
// E2E Claude Code + Proxy tests — real Claude session through proxy
// ---------------------------------------------------------------------------

// skipIfNoHostCredentials skips the test when host Claude credentials are not
// available (required for real Claude Code execution).
func skipIfNoHostCredentials(t *testing.T) {
	t.Helper()
	if len(config.HostClaudeCredentialFiles()) == 0 {
		t.Skip("no ~/.claude credential files found; need authenticated Claude Code")
	}
}

// claudeProxyE2EResult holds the outcome of a Claude proxy E2E test run.
type claudeProxyE2EResult struct {
	Workspace     string
	HNIntercepted bool
}

// runClaudeProxyE2E is a shared helper for the allow/deny E2E tests.
// It starts a proxy, runs Claude Code with a prompt, monitors pending requests,
// and resolves the Hacker News request with the given action ("allow" or "deny").
func runClaudeProxyE2E(t *testing.T, profile string, hnAction string) claudeProxyE2EResult {
	t.Helper()

	httpproxy.Stop(profile) // clean up stale proxy from previous runs
	configDir := makeConfigDir(t)
	os.MkdirAll(filepath.Join(configDir, "proxy-profiles"), 0o755)
	workspace := makeTempDir(t)

	// Write managed settings: sandbox disabled (bubblewrap can't run in Docker)
	// with wildcard domains and httpProxyPort. Network access control is handled
	// by the proxy sidecar instead of Claude's sandbox.
	medProfile, err := sandbox.GetProfile("med")
	if err != nil {
		t.Fatal(err)
	}
	settingsJSON, err := json.MarshalIndent(medProfile.ManagedSettingsForProxy(8080, nil, nil, nil), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "managed-settings.json"), settingsJSON, 0o644); err != nil {
		t.Fatal(err)
	}

	// Start proxy sidecar.
	started, dashPort, err := httpproxy.EnsureRunning(httpproxy.ProxyOpts{
		Session:       profile,
		ConfigDir:     configDir,
		DashboardPort: 0,
	})
	if err != nil {
		t.Fatalf("EnsureRunning: %v", err)
	}
	if !started {
		t.Fatal("expected proxy to be freshly started")
	}
	t.Cleanup(func() { httpproxy.Stop(profile) })

	waitForProxyDashboard(t, dashPort, 30*time.Second)
	caCertDir := waitForCACert(t, configDir, 30*time.Second)

	// Pre-allow Anthropic API and common infrastructure so Claude can function.
	addProxyRule(t, dashPort, "allow", `^https://.*anthropic\.com(/.*)?$`, "anthropic-api")
	addProxyRule(t, dashPort, "allow", `^https://.*sentry.*(/.*)?$`, "sentry")
	addProxyRule(t, dashPort, "allow", `^https://.*statsig.*(/.*)?$`, "statsig")

	// Build docker run args for Claude container.
	containerName := "claude-e2e-" + profile
	proxyContainer := httpproxy.ContainerName(profile)
	network := httpproxy.NetworkName(profile)
	uid, gid := ContainerUID(), ContainerGID()

	prompt := `Download https://hacker-news.firebaseio.com/v0/topstories.json using curl and save the raw content to /workspace/topstories.json. If the download fails for any reason (connection reset, timeout, HTTP error, etc), instead write a file /workspace/topstories-error.txt whose first line is FAILED and whose second line describes the error.`

	args := []string{
		"run", "-d",
		"--name", containerName,
		"--network", network,
		"-e", fmt.Sprintf("HTTP_PROXY=http://%s:8080", proxyContainer),
		"-e", fmt.Sprintf("HTTPS_PROXY=http://%s:8080", proxyContainer),
		"-v", caCertDir + ":/proxy-ca:ro",
		"-e", "SSL_CERT_FILE=/proxy-ca/mitmproxy-ca-cert.pem",
		"-e", "NIX_SSL_CERT_FILE=/proxy-ca/mitmproxy-ca-cert.pem",
		"-e", "NODE_EXTRA_CA_CERTS=/proxy-ca/mitmproxy-ca-cert.pem",
		"-v", workspace + ":/workspace",
		"-v", configDir + ":/claude",
		"-e", "CLAUDE_CONFIG_DIR=/claude",
		"-e", fmt.Sprintf("USER_UID=%d", uid),
		"-e", fmt.Sprintf("USER_GID=%d", gid),
	}
	for _, f := range config.HostClaudeCredentialFiles() {
		args = append(args, "-v", f+":/mnt/claude-host/"+filepath.Base(f)+":ro")
	}
	claudeArgs := []string{"claude"}
	// In rootless Docker the container runs as root; Claude refuses
	// --dangerously-skip-permissions as root. Managed settings handle perms.
	if !IsRootless() {
		claudeArgs = append(claudeArgs, "--dangerously-skip-permissions")
	}
	claudeArgs = append(claudeArgs, "-p", prompt)
	args = append(args, ImageTag())
	args = append(args, claudeArgs...)

	cmd := exec.Command("docker", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		exec.Command("docker", "rm", "-f", containerName).Run()
	})

	// Poll pending requests. Auto-resolve anything that isn't the Hacker News
	// URL. When the HN request appears, resolve it with the specified action.
	hnIntercepted := false
	deadline := time.Now().Add(180 * time.Second)

	for time.Now().Before(deadline) {
		// Check if container is still running.
		inspectCmd := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", containerName)
		inspectOut, _ := inspectCmd.Output()
		running := strings.TrimSpace(string(inspectOut)) == "true"

		// Poll pending requests.
		curlCmd := exec.Command("curl", "-sf", "--max-time", "2",
			fmt.Sprintf("http://localhost:%d/api/pending", dashPort))
		pendingOut, curlErr := curlCmd.Output()
		if curlErr == nil {
			var pending []map[string]any
			if json.Unmarshal(pendingOut, &pending) == nil {
				for _, p := range pending {
					flowID, _ := p["flow_id"].(string)
					url, _ := p["url"].(string)
					if flowID == "" {
						continue
					}

					isHN := strings.Contains(url, "hacker-news.firebaseio")

					var action, pattern, label string
					if isHN {
						action = hnAction
						pattern = `^https://hacker-news\.firebaseio\.com(/.*)?$`
						label = "hacker-news"
						hnIntercepted = true
						t.Logf("Hacker News request intercepted (flow %s): %s → %s", flowID, url, hnAction)
					} else {
						action = "allow"
						pattern = ".*"
						label = "auto-allow"
						t.Logf("Auto-resolving (flow %s): %s", flowID, url)
					}

					resolvePayload := fmt.Sprintf(
						`{"flow_id":%q,"action":%q,"pattern":%q,"label":%q}`,
						flowID, action, pattern, label)
					exec.Command("curl", "-sf", "-X", "POST",
						"-H", "Content-Type: application/json",
						"-d", resolvePayload,
						fmt.Sprintf("http://localhost:%d/api/resolve", dashPort)).Run()
				}
			}
		}

		if !running {
			break
		}

		time.Sleep(1 * time.Second)
	}

	// If the container is still running, wait for it to finish (with timeout).
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	exec.CommandContext(ctx, "docker", "wait", containerName).Run()

	if !hnIntercepted {
		logsCmd := exec.Command("docker", "logs", "--tail", "100", containerName)
		logs, _ := logsCmd.CombinedOutput()
		t.Fatalf("hacker-news request never appeared in proxy pending\nContainer logs:\n%s", StripANSI(string(logs)))
	}

	// Fix workspace permissions so the host test process can read output
	// files. In rootless Docker, files created by the container user are
	// owned by a subordinate UID that the host user cannot read.
	fixContainerPerms(workspace)

	return claudeProxyE2EResult{
		Workspace:     workspace,
		HNIntercepted: hnIntercepted,
	}
}

// TestIntegrationE2EClaudeProxyAllow runs a real Claude Code session through
// the proxy, intercepts the Hacker News API request, allows it, and verifies
// Claude successfully writes the downloaded JSON to disk.
func TestIntegrationE2EClaudeProxyAllow(t *testing.T) {
	skipIfDockerUnavailable(t)
	skipIfProxyImageUnavailable(t)
	skipIfNoHostCredentials(t)

	result := runClaudeProxyE2E(t, "e2e-claude-allow", "allow")

	// Verify the success file was written.
	filePath := filepath.Join(result.Workspace, "topstories.json")
	data, err := os.ReadFile(filePath)
	if err != nil {
		logsCmd := exec.Command("docker", "logs", "--tail", "100", "claude-e2e-e2e-claude-allow")
		logs, _ := logsCmd.CombinedOutput()
		t.Fatalf("topstories.json not found: %v\nContainer logs:\n%s", err, StripANSI(string(logs)))
	}

	// Verify valid JSON array.
	var stories []any
	if err := json.Unmarshal(data, &stories); err != nil {
		t.Fatalf("invalid JSON in topstories.json: %v\ncontent (first 500 chars): %s",
			err, string(data[:min(len(data), 500)]))
	}
	if len(stories) == 0 {
		t.Error("topstories.json contains empty array")
	}
	t.Logf("topstories.json: valid JSON array with %d items", len(stories))

	// Verify no error file was written.
	errPath := filepath.Join(result.Workspace, "topstories-error.txt")
	if _, err := os.Stat(errPath); err == nil {
		content, _ := os.ReadFile(errPath)
		t.Errorf("unexpected error file written: %s", string(content))
	}
}

// TestIntegrationE2EClaudeProxyDeny runs a real Claude Code session through
// the proxy, intercepts the Hacker News API request, denies it, and verifies
// Claude writes a failure indicator to disk.
func TestIntegrationE2EClaudeProxyDeny(t *testing.T) {
	skipIfDockerUnavailable(t)
	skipIfProxyImageUnavailable(t)
	skipIfNoHostCredentials(t)

	result := runClaudeProxyE2E(t, "e2e-claude-deny", "deny")

	// Verify the error file was written.
	errPath := filepath.Join(result.Workspace, "topstories-error.txt")
	errData, err := os.ReadFile(errPath)
	if err != nil {
		// Claude might not have written the error file. Check if it wrote
		// the success file instead (which would be a test failure).
		successPath := filepath.Join(result.Workspace, "topstories.json")
		if data, readErr := os.ReadFile(successPath); readErr == nil {
			t.Fatalf("expected error file, but topstories.json was written (deny didn't work): %s",
				string(data[:min(len(data), 200)]))
		}

		logsCmd := exec.Command("docker", "logs", "--tail", "100", "claude-e2e-e2e-claude-deny")
		logs, _ := logsCmd.CombinedOutput()
		t.Fatalf("topstories-error.txt not found: %v\nContainer logs:\n%s", err, StripANSI(string(logs)))
	}

	content := string(errData)
	if !strings.Contains(strings.ToUpper(content), "FAIL") {
		t.Errorf("error file should contain FAIL, got: %s", content)
	}
	t.Logf("Error file content: %s", strings.TrimSpace(content))

	// topstories.json should not exist (or should be empty/invalid).
	successPath := filepath.Join(result.Workspace, "topstories.json")
	if data, err := os.ReadFile(successPath); err == nil && len(data) > 0 {
		var stories []any
		if json.Unmarshal(data, &stories) == nil && len(stories) > 0 {
			t.Errorf("topstories.json should not contain valid data when connection was denied, got %d items", len(stories))
		}
	}
}
