// internal/session/output_task.go
package session

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// TaskOpts configures WaitTask.
type TaskOpts struct {
	Model    string
	MaxTurns int
}

// TaskResult is the parsed final result of a task run.
type TaskResult struct {
	Text     string // final assistant text (caller parses from Logs)
	ExitCode int
	Logs     string // raw container logs (stream-json)
}

// WaitTask blocks until the detached task container exits and returns the
// container's logs + exit code. The container must have been started with
// task mode (claude -p --output-format stream-json) by Launch.
//
// The caller (cmd/task.go) parses Logs to extract the final assistant text.
func (h *Handle) WaitTask(ctx context.Context, opts TaskOpts) (TaskResult, error) {
	defer h.Cleanup()

	wait := exec.CommandContext(ctx, "docker", "wait", h.Container)
	out, err := wait.Output()
	if err != nil {
		return TaskResult{}, fmt.Errorf("docker wait: %w", err)
	}
	var exitCode int
	if n, _ := fmt.Sscanf(string(out), "%d", &exitCode); n != 1 {
		return TaskResult{}, fmt.Errorf("docker wait: unexpected output %q", strings.TrimSpace(string(out)))
	}

	logs := exec.CommandContext(ctx, "docker", "logs", h.Container)
	logsOut, err := logs.Output()
	if err != nil {
		return TaskResult{ExitCode: exitCode}, fmt.Errorf("docker logs: %w", err)
	}

	return TaskResult{
		Logs:     string(logsOut),
		ExitCode: exitCode,
	}, nil
}
