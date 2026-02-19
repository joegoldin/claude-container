package tmux

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const prefix = "claude-container_"

// SessionName returns the full tmux session name for the given session.
func SessionName(session string) string {
	return prefix + session
}

// NewSessionArgs returns the tmux new-session arguments for creating a
// detached session with a default shell. The actual command is sent
// separately via send-keys so that interactive programs like
// docker run -it inherit a proper controlling terminal.
func NewSessionArgs(session, workDir string) []string {
	name := SessionName(session)

	return []string{
		"new-session",
		"-d",
		"-s", name,
		"-c", workDir,
	}
}

// configureSession sets tmux options on an existing session: mouse
// support, status bar with session info and detach hint.
func configureSession(session string) {
	name := SessionName(session)
	t := func(args ...string) {
		cmd := exec.Command("tmux", args...)
		cmd.Stdout = nil
		cmd.Stderr = nil
		_ = cmd.Run()
	}

	t("set-option", "-t", name, "mouse", "on")
	t("set-option", "-t", name, "status", "on")
	t("set-option", "-t", name, "status-style", "bg=#3b3b3b,fg=#d4d4d4")
	t("set-option", "-t", name, "status-left", fmt.Sprintf("#[bg=#6a4c93,fg=#ffffff,bold]  %s ", session))
	t("set-option", "-t", name, "status-left-length", "40")
	t("set-option", "-t", name, "status-right", "#[fg=#888888] Ctrl+B,d detach ")
	t("set-option", "-t", name, "status-right-length", "30")
	t("set-option", "-t", name, "status-justify", "left")
	t("set-option", "-t", name, "window-status-format", "")
	t("set-option", "-t", name, "window-status-current-format", "")
}

// Exists returns true if a tmux session with the given name exists.
func Exists(session string) bool {
	name := SessionName(session)
	cmd := exec.Command("tmux", "has-session", "-t", name)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// Kill terminates the tmux session for the given session.
func Kill(session string) error {
	name := SessionName(session)
	cmd := exec.Command("tmux", "kill-session", "-t", name)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}

// Create creates a new detached tmux session with a default shell,
// configures session options (mouse, status bar), then sends the
// command via send-keys so docker gets a proper interactive terminal.
func Create(session, workDir string, command []string) error {
	args := NewSessionArgs(session, workDir)
	cmd := exec.Command("tmux", args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return err
	}
	configureSession(session)

	// Send the command to the shell running in the pane.
	name := SessionName(session)
	shell := shellJoin(command)
	sendCmd := exec.Command("tmux", "send-keys", "-t", name, shell, "Enter")
	sendCmd.Stdout = nil
	sendCmd.Stderr = nil
	return sendCmd.Run()
}

// CapturePaneArgs returns the tmux capture-pane arguments for the given
// session. The -p flag prints to stdout and -e preserves escape sequences.
func CapturePaneArgs(session string) []string {
	name := SessionName(session)
	return []string{
		"capture-pane",
		"-t", name,
		"-p",
		"-e",
	}
}

// CapturePane captures the current visible content of the tmux pane.
func CapturePane(session string) (string, error) {
	args := CapturePaneArgs(session)
	cmd := exec.Command("tmux", args...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("capture pane: %w", err)
	}
	return string(out), nil
}

// Attach attaches to the tmux session with direct terminal access.
// Use the standard tmux detach (Ctrl+B, d) to leave the session.
func Attach(ctx context.Context, session string) error {
	name := SessionName(session)

	// Resize the tmux window to match the current terminal size.
	ws, err := unix.IoctlGetWinsize(int(os.Stdin.Fd()), unix.TIOCGWINSZ)
	if err == nil {
		_ = exec.Command("tmux", "resize-window",
			"-t", name,
			"-x", fmt.Sprintf("%d", ws.Col),
			"-y", fmt.Sprintf("%d", ws.Row)).Run()
	}

	// Attach directly — tmux handles raw mode and terminal management.
	cmd := exec.CommandContext(ctx, "tmux", "attach-session", "-t", name)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// ListSessions returns the names of all tmux sessions that have the
// claude-container_ prefix, with the prefix stripped.
func ListSessions() []string {
	cmd := exec.Command("tmux", "list-sessions", "-F", "#{session_name}")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	var sessions []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.HasPrefix(line, prefix) {
			sessions = append(sessions, strings.TrimPrefix(line, prefix))
		}
	}
	return sessions
}

// IsResponsive returns true if the tmux session exists and responds
// within a short timeout.
func IsResponsive(session string) bool {
	name := SessionName(session)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "tmux", "has-session", "-t", name)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// shellJoin concatenates command arguments into a single shell-safe
// string, quoting arguments that contain special characters.
func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		if arg == "" || strings.ContainsAny(arg, " \t\n\"'\\|&;()<>$`!{}*?[]#~") {
			// Single-quote the argument, escaping embedded single quotes.
			escaped := strings.ReplaceAll(arg, "'", "'\"'\"'")
			quoted[i] = "'" + escaped + "'"
		} else {
			quoted[i] = arg
		}
	}
	return strings.Join(quoted, " ")
}
