package httpproxy

import (
	"os/exec"
	"testing"
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

func TestIntegrationProxyStartStop(t *testing.T) {
	skipIfDockerUnavailable(t)

	profile := "integration-test"
	opts := ProxyOpts{
		Profile:       profile,
		ConfigDir:     t.TempDir(),
		DashboardPort: 18081,
	}

	// Start proxy
	started, err := EnsureRunning(opts)
	if err != nil {
		t.Fatalf("EnsureRunning: %v", err)
	}
	if !started {
		t.Error("expected proxy to be started, not reused")
	}
	t.Cleanup(func() { Stop(profile) })

	// Verify running
	if !IsRunning(profile) {
		t.Error("proxy should be running")
	}

	// Reuse
	started2, err := EnsureRunning(opts)
	if err != nil {
		t.Fatalf("EnsureRunning (reuse): %v", err)
	}
	if started2 {
		t.Error("expected proxy to be reused, not started")
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
	opts := ProxyOpts{
		Profile:       profile,
		ConfigDir:     t.TempDir(),
		DashboardPort: 18082,
	}

	started, err := EnsureRunning(opts)
	if err != nil {
		t.Fatalf("EnsureRunning: %v", err)
	}
	if !started {
		t.Fatal("expected fresh start")
	}
	t.Cleanup(func() { Stop(profile) })

	// Health endpoint should respond
	count := PendingCount(18082)
	if count != 0 {
		t.Errorf("PendingCount = %d, want 0", count)
	}
}
