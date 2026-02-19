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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

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
	DockerArgs []string
	StatusBar  StatusBarInfo
	AutoRemove bool
	Cleanup    CleanupFunc
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
const prefixTimeout = 200 * time.Millisecond

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

	// Clear screen, set up scroll region, and render initial status bar.
	fmt.Fprint(os.Stdout, "\033[2J\033[H") // clear screen + cursor home
	setScrollRegion(height)
	renderStatusBar(os.Stdout, width, height, opts.StatusBar, false)

	// done channel signals goroutines to exit.
	done := make(chan struct{})
	var wg sync.WaitGroup

	var detached bool
	var quit bool

	// stdout proxy: docker pty -> host stdout.
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			select {
			case <-done:
				return
			default:
			}
			n, err := ptmx.Read(buf)
			if n > 0 {
				os.Stdout.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()

	// stdin proxy: host stdin -> docker pty, with prefix key interception.
	wg.Add(1)
	go func() {
		defer wg.Done()

		state := stateNormal
		var prefixTimer *time.Timer
		buf := make([]byte, 1)

		for {
			select {
			case <-done:
				return
			default:
			}

			n, err := os.Stdin.Read(buf)
			if err != nil {
				return
			}
			if n == 0 {
				continue
			}

			b := buf[0]

			switch state {
			case stateNormal:
				if b == prefixKey {
					state = statePrefixWait
					// Show prefix-active status bar.
					renderStatusBar(os.Stdout, width, height, opts.StatusBar, true)
					// Start timeout.
					prefixTimer = time.AfterFunc(prefixTimeout, func() {
						state = stateNormal
						renderStatusBar(os.Stdout, width, height, opts.StatusBar, false)
					})
				} else {
					ptmx.Write([]byte{b})
				}

			case statePrefixWait:
				if prefixTimer != nil {
					prefixTimer.Stop()
				}
				state = stateNormal
				renderStatusBar(os.Stdout, width, height, opts.StatusBar, false)

				switch b {
				case 'd':
					detached = true
					close(done)
					return
				case 'q':
					quit = true
					close(done)
					return
				case prefixKey:
					// Forward literal Ctrl+B.
					ptmx.Write([]byte{prefixKey})
				default:
					// Ignore unknown prefix command.
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
					setScrollRegion(height)
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
		if quit {
			// Send SIGTERM to the docker subprocess, then wait for it.
			if cmd.Process != nil {
				cmd.Process.Signal(syscall.SIGTERM)
			}
			// Wait for process to exit (with a timeout to force kill).
			select {
			case cmdErr = <-cmdDone:
			case <-time.After(5 * time.Second):
				if cmd.Process != nil {
					cmd.Process.Kill()
				}
				cmdErr = <-cmdDone
			}
		} else {
			// Detached: close PTY master so the container keeps running.
			ptmx.Close()
			// Don't wait for docker to exit; just proceed with cleanup.
		}
	}

	// Wait for goroutines to finish.
	signal.Stop(sigwinch)
	wg.Wait()

	// Restore terminal.
	clearScrollRegion()
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

	// Save cursor, move to status row, clear line, draw bar in inverse video, restore cursor.
	fmt.Fprintf(w, "\033[s\033[%d;1H\033[2K\033[7m%s\033[0m\033[u", height, bar)
}
