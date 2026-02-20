package httpproxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProxyContainerName(t *testing.T) {
	tests := []struct {
		profile string
		want    string
	}{
		{"default", "claude-proxy_default"},
		{"work", "claude-proxy_work"},
		{"my-project", "claude-proxy_my-project"},
	}
	for _, tc := range tests {
		got := ContainerName(tc.profile)
		if got != tc.want {
			t.Errorf("ContainerName(%q) = %q, want %q", tc.profile, got, tc.want)
		}
	}
}

func TestNetworkName(t *testing.T) {
	tests := []struct {
		profile string
		want    string
	}{
		{"default", "claude-proxy-net_default"},
		{"work", "claude-proxy-net_work"},
		{"my-project", "claude-proxy-net_my-project"},
	}
	for _, tc := range tests {
		got := NetworkName(tc.profile)
		if got != tc.want {
			t.Errorf("NetworkName(%q) = %q, want %q", tc.profile, got, tc.want)
		}
	}
}

func TestRunArgs(t *testing.T) {
	opts := ProxyOpts{
		Profile:       "default",
		ConfigDir:     "/home/user/.config/claude-container",
		DashboardPort: 8081,
	}

	// Set a known image tag for predictable test output.
	os.Setenv("CLAUDE_PROXY_IMAGE_TAG", "claude-proxy:test")
	defer os.Unsetenv("CLAUDE_PROXY_IMAGE_TAG")

	args := RunArgs(opts)
	joined := strings.Join(args, " ")

	// Verify all expected flags are present.
	checks := []struct {
		name string
		want string
	}{
		{"run command", "run"},
		{"detached flag", "-d"},
		{"auto-remove", "--rm"},
		{"container name", "--name claude-proxy_default"},
		{"network", "--network claude-proxy-net_default"},
		{"dashboard port", "-p 8081:8081"},
		{"config volume", "-v /home/user/.config/claude-container/proxy-profiles:/config"},
		{"profile env", "-e PROXY_PROFILE=default"},
		{"image tag", "claude-proxy:test"},
	}

	for _, c := range checks {
		if !strings.Contains(joined, c.want) {
			t.Errorf("RunArgs: missing %s (%q) in args: %s", c.name, c.want, joined)
		}
	}

	// Verify ordering: "run" should be first, image tag should be last.
	if args[0] != "run" {
		t.Errorf("RunArgs: first arg should be 'run', got %q", args[0])
	}
	if args[len(args)-1] != "claude-proxy:test" {
		t.Errorf("RunArgs: last arg should be image tag, got %q", args[len(args)-1])
	}
}

func TestClaudeNetworkArgs(t *testing.T) {
	profile := "work"
	caCertDir := "/home/user/.config/claude-container/proxy-profiles/ca"

	args := ClaudeNetworkArgs(profile, caCertDir)
	joined := strings.Join(args, " ")

	checks := []struct {
		name string
		want string
	}{
		{"network", "--network claude-proxy-net_work"},
		{"HTTP_PROXY", "-e HTTP_PROXY=http://claude-proxy_work:8080"},
		{"HTTPS_PROXY", "-e HTTPS_PROXY=http://claude-proxy_work:8080"},
		{"CA cert volume", "-v " + caCertDir + ":/proxy-ca:ro"},
		{"SSL_CERT_FILE", "-e SSL_CERT_FILE=/proxy-ca/mitmproxy-ca-cert.pem"},
		{"NIX_SSL_CERT_FILE", "-e NIX_SSL_CERT_FILE=/proxy-ca/mitmproxy-ca-cert.pem"},
		{"NODE_EXTRA_CA_CERTS", "-e NODE_EXTRA_CA_CERTS=/proxy-ca/mitmproxy-ca-cert.pem"},
	}

	for _, c := range checks {
		if !strings.Contains(joined, c.want) {
			t.Errorf("ClaudeNetworkArgs: missing %s (%q) in args: %s", c.name, c.want, joined)
		}
	}
}

func TestClaudeNetworkArgsNoCACert(t *testing.T) {
	profile := "default"
	caCertDir := "" // empty = no CA cert

	args := ClaudeNetworkArgs(profile, caCertDir)
	joined := strings.Join(args, " ")

	// Should have network and proxy env vars.
	if !strings.Contains(joined, "--network claude-proxy-net_default") {
		t.Error("ClaudeNetworkArgs (no cert): missing network flag")
	}
	if !strings.Contains(joined, "HTTP_PROXY=http://claude-proxy_default:8080") {
		t.Error("ClaudeNetworkArgs (no cert): missing HTTP_PROXY")
	}
	if !strings.Contains(joined, "HTTPS_PROXY=http://claude-proxy_default:8080") {
		t.Error("ClaudeNetworkArgs (no cert): missing HTTPS_PROXY")
	}

	// Should NOT have cert-related args.
	if strings.Contains(joined, "/proxy-ca") {
		t.Error("ClaudeNetworkArgs (no cert): should not contain /proxy-ca when caCertDir is empty")
	}
	if strings.Contains(joined, "SSL_CERT_FILE") {
		t.Error("ClaudeNetworkArgs (no cert): should not contain SSL_CERT_FILE when caCertDir is empty")
	}
	if strings.Contains(joined, "NIX_SSL_CERT_FILE") {
		t.Error("ClaudeNetworkArgs (no cert): should not contain NIX_SSL_CERT_FILE when caCertDir is empty")
	}
	if strings.Contains(joined, "NODE_EXTRA_CA_CERTS") {
		t.Error("ClaudeNetworkArgs (no cert): should not contain NODE_EXTRA_CA_CERTS when caCertDir is empty")
	}
}

func TestImageTagDefault(t *testing.T) {
	// Clear the env var to test default.
	os.Unsetenv("CLAUDE_PROXY_IMAGE_TAG")

	got := ImageTag()
	want := "claude-proxy:latest"
	if got != want {
		t.Errorf("ImageTag() = %q, want %q (default)", got, want)
	}
}

func TestImageTagEnvOverride(t *testing.T) {
	os.Setenv("CLAUDE_PROXY_IMAGE_TAG", "my-registry/proxy:v2")
	defer os.Unsetenv("CLAUDE_PROXY_IMAGE_TAG")

	got := ImageTag()
	want := "my-registry/proxy:v2"
	if got != want {
		t.Errorf("ImageTag() = %q, want %q", got, want)
	}
}

func TestDashboardURL(t *testing.T) {
	tests := []struct {
		port int
		want string
	}{
		{8081, "http://localhost:8081"},
		{9090, "http://localhost:9090"},
		{80, "http://localhost:80"},
	}
	for _, tc := range tests {
		got := DashboardURL(tc.port)
		if got != tc.want {
			t.Errorf("DashboardURL(%d) = %q, want %q", tc.port, got, tc.want)
		}
	}
}

func TestCACertDir(t *testing.T) {
	configDir := "/home/user/.config/claude-container"
	got := CACertDir(configDir)
	want := filepath.Join(configDir, "proxy-profiles", "ca")
	if got != want {
		t.Errorf("CACertDir(%q) = %q, want %q", configDir, got, want)
	}
}

func TestFindAvailablePort(t *testing.T) {
	port, err := FindAvailablePort()
	if err != nil {
		t.Fatalf("FindAvailablePort: %v", err)
	}
	if port < 1024 || port > 65535 {
		t.Errorf("port %d out of expected range", port)
	}
}

func TestFindAvailablePortUnique(t *testing.T) {
	ports := make(map[int]bool)
	for i := 0; i < 10; i++ {
		port, err := FindAvailablePort()
		if err != nil {
			t.Fatalf("FindAvailablePort: %v", err)
		}
		ports[port] = true
	}
	// At least 5 unique ports out of 10 calls (ports could be recycled)
	if len(ports) < 5 {
		t.Errorf("expected at least 5 unique ports, got %d", len(ports))
	}
}

func TestGetDashboardPortNoContainer(t *testing.T) {
	// Container doesn't exist — should return 0.
	port := GetDashboardPort("nonexistent-profile")
	if port != 0 {
		t.Errorf("expected 0 for nonexistent container, got %d", port)
	}
}
