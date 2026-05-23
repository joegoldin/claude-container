// Package httpproxy manages the per-session HTTP/HTTPS proxy sidecar
// container lifecycle. Each Claude session gets its own proxy sidecar that
// owns the network namespace the Claude container joins via
// `--network container:`. The proxy installs a default-deny nftables ruleset
// in that netns and REDIRECTs all TCP through mitmproxy in transparent mode.
//
// "Preset" is a separate concept: a saved JSON file of allow/deny rules that
// can be loaded as the initial state of a session's proxy. Presets live
// under <configDir>/proxy-presets/ and are seed-only — once a session
// starts, its rules diverge from the preset.
package httpproxy

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// PortRange describes a contiguous host-port range published by the proxy.
type PortRange struct {
	Base int // first port (inclusive)
	Size int // number of contiguous ports
}

// Last returns the inclusive end of the range.
func (r PortRange) Last() int { return r.Base + r.Size - 1 }

// IsZero reports whether the range was left at its zero value.
func (r PortRange) IsZero() bool { return r.Base == 0 && r.Size == 0 }

// ProxyOpts holds options for starting a per-session proxy sidecar.
type ProxyOpts struct {
	Session       string    // session name; identifies the per-session proxy
	ConfigDir     string    // base config dir (~/.config/claude-container)
	DashboardPort int       // host port for dashboard (default: pick free)
	ForceRestart  bool      // stop and restart even if already running (picks up new rules)
	PublishRange  PortRange // optional host-port range to publish from the proxy container
}

// ContainerName returns the Docker container name for the given session.
func ContainerName(session string) string {
	return "claude-proxy_" + session
}

// NetworkName returns the Docker network name for the given session.
func NetworkName(session string) string {
	return "claude-proxy-net_" + session
}

// SessionStateDir returns the per-session proxy state directory on the host.
// Holds the live rules file, dashboard token, and (via a sub-mount) the
// shared CA dir at runtime.
func SessionStateDir(configDir, session string) string {
	return filepath.Join(configDir, "proxy-state", session)
}

// SharedCADir returns the host directory where the mitmproxy CA cert lives.
// One CA is shared across every session's proxy so containers don't need
// to re-trust a new cert per session.
func SharedCADir(configDir string) string {
	return filepath.Join(configDir, "proxy-shared", "ca")
}

// CACertDir is kept for backwards compatibility; it returns the shared CA
// directory used by all per-session proxies.
func CACertDir(configDir string) string {
	return SharedCADir(configDir)
}

// PresetDir returns the host directory where saved rule presets live.
func PresetDir(configDir string) string {
	return filepath.Join(configDir, "proxy-presets")
}

// PresetPath returns the full path to a named preset JSON file.
func PresetPath(configDir, preset string) string {
	return filepath.Join(PresetDir(configDir), preset+".json")
}

// SessionRulesPath returns the path to a session's live rules file.
func SessionRulesPath(configDir, session string) string {
	return filepath.Join(SessionStateDir(configDir, session), "rules.json")
}

// EnsureSessionRules makes sure the session's live rules file exists. If it
// is missing and a seed preset name is provided, the preset is copied in
// from <configDir>/proxy-presets/<name>.json. If no preset matches, an
// empty rules file is created.
func EnsureSessionRules(configDir, session, seedPreset string) error {
	stateDir := SessionStateDir(configDir, session)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return fmt.Errorf("httpproxy: create session state dir: %w", err)
	}

	rulesPath := SessionRulesPath(configDir, session)
	if _, err := os.Stat(rulesPath); err == nil {
		return nil // already populated
	}

	if seedPreset != "" {
		if data, err := os.ReadFile(PresetPath(configDir, seedPreset)); err == nil {
			return os.WriteFile(rulesPath, data, 0o644)
		}
	}

	// No preset (or no match) — start with an empty rule list.
	return os.WriteFile(rulesPath, []byte("[]\n"), 0o644)
}

// AppendSessionRules appends the given rules (already-encoded JSON value
// list) to the session's rules file. The file is created if missing.
// Both the existing file and the new value must contain a JSON array of
// rule objects. Used by `claude new` to layer sandbox-profile-derived
// rules on top of an optional preset seed.
func AppendSessionRules(configDir, session string, newRulesJSON []byte) error {
	rulesPath := SessionRulesPath(configDir, session)
	if err := os.MkdirAll(filepath.Dir(rulesPath), 0o755); err != nil {
		return err
	}
	existing, err := os.ReadFile(rulesPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	existingTrim := strings.TrimSpace(string(existing))
	newTrim := strings.TrimSpace(string(newRulesJSON))

	// Strip the wrapping `[` `]` of each side and concatenate.
	stripBrackets := func(s string) string {
		s = strings.TrimSpace(s)
		s = strings.TrimPrefix(s, "[")
		s = strings.TrimSuffix(s, "]")
		return strings.TrimSpace(s)
	}
	left := stripBrackets(existingTrim)
	right := stripBrackets(newTrim)

	var merged string
	switch {
	case left == "" && right == "":
		merged = "[]"
	case left == "":
		merged = "[" + right + "]"
	case right == "":
		merged = "[" + left + "]"
	default:
		merged = "[" + left + ",\n" + right + "]"
	}
	return os.WriteFile(rulesPath, []byte(merged+"\n"), 0o644)
}

// SavePreset writes the given session's current rules JSON to a named
// preset file under proxy-presets/. The preset file overwrites any existing
// file with the same name.
func SavePreset(configDir, session, preset string) error {
	src := SessionRulesPath(configDir, session)
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("httpproxy: read session rules: %w", err)
	}
	if err := os.MkdirAll(PresetDir(configDir), 0o755); err != nil {
		return fmt.Errorf("httpproxy: create preset dir: %w", err)
	}
	return os.WriteFile(PresetPath(configDir, preset), data, 0o644)
}

// ImportPresetFile copies a JSON file from an arbitrary path on the host
// into the preset directory under the given name.
func ImportPresetFile(configDir, srcPath, preset string) error {
	in, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(PresetDir(configDir), 0o755); err != nil {
		return err
	}
	out, err := os.Create(PresetPath(configDir, preset))
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// ImageTag returns the proxy Docker image tag.
func ImageTag() string {
	if tag := os.Getenv("CLAUDE_PROXY_IMAGE_TAG"); tag != "" {
		return tag
	}
	return "claude-proxy:latest"
}

// ImageExists returns true if the proxy Docker image is available locally.
func ImageExists() bool {
	cmd := exec.Command("docker", "image", "inspect", ImageTag())
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// IsRunning returns true if the proxy container for the given session is
// currently running.
func IsRunning(session string) bool {
	name := ContainerName(session)
	cmd := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", name)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return false
	}
	return strings.TrimSpace(buf.String()) == "true"
}

// Exists returns true if a proxy container for the given session exists
// (running or stopped).
func Exists(session string) bool {
	name := ContainerName(session)
	cmd := exec.Command("docker", "inspect", name)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// RunArgs returns the docker run command arguments for a proxy sidecar
// container. The container is created with --rm and -d (detached, auto-remove).
//
// Two host directories are bind-mounted:
//   - the per-session state dir at /config (rules + dashboard token)
//   - the shared CA dir at /config/ca (mitmproxy CA cert, persistent across sessions)
func RunArgs(opts ProxyOpts) []string {
	name := ContainerName(opts.Session)
	network := NetworkName(opts.Session)
	stateMount := SessionStateDir(opts.ConfigDir, opts.Session)
	caMount := SharedCADir(opts.ConfigDir)

	args := []string{
		"run",
		"-d",
		"--rm",
		"--name", name,
		"--network", network,
		// NET_ADMIN lets the entrypoint install nftables rules in the
		// netns. The Claude container joins this same netns via
		// `--network container:`, so the firewall covers it too.
		"--cap-add", "NET_ADMIN",
		// no-new-privileges + resource limits — without these, a Claude-driven
		// flood of held requests (mitmproxy keeps full bodies in memory for
		// hold_timeout=3600s) can OOM the host docker daemon. The proxy
		// doesn't need much: ~50 active flows × a few hundred KB each fits
		// well inside 512MB. Override via CLAUDE_PROXY_MEMORY / _PIDS /
		// _CPUS if your workload needs more.
		"--security-opt", "no-new-privileges:true",
		"--memory", proxyEnvOr("CLAUDE_PROXY_MEMORY", "512m"),
		"--memory-swap", proxyEnvOr("CLAUDE_PROXY_MEMORY", "512m"),
		"--pids-limit", proxyEnvOr("CLAUDE_PROXY_PIDS_LIMIT", "256"),
		"--cpus", proxyEnvOr("CLAUDE_PROXY_CPUS", "1.0"),
		"-p", fmt.Sprintf("%d:8081", opts.DashboardPort),
	}

	if !opts.PublishRange.IsZero() {
		spec := fmt.Sprintf("127.0.0.1:%d-%d:%d-%d",
			opts.PublishRange.Base, opts.PublishRange.Last(),
			opts.PublishRange.Base, opts.PublishRange.Last())
		args = append(args,
			"-p", spec+"/tcp",
			"-p", spec+"/udp",
		)
	}

	args = append(args,
		"-v", stateMount+":/config",
		"-v", caMount+":/config/ca",
		"-e", "PROXY_SESSION="+opts.Session,
	)

	if !opts.PublishRange.IsZero() {
		args = append(args, "-e", fmt.Sprintf("PROXY_PUBLISH_RANGE=%d-%d",
			opts.PublishRange.Base, opts.PublishRange.Last()))
	}

	args = append(args, ImageTag())

	return args
}

// proxyEnvOr returns os.Getenv(key) if non-empty, else fallback. Local
// helper so we don't reach into the docker package for one util.
func proxyEnvOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ClaudeNetworkArgs returns extra docker run arguments that put the Claude
// container into the proxy's network namespace. With shared netns, the
// nftables rules installed by the proxy entrypoint cover both containers,
// transparently REDIRECTing every TCP connection through mitmproxy with no
// way for the Claude container to bypass it.
//
// HTTP_PROXY env vars are deliberately NOT set: transparent mode makes them
// redundant. `--network container:` is mutually exclusive with `--network`,
// `-p`, `--add-host`, etc. on the joining container — Docker enforces this.
// The Claude container doesn't publish ports so this is fine.
func ClaudeNetworkArgs(session string, caCertDir string) []string {
	name := ContainerName(session)

	args := []string{
		"--network", "container:" + name,
	}

	if caCertDir != "" {
		args = append(args,
			"-v", caCertDir+":/proxy-ca:ro",
			"-e", "SSL_CERT_FILE=/proxy-ca/mitmproxy-ca-cert.pem",
			"-e", "NIX_SSL_CERT_FILE=/proxy-ca/mitmproxy-ca-cert.pem",
			"-e", "NODE_EXTRA_CA_CERTS=/proxy-ca/mitmproxy-ca-cert.pem",
		)
	}

	return args
}

// EnsureNetwork creates the Docker network for the given session if it does
// not already exist.
func EnsureNetwork(session string) error {
	network := NetworkName(session)

	cmd := exec.Command("docker", "network", "inspect", network)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if cmd.Run() == nil {
		return nil
	}

	cmd = exec.Command("docker", "network", "create", network)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("httpproxy: failed to create network %s: %w", network, err)
	}
	return nil
}

// RemoveNetwork removes the Docker network for the given session, after
// disconnecting any containers still attached to it.
func RemoveNetwork(session string) error {
	network := NetworkName(session)

	inspect := exec.Command("docker", "network", "inspect", network,
		"--format", "{{range .Containers}}{{.Name}} {{end}}")
	if out, err := inspect.Output(); err == nil {
		for _, name := range strings.Fields(strings.TrimSpace(string(out))) {
			if name != "" {
				exec.Command("docker", "network", "disconnect", "-f", network, name).Run()
			}
		}
	}

	cmd := exec.Command("docker", "network", "rm", network)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("httpproxy: failed to remove network %s: %w", network, err)
	}
	return nil
}

// EnsureRunning starts the per-session proxy sidecar if it is not already
// running. Returns true if a new container was started, the resolved
// dashboard port, and any error.
//
// The caller is expected to have already invoked EnsureSessionRules so the
// rules file exists in the per-session state dir before the proxy starts.
func EnsureRunning(opts ProxyOpts) (started bool, port int, err error) {
	if opts.ForceRestart && IsRunning(opts.Session) {
		name := ContainerName(opts.Session)
		exec.Command("docker", "stop", name).Run()
		exec.Command("docker", "rm", "-f", name).Run()
	}

	if IsRunning(opts.Session) {
		existingPort := GetDashboardPort(opts.Session)
		if existingPort == 0 {
			return false, 0, fmt.Errorf("httpproxy: proxy running but can't determine port")
		}
		return false, existingPort, nil
	}

	if Exists(opts.Session) {
		name := ContainerName(opts.Session)
		exec.Command("docker", "rm", "-f", name).Run()
	}

	dashboardPort := opts.DashboardPort
	if dashboardPort == 0 {
		dashboardPort, err = FindAvailablePort()
		if err != nil {
			return false, 0, err
		}
	}

	// Make sure the host-side directories the proxy will mount exist
	// and are populated. The session state dir is created by
	// EnsureSessionRules; create the shared CA dir here so the bind
	// mount target exists even on first run.
	if err := os.MkdirAll(SharedCADir(opts.ConfigDir), 0o755); err != nil {
		return false, 0, fmt.Errorf("httpproxy: create shared CA dir: %w", err)
	}
	if _, statErr := os.Stat(SessionRulesPath(opts.ConfigDir, opts.Session)); os.IsNotExist(statErr) {
		// Defensive: caller should have done this, but seed empty if not.
		if err := EnsureSessionRules(opts.ConfigDir, opts.Session, ""); err != nil {
			return false, 0, err
		}
	}

	if err := EnsureNetwork(opts.Session); err != nil {
		return false, 0, err
	}

	resolvedOpts := ProxyOpts{
		Session:       opts.Session,
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

	if err := WaitForProxyReady(dashboardPort, 30*time.Second); err != nil {
		return false, 0, err
	}

	return true, dashboardPort, nil
}

// WaitForProxyReady polls the proxy dashboard on the given port until it
// responds to an HTTP request, or until the timeout expires.
func WaitForProxyReady(port int, timeout time.Duration) error {
	client := &http.Client{Timeout: 2 * time.Second}
	url := fmt.Sprintf("http://127.0.0.1:%d/api/pending", port)
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}

	return fmt.Errorf("httpproxy: timed out waiting for proxy to be ready on port %d", port)
}

// Stop stops the per-session proxy container and removes its network.
// Best-effort: errors stopping a non-existent container are swallowed,
// but the network removal error is returned.
func Stop(session string) error {
	name := ContainerName(session)
	exec.Command("docker", "stop", name).Run()
	exec.Command("docker", "rm", "-f", name).Run()
	return RemoveNetwork(session)
}

// RemoveSessionState deletes the per-session proxy state directory
// (rules file, dashboard token). Called when a session is removed.
func RemoveSessionState(configDir, session string) error {
	return os.RemoveAll(SessionStateDir(configDir, session))
}

// DashboardURL returns the host-side proxy dashboard URL for the given port.
func DashboardURL(port int) string {
	return fmt.Sprintf("http://localhost:%d", port)
}

// DashboardURLWithToken returns the dashboard URL with the per-session auth
// token query parameter appended, when one is available.
func DashboardURLWithToken(configDir, session string, port int) string {
	base := DashboardURL(port)
	token, err := ReadDashboardToken(configDir, session)
	if err != nil || token == "" {
		return base
	}
	return base + "/?token=" + token
}

// ReadDashboardToken returns the dashboard auth token written by the proxy
// for the given session, or "" if it does not exist yet.
func ReadDashboardToken(configDir, session string) (string, error) {
	path := filepath.Join(SessionStateDir(configDir, session), "dashboard-token")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// WaitForDashboardToken polls until the proxy has written the dashboard token
// file for the given session, or until the timeout expires.
func WaitForDashboardToken(configDir, session string, timeout time.Duration) error {
	path := filepath.Join(SessionStateDir(configDir, session), "dashboard-token")
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("httpproxy: timed out waiting for dashboard token at %s", path)
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
// the proxy with the given session. Returns 0 if not running.
func GetDashboardPort(session string) int {
	name := ContainerName(session)
	cmd := exec.Command("docker", "port", name, "8081")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return 0
	}
	output := strings.TrimSpace(buf.String())
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

// WaitForCACert waits for the mitmproxy CA certificate to exist in the
// shared CA cert directory. It polls every 500ms for up to 30 seconds.
func WaitForCACert(configDir string) error {
	certPath := filepath.Join(SharedCADir(configDir), "mitmproxy-ca-cert.pem")
	deadline := time.Now().Add(30 * time.Second)

	for time.Now().Before(deadline) {
		if _, err := os.Stat(certPath); err == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}

	return fmt.Errorf("httpproxy: timed out waiting for CA cert at %s", certPath)
}
