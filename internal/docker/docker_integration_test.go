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
	"time"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/httpproxy"
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
	ProxyProfile    string   // connect to proxy network and set proxy env vars
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

	// Proxy network and env vars (mirrors RunArgs when ProxyProfile is set).
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
	// Use the Nix-built image which includes curl and all tools.
	t.Setenv("CLAUDE_CONTAINER_IMAGE_TAG", "claude-code:nix")

	skipIfDockerUnavailable(t)
	skipIfProxyImageUnavailable(t)

	profile := "docker-e2e"
	configDir := makeConfigDir(t)
	os.MkdirAll(filepath.Join(configDir, "proxy-profiles"), 0o755)

	// Start the proxy sidecar.
	started, port, err := httpproxy.EnsureRunning(httpproxy.ProxyOpts{
		Profile:       profile,
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

	uid := os.Getuid()
	gid := os.Getgid()

	t.Run("ProxyEnvVarsSet", func(t *testing.T) {
		result := runContainer(t, runContainerOpts{
			ConfigDir:    configDir,
			UID:          uid,
			GID:          gid,
			ProxyProfile: profile,
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
			ProxyProfile:   profile,
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
			ProxyProfile:   profile,
			ProxyCACertDir: caCertDir,
			Command:        []string{"sh", "-c", "echo $SSL_CERT_FILE"},
		})
		got := strings.TrimSpace(result.Stdout)
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
			ProxyProfile: profile,
			Command: []string{"curl", "-s", "--proxy", proxyURL,
				"--max-time", "15", dashboardURL},
		})
		if !strings.Contains(result.Stdout, `"ok"`) {
			t.Errorf("expected health response with 'ok', got: %s", result.Stdout)
		}
	})

	t.Run("UnmatchedTrafficHeld", func(t *testing.T) {
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
