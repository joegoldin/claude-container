# claude-container Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Implement all internal packages and wire them into the CLI commands and TUI dashboard so `claude-container` is fully functional.

**Architecture:** Internal packages (`config`, `docker`, `tmux`, `git`) wrap shell commands behind Go interfaces. Commands in `cmd/` orchestrate these packages. The TUI uses Bubble Tea with a session list, preview pane, and diff view. An `Executor` interface abstracts `exec.Command` for testability.

**Tech Stack:** Go, Cobra, Bubble Tea, Lip Gloss, Bubbles, creack/pty, tmux, docker, git

---

### Task 1: Executor Interface + Config Package

**Files:**
- Create: `internal/exec.go`
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`

**Step 1: Write the executor interface**

`internal/exec.go`:
```go
package internal

import (
	"context"
	"io"
	"os/exec"
)

// Commander builds exec.Cmd instances. Mockable for tests.
type Commander interface {
	Command(name string, args ...string) *exec.Cmd
	CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd
}

// RealCommander uses os/exec directly.
type RealCommander struct{}

func (r RealCommander) Command(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}

func (r RealCommander) CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}

// RunCapture runs a command and returns combined stdout+stderr.
func RunCapture(cmd *exec.Cmd) (string, error) {
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// RunAttached runs a command with stdin/stdout/stderr connected.
func RunAttached(cmd *exec.Cmd, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}
```

**Step 2: Write failing tests for config**

`internal/config/config_test.go`:
```go
package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSessionRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	s := &Session{
		Name:          "test-session",
		Branch:        "feature-test",
		WorktreePath:  "/tmp/worktrees/test",
		RepoPath:      "/home/user/project",
		ContainerName: "claude-container_test-session",
		TmuxSession:   "claude-container_test-session",
		Yolo:          true,
		CreatedAt:     time.Date(2026, 2, 18, 10, 0, 0, 0, time.UTC),
	}

	if err := store.Save(s); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := store.Get("test-session")
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if loaded.Name != s.Name {
		t.Errorf("name: got %q, want %q", loaded.Name, s.Name)
	}
	if loaded.Branch != s.Branch {
		t.Errorf("branch: got %q, want %q", loaded.Branch, s.Branch)
	}
	if loaded.Yolo != s.Yolo {
		t.Errorf("yolo: got %v, want %v", loaded.Yolo, s.Yolo)
	}
}

func TestListSessions(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	sessions := store.List()
	if len(sessions) != 0 {
		t.Fatalf("expected 0 sessions, got %d", len(sessions))
	}

	store.Save(&Session{Name: "a", CreatedAt: time.Now()})
	store.Save(&Session{Name: "b", CreatedAt: time.Now()})

	sessions = store.List()
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}
}

func TestDeleteSession(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	store.Save(&Session{Name: "doomed", CreatedAt: time.Now()})
	if err := store.Delete("doomed"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	_, err := store.Get("doomed")
	if err == nil {
		t.Fatal("expected error getting deleted session")
	}
}

func TestStoreCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "dir")
	store := NewStore(dir)
	store.Save(&Session{Name: "x", CreatedAt: time.Now()})

	if _, err := os.Stat(filepath.Join(dir, "sessions.json")); err != nil {
		t.Fatalf("sessions.json not created: %v", err)
	}
}

func TestSanitizeName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"feature/auth", "feature-auth"},
		{"fix payments", "fix-payments"},
		{"simple", "simple"},
		{"a/b/c", "a-b-c"},
		{"dots.and.stuff", "dots.and.stuff"},
	}
	for _, tc := range cases {
		got := SanitizeName(tc.in)
		if got != tc.want {
			t.Errorf("SanitizeName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
```

**Step 3: Run tests to verify they fail**

Run: `cd /home/joe/Development/claude-container && nix develop --command go test ./internal/config/ -v`
Expected: FAIL (package doesn't exist yet)

**Step 4: Implement config package**

`internal/config/config.go`:
```go
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

const (
	Prefix      = "claude-container_"
	SessionFile = "sessions.json"
)

type Session struct {
	Name          string    `json:"name"`
	Branch        string    `json:"branch"`
	WorktreePath  string    `json:"worktree_path,omitempty"`
	RepoPath      string    `json:"repo_path"`
	ContainerName string    `json:"container_name"`
	TmuxSession   string    `json:"tmux_session"`
	Yolo          bool      `json:"yolo"`
	CreatedAt     time.Time `json:"created_at"`
}

type storeData struct {
	Sessions map[string]*Session `json:"sessions"`
}

type Store struct {
	dir string
	mu  sync.Mutex
}

func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

// DefaultDir returns ~/.config/claude-container.
func DefaultDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "claude-container")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "claude-container")
}

// WorktreeDir returns the worktrees subdirectory.
func (s *Store) WorktreeDir() string {
	return filepath.Join(s.dir, "worktrees")
}

func (s *Store) path() string {
	return filepath.Join(s.dir, SessionFile)
}

func (s *Store) load() (*storeData, error) {
	data := &storeData{Sessions: make(map[string]*Session)}
	raw, err := os.ReadFile(s.path())
	if os.IsNotExist(err) {
		return data, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, data); err != nil {
		return nil, err
	}
	if data.Sessions == nil {
		data.Sessions = make(map[string]*Session)
	}
	return data, nil
}

func (s *Store) save(data *storeData) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path(), raw, 0o644)
}

func (s *Store) Save(sess *Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := s.load()
	if err != nil {
		return err
	}
	data.Sessions[sess.Name] = sess
	return s.save(data)
}

func (s *Store) Get(name string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := s.load()
	if err != nil {
		return nil, err
	}
	sess, ok := data.Sessions[name]
	if !ok {
		return nil, fmt.Errorf("session %q not found", name)
	}
	return sess, nil
}

func (s *Store) List() []*Session {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, _ := s.load()
	var out []*Session
	for _, sess := range data.Sessions {
		out = append(out, sess)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

func (s *Store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := s.load()
	if err != nil {
		return err
	}
	delete(data.Sessions, name)
	return s.save(data)
}

// Names returns all session names (for tab completion).
func (s *Store) Names() []string {
	sessions := s.List()
	names := make([]string, len(sessions))
	for i, sess := range sessions {
		names[i] = sess.Name
	}
	return names
}

var unsafeChars = regexp.MustCompile(`[/\s]+`)

// SanitizeName replaces slashes and whitespace with hyphens.
func SanitizeName(name string) string {
	return unsafeChars.ReplaceAllString(name, "-")
}
```

**Step 5: Run tests to verify they pass**

Run: `cd /home/joe/Development/claude-container && nix develop --command go test ./internal/... -v`
Expected: PASS

**Step 6: Commit**

```bash
git add internal/exec.go internal/config/
git commit -m "feat: config package with session persistence and executor interface"
```

---

### Task 2: Docker Package

**Files:**
- Create: `internal/docker/docker.go`
- Create: `internal/docker/docker_test.go`

**Step 1: Write failing tests**

`internal/docker/docker_test.go`:
```go
package docker

import (
	"testing"
)

func TestContainerName(t *testing.T) {
	got := ContainerName("my-session")
	want := "claude-container_my-session"
	if got != want {
		t.Errorf("ContainerName = %q, want %q", got, want)
	}
}

func TestBuildArgs(t *testing.T) {
	args := BuildArgs("/nix/store/abc-context")
	// Should contain: build -t claude-code -f <path>/Dockerfile <path>
	if args[0] != "build" {
		t.Errorf("args[0] = %q, want %q", args[0], "build")
	}
	found := false
	for _, a := range args {
		if a == "claude-code" {
			found = true
		}
	}
	if !found {
		t.Error("expected 'claude-code' image name in args")
	}
}

func TestRunArgs(t *testing.T) {
	opts := RunOpts{
		Name:       "test-session",
		Workspace:  "/tmp/workspace",
		ConfigDir:  "/tmp/config",
		UID:        1000,
		GID:        1000,
		Yolo:       false,
		Prompt:     "",
		Continue:   false,
	}
	args := RunArgs(opts)

	// Should contain: run --name <name> -v workspace:/workspace ...
	hasName := false
	for i, a := range args {
		if a == "--name" && i+1 < len(args) && args[i+1] == ContainerName("test-session") {
			hasName = true
		}
	}
	if !hasName {
		t.Errorf("expected --name flag, got args: %v", args)
	}

	// Should NOT contain --rm
	for _, a := range args {
		if a == "--rm" {
			t.Error("should not have --rm for persistent containers")
		}
	}
}

func TestRunArgsYolo(t *testing.T) {
	opts := RunOpts{
		Name:      "yolo-session",
		Workspace: "/tmp/ws",
		ConfigDir: "/tmp/cfg",
		UID:       1000,
		GID:       1000,
		Yolo:      true,
	}
	args := RunArgs(opts)

	hasSkipPerms := false
	for _, a := range args {
		if a == "--dangerously-skip-permissions" {
			hasSkipPerms = true
		}
	}
	if !hasSkipPerms {
		t.Error("yolo mode should include --dangerously-skip-permissions")
	}
}

func TestRunArgsWithPrompt(t *testing.T) {
	opts := RunOpts{
		Name:      "prompted",
		Workspace: "/tmp/ws",
		ConfigDir: "/tmp/cfg",
		UID:       1000,
		GID:       1000,
		Prompt:    "fix the auth bug",
	}
	args := RunArgs(opts)

	hasP := false
	for i, a := range args {
		if a == "-p" && i+1 < len(args) && args[i+1] == "fix the auth bug" {
			hasP = true
		}
	}
	if !hasP {
		t.Errorf("expected -p flag with prompt, got args: %v", args)
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `cd /home/joe/Development/claude-container && nix develop --command go test ./internal/docker/ -v`
Expected: FAIL

**Step 3: Implement docker package**

`internal/docker/docker.go`:
```go
package docker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const ImageName = "claude-code"

// ContainerName returns the docker container name for a session.
func ContainerName(session string) string {
	return "claude-container_" + session
}

// RunOpts configures a docker run invocation.
type RunOpts struct {
	Name      string
	Workspace string
	ConfigDir string
	UID       int
	GID       int
	Yolo      bool
	Prompt    string
	Continue  bool
}

// BuildArgs returns the docker build arguments.
func BuildArgs(contextDir string) []string {
	return []string{
		"build", "-t", ImageName,
		"-f", contextDir + "/Dockerfile",
		contextDir,
	}
}

// RunArgs returns the docker run arguments (without "docker" prefix).
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
		ImageName,
	}

	claudeArgs := []string{"claude"}
	if opts.Yolo {
		claudeArgs = append(claudeArgs, "--dangerously-skip-permissions")
	}
	if opts.Continue {
		claudeArgs = append(claudeArgs, "--continue")
	}
	if opts.Prompt != "" {
		claudeArgs = append(claudeArgs, "-p", opts.Prompt)
	}

	return append(args, claudeArgs...)
}

// ShellArgs returns docker run args for a debug shell.
func ShellArgs(workspace, configDir string, uid, gid int) []string {
	return []string{
		"run", "--rm", "-it",
		"-v", workspace + ":/workspace",
		"-v", configDir + ":/claude",
		"-e", "CLAUDE_CONFIG_DIR=/claude",
		"-e", fmt.Sprintf("USER_UID=%d", uid),
		"-e", fmt.Sprintf("USER_GID=%d", gid),
		ImageName, "/bin/bash",
	}
}

// ImageExists checks if the docker image has been built.
func ImageExists() bool {
	cmd := exec.Command("docker", "image", "inspect", ImageName)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// IsRunning checks if a container is currently running.
func IsRunning(session string) bool {
	name := ContainerName(session)
	out, err := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", name).CombinedOutput()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

// Exists checks if a container exists (running or stopped).
func Exists(session string) bool {
	name := ContainerName(session)
	cmd := exec.Command("docker", "inspect", name)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// Stop stops a running container.
func Stop(session string) error {
	name := ContainerName(session)
	return exec.Command("docker", "stop", name).Run()
}

// Remove removes a container.
func Remove(session string) error {
	name := ContainerName(session)
	return exec.Command("docker", "rm", "-f", name).Run()
}

// Start restarts a stopped container.
func Start(session string) error {
	name := ContainerName(session)
	return exec.Command("docker", "start", name).Run()
}

// Logs streams container logs. If follow is true, streams continuously.
func Logs(ctx context.Context, session string, follow bool) *exec.Cmd {
	name := ContainerName(session)
	args := []string{"logs"}
	if follow {
		args = append(args, "-f")
	}
	args = append(args, name)
	return exec.CommandContext(ctx, "docker", args...)
}

// Build builds the docker image from the context directory.
func Build(contextDir string) *exec.Cmd {
	args := BuildArgs(contextDir)
	cmd := exec.Command("docker", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}
```

**Step 4: Run tests to verify they pass**

Run: `cd /home/joe/Development/claude-container && nix develop --command go test ./internal/docker/ -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/docker/
git commit -m "feat: docker package for container lifecycle management"
```

---

### Task 3: Tmux Package

**Files:**
- Create: `internal/tmux/tmux.go`
- Create: `internal/tmux/tmux_test.go`

**Step 1: Write failing tests**

`internal/tmux/tmux_test.go`:
```go
package tmux

import (
	"testing"
)

func TestSessionName(t *testing.T) {
	got := SessionName("my-session")
	want := "claude-container_my-session"
	if got != want {
		t.Errorf("SessionName = %q, want %q", got, want)
	}
}

func TestNewSessionArgs(t *testing.T) {
	args := NewSessionArgs("test", "/tmp/workspace", []string{"docker", "run", "claude-code"})

	// First arg should be "new-session"
	if args[0] != "new-session" {
		t.Errorf("args[0] = %q, want %q", args[0], "new-session")
	}

	// Should be detached
	hasD := false
	for _, a := range args {
		if a == "-d" {
			hasD = true
		}
	}
	if !hasD {
		t.Error("expected -d flag for detached session")
	}

	// Should set mouse mode on
	// The command at the end should include docker run
	last := args[len(args)-1]
	if last == "" {
		t.Error("expected non-empty last arg")
	}
}

func TestCapturePaneArgs(t *testing.T) {
	args := CapturePaneArgs("test")
	found := false
	for _, a := range args {
		if a == SessionName("test") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected session name in args: %v", args)
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `cd /home/joe/Development/claude-container && nix develop --command go test ./internal/tmux/ -v`
Expected: FAIL

**Step 3: Implement tmux package**

`internal/tmux/tmux.go`:
```go
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
)

const prefix = "claude-container_"

// SessionName returns the tmux session name for a claude-container session.
func SessionName(session string) string {
	return prefix + session
}

// NewSessionArgs returns tmux args to create a new detached session.
// The session runs the given command inside the specified working directory.
func NewSessionArgs(session, workDir string, command []string) []string {
	name := SessionName(session)
	// Build the shell command that enables mouse mode then execs the real command
	shellCmd := fmt.Sprintf(
		"tmux set-option -t %s mouse on; exec %s",
		name, shellJoin(command),
	)
	return []string{
		"new-session",
		"-d",
		"-s", name,
		"-c", workDir,
		"bash", "-c", shellCmd,
	}
}

// Exists checks if a tmux session exists.
func Exists(session string) bool {
	name := SessionName(session)
	cmd := exec.Command("tmux", "has-session", "-t", name)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// Kill kills a tmux session.
func Kill(session string) error {
	name := SessionName(session)
	return exec.Command("tmux", "kill-session", "-t", name).Run()
}

// Create creates a new tmux session running the given command.
func Create(session, workDir string, command []string) error {
	args := NewSessionArgs(session, workDir, command)
	cmd := exec.Command("tmux", args...)
	return cmd.Run()
}

// CapturePaneArgs returns tmux args to capture the visible pane content.
func CapturePaneArgs(session string) []string {
	name := SessionName(session)
	return []string{
		"capture-pane",
		"-t", name,
		"-p",       // print to stdout
		"-e",       // include escape sequences (for colors)
	}
}

// CapturePane captures the current visible content of a session's pane.
func CapturePane(session string) (string, error) {
	args := CapturePaneArgs(session)
	out, err := exec.Command("tmux", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("capture-pane: %w: %s", err, out)
	}
	return string(out), nil
}

// Attach attaches to a tmux session via a PTY, intercepting Ctrl+Q to detach.
// Returns when the user presses Ctrl+Q or the session ends.
func Attach(ctx context.Context, session string) error {
	name := SessionName(session)

	// Resize tmux to match current terminal
	resizeTmux(name)

	cmd := exec.CommandContext(ctx, "tmux", "attach-session", "-t", name)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("start pty: %w", err)
	}
	defer ptmx.Close()

	// Handle terminal resize
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	go func() {
		for range ch {
			if err := pty.InheritSize(os.Stdin, ptmx); err != nil {
				continue
			}
			resizeTmux(name)
		}
	}()
	defer signal.Stop(ch)
	defer close(ch)

	// Set initial size
	pty.InheritSize(os.Stdin, ptmx)

	// Set terminal to raw mode
	oldState, err := makeRaw(os.Stdin.Fd())
	if err != nil {
		return fmt.Errorf("make raw: %w", err)
	}
	defer restore(os.Stdin.Fd(), oldState)

	// Copy tmux output to stdout
	done := make(chan struct{})
	go func() {
		io.Copy(os.Stdout, ptmx)
		close(done)
	}()

	// Copy stdin to tmux, intercepting Ctrl+Q
	detach := make(chan struct{})
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				return
			}
			for i := 0; i < n; i++ {
				if buf[i] == 0x11 { // Ctrl+Q
					close(detach)
					return
				}
			}
			ptmx.Write(buf[:n])
		}
	}()

	// Wait for detach or session end
	select {
	case <-detach:
		// User pressed Ctrl+Q
		return nil
	case <-done:
		// Session ended
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func resizeTmux(name string) {
	// Get terminal size and tell tmux
	ws, err := pty.GetsizeFull(os.Stdin)
	if err != nil {
		return
	}
	exec.Command("tmux", "resize-window",
		"-t", name,
		"-x", fmt.Sprint(ws.Cols),
		"-y", fmt.Sprint(ws.Rows),
	).Run()
}

// ListSessions returns names of all claude-container tmux sessions.
func ListSessions() []string {
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").CombinedOutput()
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

// IsResponsive checks if a tmux session is alive and responsive.
func IsResponsive(session string) bool {
	name := SessionName(session)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "tmux", "has-session", "-t", name)
	return cmd.Run() == nil
}

func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		if strings.ContainsAny(a, " \t\n\"'\\") {
			quoted[i] = fmt.Sprintf("%q", a)
		} else {
			quoted[i] = a
		}
	}
	return strings.Join(quoted, " ")
}
```

**Step 4: Create terminal raw mode helpers**

`internal/tmux/raw.go`:
```go
package tmux

import (
	"golang.org/x/sys/unix"
)

type termState struct {
	termios unix.Termios
}

func makeRaw(fd uintptr) (*termState, error) {
	termios, err := unix.IoctlGetTermios(int(fd), unix.TCGETS)
	if err != nil {
		return nil, err
	}
	old := &termState{termios: *termios}

	raw := *termios
	raw.Iflag &^= unix.BRKINT | unix.ICRNL | unix.INPCK | unix.ISTRIP | unix.IXON
	raw.Oflag &^= unix.OPOST
	raw.Cflag |= unix.CS8
	raw.Lflag &^= unix.ECHO | unix.ICANON | unix.IEXTEN | unix.ISIG
	raw.Cc[unix.VMIN] = 1
	raw.Cc[unix.VTIME] = 0

	if err := unix.IoctlSetTermios(int(fd), unix.TCSETS, &raw); err != nil {
		return nil, err
	}
	return old, nil
}

func restore(fd uintptr, state *termState) error {
	return unix.IoctlSetTermios(int(fd), unix.TCSETS, &state.termios)
}
```

**Step 5: Run tests to verify they pass**

Run: `cd /home/joe/Development/claude-container && nix develop --command go test ./internal/tmux/ -v`
Expected: PASS (the unit tests only test arg construction, not actual tmux)

**Step 6: Commit**

```bash
git add internal/tmux/
git commit -m "feat: tmux package for session management and PTY attach/detach"
```

---

### Task 4: Git Package

**Files:**
- Create: `internal/git/git.go`
- Create: `internal/git/git_test.go`

**Step 1: Write failing tests**

`internal/git/git_test.go`:
```go
package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func setupTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "git", "init")
	run(t, dir, "git", "config", "user.email", "test@test.com")
	run(t, dir, "git", "config", "user.name", "Test")
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("hello"), 0o644)
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "init")
	return dir
}

func run(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

func TestCreateWorktree(t *testing.T) {
	repo := setupTestRepo(t)
	wtDir := filepath.Join(t.TempDir(), "wt")

	err := CreateWorktree(repo, wtDir, "test-branch")
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	// Worktree dir should exist
	if _, err := os.Stat(wtDir); err != nil {
		t.Fatalf("worktree dir not created: %v", err)
	}

	// Should be on the new branch
	branch, err := CurrentBranch(wtDir)
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}
	if branch != "test-branch" {
		t.Errorf("branch = %q, want %q", branch, "test-branch")
	}
}

func TestRemoveWorktree(t *testing.T) {
	repo := setupTestRepo(t)
	wtDir := filepath.Join(t.TempDir(), "wt")

	CreateWorktree(repo, wtDir, "remove-me")
	err := RemoveWorktree(repo, wtDir, "remove-me")
	if err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}

	// Directory should be gone
	if _, err := os.Stat(wtDir); !os.IsNotExist(err) {
		t.Error("worktree dir still exists after removal")
	}
}

func TestCreateWorktreeFromBranch(t *testing.T) {
	repo := setupTestRepo(t)

	// Create a branch in the repo
	run(t, repo, "git", "branch", "base-feature")

	wtDir := filepath.Join(t.TempDir(), "wt")
	err := CreateWorktreeFromBranch(repo, wtDir, "new-work", "base-feature")
	if err != nil {
		t.Fatalf("CreateWorktreeFromBranch: %v", err)
	}

	branch, err := CurrentBranch(wtDir)
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}
	if branch != "new-work" {
		t.Errorf("branch = %q, want %q", branch, "new-work")
	}
}

func TestListBranches(t *testing.T) {
	repo := setupTestRepo(t)
	run(t, repo, "git", "branch", "feature-a")
	run(t, repo, "git", "branch", "feature-b")

	branches, err := ListBranches(repo)
	if err != nil {
		t.Fatalf("ListBranches: %v", err)
	}

	if len(branches) < 3 { // main + feature-a + feature-b
		t.Errorf("expected at least 3 branches, got %d: %v", len(branches), branches)
	}
}

func TestDiff(t *testing.T) {
	repo := setupTestRepo(t)
	wtDir := filepath.Join(t.TempDir(), "wt")
	CreateWorktree(repo, wtDir, "diff-test")

	// Make a change in the worktree
	os.WriteFile(filepath.Join(wtDir, "new.txt"), []byte("new file"), 0o644)

	diff, err := Diff(wtDir)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}

	// Diff should show the untracked file or staged changes
	// For untracked files, we use diff against HEAD
	if diff == "" {
		// Also check status
		status, _ := Status(wtDir)
		if status == "" {
			t.Error("expected non-empty diff or status for modified worktree")
		}
	}
}

func TestRepoRoot(t *testing.T) {
	repo := setupTestRepo(t)
	sub := filepath.Join(repo, "subdir")
	os.MkdirAll(sub, 0o755)

	root, err := RepoRoot(sub)
	if err != nil {
		t.Fatalf("RepoRoot: %v", err)
	}
	// Normalize for comparison
	repoReal, _ := filepath.EvalSymlinks(repo)
	rootReal, _ := filepath.EvalSymlinks(root)
	if rootReal != repoReal {
		t.Errorf("RepoRoot = %q, want %q", rootReal, repoReal)
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `cd /home/joe/Development/claude-container && nix develop --command go test ./internal/git/ -v`
Expected: FAIL

**Step 3: Implement git package**

`internal/git/git.go`:
```go
package git

import (
	"fmt"
	"os/exec"
	"strings"
)

// RepoRoot returns the git repository root from any path inside it.
func RepoRoot(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git rev-parse: %w: %s", err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

// CurrentBranch returns the current branch name.
func CurrentBranch(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git rev-parse: %w: %s", err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

// HeadCommit returns the HEAD commit hash.
func HeadCommit(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD: %w: %s", err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

// CreateWorktree creates a new git worktree with a new branch from HEAD.
func CreateWorktree(repoDir, worktreeDir, branch string) error {
	cmd := exec.Command("git", "worktree", "add", "-b", branch, worktreeDir)
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree add: %w: %s", err, out)
	}
	return nil
}

// CreateWorktreeFromBranch creates a new worktree with a new branch starting from baseBranch.
func CreateWorktreeFromBranch(repoDir, worktreeDir, newBranch, baseBranch string) error {
	// Resolve base branch commit
	ref := baseBranch
	// Try local, then origin/<branch>
	cmd := exec.Command("git", "rev-parse", "--verify", ref)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		ref = "origin/" + baseBranch
		cmd2 := exec.Command("git", "rev-parse", "--verify", ref)
		cmd2.Dir = repoDir
		if out2, err2 := cmd2.CombinedOutput(); err2 != nil {
			return fmt.Errorf("branch %q not found locally or as origin/%s: %s %s", baseBranch, baseBranch, out, out2)
		}
	}

	cmd = exec.Command("git", "worktree", "add", "-b", newBranch, worktreeDir, ref)
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree add from %s: %w: %s", ref, err, out)
	}
	return nil
}

// CheckoutWorktree creates a worktree that checks out an existing branch.
func CheckoutWorktree(repoDir, worktreeDir, existingBranch string) error {
	cmd := exec.Command("git", "worktree", "add", worktreeDir, existingBranch)
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree add: %w: %s", err, out)
	}
	return nil
}

// RemoveWorktree removes a worktree and its branch.
func RemoveWorktree(repoDir, worktreeDir, branch string) error {
	// Remove the worktree
	cmd := exec.Command("git", "worktree", "remove", "-f", worktreeDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree remove: %w: %s", err, out)
	}

	// Prune stale worktree admin files
	exec.Command("git", "-C", repoDir, "worktree", "prune").Run()

	// Delete the branch
	cmd = exec.Command("git", "branch", "-D", branch)
	cmd.Dir = repoDir
	cmd.Run() // best effort - branch may already be gone

	return nil
}

// ListBranches returns all local branch names.
func ListBranches(dir string) ([]string, error) {
	cmd := exec.Command("git", "branch", "--format=%(refname:short)")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git branch: %w: %s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var branches []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" {
			branches = append(branches, l)
		}
	}
	return branches, nil
}

// Diff returns the git diff of uncommitted changes (staged + unstaged + untracked).
func Diff(dir string) (string, error) {
	// Get diff of tracked changes
	cmd := exec.Command("git", "diff", "HEAD")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		// May fail if no commits yet on branch, try without HEAD
		cmd2 := exec.Command("git", "diff")
		cmd2.Dir = dir
		out, err = cmd2.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("git diff: %w: %s", err, out)
		}
	}
	return string(out), nil
}

// Status returns the short git status output.
func Status(dir string) (string, error) {
	cmd := exec.Command("git", "status", "--short")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git status: %w: %s", err, out)
	}
	return string(out), nil
}

// IsBranchCheckedOut checks if a branch is currently checked out in the main worktree.
func IsBranchCheckedOut(repoDir, branch string) bool {
	current, err := CurrentBranch(repoDir)
	if err != nil {
		return false
	}
	return current == branch
}
```

**Step 4: Run tests to verify they pass**

Run: `cd /home/joe/Development/claude-container && nix develop --command go test ./internal/git/ -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/git/
git commit -m "feat: git package for worktree and branch management"
```

---

### Task 5: Add Go Dependencies

**Step 1: Add all required dependencies**

Run:
```bash
cd /home/joe/Development/claude-container && nix develop --command bash -c "
  go get github.com/creack/pty@latest
  go get golang.org/x/sys@latest
  go get github.com/charmbracelet/bubbletea@latest
  go get github.com/charmbracelet/bubbles@latest
  go get github.com/charmbracelet/lipgloss@latest
  go mod tidy
"
```

**Step 2: Verify build**

Run: `cd /home/joe/Development/claude-container && nix develop --command go build ./...`
Expected: success

**Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: add bubbletea, bubbles, lipgloss, pty, x/sys dependencies"
```

---

### Task 6: Wire Up Build, Shell, Logs Commands

**Files:**
- Modify: `cmd/build.go`
- Modify: `cmd/shell.go`
- Modify: `cmd/logs.go`

**Step 1: Implement build command**

`cmd/build.go` - replace the Run function body:
```go
package cmd

import (
	"fmt"
	"os"

	"github.com/joegoldin/claude-container/internal/docker"
	"github.com/spf13/cobra"
)

var buildCmd = &cobra.Command{
	Use:   "build",
	Short: "Build the Claude Code container image",
	RunE: func(cmd *cobra.Command, args []string) error {
		contextDir := os.Getenv("CLAUDE_CONTAINER_DOCKER_CONTEXT")
		if contextDir == "" {
			return fmt.Errorf("CLAUDE_CONTAINER_DOCKER_CONTEXT not set (are you using the nix-wrapped binary?)")
		}
		fmt.Println("Building Claude Code container...")
		return docker.Build(contextDir).Run()
	},
}

func init() {
	rootCmd.AddCommand(buildCmd)
}
```

**Step 2: Implement shell command**

`cmd/shell.go`:
```go
package cmd

import (
	"os"
	"os/exec"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/docker"
	"github.com/spf13/cobra"
)

var shellCmd = &cobra.Command{
	Use:   "shell [workspace]",
	Short: "Drop into a bash shell in a container",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws, _ := os.Getwd()
		if len(args) > 0 {
			ws = args[0]
		}
		configDir := config.DefaultDir()
		os.MkdirAll(configDir, 0o755)

		shellArgs := docker.ShellArgs(ws, configDir, os.Getuid(), os.Getgid())
		c := exec.Command("docker", shellArgs...)
		c.Stdin = os.Stdin
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		return c.Run()
	},
}

func init() {
	rootCmd.AddCommand(shellCmd)
}
```

**Step 3: Implement logs command**

`cmd/logs.go`:
```go
package cmd

import (
	"context"
	"os"
	"os/signal"

	"github.com/joegoldin/claude-container/internal/docker"
	"github.com/spf13/cobra"
)

var logsFollow bool

var logsCmd = &cobra.Command{
	Use:               "logs <session>",
	Short:             "Stream logs from a session",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()

		c := docker.Logs(ctx, args[0], logsFollow)
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		return c.Run()
	},
}

func init() {
	logsCmd.Flags().BoolVarP(&logsFollow, "follow", "f", false, "Stream logs continuously")
	rootCmd.AddCommand(logsCmd)
}
```

**Step 4: Verify build**

Run: `cd /home/joe/Development/claude-container && nix develop --command go build ./...`
Expected: success

**Step 5: Commit**

```bash
git add cmd/build.go cmd/shell.go cmd/logs.go
git commit -m "feat: implement build, shell, and logs commands"
```

---

### Task 7: Wire Up PS Command + State Reconciliation

**Files:**
- Modify: `cmd/ps.go`
- Modify: `cmd/attach.go` (tab completion)

**Step 1: Implement ps command**

`cmd/ps.go`:
```go
package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/docker"
	"github.com/joegoldin/claude-container/internal/tmux"
	"github.com/spf13/cobra"
)

var psJSON bool

type sessionStatus struct {
	Name      string `json:"name"`
	Branch    string `json:"branch"`
	Status    string `json:"status"`
	Uptime    string `json:"uptime,omitempty"`
	RepoPath  string `json:"repo_path"`
}

var psCmd = &cobra.Command{
	Use:   "ps",
	Short: "List all sessions",
	RunE: func(cmd *cobra.Command, args []string) error {
		store := config.NewStore(config.DefaultDir())
		sessions := store.List()

		if len(sessions) == 0 {
			if !psJSON {
				fmt.Println("No sessions.")
			} else {
				fmt.Println("[]")
			}
			return nil
		}

		var statuses []sessionStatus
		for _, s := range sessions {
			st := sessionStatus{
				Name:     s.Name,
				Branch:   s.Branch,
				RepoPath: s.RepoPath,
			}

			tmuxAlive := tmux.Exists(s.Name)
			containerRunning := docker.IsRunning(s.Name)

			switch {
			case tmuxAlive && containerRunning:
				st.Status = "running"
				st.Uptime = time.Since(s.CreatedAt).Truncate(time.Second).String()
			case tmuxAlive:
				st.Status = "exited"
			default:
				st.Status = "stopped"
			}

			statuses = append(statuses, st)
		}

		if psJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(statuses)
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tBRANCH\tSTATUS\tUPTIME\tREPO")
		for _, s := range statuses {
			uptime := s.Uptime
			if uptime == "" {
				uptime = "-"
			}
			repo := s.RepoPath
			// Shorten home dir
			if home, err := os.UserHomeDir(); err == nil {
				repo = strings.Replace(repo, home, "~", 1)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", s.Name, s.Branch, s.Status, uptime, repo)
		}
		return w.Flush()
	},
}

func init() {
	psCmd.Flags().BoolVar(&psJSON, "json", false, "Machine-readable JSON output")
	rootCmd.AddCommand(psCmd)
}
```

**Step 2: Wire up tab completion**

Replace `completeSessionNames` in `cmd/attach.go`:
```go
func completeSessionNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	store := config.NewStore(config.DefaultDir())
	return store.Names(), cobra.ShellCompDirectiveNoFileComp
}
```

And add the import:
```go
import (
	"github.com/joegoldin/claude-container/internal/config"
)
```

**Step 3: Verify build**

Run: `cd /home/joe/Development/claude-container && nix develop --command go build ./...`
Expected: success

**Step 4: Commit**

```bash
git add cmd/ps.go cmd/attach.go
git commit -m "feat: implement ps command with state reconciliation and tab completion"
```

---

### Task 8: Wire Up New Command (Flag-Based, No Wizard Yet)

**Files:**
- Modify: `cmd/new.go`

**Step 1: Implement new command with flag-based flow**

`cmd/new.go`:
```go
package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/docker"
	gitpkg "github.com/joegoldin/claude-container/internal/git"
	"github.com/joegoldin/claude-container/internal/tmux"
	"github.com/spf13/cobra"
)

var (
	newName       string
	newWorktree   string
	newFrom       string
	newNoWorktree bool
	newYolo       bool
	newPrompt     string
	newContinue   bool
)

var newCmd = &cobra.Command{
	Use:   "new",
	Short: "Create a new Claude Code session",
	Long:  `Create a new session with an interactive wizard, or use flags to skip the wizard.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return createSession(createOpts{
			name:       newName,
			worktree:   newWorktree,
			from:       newFrom,
			noWorktree: newNoWorktree,
			yolo:       newYolo,
			prompt:     newPrompt,
			cont:       newContinue,
		})
	},
}

type createOpts struct {
	name       string
	worktree   string
	from       string
	noWorktree bool
	yolo       bool
	prompt     string
	cont       bool
}

func createSession(opts createOpts) error {
	store := config.NewStore(config.DefaultDir())

	// Determine workspace directory
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}

	// Resolve repo path
	repoPath, err := gitpkg.RepoRoot(cwd)
	if err != nil && !opts.noWorktree {
		return fmt.Errorf("not inside a git repo (use --no-worktree for non-git directories): %w", err)
	}
	if opts.noWorktree {
		repoPath = cwd
	}

	// Determine session name
	name := opts.name
	if name == "" {
		if opts.worktree != "" {
			name = config.SanitizeName(opts.worktree)
		} else {
			return fmt.Errorf("--name is required (or use --worktree to auto-derive name)")
		}
	}
	name = config.SanitizeName(name)

	// Check for existing session
	if _, err := store.Get(name); err == nil {
		return fmt.Errorf("session %q already exists", name)
	}

	// Check docker image
	contextDir := os.Getenv("CLAUDE_CONTAINER_DOCKER_CONTEXT")
	if !docker.ImageExists() {
		if contextDir == "" {
			return fmt.Errorf("docker image %q not found and CLAUDE_CONTAINER_DOCKER_CONTEXT not set", docker.ImageName)
		}
		fmt.Println("Docker image not found, building...")
		if err := docker.Build(contextDir).Run(); err != nil {
			return fmt.Errorf("docker build: %w", err)
		}
	}

	// Create worktree if requested
	workspace := cwd
	branch := ""
	if !opts.noWorktree && opts.worktree != "" {
		branch = opts.worktree
		wtDir := filepath.Join(store.WorktreeDir(), name)

		if opts.from != "" {
			err = gitpkg.CreateWorktreeFromBranch(repoPath, wtDir, branch, opts.from)
		} else {
			err = gitpkg.CreateWorktree(repoPath, wtDir, branch)
		}
		if err != nil {
			return fmt.Errorf("create worktree: %w", err)
		}
		workspace = wtDir
		fmt.Printf("Created worktree: %s (branch: %s)\n", wtDir, branch)
	}

	// Build docker run command
	configDir := config.DefaultDir()
	os.MkdirAll(configDir, 0o755)
	dockerArgs := docker.RunArgs(docker.RunOpts{
		Name:      name,
		Workspace: workspace,
		ConfigDir: configDir,
		UID:       os.Getuid(),
		GID:       os.Getgid(),
		Yolo:      opts.yolo,
		Prompt:    opts.prompt,
		Continue:  opts.cont,
	})

	// Create tmux session running the docker container
	fullDockerCmd := append([]string{"docker"}, dockerArgs...)
	if err := tmux.Create(name, workspace, fullDockerCmd); err != nil {
		return fmt.Errorf("create tmux session: %w", err)
	}

	// Save session metadata
	sess := &config.Session{
		Name:          name,
		Branch:        branch,
		WorktreePath:  workspace,
		RepoPath:      repoPath,
		ContainerName: docker.ContainerName(name),
		TmuxSession:   tmux.SessionName(name),
		Yolo:          opts.yolo,
		CreatedAt:     time.Now(),
	}
	if err := store.Save(sess); err != nil {
		return fmt.Errorf("save session: %w", err)
	}

	fmt.Printf("Session %q created and running.\n", name)
	fmt.Printf("  Attach: claude-container attach %s\n", name)
	return nil
}

func init() {
	newCmd.Flags().StringVar(&newName, "name", "", "Session name")
	newCmd.Flags().StringVar(&newWorktree, "worktree", "", "Create worktree on new branch")
	newCmd.Flags().StringVar(&newFrom, "from", "", "Base branch for worktree (default: current HEAD)")
	newCmd.Flags().BoolVar(&newNoWorktree, "no-worktree", false, "Use current directory directly")
	newCmd.Flags().BoolVar(&newYolo, "yolo", false, "Skip permission prompts")
	newCmd.Flags().StringVarP(&newPrompt, "prompt", "p", "", "Initial prompt to send to Claude")
	newCmd.Flags().BoolVarP(&newContinue, "continue", "c", false, "Resume previous conversation")
	rootCmd.AddCommand(newCmd)
}
```

**Step 2: Verify build**

Run: `cd /home/joe/Development/claude-container && nix develop --command go build ./...`
Expected: success

**Step 3: Commit**

```bash
git add cmd/new.go
git commit -m "feat: implement new command with flag-based session creation"
```

---

### Task 9: Wire Up Attach, Stop, Rm Commands

**Files:**
- Modify: `cmd/attach.go`
- Modify: `cmd/stop.go`
- Modify: `cmd/rm.go`

**Step 1: Implement attach**

`cmd/attach.go`:
```go
package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/docker"
	"github.com/joegoldin/claude-container/internal/tmux"
	"github.com/spf13/cobra"
)

var attachCmd = &cobra.Command{
	Use:               "attach <session>",
	Short:             "Attach to a running session",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		store := config.NewStore(config.DefaultDir())

		sess, err := store.Get(name)
		if err != nil {
			return fmt.Errorf("session %q not found", name)
		}

		// If tmux session doesn't exist, try to restart
		if !tmux.Exists(name) {
			// Check if container exists but stopped
			if docker.Exists(name) {
				docker.Remove(name)
			}

			// Rebuild the docker command
			configDir := config.DefaultDir()
			dockerArgs := docker.RunArgs(docker.RunOpts{
				Name:      name,
				Workspace: sess.WorktreePath,
				ConfigDir: configDir,
				UID:       os.Getuid(),
				GID:       os.Getgid(),
				Yolo:      sess.Yolo,
				Continue:  true, // always resume previous conversation
			})
			fullCmd := append([]string{"docker"}, dockerArgs...)
			if err := tmux.Create(name, sess.WorktreePath, fullCmd); err != nil {
				return fmt.Errorf("restart session: %w", err)
			}
			fmt.Printf("Restarted session %q with --continue\n", name)
		}

		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()

		fmt.Printf("Attaching to %q (Ctrl+Q to detach)...\n", name)
		return tmux.Attach(ctx, name)
	},
}

func init() {
	rootCmd.AddCommand(attachCmd)
}

func completeSessionNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	store := config.NewStore(config.DefaultDir())
	return store.Names(), cobra.ShellCompDirectiveNoFileComp
}
```

**Step 2: Implement stop**

`cmd/stop.go`:
```go
package cmd

import (
	"fmt"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/docker"
	"github.com/joegoldin/claude-container/internal/tmux"
	"github.com/spf13/cobra"
)

var stopCmd = &cobra.Command{
	Use:               "stop <session>",
	Short:             "Stop a session (keep worktree)",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		store := config.NewStore(config.DefaultDir())

		if _, err := store.Get(name); err != nil {
			return fmt.Errorf("session %q not found", name)
		}

		if docker.IsRunning(name) {
			if err := docker.Stop(name); err != nil {
				return fmt.Errorf("stop container: %w", err)
			}
		}

		if tmux.Exists(name) {
			tmux.Kill(name)
		}

		fmt.Printf("Session %q stopped. Worktree preserved.\n", name)
		fmt.Printf("  Resume: claude-container attach %s\n", name)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(stopCmd)
}
```

**Step 3: Implement rm**

`cmd/rm.go`:
```go
package cmd

import (
	"fmt"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/docker"
	gitpkg "github.com/joegoldin/claude-container/internal/git"
	"github.com/joegoldin/claude-container/internal/tmux"
	"github.com/spf13/cobra"
)

var rmCmd = &cobra.Command{
	Use:               "rm <session>",
	Short:             "Remove a session (stop + delete worktree + branch)",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSessionNames,
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		store := config.NewStore(config.DefaultDir())

		sess, err := store.Get(name)
		if err != nil {
			return fmt.Errorf("session %q not found", name)
		}

		// Stop and remove container
		if docker.Exists(name) {
			docker.Remove(name)
		}

		// Kill tmux session
		if tmux.Exists(name) {
			tmux.Kill(name)
		}

		// Remove worktree and branch
		if sess.Branch != "" && sess.RepoPath != "" {
			if err := gitpkg.RemoveWorktree(sess.RepoPath, sess.WorktreePath, sess.Branch); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: worktree cleanup: %v\n", err)
			}
		}

		// Remove from store
		if err := store.Delete(name); err != nil {
			return fmt.Errorf("delete session record: %w", err)
		}

		fmt.Printf("Session %q removed.\n", name)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(rmCmd)
}
```

**Step 4: Verify build**

Run: `cd /home/joe/Development/claude-container && nix develop --command go build ./...`
Expected: success

**Step 5: Commit**

```bash
git add cmd/attach.go cmd/stop.go cmd/rm.go
git commit -m "feat: implement attach (Ctrl+Q detach), stop, and rm commands"
```

---

### Task 10: TUI Dashboard

**Files:**
- Create: `internal/tui/dashboard.go`
- Create: `internal/tui/styles.go`
- Modify: `cmd/root.go`

**Step 1: Create TUI styles**

`internal/tui/styles.go`:
```go
package tui

import "github.com/charmbracelet/lipgloss"

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("170"))

	selectedStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("229"))

	dimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241"))

	statusRunning = lipgloss.NewStyle().
			Foreground(lipgloss.Color("42"))

	statusStopped = lipgloss.NewStyle().
			Foreground(lipgloss.Color("196"))

	statusExited = lipgloss.NewStyle().
			Foreground(lipgloss.Color("214"))

	borderStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("238"))

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241"))

	previewTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("75"))
)
```

**Step 2: Create TUI dashboard model**

`internal/tui/dashboard.go`:
```go
package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/docker"
	"github.com/joegoldin/claude-container/internal/git"
	"github.com/joegoldin/claude-container/internal/tmux"
)

type sessionInfo struct {
	session *config.Session
	status  string
}

type dashboardModel struct {
	store     *config.Store
	sessions  []sessionInfo
	cursor    int
	width     int
	height    int
	preview   viewport.Model
	showDiff  bool
	quitting  bool
	attached  string // non-empty = attach to this session, then return
	creating  bool   // true = launch wizard
}

type tickMsg time.Time
type previewMsg struct {
	content string
}

func NewDashboard(store *config.Store) dashboardModel {
	vp := viewport.New(0, 0)
	return dashboardModel{
		store:   store,
		preview: vp,
	}
}

func (m dashboardModel) Init() tea.Cmd {
	return tea.Batch(
		m.refreshSessions(),
		m.tickCmd(),
	)
}

func (m dashboardModel) tickCmd() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m dashboardModel) refreshSessions() tea.Cmd {
	return func() tea.Msg {
		sessions := m.store.List()
		var infos []sessionInfo
		for _, s := range sessions {
			status := "stopped"
			tmuxAlive := tmux.Exists(s.Name)
			containerRunning := docker.IsRunning(s.Name)
			switch {
			case tmuxAlive && containerRunning:
				status = "running"
			case tmuxAlive:
				status = "exited"
			}
			infos = append(infos, sessionInfo{session: s, status: status})
		}
		return refreshMsg(infos)
	}
}

type refreshMsg []sessionInfo

func (m dashboardModel) fetchPreview() tea.Cmd {
	return func() tea.Msg {
		if m.cursor >= len(m.sessions) {
			return previewMsg{}
		}
		s := m.sessions[m.cursor]

		if m.showDiff {
			if s.session.WorktreePath != "" {
				diff, err := git.Diff(s.session.WorktreePath)
				if err != nil {
					return previewMsg{content: fmt.Sprintf("diff error: %v", err)}
				}
				if diff == "" {
					status, _ := git.Status(s.session.WorktreePath)
					if status != "" {
						return previewMsg{content: "Untracked changes:\n" + status}
					}
					return previewMsg{content: "(no changes)"}
				}
				return previewMsg{content: diff}
			}
			return previewMsg{content: "(no worktree)"}
		}

		if s.status == "running" || s.status == "exited" {
			content, err := tmux.CapturePane(s.session.Name)
			if err != nil {
				return previewMsg{content: fmt.Sprintf("capture error: %v", err)}
			}
			return previewMsg{content: content}
		}
		return previewMsg{content: "(session stopped)"}
	}
}

func (m dashboardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "j", "down":
			if m.cursor < len(m.sessions)-1 {
				m.cursor++
				return m, m.fetchPreview()
			}
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
				return m, m.fetchPreview()
			}
		case "enter":
			if m.cursor < len(m.sessions) {
				m.attached = m.sessions[m.cursor].session.Name
				return m, tea.Quit
			}
		case "n":
			m.creating = true
			return m, tea.Quit
		case "d":
			if m.cursor < len(m.sessions) {
				name := m.sessions[m.cursor].session.Name
				if docker.IsRunning(name) {
					docker.Stop(name)
				}
				if tmux.Exists(name) {
					tmux.Kill(name)
				}
				return m, m.refreshSessions()
			}
		case "x":
			if m.cursor < len(m.sessions) {
				s := m.sessions[m.cursor]
				if docker.Exists(s.session.Name) {
					docker.Remove(s.session.Name)
				}
				if tmux.Exists(s.session.Name) {
					tmux.Kill(s.session.Name)
				}
				if s.session.Branch != "" && s.session.RepoPath != "" {
					git.RemoveWorktree(s.session.RepoPath, s.session.WorktreePath, s.session.Branch)
				}
				m.store.Delete(s.session.Name)
				if m.cursor >= len(m.sessions)-1 && m.cursor > 0 {
					m.cursor--
				}
				return m, m.refreshSessions()
			}
		case "tab":
			m.showDiff = !m.showDiff
			return m, m.fetchPreview()
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.preview.Width = msg.Width/2 - 4
		m.preview.Height = msg.Height - 6
		return m, m.fetchPreview()

	case refreshMsg:
		m.sessions = []sessionInfo(msg)
		return m, m.fetchPreview()

	case previewMsg:
		m.preview.SetContent(msg.content)
		return m, nil

	case tickMsg:
		return m, tea.Batch(m.refreshSessions(), m.tickCmd())
	}

	return m, nil
}

func (m dashboardModel) View() string {
	if m.quitting {
		return ""
	}
	if m.width == 0 {
		return "Loading..."
	}

	leftWidth := m.width/3 - 2
	rightWidth := m.width - leftWidth - 6
	contentHeight := m.height - 4

	// Session list
	var listLines []string
	listLines = append(listLines, titleStyle.Render("Sessions"))
	listLines = append(listLines, "")

	for i, s := range m.sessions {
		name := s.session.Name
		var statusStr string
		switch s.status {
		case "running":
			statusStr = statusRunning.Render("[running]")
		case "exited":
			statusStr = statusExited.Render("[exited]")
		default:
			statusStr = statusStopped.Render("[stopped]")
		}

		line := fmt.Sprintf("  %s  %s", name, statusStr)
		if i == m.cursor {
			line = selectedStyle.Render(fmt.Sprintf("> %s  %s", name, statusStr))
		}
		listLines = append(listLines, line)
	}

	if len(m.sessions) == 0 {
		listLines = append(listLines, dimStyle.Render("  (no sessions)"))
		listLines = append(listLines, "")
		listLines = append(listLines, dimStyle.Render("  Press 'n' to create one"))
	}

	// Pad list to fill height
	for len(listLines) < contentHeight {
		listLines = append(listLines, "")
	}

	leftPanel := borderStyle.Width(leftWidth).Height(contentHeight).Render(
		strings.Join(listLines[:contentHeight], "\n"),
	)

	// Preview
	var previewTitle string
	if m.showDiff {
		previewTitle = previewTitleStyle.Render("Diff")
	} else {
		previewTitle = previewTitleStyle.Render("Preview")
	}

	previewContent := m.preview.View()
	rightLines := []string{previewTitle, "", previewContent}
	for len(rightLines) < contentHeight {
		rightLines = append(rightLines, "")
	}

	rightPanel := borderStyle.Width(rightWidth).Height(contentHeight).Render(
		strings.Join(rightLines[:contentHeight], "\n"),
	)

	main := lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, rightPanel)

	help := helpStyle.Render("n:new  enter:attach  d:stop  x:rm  tab:diff  q:quit")

	return lipgloss.JoinVertical(lipgloss.Left, main, help)
}

// Attached returns the session name to attach to (empty if none).
func (m dashboardModel) Attached() string {
	return m.attached
}

// Creating returns true if the user wants to create a new session.
func (m dashboardModel) Creating() bool {
	return m.creating
}
```

**Step 3: Wire dashboard into root command**

`cmd/root.go`:
```go
package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/tmux"
	"github.com/joegoldin/claude-container/internal/tui"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "claude-container",
	Short: "Run multiple Claude Code instances in isolated containers",
	Long:  `A CLI tool for running multiple Claude Code instances in isolated, sandboxed Docker containers with git worktree separation and a TUI dashboard.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		store := config.NewStore(config.DefaultDir())

		for {
			model := tui.NewDashboard(store)
			p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseAllMotion())

			finalModel, err := p.Run()
			if err != nil {
				return fmt.Errorf("tui: %w", err)
			}

			m := finalModel.(tui.DashboardModel)

			if m.Attached() != "" {
				ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
				tmux.Attach(ctx, m.Attached())
				cancel()
				continue // return to dashboard after detach
			}

			if m.Creating() {
				// TODO: launch wizard, for now print hint
				fmt.Println("Wizard not yet implemented. Use: claude-container new --name <name> --worktree <branch>")
				return nil
			}

			// User quit
			return nil
		}
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
```

**Step 4: Export the DashboardModel type for cmd to access**

The dashboard model type needs to be exported. Change `dashboardModel` to `DashboardModel` throughout `internal/tui/dashboard.go` (the struct name, constructor, and methods).

**Step 5: Verify build**

Run: `cd /home/joe/Development/claude-container && nix develop --command go build ./...`
Expected: success

**Step 6: Commit**

```bash
git add internal/tui/ cmd/root.go
git commit -m "feat: TUI dashboard with session list, preview pane, and diff view"
```

---

### Task 11: TUI Wizard

**Files:**
- Create: `internal/tui/wizard.go`
- Modify: `cmd/root.go`
- Modify: `cmd/new.go`

**Step 1: Implement wizard model**

`internal/tui/wizard.go`:
```go
package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	gitpkg "github.com/joegoldin/claude-container/internal/git"
)

type wizardStep int

const (
	stepName wizardStep = iota
	stepWorktree
	stepBranch
	stepMode
	stepPrompt
	stepDone
)

// WizardResult contains the user's choices from the wizard.
type WizardResult struct {
	Name       string
	Worktree   string // branch name, empty if no worktree
	From       string // base branch, empty for HEAD
	NoWorktree bool
	Yolo       bool
	Prompt     string
	Cancelled  bool
}

type WizardModel struct {
	step      wizardStep
	textInput textinput.Model
	result    WizardResult
	choices   []string
	cursor    int
	repoPath  string
	branches  []string
	width     int
	height    int
}

func NewWizard(repoPath string) WizardModel {
	ti := textinput.New()
	ti.Placeholder = "session-name"
	ti.Focus()

	var branches []string
	if repoPath != "" {
		branches, _ = gitpkg.ListBranches(repoPath)
	}

	return WizardModel{
		step:      stepName,
		textInput: ti,
		repoPath:  repoPath,
		branches:  branches,
	}
}

func (m WizardModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m WizardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.result.Cancelled = true
			return m, tea.Quit
		}

		switch m.step {
		case stepName:
			return m.updateName(msg)
		case stepWorktree:
			return m.updateWorktree(msg)
		case stepBranch:
			return m.updateBranch(msg)
		case stepMode:
			return m.updateMode(msg)
		case stepPrompt:
			return m.updatePrompt(msg)
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	}

	return m, nil
}

func (m WizardModel) updateName(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		val := strings.TrimSpace(m.textInput.Value())
		if val == "" {
			return m, nil
		}
		m.result.Name = val
		m.step = stepWorktree
		m.choices = []string{"New branch", "From existing branch", "No worktree (use current dir)"}
		m.cursor = 0
		return m, nil
	default:
		var cmd tea.Cmd
		m.textInput, cmd = m.textInput.Update(msg)
		return m, cmd
	}
}

func (m WizardModel) updateWorktree(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "j", "down":
		if m.cursor < len(m.choices)-1 {
			m.cursor++
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "enter":
		switch m.cursor {
		case 0: // New branch
			m.result.Worktree = m.result.Name
			m.step = stepMode
			m.choices = []string{"Normal", "Yolo (skip permissions)"}
			m.cursor = 0
		case 1: // From existing branch
			m.step = stepBranch
			m.choices = m.branches
			m.cursor = 0
		case 2: // No worktree
			m.result.NoWorktree = true
			m.step = stepMode
			m.choices = []string{"Normal", "Yolo (skip permissions)"}
			m.cursor = 0
		}
	}
	return m, nil
}

func (m WizardModel) updateBranch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "j", "down":
		if m.cursor < len(m.choices)-1 {
			m.cursor++
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "enter":
		if m.cursor < len(m.choices) {
			m.result.From = m.choices[m.cursor]
			m.result.Worktree = m.result.Name
			m.step = stepMode
			m.choices = []string{"Normal", "Yolo (skip permissions)"}
			m.cursor = 0
		}
	}
	return m, nil
}

func (m WizardModel) updateMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "j", "down":
		if m.cursor < len(m.choices)-1 {
			m.cursor++
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "enter":
		m.result.Yolo = m.cursor == 1
		m.step = stepPrompt
		m.textInput.SetValue("")
		m.textInput.Placeholder = "(optional) initial prompt for Claude"
		m.textInput.Focus()
		return m, textinput.Blink
	}
	return m, nil
}

func (m WizardModel) updatePrompt(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		m.result.Prompt = strings.TrimSpace(m.textInput.Value())
		m.step = stepDone
		return m, tea.Quit
	default:
		var cmd tea.Cmd
		m.textInput, cmd = m.textInput.Update(msg)
		return m, cmd
	}
}

func (m WizardModel) View() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render("New Session"))
	b.WriteString("\n\n")

	switch m.step {
	case stepName:
		b.WriteString("Session name:\n\n")
		b.WriteString(m.textInput.View())

	case stepWorktree:
		b.WriteString(fmt.Sprintf("Session: %s\n\n", selectedStyle.Render(m.result.Name)))
		b.WriteString("Worktree setup:\n\n")
		for i, c := range m.choices {
			cursor := "  "
			if i == m.cursor {
				cursor = selectedStyle.Render("> ")
				b.WriteString(cursor + selectedStyle.Render(c) + "\n")
			} else {
				b.WriteString(cursor + c + "\n")
			}
		}

	case stepBranch:
		b.WriteString("Select base branch:\n\n")
		visible := m.choices
		start := 0
		maxVisible := 15
		if len(visible) > maxVisible {
			start = m.cursor - maxVisible/2
			if start < 0 {
				start = 0
			}
			if start+maxVisible > len(visible) {
				start = len(visible) - maxVisible
			}
			visible = visible[start : start+maxVisible]
		}
		for i, c := range visible {
			idx := start + i
			if idx == m.cursor {
				b.WriteString(selectedStyle.Render(fmt.Sprintf("> %s", c)) + "\n")
			} else {
				b.WriteString(fmt.Sprintf("  %s\n", c))
			}
		}

	case stepMode:
		b.WriteString(fmt.Sprintf("Session: %s", selectedStyle.Render(m.result.Name)))
		if m.result.Worktree != "" {
			b.WriteString(fmt.Sprintf("  Branch: %s", selectedStyle.Render(m.result.Worktree)))
		}
		b.WriteString("\n\n")
		b.WriteString("Mode:\n\n")
		for i, c := range m.choices {
			if i == m.cursor {
				b.WriteString(selectedStyle.Render(fmt.Sprintf("> %s", c)) + "\n")
			} else {
				b.WriteString(fmt.Sprintf("  %s\n", c))
			}
		}

	case stepPrompt:
		b.WriteString("Initial prompt (Enter to skip):\n\n")
		b.WriteString(m.textInput.View())
	}

	b.WriteString("\n\n")
	b.WriteString(helpStyle.Render("enter: select  esc: cancel"))

	return lipgloss.NewStyle().Padding(1, 2).Render(b.String())
}

// Result returns the wizard's result.
func (m WizardModel) Result() WizardResult {
	return m.result
}
```

**Step 2: Update root.go to launch wizard and create session from result**

In the `m.Creating()` block in `cmd/root.go`, replace the TODO with:

```go
if m.Creating() {
	cwd, _ := os.Getwd()
	repoPath, _ := gitpkg.RepoRoot(cwd)
	wizModel := tui.NewWizard(repoPath)
	wp := tea.NewProgram(wizModel, tea.WithAltScreen())
	finalWiz, err := wp.Run()
	if err != nil {
		return fmt.Errorf("wizard: %w", err)
	}
	wm := finalWiz.(tui.WizardModel)
	r := wm.Result()
	if r.Cancelled {
		continue
	}
	err = createSession(createOpts{
		name:       r.Name,
		worktree:   r.Worktree,
		from:       r.From,
		noWorktree: r.NoWorktree,
		yolo:       r.Yolo,
		prompt:     r.Prompt,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
	}
	continue
}
```

Add imports for `gitpkg "github.com/joegoldin/claude-container/internal/git"` and `"github.com/joegoldin/claude-container/internal/tui"` (already there).

Also export `createSession` by renaming it to `CreateSession` in `cmd/new.go`, or keep it unexported since both are in the `cmd` package (same package, so it's fine).

**Step 3: Also launch wizard from `new` when no flags provided**

In `cmd/new.go`, update the RunE to detect when no flags are provided and launch the wizard:

```go
RunE: func(cmd *cobra.Command, args []string) error {
	// If no flags provided, launch wizard
	if newName == "" && newWorktree == "" && !newNoWorktree {
		cwd, _ := os.Getwd()
		repoPath, _ := gitpkg.RepoRoot(cwd)
		wizModel := tui.NewWizard(repoPath)
		p := tea.NewProgram(wizModel, tea.WithAltScreen())
		finalModel, err := p.Run()
		if err != nil {
			return fmt.Errorf("wizard: %w", err)
		}
		wm := finalModel.(tui.WizardModel)
		r := wm.Result()
		if r.Cancelled {
			return nil
		}
		return createSession(createOpts{
			name:       r.Name,
			worktree:   r.Worktree,
			from:       r.From,
			noWorktree: r.NoWorktree,
			yolo:       r.Yolo,
			prompt:     r.Prompt,
		})
	}
	return createSession(createOpts{
		name:       newName,
		worktree:   newWorktree,
		from:       newFrom,
		noWorktree: newNoWorktree,
		yolo:       newYolo,
		prompt:     newPrompt,
		cont:       newContinue,
	})
},
```

Add imports: `gitpkg "github.com/joegoldin/claude-container/internal/git"`, `"github.com/joegoldin/claude-container/internal/tui"`, `tea "github.com/charmbracelet/bubbletea"`, `"os"`.

**Step 4: Verify build**

Run: `cd /home/joe/Development/claude-container && nix develop --command go build ./...`
Expected: success

**Step 5: Commit**

```bash
git add internal/tui/wizard.go cmd/root.go cmd/new.go
git commit -m "feat: interactive wizard for new session creation"
```

---

### Task 12: Update vendorHash + Nix Build Verification

**Step 1: Vendor Go dependencies**

Run:
```bash
cd /home/joe/Development/claude-container && nix develop --command bash -c "go mod vendor && go mod tidy"
```

**Step 2: Compute vendorHash for flake.nix**

Run:
```bash
cd /home/joe/Development/claude-container && git add -A && nix build 2>&1 | grep "got:"
```

Take the hash from the error and update `vendorHash` in `flake.nix`.

**Step 3: Verify nix build**

Run: `cd /home/joe/Development/claude-container && nix build`
Expected: success, creates `result/bin/claude-container`

**Step 4: Verify completions work**

Run: `./result/bin/claude-container completion bash | head -5`
Expected: bash completion script output

**Step 5: Commit**

```bash
git add flake.nix go.mod go.sum vendor/
git commit -m "chore: vendor dependencies and fix nix build"
```

---

### Task 13: Dotfiles Integration

**Files:**
- Modify: `/home/joe/dotfiles/inputs.nix`
- Modify: `/home/joe/dotfiles/hosts/common/system/overlays/default.nix`
- Modify: `/home/joe/dotfiles/hosts/common/home/packages.nix`
- Remove: `/home/joe/dotfiles/docker/claude-code/` (moved to own repo)

**Step 1: Add flake input**

Add to `/home/joe/dotfiles/inputs.nix`:
```nix
claude-container = {
  url = "github:joegoldin/claude-container";
  inputs.nixpkgs.follows = "nixpkgs";
};
```

**Step 2: Add overlay**

Add to `/home/joe/dotfiles/hosts/common/system/overlays/default.nix`:
```nix
claude-container-packages = inputs.claude-container.overlays.default;
```

**Step 3: Add to packages**

Add `claude-container` to the package list in `/home/joe/dotfiles/hosts/common/home/packages.nix`.

**Step 4: Remove old docker/claude-code directory**

```bash
rm -rf /home/joe/dotfiles/docker/claude-code/
```

Also remove the `package.nix` reference if it's imported anywhere.

**Step 5: Commit dotfiles changes**

```bash
cd /home/joe/dotfiles
git add inputs.nix hosts/common/system/overlays/default.nix hosts/common/home/packages.nix
git rm -r docker/claude-code/
git commit -m "feat: replace inline claude-container with dedicated flake"
```

---

### Task 14: Push and Verify

**Step 1: Push claude-container repo**

```bash
cd /home/joe/Development/claude-container && git push origin main
```

**Step 2: Update dotfiles flake lock**

```bash
cd /home/joe/dotfiles && nix flake update claude-container
```

**Step 3: Verify dotfiles build**

```bash
cd /home/joe/dotfiles && just build
```

**Step 4: Commit dotfiles lock update**

```bash
cd /home/joe/dotfiles && git add flake.lock && git commit -m "chore: add claude-container to flake.lock"
```
