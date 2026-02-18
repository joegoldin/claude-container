package tmux

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

const prefix = "claude-container_"

// SessionName returns the full tmux session name for the given session.
func SessionName(session string) string {
	return prefix + session
}

// NewSessionArgs returns the tmux new-session arguments for creating a
// detached session with mouse mode enabled that runs the given command.
func NewSessionArgs(session, workDir string, command []string) []string {
	name := SessionName(session)
	shell := shellJoin(command)

	// We chain two tmux commands via the shell:
	//   1. set-option -g mouse on  (enable mouse support)
	//   2. the actual command
	// tmux new-session runs the shell command in the new session.
	wrapped := fmt.Sprintf("tmux set-option -g mouse on ; %s", shell)

	return []string{
		"new-session",
		"-d",
		"-s", name,
		"-c", workDir,
		"sh", "-c", wrapped,
	}
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

// Create creates a new detached tmux session that runs the given command.
func Create(session, workDir string, command []string) error {
	args := NewSessionArgs(session, workDir, command)
	cmd := exec.Command("tmux", args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
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

// Attach attaches to the tmux session using a PTY for proper terminal
// handling. It intercepts Ctrl+Q (0x11) on stdin to detach cleanly.
func Attach(ctx context.Context, session string) error {
	name := SessionName(session)

	// 1. Resize the tmux window to match the current terminal size.
	ws, err := unix.IoctlGetWinsize(int(os.Stdin.Fd()), unix.TIOCGWINSZ)
	if err == nil {
		resizeCmd := exec.Command("tmux", "resize-window",
			"-t", name,
			"-x", fmt.Sprintf("%d", ws.Col),
			"-y", fmt.Sprintf("%d", ws.Row))
		_ = resizeCmd.Run()
	}

	// 2. Start tmux attach-session via a PTY.
	cmd := exec.CommandContext(ctx, "tmux", "attach-session", "-t", name)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("start pty: %w", err)
	}
	defer ptmx.Close()

	// Apply initial terminal size to the PTY as well.
	if ws != nil {
		_ = pty.Setsize(ptmx, &pty.Winsize{
			Rows: ws.Row,
			Cols: ws.Col,
		})
	}

	// 3. Handle SIGWINCH to resize PTY and tmux on terminal resize.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	defer signal.Stop(sigCh)

	go func() {
		for range sigCh {
			newWs, err := unix.IoctlGetWinsize(int(os.Stdin.Fd()), unix.TIOCGWINSZ)
			if err != nil {
				continue
			}
			_ = pty.Setsize(ptmx, &pty.Winsize{
				Rows: newWs.Row,
				Cols: newWs.Col,
			})
			resizeCmd := exec.Command("tmux", "resize-window",
				"-t", name,
				"-x", fmt.Sprintf("%d", newWs.Col),
				"-y", fmt.Sprintf("%d", newWs.Row))
			_ = resizeCmd.Run()
		}
	}()

	// 4. Set stdin to raw mode so keypresses are forwarded immediately.
	oldState, err := makeRaw(os.Stdin.Fd())
	if err != nil {
		return fmt.Errorf("set raw mode: %w", err)
	}
	defer restore(os.Stdin.Fd(), oldState)

	// 5. Goroutine: copy PTY output to stdout.
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(os.Stdout, ptmx)
		close(done)
	}()

	// 6. Goroutine: read stdin byte-by-byte, intercept Ctrl+Q to detach.
	detach := make(chan struct{})
	go func() {
		buf := make([]byte, 1)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				if buf[0] == 0x11 { // Ctrl+Q
					close(detach)
					return
				}
				_, _ = ptmx.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()

	// 7. Wait for detach, PTY EOF, or context cancellation.
	select {
	case <-detach:
		// User pressed Ctrl+Q -- detach gracefully.
	case <-done:
		// PTY closed (tmux session ended).
	case <-ctx.Done():
		// Context cancelled.
	}

	// 8. Terminal restore and PTY close handled by defers.
	return nil
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
