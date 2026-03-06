package httpproxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func skipIfDockerUnavailable(t *testing.T) {
	t.Helper()
	cmd := exec.Command("docker", "info")
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		t.Skip("docker not available")
	}
	if !ImageExists() {
		t.Skipf("proxy image %q not loaded", ImageTag())
	}
}

// cleanupStaleProxy removes any leftover proxy container and network from a
// previous test run that may not have cleaned up properly.
func cleanupStaleProxy(t *testing.T, profile string) {
	t.Helper()
	Stop(profile) // best-effort: removes container + network
}

func TestIntegrationProxyStartStop(t *testing.T) {
	skipIfDockerUnavailable(t)

	profile := "integration-test"
	cleanupStaleProxy(t, profile)

	opts := ProxyOpts{
		Profile:       profile,
		ConfigDir:     t.TempDir(),
		DashboardPort: 18081,
	}

	// Start proxy
	started, port, err := EnsureRunning(opts)
	if err != nil {
		t.Fatalf("EnsureRunning: %v", err)
	}
	if !started {
		t.Error("expected proxy to be started, not reused")
	}
	if port == 0 {
		t.Error("expected non-zero port")
	}
	t.Cleanup(func() { Stop(profile) })

	// Verify running
	if !IsRunning(profile) {
		t.Error("proxy should be running")
	}

	// Reuse
	started2, port2, err := EnsureRunning(opts)
	if err != nil {
		t.Fatalf("EnsureRunning (reuse): %v", err)
	}
	if started2 {
		t.Error("expected proxy to be reused, not started")
	}
	if port2 == 0 {
		t.Errorf("expected non-zero port on reuse, got %d", port2)
	}

	// Stop
	Stop(profile)
	if IsRunning(profile) {
		t.Error("proxy should be stopped")
	}
}

func TestIntegrationProxyHealthEndpoint(t *testing.T) {
	skipIfDockerUnavailable(t)

	profile := "health-test"
	cleanupStaleProxy(t, profile)

	opts := ProxyOpts{
		Profile:       profile,
		ConfigDir:     t.TempDir(),
		DashboardPort: 18082,
	}

	started, port, err := EnsureRunning(opts)
	if err != nil {
		t.Fatalf("EnsureRunning: %v", err)
	}
	if !started {
		t.Fatal("expected fresh start")
	}
	if port == 0 {
		t.Fatal("expected non-zero port")
	}
	t.Cleanup(func() { Stop(profile) })

	// Health endpoint should respond
	count := PendingCount(port)
	if count != 0 {
		t.Errorf("PendingCount = %d, want 0", count)
	}
}

// --- E2E helpers ---

// waitForDashboard polls the dashboard health endpoint until it responds "ok"
// or the timeout expires.
func waitForDashboard(t *testing.T, port int, timeout time.Duration) {
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
	t.Fatalf("dashboard at port %d did not become healthy within %v", port, timeout)
}

// addAllowRule posts an allow rule to the dashboard REST API.
func addAllowRule(t *testing.T, port int, pattern, label string) {
	t.Helper()
	url := fmt.Sprintf("http://localhost:%d/api/rules", port)
	payload := fmt.Sprintf(`{"type":"allow","pattern":%q,"label":%q}`, pattern, label)
	cmd := exec.Command("curl", "-sf", "-X", "POST",
		"-H", "Content-Type: application/json",
		"-d", payload, url)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("addAllowRule: %v\noutput: %s", err, out)
	}
	if !strings.Contains(string(out), `"id"`) {
		t.Fatalf("addAllowRule: expected id in response, got: %s", out)
	}
}

// curlViaProxy runs curl in a temporary Docker container on the given network,
// routing traffic through the HTTP proxy. Returns the response body.
func curlViaProxy(t *testing.T, network, proxyAddr, targetURL string, timeoutSec int) string {
	t.Helper()
	out, err := curlViaProxyRaw(network, proxyAddr, targetURL, timeoutSec)
	if err != nil {
		t.Fatalf("curlViaProxy failed: %v\noutput: %s", err, out)
	}
	return out
}

// curlViaProxyRaw runs curl in a container and returns stdout + error.
// Uses lowercase env vars because curl respects http_proxy, not HTTP_PROXY.
func curlViaProxyRaw(network, proxyAddr, targetURL string, timeoutSec int) (string, error) {
	cmd := exec.Command("docker", "run", "--rm",
		"--network", network,
		"-e", "http_proxy="+proxyAddr,
		"-e", "https_proxy="+proxyAddr,
		"--entrypoint", "curl",
		ImageTag(),
		"-s", "--max-time", fmt.Sprintf("%d", timeoutSec),
		targetURL,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), err
}

// TestIntegrationE2EProxyTraffic verifies that real HTTP requests are routed
// through the proxy sidecar: allowed domains succeed, unmatched domains are held.
func TestIntegrationE2EProxyTraffic(t *testing.T) {
	skipIfDockerUnavailable(t)

	profile := "e2e-traffic"
	cleanupStaleProxy(t, profile)
	configDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(configDir, "proxy-profiles"), 0o755); err != nil {
		t.Fatal(err)
	}

	opts := ProxyOpts{
		Profile:       profile,
		ConfigDir:     configDir,
		DashboardPort: 0, // auto-assign random port
	}

	started, port, err := EnsureRunning(opts)
	if err != nil {
		t.Fatalf("EnsureRunning: %v", err)
	}
	if !started {
		t.Fatal("expected proxy to be freshly started")
	}
	t.Cleanup(func() { Stop(profile) })

	// Wait for the dashboard API to become healthy.
	waitForDashboard(t, port, 30*time.Second)

	proxyContainer := ContainerName(profile)
	network := NetworkName(profile)
	proxyAddr := fmt.Sprintf("http://%s:8080", proxyContainer)
	dashboardURL := fmt.Sprintf("http://%s:8081/api/health", proxyContainer)

	// Add an allow rule so the proxy forwards requests to its own dashboard.
	addAllowRule(t, port,
		fmt.Sprintf(`^http://%s:8081(/.*)?$`, proxyContainer),
		"proxy-dashboard")

	t.Run("AllowedTrafficSucceeds", func(t *testing.T) {
		body := curlViaProxy(t, network, proxyAddr, dashboardURL, 15)
		if !strings.Contains(body, `"ok"`) {
			t.Errorf("expected health response containing 'ok', got: %s", body)
		}
	})

	t.Run("UnmatchedTrafficIsHeld", func(t *testing.T) {
		start := time.Now()
		// Use a real domain that resolves but has no matching proxy rule.
		_, err := curlViaProxyRaw(network, proxyAddr,
			"http://www.google.com/", 5)
		elapsed := time.Since(start)

		if err == nil {
			t.Error("expected timeout for domain with no matching rule")
		}
		// Verify the request was held for close to the timeout, not rejected
		// instantly (which would indicate the hold isn't working).
		if elapsed < 4*time.Second {
			t.Errorf("request finished in %v; expected proxy to hold until timeout (~5s)", elapsed)
		}
	})
}

// TestIntegrationE2EResolveWhileHeld verifies that a request blocked by the
// proxy is released immediately when the dashboard resolve API is called.
// This simulates clicking "Accept" in the dashboard while a request is pending.
func TestIntegrationE2EResolveWhileHeld(t *testing.T) {
	skipIfDockerUnavailable(t)

	profile := "e2e-resolve"
	cleanupStaleProxy(t, profile)
	configDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(configDir, "proxy-profiles"), 0o755); err != nil {
		t.Fatal(err)
	}

	opts := ProxyOpts{
		Profile:       profile,
		ConfigDir:     configDir,
		DashboardPort: 0,
	}

	started, port, err := EnsureRunning(opts)
	if err != nil {
		t.Fatalf("EnsureRunning: %v", err)
	}
	if !started {
		t.Fatal("expected proxy to be freshly started")
	}
	t.Cleanup(func() { Stop(profile) })

	waitForDashboard(t, port, 30*time.Second)

	proxyContainer := ContainerName(profile)
	network := NetworkName(profile)
	proxyAddr := fmt.Sprintf("http://%s:8080", proxyContainer)
	targetURL := fmt.Sprintf("http://%s:8081/api/health", proxyContainer)

	// No rules — the request will be held by the proxy.

	// Run curl in a background goroutine (it will block until resolved).
	type curlResult struct {
		output string
		err    error
	}
	ch := make(chan curlResult, 1)
	go func() {
		out, err := curlViaProxyRaw(network, proxyAddr, targetURL, 30)
		ch <- curlResult{out, err}
	}()

	// Poll the pending endpoint until our request appears.
	var flowID string
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		cmd := exec.Command("curl", "-sf", "--max-time", "2",
			fmt.Sprintf("http://localhost:%d/api/pending", port))
		out, err := cmd.Output()
		if err == nil {
			var pending []map[string]any
			if json.Unmarshal(out, &pending) == nil && len(pending) > 0 {
				if id, ok := pending[0]["flow_id"].(string); ok {
					flowID = id
					break
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	if flowID == "" {
		t.Fatal("pending request did not appear within 15s")
	}

	// Resolve the pending request via the dashboard API (simulates "Accept").
	allowPattern := fmt.Sprintf(`^http://%s:8081(/.*)?$`, proxyContainer)
	resolvePayload := fmt.Sprintf(
		`{"flow_id":%q,"action":"allow","pattern":%q,"label":"resolved-test"}`,
		flowID, allowPattern)
	resolveCmd := exec.Command("curl", "-sf", "-X", "POST",
		"-H", "Content-Type: application/json",
		"-d", resolvePayload,
		fmt.Sprintf("http://localhost:%d/api/resolve", port))
	resolveOut, err := resolveCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("resolve API call failed: %v\noutput: %s", err, resolveOut)
	}

	// The curl request should now complete successfully.
	select {
	case result := <-ch:
		if result.err != nil {
			t.Fatalf("curl failed after resolve: %v\noutput: %s", result.err, result.output)
		}
		if !strings.Contains(result.output, `"ok"`) {
			t.Errorf("expected health response after resolve, got: %s", result.output)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("curl did not complete within 15s after resolve")
	}
}
