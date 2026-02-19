package docker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ImageName is the Docker image tag used for Claude Code containers.
const ImageName = "claude-code"

// ContainerName returns the Docker container name for the given session.
func ContainerName(session string) string {
	return "claude-container_" + session
}

// RunOpts holds options for running a Claude Code container.
type RunOpts struct {
	Name            string
	Workspace       string
	ConfigDir       string
	CredentialsFile string // host path to .credentials.json (mounted read-only)
	UID             int
	GID             int
	Yolo            bool
	Prompt          string
	Continue        bool
}

// BuildArgs returns the docker build command arguments for the given
// context directory.
func BuildArgs(contextDir string) []string {
	return []string{
		"build",
		"-t", ImageName,
		contextDir,
	}
}

// RunArgs returns the docker run command arguments for a persistent
// Claude Code container. The container is NOT created with --rm so it
// can be reattached later.
func RunArgs(opts RunOpts) []string {
	name := ContainerName(opts.Name)

	args := []string{
		"run",
		"--name", name,
		"-it",
		"-v", opts.Workspace + ":/workspace",
		"-v", opts.ConfigDir + ":/claude",
		"-e", "CLAUDE_CONFIG_DIR=/claude",
		"-e", fmt.Sprintf("USER_UID=%d", opts.UID),
		"-e", fmt.Sprintf("USER_GID=%d", opts.GID),
	}

	// Mount host credentials file at a temp path. The entrypoint copies
	// it to $HOME/.claude/.credentials.json with correct ownership,
	// which handles Docker user namespace remapping.
	if opts.CredentialsFile != "" {
		args = append(args, "-v", opts.CredentialsFile+":/tmp/host-credentials.json:ro")
	}

	args = append(args, ImageName, "claude")

	// Append optional claude flags after the base "claude" command.
	if opts.Yolo {
		args = append(args, "--dangerously-skip-permissions")
	}
	if opts.Continue {
		args = append(args, "--continue")
	}
	if opts.Prompt != "" {
		args = append(args, "-p", opts.Prompt)
	}

	return args
}

// ShellArgs returns the docker run command arguments for an ephemeral
// debug shell. Unlike RunArgs the container IS created with --rm.
func ShellArgs(workspace, configDir string, uid, gid int) []string {
	return []string{
		"run",
		"--rm",
		"-it",
		"-v", workspace + ":/workspace",
		"-v", configDir + ":/claude",
		"-e", "CLAUDE_CONFIG_DIR=/claude",
		"-e", fmt.Sprintf("USER_UID=%d", uid),
		"-e", fmt.Sprintf("USER_GID=%d", gid),
		ImageName,
		"/bin/bash",
	}
}

// ImageExists returns true if the Claude Code Docker image has been built.
func ImageExists() bool {
	cmd := exec.Command("docker", "image", "inspect", ImageName)
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

// Start restarts a stopped container for the given session.
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

// Build returns a prepared *exec.Cmd that builds the Docker image from
// the given context directory. Stdout and stderr are connected to the
// current process so the user sees build output.
func Build(contextDir string) *exec.Cmd {
	args := BuildArgs(contextDir)
	cmd := exec.Command("docker", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}
