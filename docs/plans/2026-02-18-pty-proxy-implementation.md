# PTY Proxy + Auth Improvements + Doctor/GC Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace Docker's native detach with a Go PTY proxy that intercepts Ctrl+B prefix key, renders a persistent status bar, handles --rm cleanup, improves auth flow, and adds doctor/gc commands.

**Architecture:** A new `internal/proxy` package provides a PTY proxy that sits between the host terminal and Docker subprocess, intercepting stdin for prefix key handling and reserving the bottom terminal row for a status bar. All cmd/ callers switch from `syscall.Exec` / `docker.Attach` to `proxy.Run()`. Auth gets `--dangerously-skip-permissions` and auto-exit on credential detection. New `doctor` and `gc` commands for diagnostics and cleanup.

**Tech Stack:** Go stdlib (`os`, `os/signal`, `os/exec`, `syscall`, `golang.org/x/term`), ANSI escape sequences for scroll region and status bar rendering.

---

### Task 1: Add `golang.org/x/term` dependency

This package provides `term.MakeRaw()` and `term.Restore()` for putting the terminal into raw mode, plus `term.GetSize()` for terminal dimensions.

**Files:**
- Modify: `go.mod`

**Step 1: Add dependency**

Run:
```bash
cd /home/joe/Development/claude-container && go get golang.org/x/term
```

**Step 2: Verify**

Run: `go build ./...`
Expected: exit 0

**Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: add golang.org/x/term dependency"
```

---

### Task 2: Create `internal/proxy/proxy.go` — core PTY proxy

The proxy runs a docker subprocess with piped stdin/stdout/stderr, puts the host terminal in raw mode, sets up an ANSI scroll region (rows 1 to height-1), and proxies bytes bidirectionally. It does NOT yet handle prefix keys or the status bar — those come in later tasks. This task is the foundation: raw mode, byte proxying, clean teardown.

**Files:**
- Create: `internal/proxy/proxy.go`
- Create: `internal/proxy/proxy_test.go`

**Step 1: Write test for proxy start/stop lifecycle**

```go
// internal/proxy/proxy_test.go
package proxy

import (
	"testing"
)

func TestOptsValidation(t *testing.T) {
	// Nil DockerArgs should error.
	err := Run(Opts{})
	if err == nil {
		t.Fatal("Run with empty DockerArgs should error")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd /home/joe/Development/claude-container && go test ./internal/proxy/ -v -run TestOptsValidation`
Expected: FAIL (package doesn't exist yet)

**Step 3: Write `internal/proxy/proxy.go`**

```go
package proxy

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"

	"golang.org/x/term"
)

// StatusBarInfo holds metadata displayed in the status bar.
type StatusBarInfo struct {
	Name   string
	Branch string
	Yolo   bool
}

// CleanupFunc is called when the proxy exits and AutoRemove is true.
// It receives the session name.
type CleanupFunc func(name string)

// Opts configures the proxy.
type Opts struct {
	DockerArgs []string      // full docker command args (e.g. ["run", "--name", ...])
	StatusBar  StatusBarInfo // info for status bar rendering
	AutoRemove bool          // if true, call Cleanup on container exit or quit
	Cleanup    CleanupFunc   // called when AutoRemove is true and container exits
}

// Run starts the PTY proxy. It puts the terminal in raw mode, launches
// the docker subprocess, and proxies bytes between the terminal and
// docker. Returns when the user detaches (Ctrl+B,d), quits (Ctrl+B,q),
// or the container exits.
func Run(opts Opts) error {
	if len(opts.DockerArgs) == 0 {
		return fmt.Errorf("proxy: DockerArgs is required")
	}

	// Save and restore terminal state.
	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("proxy: make raw: %w", err)
	}
	defer term.Restore(fd, oldState)

	// Get terminal size for scroll region.
	width, height, err := term.GetSize(fd)
	if err != nil {
		term.Restore(fd, oldState)
		return fmt.Errorf("proxy: get terminal size: %w", err)
	}

	// Start docker subprocess with piped IO.
	cmd := exec.Command("docker", opts.DockerArgs...)
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("proxy: stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("proxy: stdout pipe: %w", err)
	}
	cmd.Stderr = cmd.Stdout // merge stderr into stdout

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("proxy: start docker: %w", err)
	}

	// Set up scroll region: rows 1..(height-1), leaving last row for status bar.
	setScrollRegion(height)
	renderStatusBar(width, height, opts.StatusBar, false)

	// Handle terminal resize.
	sigWinch := make(chan os.Signal, 1)
	signal.Notify(sigWinch, syscall.SIGWINCH)
	defer signal.Stop(sigWinch)

	// Proxy goroutines.
	var wg sync.WaitGroup
	done := make(chan struct{})

	// stdout: docker → terminal (write inside scroll region)
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, err := stdoutPipe.Read(buf)
			if n > 0 {
				// Move cursor into scroll region before writing.
				os.Stdout.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()

	// stdin: terminal → docker (with prefix key interception)
	detached := false
	quit := false
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer stdinPipe.Close()
		buf := make([]byte, 1)
		prefixState := false
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

			if prefixState {
				prefixState = false
				renderStatusBar(width, height, opts.StatusBar, false)
				switch b {
				case 'd': // detach
					detached = true
					return
				case 'q': // quit
					quit = true
					return
				case 0x02: // Ctrl+B again — send literal Ctrl+B
					stdinPipe.Write([]byte{0x02})
				default:
					// Unknown key after prefix — ignore
				}
				continue
			}

			if b == 0x02 { // Ctrl+B
				prefixState = true
				renderStatusBar(width, height, opts.StatusBar, true)
				continue
			}

			// Forward byte to container.
			stdinPipe.Write(buf[:n])
		}
	}()

	// Resize handler.
	go func() {
		for range sigWinch {
			w, h, err := term.GetSize(fd)
			if err == nil {
				width = w
				height = h
				setScrollRegion(h)
				renderStatusBar(w, h, opts.StatusBar, false)
			}
		}
	}()

	// Wait for docker to exit.
	cmdErr := cmd.Wait()
	close(done)
	wg.Wait()

	// Clear scroll region and status bar.
	clearScrollRegion()

	// Handle cleanup.
	if quit && opts.AutoRemove && opts.Cleanup != nil {
		opts.Cleanup(opts.StatusBar.Name)
	} else if !detached && !quit && opts.AutoRemove && opts.Cleanup != nil {
		// Container exited on its own (Claude exited/crashed).
		opts.Cleanup(opts.StatusBar.Name)
	}

	if detached {
		fmt.Printf("\r\nDetached from %s.\r\n", opts.StatusBar.Name)
		return nil
	}
	if quit {
		return nil
	}

	// Container exited.
	if cmdErr != nil {
		return nil // Container exit is not an error for the proxy.
	}
	return nil
}

// setScrollRegion sets the terminal scroll region to rows 1..height-1,
// reserving the last row for the status bar.
func setScrollRegion(height int) {
	if height < 2 {
		return
	}
	// Set scroll region: ESC [ top ; bottom r
	fmt.Fprintf(os.Stdout, "\033[1;%dr", height-1)
	// Move cursor to top-left of scroll region.
	fmt.Fprintf(os.Stdout, "\033[H")
}

// clearScrollRegion resets scroll region to full terminal.
func clearScrollRegion() {
	fmt.Fprintf(os.Stdout, "\033[r")
	// Clear the status bar line.
	fmt.Fprintf(os.Stdout, "\033[999;1H\033[2K")
}

// renderStatusBar draws the status bar on the last terminal row.
func renderStatusBar(width, height int, info StatusBarInfo, prefixActive bool) {
	if height < 2 {
		return
	}

	// Save cursor, move to status bar row, clear line.
	fmt.Fprintf(os.Stdout, "\033[s\033[%d;1H\033[2K", height)

	// Build status bar content.
	mode := ""
	if info.Yolo {
		mode = " │ yolo"
	}
	branch := ""
	if info.Branch != "" {
		branch = " │ " + info.Branch
	}

	var bar string
	if prefixActive {
		// Show available commands when prefix is active.
		bar = fmt.Sprintf(" %s%s%s │ d:detach  q:quit  ^B:literal", info.Name, branch, mode)
	} else {
		bar = fmt.Sprintf(" %s%s%s │ ^B d:detach q:quit", info.Name, branch, mode)
	}

	// Truncate to terminal width.
	if len(bar) > width {
		bar = bar[:width]
	}
	// Pad to full width for background color.
	for len(bar) < width {
		bar += " "
	}

	// Render with inverted colors (white on dark gray).
	fmt.Fprintf(os.Stdout, "\033[7m%s\033[0m", bar)

	// Restore cursor to scroll region.
	fmt.Fprintf(os.Stdout, "\033[u")
}
```

**Step 4: Run test to verify it passes**

Run: `cd /home/joe/Development/claude-container && go test ./internal/proxy/ -v -run TestOptsValidation`
Expected: PASS

**Step 5: Add more unit tests**

```go
// Add to internal/proxy/proxy_test.go

func TestRenderStatusBar(t *testing.T) {
	// Just verify it doesn't panic with various inputs.
	renderStatusBar(80, 24, StatusBarInfo{Name: "test", Branch: "main", Yolo: true}, false)
	renderStatusBar(80, 24, StatusBarInfo{Name: "test"}, true)
	renderStatusBar(10, 2, StatusBarInfo{Name: "very-long-session-name"}, false)
	renderStatusBar(80, 1, StatusBarInfo{Name: "test"}, false) // height < 2, noop
}

func TestSetScrollRegion(t *testing.T) {
	// Just verify no panic.
	setScrollRegion(24)
	setScrollRegion(1) // too small, noop
	clearScrollRegion()
}
```

**Step 6: Run full test suite**

Run: `cd /home/joe/Development/claude-container && go test ./internal/proxy/ -v`
Expected: all pass

**Step 7: Commit**

```bash
git add internal/proxy/proxy.go internal/proxy/proxy_test.go
git commit -m "feat: add PTY proxy with prefix key interception and status bar"
```

---

### Task 3: Remove old attach functions from `internal/docker/docker.go`

Remove `ExecAttach`, `ExecStartAttach`, `ExecForeground`, and `Attach` from docker.go. Also remove the `syscall` import if no longer used. Keep all other functions (`RunDetached`, `Stop`, `Remove`, `Start`, `IsRunning`, `Exists`, `Build`, `Logs`, `LogsTail`, `ExecAttach` is at lines 216-226, `Attach` at 228-237, `ExecStartAttach` at 180-190, `ExecForeground` at 98-112).

**Files:**
- Modify: `internal/docker/docker.go:98-112,180-190,216-237` (remove functions)
- Modify: `internal/docker/docker_test.go` (no changes expected — no tests for removed functions)

**Step 1: Remove `ExecForeground` (lines 98-112)**

Delete the function and its comment.

**Step 2: Remove `ExecStartAttach` (lines 180-190)**

Delete the function and its comment.

**Step 3: Remove `ExecAttach` (lines 216-226)**

Delete the function and its comment.

**Step 4: Remove `Attach` (lines 228-237)**

Delete the function and its comment.

**Step 5: Clean up imports**

Remove `"syscall"` from imports — it's no longer used in docker.go after removing ExecForeground, ExecAttach, ExecStartAttach.

**Step 6: Verify build and tests**

Run: `cd /home/joe/Development/claude-container && go build ./internal/docker/ && go test ./internal/docker/ -v`
Expected: build succeeds, all existing tests pass (none tested the removed functions)

Note: `go build ./...` will fail because cmd/ still references the removed functions. That's expected — cmd/ gets updated in the next task.

**Step 7: Commit**

```bash
git add internal/docker/docker.go
git commit -m "refactor: remove syscall.Exec attach functions (replaced by proxy)"
```

---

### Task 4: Update all cmd/ callers to use `proxy.Run()`

Switch `cmd/new.go`, `cmd/attach.go`, `cmd/root.go`, and `cmd/shell.go` from the old attach functions to `proxy.Run()`.

**Files:**
- Modify: `cmd/new.go:218-223` (foreground attach)
- Modify: `cmd/attach.go:26-51` (all three attach cases)
- Modify: `cmd/root.go:38-47,83-87` (dashboard attach)
- Modify: `cmd/shell.go:31-36` (debug shell)

**Step 1: Update `cmd/new.go`**

Replace lines 218-223 (the foreground mode section):

```go
	// i. Foreground mode: attach via proxy for full terminal control
	// with status bar and keybindings.
	dockerArgs := docker.RunArgs(runOpts, false)
	fmt.Printf("Session %q created.\n", name)
	return proxy.Run(proxy.Opts{
		DockerArgs: dockerArgs,
		StatusBar:  proxy.StatusBarInfo{Name: name, Branch: branch, Yolo: opts.yolo},
		AutoRemove: opts.autoRemove,
		Cleanup:    func(_ string) { removeSession(store, name) },
	})
```

Add import: `"github.com/joegoldin/claude-container/internal/proxy"`
Remove import: `docker.ExecForeground` is no longer called, but `docker` import is still used for `RunArgs`, `RunDetached`, etc.

**Step 2: Update `cmd/attach.go`**

Replace the entire switch block (lines 26-51):

```go
		store := config.NewStore(config.DefaultDir())
		sess, err := store.Get(name)
		if err != nil {
			return fmt.Errorf("session %q not found", name)
		}

		var dockerArgs []string
		switch {
		case docker.IsRunning(name):
			fmt.Println("Attaching...")
			dockerArgs = []string{"attach", docker.ContainerName(name)}

		case docker.Exists(name):
			fmt.Println("Restarting stopped container...")
			dockerArgs = []string{"start", "-ai", docker.ContainerName(name)}

		default:
			fmt.Println("Recreating container with --continue...")
			dockerArgs = docker.RunArgs(docker.RunOpts{
				Name:      name,
				Workspace: sess.WorktreePath,
				ConfigDir: store.ClaudeConfigDir(),
				UID:       os.Getuid(),
				GID:       os.Getgid(),
				Yolo:      sess.Yolo,
				Continue:  true,
			}, false)
		}

		return proxy.Run(proxy.Opts{
			DockerArgs: dockerArgs,
			StatusBar:  proxy.StatusBarInfo{Name: name, Branch: sess.Branch, Yolo: sess.Yolo},
			AutoRemove: sess.AutoRemove,
			Cleanup:    func(_ string) { removeSession(store, name) },
		})
```

Add import: `"github.com/joegoldin/claude-container/internal/proxy"`

**Step 3: Update `cmd/root.go`**

Replace the dashboard attach section (lines 38-47):

```go
			if dm.Attached() != "" {
				attachName := dm.Attached()
				sess, _ := store.Get(attachName)
				var dockerArgs []string
				if docker.IsRunning(attachName) {
					dockerArgs = []string{"attach", docker.ContainerName(attachName)}
				} else if docker.Exists(attachName) {
					dockerArgs = []string{"start", "-ai", docker.ContainerName(attachName)}
				} else {
					continue // container gone, return to dashboard
				}
				branch := ""
				yolo := false
				autoRemove := false
				if sess != nil {
					branch = sess.Branch
					yolo = sess.Yolo
					autoRemove = sess.AutoRemove
				}
				_ = proxy.Run(proxy.Opts{
					DockerArgs: dockerArgs,
					StatusBar:  proxy.StatusBarInfo{Name: attachName, Branch: branch, Yolo: yolo},
					AutoRemove: autoRemove,
					Cleanup:    func(_ string) { removeSession(store, attachName) },
				})
				continue // return to dashboard after detach
			}
```

Replace the create-then-attach section (lines 83-87):

```go
				if !res.Background {
					var dockerArgs []string
					if docker.IsRunning(res.Name) {
						dockerArgs = []string{"attach", docker.ContainerName(res.Name)}
					} else {
						dockerArgs = []string{"start", "-ai", docker.ContainerName(res.Name)}
					}
					_ = proxy.Run(proxy.Opts{
						DockerArgs: dockerArgs,
						StatusBar:  proxy.StatusBarInfo{Name: res.Name, Yolo: res.Yolo},
					})
				}
```

Add import: `"github.com/joegoldin/claude-container/internal/proxy"`
Remove imports: `"context"`, `"os/signal"` (no longer needed — proxy handles signals internally)

**Step 4: Update `cmd/shell.go`**

Replace the exec.Command block (lines 31-36):

```go
		return proxy.Run(proxy.Opts{
			DockerArgs: shellArgs,
			StatusBar:  proxy.StatusBarInfo{Name: "_shell"},
		})
```

Add import: `"github.com/joegoldin/claude-container/internal/proxy"`
Remove import: `"os/exec"` (no longer used)

**Step 5: Verify build**

Run: `cd /home/joe/Development/claude-container && go build ./...`
Expected: exit 0

**Step 6: Run all tests**

Run: `cd /home/joe/Development/claude-container && go test ./...`
Expected: all pass

**Step 7: Commit**

```bash
git add cmd/new.go cmd/attach.go cmd/root.go cmd/shell.go
git commit -m "feat: switch all attach paths to PTY proxy with status bar"
```

---

### Task 5: Auth improvements — `--dangerously-skip-permissions` and auto-exit

**Files:**
- Modify: `cmd/auth.go:55-98` (authLoginRun function)

**Step 1: Add `--dangerously-skip-permissions` to auth docker args**

In `authLoginRun`, change line 78 from:
```go
		docker.ImageName,
		"claude",
```
to:
```go
		docker.ImageName,
		"claude",
		"--dangerously-skip-permissions",
```

**Step 2: Add credential-polling goroutine for auto-exit**

Replace the `authLoginRun` function body after the docker args setup:

```go
func authLoginRun(cmd *cobra.Command, args []string) error {
	store := config.NewStore(config.DefaultDir())

	// Ensure the shared config directory exists.
	if err := os.MkdirAll(store.ClaudeConfigDir(), 0o755); err != nil {
		return fmt.Errorf("create claude config dir: %w", err)
	}

	// Check that the docker image has been built.
	if !docker.ImageExists() {
		return fmt.Errorf("docker image %q not found; run 'claude-container build' first", docker.ImageName)
	}

	// Run an interactive container so the user can authenticate.
	dockerArgs := []string{
		"run",
		"--rm",
		"-it",
		"-v", store.ClaudeConfigDir() + ":/claude",
		"-e", "CLAUDE_CONFIG_DIR=/claude",
		"-e", fmt.Sprintf("USER_UID=%d", os.Getuid()),
		"-e", fmt.Sprintf("USER_GID=%d", os.Getgid()),
		docker.ImageName,
		"claude",
		"--dangerously-skip-permissions",
	}

	c := exec.Command("docker", dockerArgs...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr

	if err := c.Start(); err != nil {
		return fmt.Errorf("docker run: %w", err)
	}

	// Poll for credentials file appearing — auto-exit when authenticated.
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for range ticker.C {
			if store.IsAuthenticated() {
				// Credentials appeared — signal the container to exit.
				c.Process.Signal(os.Interrupt)
				return
			}
			// Check if process already exited.
			if c.ProcessState != nil {
				return
			}
		}
	}()

	_ = c.Wait()

	// Report auth status.
	if store.IsAuthenticated() {
		fmt.Println("\nAuthentication successful.")
	} else {
		fmt.Println("\nAuthentication was not completed. Run 'claude-container auth' to try again.")
	}

	return nil
}
```

Add import: `"time"` (for ticker)

**Step 3: Verify build**

Run: `cd /home/joe/Development/claude-container && go build ./...`
Expected: exit 0

**Step 4: Commit**

```bash
git add cmd/auth.go
git commit -m "feat: auth auto-exit on login + skip permissions"
```

---

### Task 6: Add `claude-container doctor` command

Diagnostics command that checks system health: Docker available, image built, authenticated, etc.

**Files:**
- Create: `cmd/doctor.go`

**Step 1: Write `cmd/doctor.go`**

```go
package cmd

import (
	"fmt"
	"os/exec"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/docker"
	"github.com/spf13/cobra"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check system health and configuration",
	RunE: func(cmd *cobra.Command, args []string) error {
		ok := true

		// 1. Docker available?
		if _, err := exec.LookPath("docker"); err != nil {
			fmt.Println("  [FAIL] Docker not found in PATH")
			ok = false
		} else {
			fmt.Println("  [ OK ] Docker found")
		}

		// 2. Docker daemon running?
		ping := exec.Command("docker", "info")
		ping.Stdout = nil
		ping.Stderr = nil
		if err := ping.Run(); err != nil {
			fmt.Println("  [FAIL] Docker daemon not running")
			ok = false
		} else {
			fmt.Println("  [ OK ] Docker daemon running")
		}

		// 3. Image built?
		if docker.ImageExists() {
			fmt.Println("  [ OK ] Docker image '" + docker.ImageName + "' found")
		} else {
			fmt.Println("  [FAIL] Docker image '" + docker.ImageName + "' not found (run 'claude-container build')")
			ok = false
		}

		// 4. Authenticated?
		store := config.NewStore(config.DefaultDir())
		if store.IsAuthenticated() {
			fmt.Println("  [ OK ] Authenticated")
		} else {
			fmt.Println("  [WARN] Not authenticated (run 'claude-container auth')")
		}

		// 5. Config dir exists?
		fmt.Println("  [INFO] Config dir: " + config.DefaultDir())
		fmt.Println("  [INFO] Claude config: " + store.ClaudeConfigDir())

		if !ok {
			return fmt.Errorf("doctor found issues")
		}
		fmt.Println("\nAll checks passed.")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(doctorCmd)
}
```

**Step 2: Verify build**

Run: `cd /home/joe/Development/claude-container && go build ./...`
Expected: exit 0

**Step 3: Commit**

```bash
git add cmd/doctor.go
git commit -m "feat: add doctor command for system health checks"
```

---

### Task 7: Add `claude-container gc` command

Garbage collection command to clean up stopped containers and optionally remove unused data.

**Files:**
- Create: `cmd/gc.go`

**Step 1: Write `cmd/gc.go`**

```go
package cmd

import (
	"fmt"
	"os"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/docker"
	"github.com/spf13/cobra"
)

var (
	gcAll bool // also remove worktrees and session records
)

var gcCmd = &cobra.Command{
	Use:   "gc",
	Short: "Clean up stopped containers and stale sessions",
	Long: `Remove stopped Docker containers for tracked sessions.

By default, only stopped containers are removed. Use --all to also
remove worktrees, branches, and session records for stopped sessions.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		store := config.NewStore(config.DefaultDir())
		sessions := store.List()

		if len(sessions) == 0 {
			fmt.Println("No sessions found.")
			return nil
		}

		cleaned := 0
		for _, sess := range sessions {
			if docker.IsRunning(sess.Name) {
				continue // skip running containers
			}

			// Remove stopped container if it exists.
			if docker.Exists(sess.Name) {
				if err := docker.Remove(sess.Name); err != nil {
					fmt.Fprintf(os.Stderr, "warning: remove container %s: %v\n", sess.Name, err)
					continue
				}
				fmt.Printf("Removed container: %s\n", sess.Name)
				cleaned++
			}

			if gcAll {
				removeSession(store, sess.Name)
				fmt.Printf("Removed session: %s\n", sess.Name)
			}
		}

		if cleaned == 0 && !gcAll {
			fmt.Println("Nothing to clean up.")
		}
		return nil
	},
}

func init() {
	gcCmd.Flags().BoolVar(&gcAll, "all", false, "Also remove worktrees, branches, and session records")
	rootCmd.AddCommand(gcCmd)
}
```

**Step 2: Verify build**

Run: `cd /home/joe/Development/claude-container && go build ./...`
Expected: exit 0

**Step 3: Commit**

```bash
git add cmd/gc.go
git commit -m "feat: add gc command to clean up stopped containers"
```

---

### Task 8: Update docker tests and run full verification

**Files:**
- Modify: `internal/docker/docker_test.go` (if any tests reference removed functions)

**Step 1: Check for compilation errors**

Run: `cd /home/joe/Development/claude-container && go build ./...`
Expected: exit 0

**Step 2: Run full test suite**

Run: `cd /home/joe/Development/claude-container && go test ./...`
Expected: all pass

**Step 3: Verify no references to removed functions**

Run: `cd /home/joe/Development/claude-container && grep -r 'ExecAttach\|ExecStartAttach\|ExecForeground\|docker\.Attach' --include='*.go' .`
Expected: no matches (all old attach references removed)

**Step 4: Final commit if any fixes needed**

```bash
git add -A
git commit -m "chore: fix any remaining issues from proxy migration"
```
