// Package httpproxy manages the HTTP/HTTPS proxy sidecar container lifecycle.
// Proxy containers are named by profile (not session), allowing multiple
// Claude sessions to share one proxy via the same --proxy-profile.
package httpproxy

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ProxyOpts holds options for starting a proxy sidecar.
type ProxyOpts struct {
	Profile       string
	ConfigDir     string // base config dir (~/.config/claude-container)
	DashboardPort int    // host port for dashboard (default 8081)
}

// ContainerName returns the Docker container name for the given proxy profile.
func ContainerName(profile string) string {
	return "claude-proxy_" + profile
}

// NetworkName returns the Docker network name for the given proxy profile.
func NetworkName(profile string) string {
	return "claude-proxy-net_" + profile
}

// ImageTag returns the proxy Docker image tag.
// Reads from CLAUDE_PROXY_IMAGE_TAG env var, defaulting to "claude-proxy:nix".
func ImageTag() string {
	if tag := os.Getenv("CLAUDE_PROXY_IMAGE_TAG"); tag != "" {
		return tag
	}
	return "claude-proxy:nix"
}

// ImageExists returns true if the proxy Docker image is available locally.
func ImageExists() bool {
	cmd := exec.Command("docker", "image", "inspect", ImageTag())
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// IsRunning returns true if the proxy container for the given profile is
// currently running.
func IsRunning(profile string) bool {
	name := ContainerName(profile)
	cmd := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", name)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return false
	}
	return strings.TrimSpace(buf.String()) == "true"
}

// Exists returns true if a proxy container for the given profile exists
// (running or stopped).
func Exists(profile string) bool {
	name := ContainerName(profile)
	cmd := exec.Command("docker", "inspect", name)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// RunArgs returns the docker run command arguments for a proxy sidecar
// container. The container is created with --rm and -d (detached, auto-remove).
func RunArgs(opts ProxyOpts) []string {
	name := ContainerName(opts.Profile)
	network := NetworkName(opts.Profile)
	configMount := filepath.Join(opts.ConfigDir, "proxy-profiles")

	args := []string{
		"run",
		"-d",
		"--rm",
		"--name", name,
		"--network", network,
		"-p", fmt.Sprintf("%d:8081", opts.DashboardPort),
		"-v", configMount + ":/config",
		"-e", "PROXY_PROFILE=" + opts.Profile,
		ImageTag(),
	}

	return args
}

// ClaudeNetworkArgs returns extra docker run arguments to connect a Claude
// container to the proxy network and configure proxy environment variables.
// If caCertDir is non-empty, the CA certificate directory is mounted and
// SSL cert env vars are set.
func ClaudeNetworkArgs(profile string, caCertDir string) []string {
	name := ContainerName(profile)
	network := NetworkName(profile)
	proxyURL := fmt.Sprintf("http://%s:8080", name)

	args := []string{
		"--network", network,
		"-e", "HTTP_PROXY=" + proxyURL,
		"-e", "HTTPS_PROXY=" + proxyURL,
	}

	if caCertDir != "" {
		args = append(args,
			"-v", caCertDir+":/proxy-ca:ro",
			"-e", "SSL_CERT_FILE=/proxy-ca/mitmproxy-ca-cert.pem",
			"-e", "NIX_SSL_CERT_FILE=/proxy-ca/mitmproxy-ca-cert.pem",
		)
	}

	return args
}

// EnsureNetwork creates the Docker network for the given profile if it does
// not already exist.
func EnsureNetwork(profile string) error {
	network := NetworkName(profile)

	// Check if network already exists.
	cmd := exec.Command("docker", "network", "inspect", network)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if cmd.Run() == nil {
		return nil // already exists
	}

	// Create the network.
	cmd = exec.Command("docker", "network", "create", network)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("httpproxy: failed to create network %s: %w", network, err)
	}
	return nil
}

// RemoveNetwork removes the Docker network for the given profile.
func RemoveNetwork(profile string) error {
	network := NetworkName(profile)
	cmd := exec.Command("docker", "network", "rm", network)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("httpproxy: failed to remove network %s: %w", network, err)
	}
	return nil
}

// EnsureRunning starts the proxy sidecar if it is not already running.
// Returns true if a new container was started, the resolved dashboard port,
// and any error.
func EnsureRunning(opts ProxyOpts) (started bool, port int, err error) {
	if IsRunning(opts.Profile) {
		// Proxy already running — discover its port.
		existingPort := GetDashboardPort(opts.Profile)
		if existingPort == 0 {
			return false, 0, fmt.Errorf("httpproxy: proxy running but can't determine port")
		}
		return false, existingPort, nil
	}

	// If a stopped container exists, remove it first.
	if Exists(opts.Profile) {
		name := ContainerName(opts.Profile)
		rm := exec.Command("docker", "rm", "-f", name)
		rm.Stdout = nil
		rm.Stderr = nil
		rm.Run()
	}

	// Resolve dashboard port: pick a random one if 0.
	dashboardPort := opts.DashboardPort
	if dashboardPort == 0 {
		dashboardPort, err = FindAvailablePort()
		if err != nil {
			return false, 0, err
		}
	}

	if err := EnsureNetwork(opts.Profile); err != nil {
		return false, 0, err
	}

	resolvedOpts := ProxyOpts{
		Profile:       opts.Profile,
		ConfigDir:     opts.ConfigDir,
		DashboardPort: dashboardPort,
	}
	args := RunArgs(resolvedOpts)
	cmd := exec.Command("docker", args...)
	var stderr bytes.Buffer
	cmd.Stdout = nil
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return false, 0, fmt.Errorf("httpproxy: failed to start proxy: %s: %w", stderr.String(), err)
	}

	return true, dashboardPort, nil
}

// Stop stops the proxy container for the given profile and removes the network.
func Stop(profile string) error {
	name := ContainerName(profile)

	// Stop the container (which also removes it due to --rm).
	cmd := exec.Command("docker", "stop", name)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Run() // best-effort; container may already be stopped

	// Force remove in case --rm didn't trigger.
	rm := exec.Command("docker", "rm", "-f", name)
	rm.Stdout = nil
	rm.Stderr = nil
	rm.Run() // best-effort

	// Remove the network.
	return RemoveNetwork(profile)
}

// DashboardURL returns the proxy dashboard URL for the given port.
func DashboardURL(port int) string {
	return fmt.Sprintf("http://localhost:%d", port)
}

// FindAvailablePort finds a random available TCP port by binding to :0.
func FindAvailablePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("httpproxy: find available port: %w", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port, nil
}

// GetDashboardPort returns the host port mapped to container port 8081 for
// the proxy with the given profile. Returns 0 if the container is not
// running or the port can't be determined.
func GetDashboardPort(profile string) int {
	name := ContainerName(profile)
	cmd := exec.Command("docker", "port", name, "8081")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return 0
	}
	// Output is like "0.0.0.0:18081\n" — extract port after last colon.
	output := strings.TrimSpace(buf.String())
	// May have multiple lines (IPv4 and IPv6). Take the first.
	if idx := strings.Index(output, "\n"); idx >= 0 {
		output = output[:idx]
	}
	if idx := strings.LastIndex(output, ":"); idx >= 0 {
		var port int
		if _, err := fmt.Sscanf(output[idx+1:], "%d", &port); err == nil {
			return port
		}
	}
	return 0
}

// PendingCount queries the proxy dashboard API and returns the number of
// pending requests by counting flow_id occurrences in the response.
func PendingCount(port int) int {
	url := fmt.Sprintf("http://localhost:%d/api/pending", port)
	cmd := exec.Command("curl", "-s", "--max-time", "2", url)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return 0
	}
	return strings.Count(buf.String(), "flow_id")
}

// CACertDir returns the path to the CA certificate directory within the
// proxy profile configuration.
func CACertDir(configDir string) string {
	return filepath.Join(configDir, "proxy-profiles", "ca")
}

// WaitForCACert waits for the mitmproxy CA certificate to exist in the
// CA cert directory. It polls every 500ms for up to 30 seconds.
func WaitForCACert(configDir string) error {
	certPath := filepath.Join(CACertDir(configDir), "mitmproxy-ca-cert.pem")
	deadline := time.Now().Add(30 * time.Second)

	for time.Now().Before(deadline) {
		if _, err := os.Stat(certPath); err == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}

	return fmt.Errorf("httpproxy: timed out waiting for CA cert at %s", certPath)
}
