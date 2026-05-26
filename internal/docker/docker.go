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
	"sync"
)

var (
	isRootlessOnce sync.Once
	isRootlessVal  bool
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

// LoadImage loads the Docker image from the Nix-built tarball. Silent on
// success (callers are expected to provide their own progress indicator —
// cmd/auth.go shows a spinner during EnsureImage, for example). Docker's
// own "Loaded image: ..." line still goes to stdout.
func LoadImage() error {
	tarball := os.Getenv("CLAUDE_CONTAINER_IMAGE_TARBALL")
	if tarball == "" {
		return fmt.Errorf("CLAUDE_CONTAINER_IMAGE_TARBALL is not set")
	}
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
	HostClaudeFiles []string // individual host credential files, mounted read-only
	UID             int
	GID             int
	Yolo            bool
	AutoMode        bool // claude --enable-auto-mode (mutually exclusive with Yolo)
	Prompt          string
	Continue        bool
	Resume          string   // claude --resume <id>
	ExtraWorkspaces []string // additional folders mounted as /workspace/<basename>
	ProxyCACertDir     string // path to mitmproxy CA cert directory
	ProxyDashboardPort int    // host-side dashboard port, surfaced to hooks so they can tell the user where to open the browser
	RepoPath        string   // host repo path, mounted at /mnt/repo for worktree creation
	WorktreeBranch  string   // branch name — entrypoint creates worktree at /workspace
	WorktreeFrom    string   // optional base branch for worktree
	WorktreeRepos   []string // extra git repos: mounted at /mnt/repos/<basename>, worktrees at /workspace/<basename>
	Packages        []string // extra nixpkgs to install at container start
	// ProxyEnabled is true when this container should join the per-session
	// proxy's network namespace. The proxy container is named after the
	// session (opts.Name), not after a profile.
	ProxyEnabled bool
	// Mode is one of "tty", "task", "background". Passed to the
	// container as CLAUDE_CONTAINER_MODE. Empty defaults to "tty".
	Mode string
}

// resourceLimitArgs returns docker --pids-limit / --memory / --cpus
// arguments so the Claude container can't fork-bomb, OOM, or peg the
// host CPU. Defaults are generous for normal use but bounded; override
// via env vars for unusual workloads.
//
//	CLAUDE_CONTAINER_PIDS_LIMIT   (default 4096)
//	CLAUDE_CONTAINER_MEMORY       (default 8g)
//	CLAUDE_CONTAINER_CPUS         (default 4.0)
//
// Setting any of these to "0" or "unlimited" removes that specific limit
// (useful for power users who explicitly opt out).
func resourceLimitArgs() []string {
	pids := envOr("CLAUDE_CONTAINER_PIDS_LIMIT", "4096")
	mem := envOr("CLAUDE_CONTAINER_MEMORY", "8g")
	cpus := envOr("CLAUDE_CONTAINER_CPUS", "4.0")
	var args []string
	if pids != "0" && pids != "unlimited" {
		args = append(args, "--pids-limit", pids)
	}
	if mem != "0" && mem != "unlimited" {
		// memory-swap == memory means no swap — the container can't
		// trade RAM pressure for slow disk-backed OOM avoidance.
		args = append(args, "--memory", mem, "--memory-swap", mem)
	}
	if cpus != "0" && cpus != "unlimited" {
		args = append(args, "--cpus", cpus)
	}
	return args
}

// securityHardeningArgs returns docker --security-opt and --cap-drop
// flags that strengthen the container's confinement beyond docker's
// defaults.
//
//   - no-new-privileges:true  — prevents setuid/setgid escalation
//     inside the container via execve of suid binaries.
//
//   - --cap-drop SETUID and --cap-drop SETGID (rootless mode only) —
//     closes the setuid-bypass-of-netfilter-skuid-exemption attack
//     (audit-2026-05-23 §Finding 1). The proxy container's nftables
//     ruleset has `meta skuid 1500 accept` rules that exempt
//     mitmproxy from filtering. Without these cap drops, a process
//     in the Claude container running as container uid 0 could call
//     setuid(1500) and inherit the exemption — every outbound TCP
//     and UDP packet would skip mitmproxy and udp-redir. Dropping
//     CAP_SETUID/CAP_SETGID makes that syscall fail with EPERM.
//
//     We only apply the drop in rootless mode because non-rootless
//     mode uses `su-exec` in the entrypoint to switch from container
//     uid 0 to USER_UID — su-exec calls setuid() and needs the cap.
//     Rootless mode runs the entire container as container uid 0 = the
//     host user, so su-exec is never invoked.
//
// Docker's default seccomp + AppArmor profiles still apply on top of
// these.
func securityHardeningArgs() []string {
	args := []string{
		"--security-opt", "no-new-privileges:true",
	}
	if IsRootless() {
		args = append(args,
			"--cap-drop", "SETUID",
			"--cap-drop", "SETGID",
		)
	}
	return args
}

// managedSettingsROMount returns a -v overlay that re-mounts the
// managed-settings.json file read-only on top of the writable /claude
// bind mount. This stops Claude from rewriting its own permission rules
// from inside the container.
//
// Layering: /claude is mounted RW so credentials and conversation state
// can be persisted; the single file managed-settings.json gets a second,
// RO bind mount on top of it. Docker accepts file-level overlays.
func managedSettingsROMount(configDir string) []string {
	if configDir == "" {
		return nil
	}
	src := filepath.Join(configDir, "managed-settings.json")
	return []string{"-v", src + ":/claude/managed-settings.json:ro"}
}

// envOr returns os.Getenv(key) if non-empty, else fallback.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
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

	// Mount primary workspace (if set) — skip when worktree mode is active
	// (the container entrypoint creates /workspace from /mnt/repo).
	if opts.Workspace != "" && opts.WorktreeBranch == "" {
		args = append(args, "-v", opts.Workspace+":/workspace")
	}

	// Worktree env vars (shared by single-repo and multi-repo modes).
	if opts.WorktreeBranch != "" {
		args = append(args, "-e", "WORKTREE_BRANCH="+opts.WorktreeBranch)
		if opts.WorktreeFrom != "" {
			args = append(args, "-e", "WORKTREE_FROM="+opts.WorktreeFrom)
		}
	}

	// Single-repo worktree: mount repo at /mnt/repo → entrypoint creates /workspace.
	if opts.RepoPath != "" && opts.WorktreeBranch != "" {
		args = append(args, "-v", opts.RepoPath+":/mnt/repo")
	}

	// Multi-repo worktrees: mount each at /mnt/repos/<basename> → entrypoint creates /workspace/<basename>.
	if len(opts.WorktreeRepos) > 0 && opts.WorktreeBranch != "" {
		var names []string
		for _, ws := range opts.WorktreeRepos {
			base := filepath.Base(ws)
			args = append(args, "-v", ws+":/mnt/repos/"+base)
			names = append(names, base)
		}
		args = append(args, "-e", "WORKTREE_REPOS="+strings.Join(names, ","))
	}

	// Mount extra workspaces as subdirectories of /workspace.
	for _, ws := range opts.ExtraWorkspaces {
		base := filepath.Base(ws)
		args = append(args, "-v", ws+":/workspace/"+base)
	}

	// When proxy is enabled, add network and proxy env vars.
	if opts.ProxyEnabled {
		// Share the per-session proxy container's network namespace. The proxy
		// entrypoint installs nftables rules in this netns that REDIRECT every
		// TCP connection to mitmproxy's transparent listener and default-deny
		// everything else (UDP, raw sockets, QUIC). The Claude container has
		// no NIC of its own and no way to bypass the firewall. HTTP_PROXY env
		// vars are intentionally not set: transparent mode makes them moot.
		// `--network container:` is mutually exclusive with `--network`/`-p`,
		// which is fine because Claude containers don't publish ports.
		proxyContainer := "claude-proxy_" + opts.Name
		args = append(args,
			"--network", "container:"+proxyContainer,
			// Dashboard lives on loopback inside the shared netns. Mutating
			// endpoints still require an auth token the container does not
			// have, so it cannot approve its own held flows.
			"-e", "CLAUDE_PROXY_DASHBOARD_URL=http://127.0.0.1:8081",
		)
		if opts.ProxyDashboardPort > 0 {
			// Tell hooks where the user should open their browser. This is
			// the host-side localhost URL, not the container-network URL.
			args = append(args,
				"-e", fmt.Sprintf("CLAUDE_PROXY_DASHBOARD_HOST_URL=http://localhost:%d", opts.ProxyDashboardPort),
			)
		}
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
	// Layer the managed-settings.json file on top of the /claude mount
	// as read-only so Claude cannot rewrite its own permission rules.
	args = append(args, managedSettingsROMount(opts.ConfigDir)...)

	// Resource limits — prevent fork bombs, memory bombs, CPU pegging.
	args = append(args, resourceLimitArgs()...)
	// Security hardening — no-new-privileges, etc.
	args = append(args, securityHardeningArgs()...)

	mode := opts.Mode
	if mode == "" {
		mode = "tty"
	}
	args = append(args, "-e", "CLAUDE_CONTAINER_MODE="+mode)

	// Persistent Nix store for runtime package installs.
	args = append(args, "-v", "claude-nix-store:/nix/var")

	if len(opts.Packages) > 0 {
		args = append(args, "-e", "EXTRA_PACKAGES="+strings.Join(opts.Packages, ","))
	}

	// Mount individual host credential files read-only for entrypoint to copy.
	for _, f := range opts.HostClaudeFiles {
		args = append(args, "-v", f+":/mnt/claude-host/"+filepath.Base(f)+":ro")
	}

	args = append(args, ImageTag(), "claude")

	// Append optional claude flags after the base "claude" command.
	// In rootless Docker the container runs as UID 0 and Claude Code refuses
	// --dangerously-skip-permissions as root. Managed settings (always written
	// by the Go binary) handle permission control instead.
	if opts.Yolo && !IsRootless() {
		args = append(args, "--dangerously-skip-permissions")
	}
	// Auto mode is a separate UX mode where Claude classifies each tool
	// call for risk and auto-approves safe ones. Mutually exclusive with
	// Yolo at the profile level (`auto` profile sets AutoMode=true and
	// Yolo=false). Managed-settings also sets defaultMode=auto +
	// skipAutoPermissionPrompt=true so the first-launch dialog is pre-
	// answered, but passing the flag is belt-and-suspenders for older
	// claude-code versions that prefer CLI to settings.
	if opts.AutoMode {
		args = append(args, "--enable-auto-mode")
	}
	if opts.Resume == "__picker__" {
		args = append(args, "--resume")
	} else if opts.Resume != "" {
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

	if opts.Workspace != "" && opts.WorktreeBranch == "" {
		args = append(args, "-v", opts.Workspace+":/workspace")
	}

	// Worktree env vars (shared by single-repo and multi-repo modes).
	if opts.WorktreeBranch != "" {
		args = append(args, "-e", "WORKTREE_BRANCH="+opts.WorktreeBranch)
		if opts.WorktreeFrom != "" {
			args = append(args, "-e", "WORKTREE_FROM="+opts.WorktreeFrom)
		}
	}

	// Single-repo worktree: mount repo at /mnt/repo.
	if opts.RepoPath != "" && opts.WorktreeBranch != "" {
		args = append(args, "-v", opts.RepoPath+":/mnt/repo")
	}

	// Multi-repo worktrees: mount each at /mnt/repos/<basename>.
	if len(opts.WorktreeRepos) > 0 && opts.WorktreeBranch != "" {
		var names []string
		for _, ws := range opts.WorktreeRepos {
			base := filepath.Base(ws)
			args = append(args, "-v", ws+":/mnt/repos/"+base)
			names = append(names, base)
		}
		args = append(args, "-e", "WORKTREE_REPOS="+strings.Join(names, ","))
	}

	for _, ws := range opts.ExtraWorkspaces {
		base := filepath.Base(ws)
		args = append(args, "-v", ws+":/workspace/"+base)
	}

	if opts.ProxyEnabled {
		// Share the per-session proxy container's network namespace. The proxy
		// entrypoint installs nftables rules in this netns that REDIRECT every
		// TCP connection to mitmproxy's transparent listener and default-deny
		// everything else (UDP, raw sockets, QUIC). The Claude container has
		// no NIC of its own and no way to bypass the firewall. HTTP_PROXY env
		// vars are intentionally not set: transparent mode makes them moot.
		// `--network container:` is mutually exclusive with `--network`/`-p`,
		// which is fine because Claude containers don't publish ports.
		proxyContainer := "claude-proxy_" + opts.Name
		args = append(args,
			"--network", "container:"+proxyContainer,
			// Dashboard lives on loopback inside the shared netns. Mutating
			// endpoints still require an auth token the container does not
			// have, so it cannot approve its own held flows.
			"-e", "CLAUDE_PROXY_DASHBOARD_URL=http://127.0.0.1:8081",
		)
		if opts.ProxyDashboardPort > 0 {
			// Tell hooks where the user should open their browser. This is
			// the host-side localhost URL, not the container-network URL.
			args = append(args,
				"-e", fmt.Sprintf("CLAUDE_PROXY_DASHBOARD_HOST_URL=http://localhost:%d", opts.ProxyDashboardPort),
			)
		}
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
	// Read-only overlay of managed-settings.json (GAP-2 mitigation).
	args = append(args, managedSettingsROMount(opts.ConfigDir)...)

	// Resource limits (GAP-3 mitigation).
	args = append(args, resourceLimitArgs()...)
	// Security hardening (no-new-privileges, etc.).
	args = append(args, securityHardeningArgs()...)

	mode := opts.Mode
	if mode == "" {
		mode = "task"
	}
	args = append(args, "-e", "CLAUDE_CONTAINER_MODE="+mode)

	// Persistent Nix store for runtime package installs.
	args = append(args, "-v", "claude-nix-store:/nix/var")

	if len(opts.Packages) > 0 {
		args = append(args, "-e", "EXTRA_PACKAGES="+strings.Join(opts.Packages, ","))
	}

	// Mount individual host credential files read-only for entrypoint to copy.
	for _, f := range opts.HostClaudeFiles {
		args = append(args, "-v", f+":/mnt/claude-host/"+filepath.Base(f)+":ro")
	}

	args = append(args, ImageTag(), "claude",
		"-p",
		"--output-format", "stream-json",
		"--verbose",
	)

	// In rootless Docker the container runs as UID 0 and Claude Code refuses
	// --dangerously-skip-permissions as root. Managed settings handle permissions.
	if !IsRootless() {
		args = append(args, "--dangerously-skip-permissions")
	}

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
func ShellArgs(workspace, configDir string, hostClaudeFiles []string, uid, gid int) []string {
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
	args = append(args, managedSettingsROMount(configDir)...)
	args = append(args, resourceLimitArgs()...)
	args = append(args, securityHardeningArgs()...)
	for _, f := range hostClaudeFiles {
		args = append(args, "-v", f+":/mnt/claude-host/"+filepath.Base(f)+":ro")
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

// IsRootless returns true if the Docker daemon is running in rootless mode.
// In rootless mode, UID mapping is different: host UID maps to container UID 0.
// The result is cached after the first call.
func IsRootless() bool {
	isRootlessOnce.Do(func() {
		cmd := exec.Command("docker", "info", "-f", "{{.SecurityOptions}}")
		out, err := cmd.Output()
		if err == nil {
			isRootlessVal = strings.Contains(string(out), "rootless")
		}
	})
	return isRootlessVal
}

// ContainerUID returns the UID to use inside the container. In rootless Docker,
// returns 0 because host UID maps to container root. Otherwise returns the
// caller's UID.
func ContainerUID() int {
	if IsRootless() {
		return 0
	}
	return os.Getuid()
}

// ContainerGID returns the GID to use inside the container. In rootless Docker,
// returns 0 because host GID maps to container root group. Otherwise returns
// the caller's GID.
func ContainerGID() int {
	if IsRootless() {
		return 0
	}
	return os.Getgid()
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
	cmd.Stdout = nil
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Remove removes the container for the given session.
func Remove(session string) error {
	name := ContainerName(session)
	cmd := exec.Command("docker", "rm", name)
	cmd.Stdout = nil
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

// refreshAuthScript is the shell snippet that copies host credentials
// from the read-only mounts into /claude/. Mirrors the entrypoint logic
// in nix/image.nix.
const refreshAuthScript = `
for f in .credentials.json settings.json .claude.json; do
  if [ -f "/mnt/claude-host/$f" ]; then
    cp -L "/mnt/claude-host/$f" "/claude/$f" && chmod 600 "/claude/$f"
  fi
done`

// RefreshAuthCmd returns a prepared *exec.Cmd that re-copies host
// credentials from the read-only mounts into /claude/ inside the
// container. The caller is responsible for running the command.
func RefreshAuthCmd(session string) *exec.Cmd {
	name := ContainerName(session)
	return exec.Command("docker", "exec", name, "sh", "-c", refreshAuthScript)
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
