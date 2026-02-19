// Package proxy provides a PTY proxy between the host terminal and a Docker
// subprocess. It intercepts a configurable prefix key (Ctrl+B) to provide
// detach/quit functionality and renders a persistent status bar on the last
// terminal row.
package proxy

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

// ansiRe matches ANSI escape sequences (CSI, OSC, and simple escapes).
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]|\x1b\][^\x07]*\x07|\x1b[^[\]]`)

// stripANSI removes ANSI escape sequences from b.
func stripANSI(b []byte) []byte {
	return ansiRe.ReplaceAll(b, nil)
}

// StatusBarInfo holds the data rendered in the status bar.
type StatusBarInfo struct {
	Name   string
	Branch string
	Yolo   bool
}

// CleanupFunc is called after the container exits to perform resource cleanup
// (e.g. removing containers/volumes). It receives the container name.
type CleanupFunc func(name string)

// Opts configures a proxy session.
type Opts struct {
	DockerArgs    []string
	ContainerName string // Docker container name; when set, detach keeps container alive
	StatusBar     StatusBarInfo
	AutoRemove    bool
	Cleanup       CleanupFunc
}

// inputState tracks the prefix key state machine.
type inputState int

const (
	stateNormal     inputState = 0
	statePrefixWait inputState = 1
)

// prefixKey is Ctrl+B (0x02).
const prefixKey byte = 0x02

// prefixTimeout is how long we wait for a command key after Ctrl+B.
const prefixTimeout = 2 * time.Second

// Run starts a Docker subprocess and proxies I/O between the host terminal
// and the container, providing prefix key interception and a status bar.
func Run(opts Opts) error {
	if len(opts.DockerArgs) == 0 {
		return errors.New("proxy: DockerArgs must not be empty")
	}

	stdinFd := int(os.Stdin.Fd())

	// Save and restore terminal state.
	oldState, err := term.MakeRaw(stdinFd)
	if err != nil {
		return fmt.Errorf("proxy: failed to set raw mode: %w", err)
	}
	defer term.Restore(stdinFd, oldState)

	// Get terminal dimensions.
	width, height, err := term.GetSize(stdinFd)
	if err != nil {
		// Fallback to reasonable defaults if not a real terminal (e.g. tests).
		width, height = 80, 24
	}

	// Start docker subprocess with a real PTY so Docker's -it flag works.
	cmd := exec.Command("docker", opts.DockerArgs...)

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{
		Rows: uint16(height - 1), // reserve one row for status bar
		Cols: uint16(width),
	})
	if err != nil {
		return fmt.Errorf("proxy: failed to start docker with pty: %w", err)
	}
	defer ptmx.Close()

	// Put the PTY into raw mode to disable echo and line processing.
	// Without this, input written to the master gets echoed back and
	// mixes with the container's actual output.
	if _, err := term.MakeRaw(int(ptmx.Fd())); err != nil {
		return fmt.Errorf("proxy: failed to set pty raw mode: %w", err)
	}

	// Clear screen and render initial status bar (which also sets scroll region).
	fmt.Fprint(os.Stdout, "\033[2J\033[H") // clear screen + cursor home
	renderStatusBar(os.Stdout, width, height, opts.StatusBar, false)

	// done channel signals goroutines to exit.
	done := make(chan struct{})
	var wg sync.WaitGroup

	var detached bool
	var quit bool

	// stdout proxy: docker pty -> host stdout.
	// Also watches for Claude Code's workspace trust prompt and auto-accepts it.
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 4096)

		// Rolling buffer to detect the workspace trust prompt across
		// read boundaries. We only need enough to match the needle.
		// Only active during the first 5 seconds of the session to
		// avoid accidental triggers during normal operation.
		const needle = "Yes, I trust this folder"
		var ring [256]byte
		var ringLen int
		scanning := true
		scanDeadline := time.After(5 * time.Second)

		for {
			select {
			case <-done:
				return
			default:
			}
			n, err := ptmx.Read(buf)
			if n > 0 {
				os.Stdout.Write(buf[:n])

				// Check for workspace trust prompt during startup window.
				if scanning {
					select {
					case <-scanDeadline:
						scanning = false
					default:
					}
				}
				if scanning {
					// Append new data to ring buffer.
					for i := 0; i < n; i++ {
						if ringLen < len(ring) {
							ring[ringLen] = buf[i]
							ringLen++
						} else {
							copy(ring[:], ring[1:])
							ring[len(ring)-1] = buf[i]
						}
					}
					// Strip ANSI escape sequences for matching.
					clean := stripANSI(ring[:ringLen])
					if strings.Contains(string(clean), needle) {
						scanning = false
						ptmx.Write([]byte("\r"))
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// Periodic status bar refresh: Claude Code's TUI (bubbletea) switches
	// to the alternate screen buffer on startup, which resets the scroll
	// region and overwrites the status bar. A ticker re-renders it so the
	// bar stays visible regardless of what the inner program does.
	// Wait briefly before starting to avoid fighting with the container's
	// initial screen setup.
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case <-done:
			return
		case <-time.After(2 * time.Second):
		}
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				renderStatusBar(os.Stdout, width, height, opts.StatusBar, false)
			}
		}
	}()

	// stdin proxy: host stdin -> docker pty, with prefix key interception.
	// We read stdin in a goroutine and send bytes on a channel so the
	// main input loop can also select on a timeout channel (avoiding
	// races between timer callbacks and the read loop).
	stdinCh := make(chan byte, 64)

	// NOTE: the stdin reader is deliberately NOT in the WaitGroup.
	// os.Stdin.Read() blocks until the user types something, and there's
	// no portable way to interrupt it. Excluding it from wg prevents
	// the cleanup path from hanging until the user presses a key.
	go func() {
		buf := make([]byte, 1)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				return
			}
			if n > 0 {
				select {
				case stdinCh <- buf[0]:
				case <-done:
					return
				}
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()

		state := stateNormal
		var timeout <-chan time.Time

		for {
			switch state {
			case stateNormal:
				select {
				case <-done:
					return
				case b := <-stdinCh:
					if b == prefixKey {
						state = statePrefixWait
						timeout = time.After(prefixTimeout)
						renderStatusBar(os.Stdout, width, height, opts.StatusBar, true)
					} else {
						ptmx.Write([]byte{b})
					}
				}

			case statePrefixWait:
				select {
				case <-done:
					return
				case <-timeout:
					state = stateNormal
					renderStatusBar(os.Stdout, width, height, opts.StatusBar, false)
				case b := <-stdinCh:
					state = stateNormal
					renderStatusBar(os.Stdout, width, height, opts.StatusBar, false)

					switch b {
					case 'd':
						detached = true
						renderOverlay(os.Stdout, width, height, "Detaching...")
						close(done)
						return
					case 'q':
						quit = true
						renderOverlay(os.Stdout, width, height, "Stopping container...")
						close(done)
						return
					case prefixKey:
						ptmx.Write([]byte{prefixKey})
					default:
						// Ignore unknown prefix command.
					}
				}
			}
		}
	}()

	// SIGWINCH handler: update scroll region, status bar, and PTY size on resize.
	sigwinch := make(chan os.Signal, 1)
	signal.Notify(sigwinch, syscall.SIGWINCH)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			case <-sigwinch:
				w, h, err := term.GetSize(stdinFd)
				if err == nil {
					width = w
					height = h
					renderStatusBar(os.Stdout, width, height, opts.StatusBar, false)
					// Resize the PTY so the container sees the new dimensions.
					pty.Setsize(ptmx, &pty.Winsize{
						Rows: uint16(height - 1),
						Cols: uint16(width),
					})
				}
			}
		}
	}()

	// Wait for docker to exit in a goroutine so we can also react to
	// user-initiated detach/quit via the done channel.
	cmdDone := make(chan error, 1)
	go func() {
		cmdDone <- cmd.Wait()
	}()

	// Block until either docker exits or user triggers detach/quit.
	var cmdErr error
	select {
	case cmdErr = <-cmdDone:
		// Docker exited on its own. Signal goroutines to stop.
		select {
		case <-done:
		default:
			close(done)
		}
	case <-done:
		// User triggered detach or quit.
		if opts.ContainerName != "" {
			// Container was started separately (detached); we're just
			// running "docker attach". Kill the attach process instantly
			// so it can't forward signals to the container.
			if cmd.Process != nil {
				cmd.Process.Kill()
			}
			cmdErr = <-cmdDone

			if quit {
				// Stop the container gracefully, then remove it.
				stopCmd := exec.Command("docker", "stop", "-t", "5", opts.ContainerName)
				stopCmd.Run()
				exec.Command("docker", "rm", opts.ContainerName).Run()
			}
		} else {
			// Direct docker run mode (e.g. shell). The docker process
			// IS the container lifecycle.
			if quit {
				if cmd.Process != nil {
					cmd.Process.Signal(syscall.SIGTERM)
				}
				select {
				case cmdErr = <-cmdDone:
				case <-time.After(5 * time.Second):
					if cmd.Process != nil {
						cmd.Process.Kill()
					}
					cmdErr = <-cmdDone
				}
			} else {
				ptmx.Close()
			}
		}
	}

	// Wait for goroutines to finish.
	signal.Stop(sigwinch)
	wg.Wait()

	// Restore terminal: reset scroll region, switch back from alternate
	// screen buffer if active, clear the screen, and show cursor.
	clearScrollRegion()
	fmt.Fprint(os.Stdout, "\033[?1049l") // exit alternate screen buffer
	fmt.Fprint(os.Stdout, "\033[2J\033[H") // clear screen + cursor home
	fmt.Fprint(os.Stdout, "\033[?25h")     // ensure cursor is visible
	term.Restore(stdinFd, oldState)

	// Post-exit cleanup.
	containerExitedNormally := !detached && !quit
	if quit && opts.AutoRemove && opts.Cleanup != nil {
		opts.Cleanup(opts.StatusBar.Name)
	} else if containerExitedNormally && opts.AutoRemove && opts.Cleanup != nil {
		opts.Cleanup(opts.StatusBar.Name)
	}

	if detached {
		// Print outside raw mode.
		fmt.Fprintf(os.Stderr, "Detached from %s\n", opts.StatusBar.Name)
	}

	// Return the docker exit error if any (ignore if we caused the exit).
	if cmdErr != nil && !detached && !quit {
		return cmdErr
	}
	return nil
}

// setScrollRegion sets the ANSI scroll region to rows 1 through height-1,
// reserving the last row for the status bar.
func setScrollRegion(height int) {
	if height < 2 {
		return
	}
	fmt.Fprintf(os.Stdout, "\033[1;%dr", height-1)
}

// clearScrollRegion resets the scroll region to the full terminal.
func clearScrollRegion() {
	fmt.Fprint(os.Stdout, "\033[r")
}

// renderStatusBar draws the status bar on the last row of the terminal.
// If prefixActive is true, it shows the prefix key command hints.
func renderStatusBar(w io.Writer, width, height int, info StatusBarInfo, prefixActive bool) {
	if height < 2 || width < 1 {
		return
	}

	// Build status bar content.
	var parts []string
	if info.Name != "" {
		parts = append(parts, info.Name)
	}
	if info.Branch != "" {
		parts = append(parts, info.Branch)
	}
	if info.Yolo {
		parts = append(parts, "yolo")
	}

	var hint string
	if prefixActive {
		hint = "d:detach  q:quit  ^B:literal"
	} else {
		hint = "^B d:detach q:quit"
	}

	left := strings.Join(parts, " \u2502 ")
	bar := fmt.Sprintf(" %s \u2502 %s ", left, hint)

	// Pad or truncate to terminal width.
	barRunes := []rune(bar)
	if len(barRunes) < width {
		bar = bar + strings.Repeat(" ", width-len(barRunes))
	} else if len(barRunes) > width {
		bar = string(barRunes[:width])
	}

	// Save cursor, set scroll region (rows 1..height-1), move to status row,
	// clear line, draw bar in inverse video, restore cursor. Setting the scroll
	// region here (inside save/restore) avoids the cursor-to-origin side effect
	// that causes flashing during resize.
	fmt.Fprintf(w, "\033[s\033[1;%dr\033[%d;1H\033[2K\033[7m%s\033[0m\033[u", height-1, height, bar)
}

// renderOverlay clears the scroll region and shows a centered message,
// used to provide feedback during quit/detach operations.
func renderOverlay(w io.Writer, width, height int, msg string) {
	if height < 2 || width < 1 {
		return
	}
	// Clear the scroll region.
	fmt.Fprint(w, "\033[2J\033[H")
	// Center the message vertically and horizontally.
	row := height / 2
	col := (width - len(msg)) / 2
	if col < 1 {
		col = 1
	}
	fmt.Fprintf(w, "\033[%d;%dH%s", row, col, msg)
}
