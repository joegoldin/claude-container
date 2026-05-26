package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/docker"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Authenticate Claude Code inside a container",
	Long: `Log in to Claude Code by running an interactive authentication session inside a container.

If you have already authenticated Claude Code on the host (credentials in ~/.claude/),
those credentials are automatically mounted into containers — no separate auth step needed.

Use 'claude-container auth status' to check authentication state.
Use 'claude-container gc --auth' to remove stored credentials.`,
	RunE: authLoginRun,
}

var authStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check authentication status",
	RunE: func(cmd *cobra.Command, args []string) error {
		store := config.NewStore(config.DefaultDir())
		for _, f := range config.HostClaudeCredentialFiles() {
			if filepath.Base(f) == ".credentials.json" {
				fmt.Printf("Authenticated (host credentials: %s)\n", f)
				return nil
			}
		}
		if store.IsAuthenticated() {
			fmt.Printf("Authenticated (config: %s)\n", store.ClaudeConfigDir())
		} else {
			fmt.Println("Not authenticated. Run 'claude-container auth' to log in.")
		}
		return nil
	},
}

var authRefreshCmd = &cobra.Command{
	Use:   "refresh",
	Short: "Re-copy host credentials into running containers",
	Long:  `Re-copies host credentials from ~/.claude/ into all running containers. Use after re-authenticating on the host.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		store := config.NewStore(config.DefaultDir())

		sessions := store.List()
		if len(sessions) == 0 {
			fmt.Println("No sessions found.")
			return nil
		}

		refreshed := 0
		for _, sess := range sessions {
			if !docker.IsRunning(sess.Name) {
				continue
			}
			c := docker.RefreshAuthCmd(sess.Name)
			c.Stderr = os.Stderr
			if err := c.Run(); err != nil {
				fmt.Printf("  %s: failed (%v)\n", sess.Name, err)
				continue
			}
			fmt.Printf("  %s: refreshed\n", sess.Name)
			refreshed++
		}

		if refreshed == 0 {
			fmt.Println("No running containers found.")
		} else {
			fmt.Printf("Refreshed credentials in %d container(s).\n", refreshed)
		}
		return nil
	},
}

func authLoginRun(cmd *cobra.Command, args []string) error {
	store := config.NewStore(config.DefaultDir())

	// Check if host already has credentials — no container auth needed.
	for _, f := range config.HostClaudeCredentialFiles() {
		if filepath.Base(f) == ".credentials.json" {
			fmt.Printf("Host credentials found at %s\n", f)
			fmt.Println("These are automatically mounted into containers. No auth needed.")
			return nil
		}
	}

	// Ensure the shared config directory exists.
	if err := os.MkdirAll(store.ClaudeConfigDir(), 0o755); err != nil {
		return fmt.Errorf("create claude config dir: %w", err)
	}

	// Ensure docker image is loaded (slow on first run while docker imports
	// the multi-GB tarball). Show a spinner so the user knows we're alive.
	stopImageSpinner := startAuthSpinner("Loading Docker image")
	imageErr := docker.EnsureImage(config.DefaultDir())
	stopImageSpinner()
	if imageErr != nil {
		return imageErr
	}

	// Run an interactive container so the user can authenticate. We pass
	// SKIP_NIX_WRITABLE=1 because auth just runs the claude TUI — it does
	// not need /nix to be user-writable. Skipping the entrypoint's
	// `chown -R /nix` (which copies up ~50k inodes through Docker
	// Desktop's overlay-fs) cuts cold start from ~70s to ~5s.
	fmt.Fprintln(os.Stderr, "Starting authentication container...")
	dockerArgs := []string{
		"run",
		"--rm",
		"-it",
		"-v", store.ClaudeConfigDir() + ":/claude",
		"-e", "CLAUDE_CONFIG_DIR=/claude",
		"-e", "SKIP_NIX_WRITABLE=1",
		"-e", fmt.Sprintf("USER_UID=%d", docker.ContainerUID()),
		"-e", fmt.Sprintf("USER_GID=%d", docker.ContainerGID()),
		docker.ImageTag(),
		"claude",
		"--dangerously-skip-permissions",
	}

	if err := runAuthPTY(store, dockerArgs); err != nil {
		return err
	}

	// Report auth status.
	if store.IsAuthenticated() {
		fmt.Println("\nAuthentication successful.")
	} else {
		fmt.Println("\nAuthentication was not completed. Run 'claude-container auth' to try again.")
	}

	return nil
}

// ansiCSI matches ANSI CSI escape sequences (color codes, cursor moves,
// erase line, etc.) so the URL regex can match against a clean stream.
// Claude's TUI inserts these constantly between characters.
var ansiCSI = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)

// authURLRegex captures the OAuth URL claude prints. Stop at any whitespace
// or escape byte — terminal width-wrapping inserts neither into the actual
// stream, so this gets the full URL even when it spans multiple visual rows.
var authURLRegex = regexp.MustCompile(`https://claude\.com/[^\s\x00-\x1f]+`)

// runAuthPTY runs the docker auth subprocess under a PTY we own, so we can
// (1) scan output for the OAuth URL and open it in the host browser
// automatically, and (2) intercept the user pressing `c` so the URL we
// place on the clipboard is the unbroken stream we captured rather than the
// terminal-wrapped version the user would otherwise copy by hand.
//
// The PTY wrap is necessary because docker -it normally takes the user's
// TTY directly; once docker owns the TTY we can't see input or output.
// We allocate our own pty, hand the slave end to docker, and proxy
// between user TTY and pty master while scanning both directions.
func runAuthPTY(store *config.Store, dockerArgs []string) error {
	stdinFd := int(os.Stdin.Fd())
	stdinIsTTY := term.IsTerminal(stdinFd)

	// Initial size — defaults if stdin isn't a real TTY (tests, pipes).
	width, height := 80, 24
	if stdinIsTTY {
		if w, h, err := term.GetSize(stdinFd); err == nil {
			width, height = w, h
		}
	}

	c := exec.Command("docker", dockerArgs...)
	ptmx, err := pty.StartWithSize(c, &pty.Winsize{
		Rows: uint16(height),
		Cols: uint16(width),
	})
	if err != nil {
		return fmt.Errorf("docker run with pty: %w", err)
	}
	defer ptmx.Close()

	// Forward window-size changes from the host TTY to the pty.
	sigwinch := make(chan os.Signal, 1)
	signal.Notify(sigwinch, syscall.SIGWINCH)
	defer signal.Stop(sigwinch)
	go func() {
		for range sigwinch {
			if !stdinIsTTY {
				continue
			}
			if w, h, err := term.GetSize(stdinFd); err == nil {
				_ = pty.Setsize(ptmx, &pty.Winsize{Rows: uint16(h), Cols: uint16(w)})
			}
		}
	}()

	// Put host stdin in raw mode so individual keystrokes (including the
	// `c` shortcut) reach us immediately rather than being line-buffered.
	var restoreStdin func()
	if stdinIsTTY {
		oldState, err := term.MakeRaw(stdinFd)
		if err != nil {
			return fmt.Errorf("make raw stdin: %w", err)
		}
		restoreStdin = func() { _ = term.Restore(stdinFd, oldState) }
		defer restoreStdin()
	}

	// Shared URL state, updated whenever we see a fresh OAuth URL in the
	// output stream and consumed when the user presses `c` or when we
	// auto-open the browser on first detection.
	var (
		urlMu       sync.Mutex
		latestURL   string
		openedURL   string
		ringBuf     strings.Builder
		ringLimit   = 32 * 1024
	)

	done := make(chan struct{})

	// pty master -> host stdout, scanning for the OAuth URL.
	go func() {
		defer close(done)
		buf := make([]byte, 8192)
		for {
			n, readErr := ptmx.Read(buf)
			if n > 0 {
				_, _ = os.Stdout.Write(buf[:n])

				ringBuf.Write(buf[:n])
				if ringBuf.Len() > ringLimit {
					tail := ringBuf.String()[ringBuf.Len()-ringLimit/2:]
					ringBuf.Reset()
					ringBuf.WriteString(tail)
				}

				clean := ansiCSI.ReplaceAllString(ringBuf.String(), "")
				if m := authURLRegex.FindString(clean); m != "" {
					urlMu.Lock()
					if m != latestURL {
						latestURL = m
					}
					shouldOpen := openedURL != latestURL
					if shouldOpen {
						openedURL = latestURL
					}
					urlToOpen := latestURL
					urlMu.Unlock()
					if shouldOpen {
						go openInBrowser(urlToOpen)
					}
				}
			}
			if readErr != nil {
				return
			}
		}
	}()

	// host stdin -> pty master, intercepting `c` after the URL is seen.
	go func() {
		buf := make([]byte, 256)
		for {
			n, readErr := os.Stdin.Read(buf)
			if n > 0 {
				// Single-keystroke `c` when we have a URL → copy + swallow.
				if n == 1 && (buf[0] == 'c' || buf[0] == 'C') {
					urlMu.Lock()
					u := latestURL
					urlMu.Unlock()
					if u != "" {
						if err := copyToClipboard(u); err == nil {
							// Brief audible bell as feedback; the user's
							// next paste will get the clean URL.
							_, _ = os.Stdout.Write([]byte{0x07})
							continue
						}
					}
				}
				if _, err := ptmx.Write(buf[:n]); err != nil {
					return
				}
			}
			if readErr != nil {
				return
			}
		}
	}()

	// Poll for credentials and gracefully interrupt the container when the
	// host store reports authentication completed.
	credsDone := make(chan struct{})
	go func() {
		defer close(credsDone)
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if store.IsAuthenticated() {
					time.Sleep(3 * time.Second)
					if c.Process != nil && c.ProcessState == nil {
						_ = c.Process.Signal(os.Interrupt)
					}
					return
				}
				if c.ProcessState != nil {
					return
				}
			}
		}
	}()

	_ = c.Wait()
	// Close pty to unblock the stdout reader, and wait for it to drain.
	_ = ptmx.Close()
	<-done
	<-credsDone

	if restoreStdin != nil {
		restoreStdin()
	}

	if store.IsAuthenticated() {
		fmt.Fprintln(os.Stdout, "\r\nAuthentication successful.")
	} else {
		fmt.Fprintln(os.Stdout, "\r\nAuthentication was not completed. Run 'claude-container auth' to try again.")
	}
	return nil
}

// openInBrowser opens url in the host's default browser. No-op (returns
// nil) on unsupported platforms; we just let the URL stay in the output
// for the user to copy manually.
func openInBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return
	}
	_ = cmd.Run()
}

// copyToClipboard writes s to the host clipboard. Tries the platform-
// appropriate tool; returns an error if no tool is available so the
// caller can decline to swallow the user's keystroke.
func copyToClipboard(s string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("pbcopy")
	case "linux":
		if _, err := exec.LookPath("wl-copy"); err == nil {
			cmd = exec.Command("wl-copy")
		} else if _, err := exec.LookPath("xclip"); err == nil {
			cmd = exec.Command("xclip", "-selection", "clipboard")
		} else if _, err := exec.LookPath("xsel"); err == nil {
			cmd = exec.Command("xsel", "--clipboard", "--input")
		} else {
			return fmt.Errorf("no clipboard tool found (install wl-copy, xclip, or xsel)")
		}
	case "windows":
		cmd = exec.Command("clip")
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
	cmd.Stdin = strings.NewReader(s)
	return cmd.Run()
}

// startAuthSpinner shows an animated spinner on stderr with the given message,
// returning a stop function that clears the spinner line. No-op when stderr
// isn't a TTY (CI / piped output). Modeled on cmd/task.go's startSpinner but
// labelled with a message rather than elapsed time.
func startAuthSpinner(msg string) func() {
	if !term.IsTerminal(int(os.Stderr.Fd())) {
		fmt.Fprintf(os.Stderr, "%s...\n", msg)
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		i := 0
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				fmt.Fprintf(os.Stderr, "\r%s %s...", frames[i%len(frames)], msg)
				i++
			}
		}
	}()
	return func() {
		close(done)
		fmt.Fprint(os.Stderr, "\r\033[K") // clear spinner line
	}
}

func init() {
	authCmd.AddCommand(authStatusCmd)
	authCmd.AddCommand(authRefreshCmd)
	rootCmd.AddCommand(authCmd)
}
