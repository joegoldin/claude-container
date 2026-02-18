package internal

import (
	"context"
	"io"
	"os/exec"
	"strings"
)

// Commander abstracts exec.Command so callers can be tested with fakes.
type Commander interface {
	Command(name string, arg ...string) *exec.Cmd
	CommandContext(ctx context.Context, name string, arg ...string) *exec.Cmd
}

// RealCommander delegates to the real os/exec package.
type RealCommander struct{}

func (RealCommander) Command(name string, arg ...string) *exec.Cmd {
	return exec.Command(name, arg...)
}

func (RealCommander) CommandContext(ctx context.Context, name string, arg ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, arg...)
}

// RunCapture executes cmd and returns its combined stdout+stderr as a
// trimmed string.
func RunCapture(cmd *exec.Cmd) (string, error) {
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// RunAttached connects the given readers/writers to cmd's standard IO
// streams and runs the command to completion.
func RunAttached(cmd *exec.Cmd, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}
