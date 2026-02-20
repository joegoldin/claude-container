package docker

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// ImageName is the Docker image tag used for Claude Code containers.
const ImageName = "claude-code"

// ContainerName returns the Docker container name for the given session.
func ContainerName(session string) string {
	return "claude-container_" + session
}

// ImageTag returns the full Docker image reference (name:tag).
// When CLAUDE_CONTAINER_IMAGE_TAG is set (by the Nix wrapper), it is used.
// Otherwise falls back to the legacy image name.
func ImageTag() string {
	if tag := os.Getenv("CLAUDE_CONTAINER_IMAGE_TAG"); tag != "" {
		return tag
	}
	return ImageName
}

// LoadImage loads the Docker image from the Nix-built tarball.
func LoadImage() error {
	tarball := os.Getenv("CLAUDE_CONTAINER_IMAGE_TARBALL")
	if tarball == "" {
		return fmt.Errorf("CLAUDE_CONTAINER_IMAGE_TARBALL is not set")
	}
	fmt.Println("Loading Docker image...")
	cmd := exec.Command("docker", "load", "-i", tarball)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// EnsureImage ensures the Docker image is available, loading it from the
// Nix-built tarball if necessary. The configDir is used to store a marker
// file tracking which tarball was last loaded.
func EnsureImage(configDir string) error {
	tarball := os.Getenv("CLAUDE_CONTAINER_IMAGE_TARBALL")
	if tarball == "" {
		if ImageExists() {
			return nil
		}
		return fmt.Errorf("image %q not found and CLAUDE_CONTAINER_IMAGE_TARBALL not set", ImageTag())
	}

	// Check marker to see if we already loaded this tarball.
	markerPath := filepath.Join(configDir, "loaded-image")
	if data, err := os.ReadFile(markerPath); err == nil {
		if string(bytes.TrimSpace(data)) == tarball && ImageExists() {
			return nil
		}
	}

	if err := LoadImage(); err != nil {
		return fmt.Errorf("load image: %w", err)
	}

	// Update marker.
	os.MkdirAll(configDir, 0o755)
	os.WriteFile(markerPath, []byte(tarball), 0o644)
	return nil
}

// RunOpts holds options for running a Claude Code container.
type RunOpts struct {
	Name            string
	Workspace       string
	ConfigDir       string
	HostClaudeDir   string   // host ~/.claude/ dir, mounted read-only for credential copying
	HostClaudeJSON  string   // host ~/.claude.json file, mounted read-only for onboarding/auth state
	UID             int
	GID             int
	Yolo            bool
	Prompt          string
	Continue        bool
	Resume          string   // claude --resume <id>
	ExtraWorkspaces []string // additional folders mounted as /workspace/<basename>
	ProxyProfile    string   // proxy profile name (for network/env args)
	ProxyCACertDir  string   // path to mitmproxy CA cert directory
}

// RunArgs returns the docker run command arguments for a persistent
// Claude Code container. The container is NOT created with --rm so it
// can be reattached later.
func RunArgs(opts RunOpts, detached bool) []string {
	name := ContainerName(opts.Name)

	flag := "-it"
	if detached {
		flag = "-dit"
	}

	args := []string{
		"run",
		"--name", name,
		flag,
	}

	// Mount primary workspace (if set).
	if opts.Workspace != "" {
		args = append(args, "-v", opts.Workspace+":/workspace")
	}

	// Mount extra workspaces as subdirectories of /workspace.
	for _, ws := range opts.ExtraWorkspaces {
		base := filepath.Base(ws)
		args = append(args, "-v", ws+":/workspace/"+base)
	}

	// When proxy is enabled, add network and proxy env vars.
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
				"-e", "NODE_EXTRA_CA_CERTS=/proxy-ca/mitmproxy-ca-cert.pem",
			)
		}
	}

	args = append(args,
		"-v", opts.ConfigDir+":/claude",
		"-e", "CLAUDE_CONFIG_DIR=/claude",
		"-e", fmt.Sprintf("USER_UID=%d", opts.UID),
		"-e", fmt.Sprintf("USER_GID=%d", opts.GID),
	)

	// Mount host Claude credentials read-only for entrypoint to copy.
	if opts.HostClaudeDir != "" {
		args = append(args, "-v", opts.HostClaudeDir+":/mnt/claude-host:ro")
	}
	if opts.HostClaudeJSON != "" {
		args = append(args, "-v", opts.HostClaudeJSON+":/mnt/claude-host-json:ro")
	}

	args = append(args, ImageTag(), "claude")

	// Append optional claude flags after the base "claude" command.
	if opts.Yolo {
		args = append(args, "--dangerously-skip-permissions")
	}
	if opts.Resume != "" {
		args = append(args, "--resume", opts.Resume)
	} else if opts.Continue {
		args = append(args, "--continue")
	}
	if opts.Prompt != "" {
		args = append(args, opts.Prompt)
	}

	return args
}

// TaskRunArgs returns docker run arguments for a non-interactive task container.
// The container runs detached (-d, no TTY) with claude in print mode (-p) and
// stream-json output. The model and maxTurns parameters are passed through to
// Claude CLI when non-empty/non-zero.
func TaskRunArgs(opts RunOpts, model string, maxTurns int) []string {
	name := ContainerName(opts.Name)

	args := []string{
		"run",
		"--name", name,
		"-d",
	}

	if opts.Workspace != "" {
		args = append(args, "-v", opts.Workspace+":/workspace")
	}
	for _, ws := range opts.ExtraWorkspaces {
		base := filepath.Base(ws)
		args = append(args, "-v", ws+":/workspace/"+base)
	}

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
				"-e", "NODE_EXTRA_CA_CERTS=/proxy-ca/mitmproxy-ca-cert.pem",
			)
		}
	}

	args = append(args,
		"-v", opts.ConfigDir+":/claude",
		"-e", "CLAUDE_CONFIG_DIR=/claude",
		"-e", fmt.Sprintf("USER_UID=%d", opts.UID),
		"-e", fmt.Sprintf("USER_GID=%d", opts.GID),
	)

	if opts.HostClaudeDir != "" {
		args = append(args, "-v", opts.HostClaudeDir+":/mnt/claude-host:ro")
	}
	if opts.HostClaudeJSON != "" {
		args = append(args, "-v", opts.HostClaudeJSON+":/mnt/claude-host-json:ro")
	}

	args = append(args, ImageTag(), "claude",
		"-p",
		"--output-format", "stream-json",
		"--dangerously-skip-permissions",
	)

	if model != "" {
		args = append(args, "--model", model)
	}
	if maxTurns > 0 {
		args = append(args, "--max-turns", fmt.Sprintf("%d", maxTurns))
	}
	if opts.Prompt != "" {
		args = append(args, opts.Prompt)
	}

	return args
}

// ShellArgs returns the docker run command arguments for an ephemeral
// debug shell. Unlike RunArgs the container IS created with --rm.
func ShellArgs(workspace, configDir, hostClaudeDir, hostClaudeJSON string, uid, gid int) []string {
	args := []string{
		"run",
		"--rm",
		"-it",
		"-v", workspace + ":/workspace",
		"-v", configDir + ":/claude",
		"-e", "CLAUDE_CONFIG_DIR=/claude",
		"-e", fmt.Sprintf("USER_UID=%d", uid),
		"-e", fmt.Sprintf("USER_GID=%d", gid),
	}
	if hostClaudeDir != "" {
		args = append(args, "-v", hostClaudeDir+":/mnt/claude-host:ro")
	}
	if hostClaudeJSON != "" {
		args = append(args, "-v", hostClaudeJSON+":/mnt/claude-host-json:ro")
	}
	args = append(args, ImageTag(), "/bin/bash")
	return args
}

// RunDetached executes docker run in detached mode. Stdout/stderr are
// connected so the user sees the container ID output.
func RunDetached(args []string) error {
	cmd := exec.Command("docker", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// ImageExists returns true if the Claude Code Docker image has been built.
func ImageExists() bool {
	cmd := exec.Command("docker", "image", "inspect", ImageTag())
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// IsRunning returns true if the container for the given session is currently
// running.
func IsRunning(session string) bool {
	name := ContainerName(session)
	cmd := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", name)
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

// Exists returns true if a container for the given session exists
// (running or stopped).
func Exists(session string) bool {
	name := ContainerName(session)
	cmd := exec.Command("docker", "inspect", name)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// Stop stops the container for the given session.
func Stop(session string) error {
	name := ContainerName(session)
	cmd := exec.Command("docker", "stop", name)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Remove removes the container for the given session.
func Remove(session string) error {
	name := ContainerName(session)
	cmd := exec.Command("docker", "rm", name)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Start restarts a stopped container for the given session (detached).
func Start(session string) error {
	name := ContainerName(session)
	cmd := exec.Command("docker", "start", name)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Logs returns a prepared *exec.Cmd that streams container logs. If
// follow is true the --follow flag is included. The caller is
// responsible for connecting IO and running the command.
func Logs(ctx context.Context, session string, follow bool) *exec.Cmd {
	name := ContainerName(session)
	args := []string{"logs"}
	if follow {
		args = append(args, "--follow")
	}
	args = append(args, name)
	return exec.CommandContext(ctx, "docker", args...)
}

// ansiRe matches ANSI escape sequences (CSI, OSC, and single-character escapes).
var ansiRe = regexp.MustCompile(`\x1b(?:\[[0-9;?]*[a-zA-Z]|\][^\x07]*\x07|\([A-Z])`)

// resumeRe matches "claude --resume <uuid>" in container output.
var resumeRe = regexp.MustCompile(`claude\s+--resume\s+([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`)

// ParseResumeID extracts the Claude resume session ID from container logs.
// It looks for the last occurrence of "claude --resume <uuid>" in the output.
func ParseResumeID(session string) string {
	output, err := LogsTail(session, 50)
	if err != nil {
		return ""
	}
	matches := resumeRe.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return ""
	}
	// Return the last match (most recent resume ID).
	return matches[len(matches)-1][1]
}

// StripANSI removes ANSI escape sequences from s.
func StripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

// LogsTail returns the last n lines of container logs with ANSI escape
// sequences stripped (container TUI output contains cursor/color codes
// that corrupt display in the dashboard).
func LogsTail(session string, n int) (string, error) {
	name := ContainerName(session)
	cmd := exec.Command("docker", "logs", "--tail", fmt.Sprintf("%d", n), name)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return StripANSI(buf.String()), nil
}

// ExecGitDiff returns a prepared *exec.Cmd that runs git diff --name-status HEAD
// inside the container. The caller runs it and reads stdout for changed files.
func ExecGitDiff(session string) *exec.Cmd {
	name := ContainerName(session)
	return exec.Command("docker", "exec", name, "git", "diff", "--name-status", "HEAD")
}

// WaitExitCode runs docker wait and returns the container exit code.
func WaitExitCode(session string) (int, error) {
	name := ContainerName(session)
	cmd := exec.Command("docker", "wait", name)
	out, err := cmd.Output()
	if err != nil {
		return 1, err
	}
	code := 0
	fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &code)
	return code, nil
}
