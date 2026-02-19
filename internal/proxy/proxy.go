// Package proxy provides a PTY proxy between the host terminal and a Docker
// subprocess. It intercepts a configurable prefix key (Ctrl+B) to provide
// detach/quit functionality and shows session info in the terminal title.
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

// StatusBarInfo holds the data rendered in the terminal title.
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
// and the container, providing prefix key interception and session info in
// the terminal title. The terminal's native scrollback buffer is preserved.
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
	// Give the container the full terminal height — no row reservation.
	cmd := exec.Command("docker", opts.DockerArgs...)

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{
		Rows: uint16(height),
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

	// Show session info in the terminal title.
	setTitle(os.Stdout, opts.StatusBar, false)

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
	// We read stdin in a goroutine and send bytes on a channel so the
	// main input loop can also select on a timeout channel (avoiding
	// races between timer callbacks and the read loop).
	stdinCh := make(chan byte, 64)
	wg.Add(1)
	go func() {
		defer wg.Done()
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
						setTitle(os.Stdout, opts.StatusBar, true)
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
					setTitle(os.Stdout, opts.StatusBar, false)
				case b := <-stdinCh:
					state = stateNormal
					setTitle(os.Stdout, opts.StatusBar, false)

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
						ptmx.Write([]byte{prefixKey})
					default:
						// Ignore unknown prefix command.
					}
				}
			}
		}
	}()

	// SIGWINCH handler: resize PTY on terminal resize.
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
					pty.Setsize(ptmx, &pty.Winsize{
						Rows: uint16(height),
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
				// Stop the container gracefully.
				stopCmd := exec.Command("docker", "stop", "-t", "5", opts.ContainerName)
				stopCmd.Run()
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

	// Restore terminal.
	fmt.Fprint(os.Stdout, "\033]0;\007")   // reset terminal title
	fmt.Fprint(os.Stdout, "\033[?25h")     // ensure cursor is visible
	term.Restore(stdinFd, oldState)
	fmt.Fprint(os.Stderr, "\r\n") // clean line for shell prompt

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

// setTitle sets the terminal title with session info and optional prefix hints.
func setTitle(w io.Writer, info StatusBarInfo, prefixActive bool) {
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
		hint = "^B for options"
	}

	title := strings.Join(parts, " | ")
	if hint != "" {
		if title != "" {
			title += " | "
		}
		title += hint
	}

	fmt.Fprintf(w, "\033]0;%s\007", title)
}
