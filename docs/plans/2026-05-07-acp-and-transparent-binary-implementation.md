# Transparent Binary + ACP Integration Implementation Plan

> **SUPERSEDED 2026-05-17:** ACP scope was dropped. See
> `docs/plans/2026-05-17-transparent-binary-implementation.md` for the
> active plan. This file is preserved for historical context.


> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reshape `claude-container` so bare invocation creates a sandboxed Claude session and add an `acp` subcommand for ACP-compatible IDEs (Zed). Introduce `internal/session/` as the single launcher that all commands delegate to.

**Architecture:** New `internal/session/` package owns workspace resolution, container lifecycle, and four output adapters (TTY, ACP, task, background). Existing `cmd/*.go` files become thin wrappers around `session.Launch()`. Bare-invocation behavior moves from "launch TUI dashboard" to "create a sandboxed session"; the dashboard relocates to `claude-container tui`.

**Tech Stack:** Go 1.22+, Cobra, Bubble Tea, Docker (rootless and standard), Nix (`dockerTools.buildLayeredImage`), mitmproxy (existing per-session proxy).

**Spec:** `docs/plans/2026-05-07-acp-and-transparent-binary-design.md`

---

## Working Conventions

- Run `nix develop` once at the start of each session to get `go`, `docker`, `git`, `nix` on PATH.
- Run `go build ./...` after every code change to catch compile errors early.
- Run `go test ./...` after every task to catch regressions.
- Integration tests live behind the `integration` build tag (`go test -tags=integration ./...`); run them when you finish a phase, not per task.
- Commits should be tightly scoped and use the existing imperative style (`feat:`, `fix:`, `refactor:`, `test:`, `docs:`). Don't include LLM marker phrases.

---

## Phase 1 — Foundation utilities

These tasks add new code that does not change existing behavior. After Phase 1, the codebase still builds and passes all tests, with no commands altered.

### Task 1: Add `.gitignore` helper to `internal/git`

**Files:**
- Create: `internal/git/gitignore.go`
- Test: `internal/git/gitignore_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/git/gitignore_test.go
package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestEnsureIgnored_AlreadyIgnored(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".worktrees/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	added, err := EnsureIgnored(dir, ".worktrees")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if added {
		t.Fatal("expected added=false (already ignored)")
	}
}

func TestEnsureIgnored_NotIgnored_AppendsAndReturnsTrue(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	added, err := EnsureIgnored(dir, ".worktrees")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !added {
		t.Fatal("expected added=true (newly ignored)")
	}
	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if !strings.Contains(string(data), ".worktrees/") {
		t.Fatalf(".gitignore missing entry, got: %q", string(data))
	}
}

func TestEnsureIgnored_GitignoreExistsButMissingEntry(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("node_modules/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	added, err := EnsureIgnored(dir, ".worktrees")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !added {
		t.Fatal("expected added=true")
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	got := string(data)
	if !strings.Contains(got, "node_modules/") || !strings.Contains(got, ".worktrees/") {
		t.Fatalf("expected both entries; got %q", got)
	}
}
```

Add this import line at the top alongside other imports: `"os/exec"`.

- [ ] **Step 2: Run test to verify it fails**

```sh
go test ./internal/git/ -run TestEnsureIgnored -v
```

Expected: FAIL — `EnsureIgnored` undefined.

- [ ] **Step 3: Write minimal implementation**

```go
// internal/git/gitignore.go
package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// EnsureIgnored makes sure path (relative to repoDir, e.g. ".worktrees") is
// ignored by git. Returns added=true if it appended a new line to .gitignore,
// false if the path was already ignored. Does not commit the change.
//
// If .gitignore exists but is read-only, returns the underlying error so
// callers can fall back to an alternate location.
func EnsureIgnored(repoDir, path string) (added bool, err error) {
	// Quick check: is it already ignored?
	cmd := exec.Command("git", "check-ignore", "-q", path)
	cmd.Dir = repoDir
	if cmd.Run() == nil {
		return false, nil
	}

	gitignore := filepath.Join(repoDir, ".gitignore")
	entry := strings.TrimRight(path, "/") + "/"

	// Read existing content (if any) to make sure we add a leading newline
	// when the file does not end with one.
	prefix := ""
	if data, err := os.ReadFile(gitignore); err == nil {
		if len(data) > 0 && data[len(data)-1] != '\n' {
			prefix = "\n"
		}
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("read .gitignore: %w", err)
	}

	f, err := os.OpenFile(gitignore, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return false, fmt.Errorf("open .gitignore: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(prefix + entry + "\n"); err != nil {
		return false, fmt.Errorf("append .gitignore: %w", err)
	}
	return true, nil
}
```

Add the missing `exec` import to the test file (move the inline import to the top of `gitignore_test.go`):

```go
import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)
```

- [ ] **Step 4: Run test to verify it passes**

```sh
go test ./internal/git/ -run TestEnsureIgnored -v
```

Expected: PASS — three subtests green.

- [ ] **Step 5: Commit**

```sh
git add internal/git/gitignore.go internal/git/gitignore_test.go
git commit -m "feat(git): add EnsureIgnored helper for .gitignore mutation"
```

---

### Task 2: Add `Mode` field to `config.Session`

**Files:**
- Modify: `internal/config/config.go:27-50` (Session struct)
- Test: `internal/config/config_test.go` (add a new test case)

- [ ] **Step 1: Write the failing test**

Append to `internal/config/config_test.go`:

```go
func TestSessionUnmarshal_OldRecordDefaultsModeToTTY(t *testing.T) {
	dir := t.TempDir()
	// Old records had no `mode` field. Write a sessions.json missing the field.
	old := `[{"name":"old","branch":"main","worktree_path":"","repo_path":"/tmp/x","container_name":"c","yolo":false,"created_at":"2025-01-01T00:00:00Z"}]`
	if err := os.WriteFile(filepath.Join(dir, "sessions.json"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}

	store := NewStore(dir)
	s, err := store.Get("old")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if s.Mode != "tty" {
		t.Fatalf("expected Mode=tty for old record, got %q", s.Mode)
	}
}

func TestSessionRoundTrip_PreservesMode(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	if err := store.Save(&Session{Name: "acp1", Mode: "acp", CreatedAt: time.Now()}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Get("acp1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Mode != "acp" {
		t.Fatalf("expected Mode=acp, got %q", got.Mode)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```sh
go test ./internal/config/ -run "TestSession(Unmarshal_OldRecordDefaultsModeToTTY|RoundTrip_PreservesMode)" -v
```

Expected: FAIL — `Session.Mode` undefined.

- [ ] **Step 3: Add the `Mode` field and the unmarshal default**

Edit `internal/config/config.go`. In the `Session` struct (lines 27-50), add the `Mode` field between `AutoRemove` and `ResumeID`:

```go
	AutoRemove      bool      `json:"auto_remove,omitempty"`
	Mode            string    `json:"mode,omitempty"` // tty, acp, task, background; default tty for old records
	ResumeID        string    `json:"resume_id,omitempty"`
```

Modify `loadLocked` (around line 480) so old records get `Mode = "tty"` after unmarshal. Replace the body with:

```go
func (s *Store) loadLocked() ([]*Session, error) {
	path := filepath.Join(s.dir, SessionFile)

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var sessions []*Session
	if err := json.Unmarshal(data, &sessions); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for _, sess := range sessions {
		if sess.Mode == "" {
			sess.Mode = "tty"
		}
	}
	return sessions, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

```sh
go test ./internal/config/ -v
```

Expected: PASS — the two new tests, plus existing tests, green.

- [ ] **Step 5: Commit**

```sh
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add Mode field to Session with tty default for old records"
```

---

### Task 3: Add `Mode` to `docker.RunOpts` and pass `CLAUDE_CONTAINER_MODE` env

**Files:**
- Modify: `internal/docker/docker.go:82-105` (RunOpts struct), `internal/docker/docker.go:110-235` (RunArgs)
- Test: `internal/docker/docker_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/docker/docker_test.go`:

```go
func TestRunArgs_PassesCLAUDE_CONTAINER_MODE(t *testing.T) {
	args := RunArgs(RunOpts{
		Name:      "s1",
		Workspace: "/tmp/ws",
		ConfigDir: "/tmp/cfg",
		Mode:      "acp",
	}, true)
	if !containsEnv(args, "CLAUDE_CONTAINER_MODE=acp") {
		t.Fatalf("expected CLAUDE_CONTAINER_MODE=acp env, got: %v", args)
	}
}

func TestRunArgs_DefaultModeIsTTY(t *testing.T) {
	args := RunArgs(RunOpts{
		Name:      "s1",
		Workspace: "/tmp/ws",
		ConfigDir: "/tmp/cfg",
	}, true)
	if !containsEnv(args, "CLAUDE_CONTAINER_MODE=tty") {
		t.Fatalf("expected CLAUDE_CONTAINER_MODE=tty env (default), got: %v", args)
	}
}

// containsEnv reports whether docker args contain a -e value matching want.
func containsEnv(args []string, want string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-e" && args[i+1] == want {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run test to verify it fails**

```sh
go test ./internal/docker/ -run TestRunArgs -v
```

Expected: FAIL — `RunOpts.Mode` undefined.

- [ ] **Step 3: Add the field + env var**

In `internal/docker/docker.go`, add the field to `RunOpts` (around line 105):

```go
	// ProxyEnabled is true when this container should join the per-session
	// proxy's network namespace.
	ProxyEnabled bool

	// Mode is one of "tty", "acp", "task", "background". Passed to the
	// container as CLAUDE_CONTAINER_MODE. Empty defaults to "tty".
	Mode string
}
```

In `RunArgs`, near the existing `-e CLAUDE_CONFIG_DIR=/claude` block (around line 196), set the env:

```go
	args = append(args,
		"-v", opts.ConfigDir+":/claude",
		"-e", "CLAUDE_CONFIG_DIR=/claude",
		"-e", fmt.Sprintf("USER_UID=%d", opts.UID),
		"-e", fmt.Sprintf("USER_GID=%d", opts.GID),
	)

	mode := opts.Mode
	if mode == "" {
		mode = "tty"
	}
	args = append(args, "-e", "CLAUDE_CONTAINER_MODE="+mode)
```

Do the same in `TaskRunArgs` so task containers carry the env too:

```go
	args = append(args,
		"-v", opts.ConfigDir+":/claude",
		"-e", "CLAUDE_CONFIG_DIR=/claude",
		"-e", fmt.Sprintf("USER_UID=%d", opts.UID),
		"-e", fmt.Sprintf("USER_GID=%d", opts.GID),
	)
	mode := opts.Mode
	if mode == "" {
		mode = "task"
	}
	args = append(args, "-e", "CLAUDE_CONTAINER_MODE="+mode)
```

- [ ] **Step 4: Run test to verify it passes**

```sh
go test ./internal/docker/ -v
```

Expected: PASS — including new tests and all existing tests.

- [ ] **Step 5: Commit**

```sh
git add internal/docker/docker.go internal/docker/docker_test.go
git commit -m "feat(docker): pass CLAUDE_CONTAINER_MODE env var to container"
```

---

### Task 4: Add `ACPRunArgs` for the no-PTY ephemeral ACP container

**Files:**
- Modify: `internal/docker/docker.go` (add new function alongside RunArgs/TaskRunArgs)
- Test: `internal/docker/docker_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/docker/docker_test.go`:

```go
func TestACPRunArgs_UsesRmAndStdinNoTTY(t *testing.T) {
	args := ACPRunArgs(RunOpts{
		Name:      "acp1",
		Workspace: "/tmp/ws",
		ConfigDir: "/tmp/cfg",
		Mode:      "acp",
	})

	if args[0] != "run" {
		t.Fatalf("expected first arg to be 'run', got %q", args[0])
	}
	hasRm := false
	hasI := false
	hasIT := false
	for _, a := range args {
		if a == "--rm" {
			hasRm = true
		}
		if a == "-i" {
			hasI = true
		}
		if a == "-it" || a == "-dit" {
			hasIT = true
		}
	}
	if !hasRm {
		t.Fatal("expected --rm")
	}
	if !hasI {
		t.Fatal("expected -i")
	}
	if hasIT {
		t.Fatal("expected no -it/-dit (ACP must not allocate a TTY)")
	}
	// Final arg should be the agent binary, not "claude".
	if args[len(args)-1] != "claude-agent-acp" {
		t.Fatalf("expected last arg = claude-agent-acp, got %q", args[len(args)-1])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```sh
go test ./internal/docker/ -run TestACPRunArgs -v
```

Expected: FAIL — `ACPRunArgs` undefined.

- [ ] **Step 3: Implement `ACPRunArgs`**

Append to `internal/docker/docker.go`, after `TaskRunArgs`:

```go
// ACPRunArgs returns docker run arguments for an ephemeral ACP container.
// The container runs `claude-agent-acp` with --rm and -i (no TTY) so the
// host can bridge stdio to the JSON-RPC client.
//
// All proxy / config / mounts behave the same as RunArgs; the only differences
// are the lack of a PTY, the use of --rm, and the agent binary as the command.
func ACPRunArgs(opts RunOpts) []string {
	name := ContainerName(opts.Name)

	args := []string{
		"run",
		"--name", name,
		"--rm",
		"-i",
	}

	// Mount primary workspace (ACP never uses worktree mode, so opts.Workspace
	// is the host pwd in practice). The same skip logic applies for symmetry.
	if opts.Workspace != "" && opts.WorktreeBranch == "" {
		args = append(args, "-v", opts.Workspace+":/workspace")
	}

	for _, ws := range opts.ExtraWorkspaces {
		base := filepath.Base(ws)
		args = append(args, "-v", ws+":/workspace/"+base)
	}

	if opts.ProxyEnabled {
		proxyContainer := "claude-proxy_" + opts.Name
		args = append(args,
			"--network", "container:"+proxyContainer,
			"-e", "CLAUDE_PROXY_DASHBOARD_URL=http://127.0.0.1:8081",
		)
		if opts.ProxyDashboardPort > 0 {
			args = append(args,
				"-e", fmt.Sprintf("CLAUDE_PROXY_DASHBOARD_HOST_URL=http://localhost:%d", opts.ProxyDashboardPort),
			)
		}
		if opts.ProxyCACertDir != "" {
			args = append(args,
				"-v", opts.ProxyCACertDir+":/proxy-ca:ro",
				"-e", "SSL_CERT_FILE=/proxy-ca/mitmproxy-ca-cert.pem",
				"-e", "NIX_SSL_CERT_FILE=/proxy-ca/mitmproxy-ca-cert.pem",
				"-e", "NODE_EXTRA_CA_CERTS=/proxy-ca/mitmproxy-ca-cert.pem",
			)
		}
	}

	args = append(args,
		"-v", opts.ConfigDir+":/claude",
		"-e", "CLAUDE_CONFIG_DIR=/claude",
		"-e", fmt.Sprintf("USER_UID=%d", opts.UID),
		"-e", fmt.Sprintf("USER_GID=%d", opts.GID),
	)

	mode := opts.Mode
	if mode == "" {
		mode = "acp"
	}
	args = append(args, "-e", "CLAUDE_CONTAINER_MODE="+mode)

	args = append(args, "-v", "claude-nix-store:/nix/var")

	if len(opts.Packages) > 0 {
		args = append(args, "-e", "EXTRA_PACKAGES="+strings.Join(opts.Packages, ","))
	}

	for _, f := range opts.HostClaudeFiles {
		args = append(args, "-v", f+":/mnt/claude-host/"+filepath.Base(f)+":ro")
	}

	args = append(args, ImageTag(), "claude-agent-acp")
	return args
}
```

- [ ] **Step 4: Run test to verify it passes**

```sh
go test ./internal/docker/ -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git add internal/docker/docker.go internal/docker/docker_test.go
git commit -m "feat(docker): add ACPRunArgs for ephemeral no-PTY ACP container"
```

---

## Phase 2 — Session launcher core

After Phase 2, `internal/session/` exists and has unit tests, but no `cmd/*.go` calls it yet. Existing commands are untouched.

### Task 5: `session.Opts` + per-mode defaults

**Files:**
- Create: `internal/session/options.go`
- Test: `internal/session/options_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/session/options_test.go
package session

import "testing"

func TestApplyDefaults_TTY(t *testing.T) {
	o := Opts{Mode: ModeTTY}
	o.ApplyDefaults()
	if o.Profile != "default" {
		t.Errorf("Profile: want %q, got %q", "default", o.Profile)
	}
	if o.AutoRemove {
		t.Error("AutoRemove should be false for TTY")
	}
	if o.Yolo {
		t.Error("Yolo should be false for TTY")
	}
}

func TestApplyDefaults_ACP(t *testing.T) {
	o := Opts{Mode: ModeACP}
	o.ApplyDefaults()
	if o.Profile != "med" {
		t.Errorf("Profile: want %q, got %q", "med", o.Profile)
	}
	if !o.AutoRemove {
		t.Error("AutoRemove should be true for ACP")
	}
	if o.NoWorktree != true {
		t.Error("NoWorktree should be true for ACP (always pwd passthrough)")
	}
}

func TestApplyDefaults_Task(t *testing.T) {
	o := Opts{Mode: ModeTask}
	o.ApplyDefaults()
	if !o.AutoRemove {
		t.Error("AutoRemove should be true for Task by default")
	}
}

func TestApplyDefaults_Background(t *testing.T) {
	o := Opts{Mode: ModeBackground}
	o.ApplyDefaults()
	if o.AutoRemove {
		t.Error("AutoRemove should be false for Background")
	}
}

func TestApplyDefaults_RespectsExplicitProfile(t *testing.T) {
	o := Opts{Mode: ModeACP, Profile: "high"}
	o.ApplyDefaults()
	if o.Profile != "high" {
		t.Errorf("explicit profile must not be overridden: got %q", o.Profile)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```sh
go test ./internal/session/ -v
```

Expected: FAIL — package doesn't exist yet.

- [ ] **Step 3: Implement `options.go`**

```go
// internal/session/options.go
package session

import "fmt"

// Mode identifies what kind of output adapter the caller wants.
type Mode string

const (
	ModeTTY        Mode = "tty"
	ModeACP        Mode = "acp"
	ModeTask       Mode = "task"
	ModeBackground Mode = "background"
)

// WorktreeMode selects how ResolveWorkspace handles git repos.
type WorktreeMode int

const (
	// WorktreeAuto creates a worktree in <repo>/.worktrees/<name>/ when in a
	// git repo, otherwise pwd passthrough. Used by bare invoke.
	WorktreeAuto WorktreeMode = iota
	// WorktreeAlways creates a worktree even if cwd is the repo root. Used by `work`.
	WorktreeAlways
	// WorktreeNever forces pwd passthrough. Used by `run` and `acp`.
	WorktreeNever
)

// Opts holds everything Launch needs to start a session.
type Opts struct {
	Name string // session name; auto-generated if empty
	Mode Mode

	// Workspace controls.
	Cwd          string
	WorktreeMode WorktreeMode
	NoWorktree   bool   // legacy alias used by --no-worktree flag; if true forces WorktreeNever
	From         string // base ref for worktree branch
	WorktreeName string // explicit branch name; empty = use Name

	// Sandbox profile and overrides.
	Profile       string
	Yolo          bool
	AllowDomains  []string
	DenyPaths     []string
	AllowCommands []string
	DenyCommands  []string
	AllowPerms    []string
	DenyPerms     []string

	// Mounts.
	Mounts        []string // -w (ad-hoc paths)
	WorkspaceName string   // -W (named workspace)

	// Container behavior.
	AutoRemove bool
	Background bool

	// Claude Code controls.
	Prompt   string
	Resume   string
	Continue bool

	// Packages and proxy.
	Packages        []string
	ProxySeedPreset string
	ProxyPort       int
}

// ApplyDefaults fills in per-mode defaults for fields the caller did not set.
// Fields explicitly set by the caller are preserved.
func (o *Opts) ApplyDefaults() {
	if o.NoWorktree {
		o.WorktreeMode = WorktreeNever
	}
	switch o.Mode {
	case ModeACP:
		// ACP is always pwd passthrough and always ephemeral.
		o.WorktreeMode = WorktreeNever
		o.NoWorktree = true
		if !o.AutoRemove {
			o.AutoRemove = true
		}
		if o.Profile == "" {
			o.Profile = "med"
		}
	case ModeTask:
		if !o.AutoRemove {
			o.AutoRemove = true
		}
		if o.Profile == "" {
			o.Profile = "default"
		}
	case ModeBackground, ModeTTY, "":
		if o.Profile == "" {
			o.Profile = "default"
		}
	}
}

// Validate returns an error when Opts is internally inconsistent.
func (o *Opts) Validate() error {
	if o.Resume != "" && o.Continue {
		return fmt.Errorf("resume and continue cannot both be set")
	}
	switch o.Mode {
	case ModeTTY, ModeACP, ModeTask, ModeBackground, "":
		// ok
	default:
		return fmt.Errorf("unknown mode %q", o.Mode)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

```sh
go test ./internal/session/ -v
```

Expected: PASS — five tests green.

- [ ] **Step 5: Commit**

```sh
git add internal/session/options.go internal/session/options_test.go
git commit -m "feat(session): add Opts struct with per-mode defaults"
```

---

### Task 6: `session.ResolveWorkspace` — non-git case

**Files:**
- Create: `internal/session/workspace.go`
- Test: `internal/session/workspace_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/session/workspace_test.go
package session

import (
	"testing"
)

func TestResolveWorkspace_NonGit_PwdPassthrough(t *testing.T) {
	dir := t.TempDir() // not a git repo
	ws, err := ResolveWorkspace(dir, Opts{Mode: ModeTTY, WorktreeMode: WorktreeAuto, Name: "s1"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if ws.HostPath != dir {
		t.Errorf("HostPath: want %q, got %q", dir, ws.HostPath)
	}
	if ws.RepoPath != "" {
		t.Errorf("RepoPath should be empty for non-git, got %q", ws.RepoPath)
	}
	if ws.Worktree {
		t.Error("Worktree should be false for non-git")
	}
	if ws.Branch != "" {
		t.Error("Branch should be empty for non-git")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```sh
go test ./internal/session/ -run TestResolveWorkspace -v
```

Expected: FAIL — `ResolveWorkspace` undefined.

- [ ] **Step 3: Implement skeleton + non-git case**

```go
// internal/session/workspace.go
package session

import (
	"fmt"
	"os"
	"path/filepath"

	gitpkg "github.com/joegoldin/claude-container/internal/git"
)

// Workspace is the result of resolving how a session's working directory
// should be wired up into the container.
type Workspace struct {
	HostPath string // path mounted as /workspace; empty when worktree mode (entrypoint creates from /mnt/repo)
	RepoPath string // git toplevel; empty if not a git repo
	Worktree bool   // true when entrypoint should create a worktree
	Branch   string // worktree branch name; empty for pwd passthrough
}

// ResolveWorkspace decides whether to create a worktree or pwd-passthrough.
// See spec at docs/plans/2026-05-07-acp-and-transparent-binary-design.md
// (Workspace resolution).
func ResolveWorkspace(cwd string, opts Opts) (Workspace, error) {
	repoRoot, repoErr := gitpkg.RepoRoot(cwd)
	inGit := repoErr == nil && repoRoot != ""

	// 1. Forced pwd passthrough.
	if opts.WorktreeMode == WorktreeNever || opts.Mode == ModeACP {
		ws := Workspace{HostPath: cwd}
		if inGit {
			ws.RepoPath = repoRoot
		}
		return ws, nil
	}

	if !inGit {
		return Workspace{HostPath: cwd}, nil
	}

	// 2. Git repo + worktree mode (auto or always). Implemented in Task 7.
	return Workspace{}, fmt.Errorf("worktree resolution not implemented yet")
}

// ensureDir is a small helper used by worktree path picking.
func ensureDir(p string) error {
	if _, err := os.Stat(p); err == nil {
		return nil
	}
	return os.MkdirAll(p, 0o755)
}

var _ = filepath.Base // keep import; used by later tasks
```

- [ ] **Step 4: Run test to verify it passes**

```sh
go test ./internal/session/ -run TestResolveWorkspace_NonGit -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git add internal/session/workspace.go internal/session/workspace_test.go
git commit -m "feat(session): ResolveWorkspace non-git pwd passthrough case"
```

---

### Task 7: `session.ResolveWorkspace` — git + already-ignored `.worktrees`

**Files:**
- Modify: `internal/session/workspace.go`
- Modify: `internal/session/workspace_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/session/workspace_test.go`:

```go
import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func setupGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-q", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func TestResolveWorkspace_Git_DotWorktreesAlreadyIgnored(t *testing.T) {
	repo := setupGitRepo(t)
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".worktrees/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, err := ResolveWorkspace(repo, Opts{Mode: ModeTTY, WorktreeMode: WorktreeAuto, Name: "myrepo-foo-bar"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if ws.RepoPath != repo {
		t.Errorf("RepoPath: want %q, got %q", repo, ws.RepoPath)
	}
	if !ws.Worktree {
		t.Error("Worktree should be true")
	}
	if ws.Branch != "myrepo-foo-bar" {
		t.Errorf("Branch: want session name, got %q", ws.Branch)
	}
	// HostPath is empty in worktree mode (entrypoint creates from /mnt/repo).
	if ws.HostPath != "" {
		t.Errorf("HostPath should be empty for worktree, got %q", ws.HostPath)
	}
	// .worktrees should not have been created on the host yet.
	if _, err := os.Stat(filepath.Join(repo, ".worktrees")); err == nil {
		t.Error(".worktrees should not be created here (entrypoint does it)")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```sh
go test ./internal/session/ -run TestResolveWorkspace_Git_DotWorktreesAlreadyIgnored -v
```

Expected: FAIL.

- [ ] **Step 3: Implement git+ignored case**

Replace the `// 2. Git repo` placeholder section in `internal/session/workspace.go` with:

```go
	// 2. Git repo + worktree mode.
	// Pick the worktree base dir, then ensure it's gitignored.
	base, err := pickWorktreeBase(repoRoot)
	if err != nil {
		return Workspace{}, fmt.Errorf("pick worktree base: %w", err)
	}

	added, ignErr := gitpkg.EnsureIgnored(repoRoot, base)
	if ignErr != nil {
		// .gitignore not writable — fall back to global location (Task 9).
		return Workspace{}, fmt.Errorf("ensure ignored: %w (fallback not implemented)", ignErr)
	}
	if added {
		fmt.Fprintf(os.Stderr, "note: added %s/ to .gitignore — commit when convenient\n", base)
	}

	branch := opts.WorktreeName
	if branch == "" {
		branch = opts.Name
	}
	if branch == "" {
		return Workspace{}, fmt.Errorf("worktree mode requires a session name")
	}

	return Workspace{
		RepoPath: repoRoot,
		Worktree: true,
		Branch:   branch,
	}, nil
}

// pickWorktreeBase returns the base directory name (relative to repoRoot)
// where new worktrees should live. Priority: existing .worktrees, then
// existing worktrees, then create .worktrees.
func pickWorktreeBase(repoRoot string) (string, error) {
	candidates := []string{".worktrees", "worktrees"}
	for _, c := range candidates {
		if info, err := os.Stat(filepath.Join(repoRoot, c)); err == nil && info.IsDir() {
			return c, nil
		}
	}
	// Create the hidden default.
	if err := os.MkdirAll(filepath.Join(repoRoot, ".worktrees"), 0o755); err != nil {
		return "", err
	}
	return ".worktrees", nil
}
```

Remove the `var _ = filepath.Base` line — it's used now.

- [ ] **Step 4: Run test to verify it passes**

```sh
go test ./internal/session/ -v
```

Expected: PASS — both `_NonGit_` and `_DotWorktreesAlreadyIgnored` green.

- [ ] **Step 5: Commit**

```sh
git add internal/session/workspace.go internal/session/workspace_test.go
git commit -m "feat(session): ResolveWorkspace git + ignored .worktrees case"
```

---

### Task 8: `session.ResolveWorkspace` — git + needs gitignore mutation

**Files:**
- Modify: `internal/session/workspace_test.go`

The implementation already handles this via `EnsureIgnored` from Task 1. We just need a test to lock it down.

- [ ] **Step 1: Write the test**

Append to `internal/session/workspace_test.go`:

```go
import "strings"

func TestResolveWorkspace_Git_AppendsGitignore(t *testing.T) {
	repo := setupGitRepo(t)
	// .gitignore exists but does NOT include .worktrees.
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("node_modules/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, err := ResolveWorkspace(repo, Opts{Mode: ModeTTY, WorktreeMode: WorktreeAuto, Name: "s1"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !ws.Worktree {
		t.Fatal("expected worktree mode")
	}
	data, _ := os.ReadFile(filepath.Join(repo, ".gitignore"))
	if !strings.Contains(string(data), ".worktrees/") {
		t.Fatalf(".gitignore was not updated: %q", string(data))
	}
}
```

(If `strings` is already imported, skip the import line.)

- [ ] **Step 2: Run test to verify it passes**

```sh
go test ./internal/session/ -v
```

Expected: PASS — relies on the `EnsureIgnored` from Task 1.

- [ ] **Step 3: Commit**

```sh
git add internal/session/workspace_test.go
git commit -m "test(session): assert .gitignore append for ResolveWorkspace"
```

---

### Task 9: `session.ResolveWorkspace` — fallback when `.gitignore` is not writable

**Files:**
- Modify: `internal/session/workspace.go`
- Modify: `internal/session/workspace_test.go`
- Modify: `internal/config/config.go` (export a helper for the fallback path)

- [ ] **Step 1: Write the failing test**

Append to `internal/session/workspace_test.go`:

```go
func TestResolveWorkspace_Git_GitignoreReadOnly_FallsBackToGlobal(t *testing.T) {
	repo := setupGitRepo(t)
	gi := filepath.Join(repo, ".gitignore")
	if err := os.WriteFile(gi, []byte("# placeholder\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(gi, 0o644) // so t.TempDir cleanup works

	tmpHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmpHome)

	ws, err := ResolveWorkspace(repo, Opts{Mode: ModeTTY, WorktreeMode: WorktreeAuto, Name: "fallback-foo"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !ws.Worktree {
		t.Fatal("expected Worktree=true")
	}
	// HostPath should be the global fallback path.
	want := filepath.Join(tmpHome, "claude-container", "worktrees")
	if !strings.HasPrefix(ws.HostPath, want) {
		t.Fatalf("expected fallback under %q, got %q", want, ws.HostPath)
	}
	if ws.Branch != "fallback-foo" {
		t.Errorf("Branch: want %q, got %q", "fallback-foo", ws.Branch)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```sh
go test ./internal/session/ -run TestResolveWorkspace_Git_GitignoreReadOnly -v
```

Expected: FAIL — fallback path not yet implemented.

- [ ] **Step 3: Implement fallback**

Modify `internal/session/workspace.go`. Replace the EnsureIgnored block with:

```go
	added, ignErr := gitpkg.EnsureIgnored(repoRoot, base)
	if ignErr != nil {
		// .gitignore not writable — fall back to global location.
		fallback, ferr := globalWorktreeDir(repoRoot, opts)
		if ferr != nil {
			return Workspace{}, fmt.Errorf("ensure ignored: %w; fallback failed: %v", ignErr, ferr)
		}
		fmt.Fprintf(os.Stderr, "note: %s/.gitignore not writable; using fallback %s\n", repoRoot, fallback)
		branch := opts.WorktreeName
		if branch == "" {
			branch = opts.Name
		}
		return Workspace{
			HostPath: fallback,
			RepoPath: repoRoot,
			Worktree: true,
			Branch:   branch,
		}, nil
	}
	if added {
		fmt.Fprintf(os.Stderr, "note: added %s/ to .gitignore — commit when convenient\n", base)
	}
```

Add the `globalWorktreeDir` helper at the bottom of `workspace.go`:

```go
import (
	// existing imports...
	"crypto/sha256"
	"encoding/hex"
)

// globalWorktreeDir returns ~/.local/share/claude-container/worktrees/<repo-id>
// (respecting XDG_DATA_HOME), creating it if missing.
func globalWorktreeDir(repoRoot string, opts Opts) (string, error) {
	xdg := os.Getenv("XDG_DATA_HOME")
	if xdg == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		xdg = filepath.Join(home, ".local", "share")
	}
	hash := sha256.Sum256([]byte(repoRoot))
	id := hex.EncodeToString(hash[:])[:12]
	branch := opts.WorktreeName
	if branch == "" {
		branch = opts.Name
	}
	dir := filepath.Join(xdg, "claude-container", "worktrees", id, branch)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	return dir, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

```sh
go test ./internal/session/ -v
```

Expected: PASS — all three workspace tests green.

- [ ] **Step 5: Commit**

```sh
git add internal/session/workspace.go internal/session/workspace_test.go
git commit -m "feat(session): fallback worktree dir when .gitignore is read-only"
```

---

### Task 10: `session.ResolveWorkspace` — `--from`, ACP overrides, `--no-worktree`

**Files:**
- Modify: `internal/session/workspace_test.go`

The implementation already covers these via `ApplyDefaults` (ACP forces NoWorktree) and via `Opts.From` not requiring host-side action (the entrypoint reads `WORKTREE_FROM`). Just add tests to lock down behavior.

- [ ] **Step 1: Write the tests**

Append to `internal/session/workspace_test.go`:

```go
func TestResolveWorkspace_ACPMode_ForcesPwd(t *testing.T) {
	repo := setupGitRepo(t)
	o := Opts{Mode: ModeACP, Name: "x"}
	o.ApplyDefaults() // forces NoWorktree
	ws, err := ResolveWorkspace(repo, o)
	if err != nil {
		t.Fatal(err)
	}
	if ws.Worktree {
		t.Error("ACP should never use worktree mode")
	}
	if ws.HostPath != repo {
		t.Errorf("HostPath: want %q, got %q", repo, ws.HostPath)
	}
	if ws.RepoPath != repo {
		t.Errorf("RepoPath should still be set informationally, got %q", ws.RepoPath)
	}
}

func TestResolveWorkspace_NoWorktreeFlag_ForcesPwd(t *testing.T) {
	repo := setupGitRepo(t)
	o := Opts{Mode: ModeTTY, NoWorktree: true, Name: "x"}
	o.ApplyDefaults()
	ws, err := ResolveWorkspace(repo, o)
	if err != nil {
		t.Fatal(err)
	}
	if ws.Worktree {
		t.Error("--no-worktree should force pwd")
	}
}
```

- [ ] **Step 2: Run tests to verify they pass**

```sh
go test ./internal/session/ -v
```

Expected: PASS.

- [ ] **Step 3: Commit**

```sh
git add internal/session/workspace_test.go
git commit -m "test(session): assert ACP and --no-worktree force pwd passthrough"
```

---

### Task 11: `session.Handle` skeleton

**Files:**
- Create: `internal/session/handle.go`

This is a thin struct + zero-method skeleton. Output adapters in following tasks attach methods.

- [ ] **Step 1: Write the file**

```go
// internal/session/handle.go
package session

import (
	"github.com/joegoldin/claude-container/internal/proxy"
)

// CleanupFunc runs once when the session ends (container removed, proxy
// torn down, session record deleted, etc.). The session-launcher composes
// these in Launch.
type CleanupFunc func()

// Handle is returned by Launch. The caller picks one of AttachTTY,
// BridgeACP, WaitTask, RunBackground based on intent.
type Handle struct {
	Name      string
	Container string
	Repo      string
	Branch    string
	ProxyPort int
	StatusBar proxy.StatusBarInfo

	cleanup CleanupFunc
}

// Cleanup runs the cleanup function (idempotent).
func (h *Handle) Cleanup() {
	if h.cleanup != nil {
		h.cleanup()
		h.cleanup = nil
	}
}
```

- [ ] **Step 2: Run build to verify it compiles**

```sh
go build ./internal/session/
```

Expected: build succeeds.

- [ ] **Step 3: Commit**

```sh
git add internal/session/handle.go
git commit -m "feat(session): add Handle skeleton"
```

---

### Task 12: `outputs/tty.go` — wraps existing `proxy.Run`

**Files:**
- Create: `internal/session/outputs/tty.go`

Note: this and subsequent output adapters live in a sub-package `outputs`. To avoid a long import cycle (Handle is in `session/`), the adapter is exposed as a method on `*Handle` in `handle.go` rather than as a separate sub-package. The plan switches to attaching methods directly under `internal/session/`.

We will instead add a new file `internal/session/output_tty.go` (and similar for the others). This keeps the `internal/session` package coherent with no sub-package.

- [ ] **Step 1: Create `internal/session/output_tty.go`**

```go
// internal/session/output_tty.go
package session

import (
	"github.com/joegoldin/claude-container/internal/proxy"
)

// AttachTTY runs the existing PTY proxy against the launched container.
// It blocks until the user detaches (Ctrl+B d) or the container exits.
func (h *Handle) AttachTTY() error {
	defer h.Cleanup()
	return proxy.Run(proxy.Opts{
		DockerArgs:    []string{"attach", h.Container},
		ContainerName: h.Container,
		StatusBar:     h.StatusBar,
	})
}
```

- [ ] **Step 2: Run build**

```sh
go build ./...
```

Expected: build succeeds.

- [ ] **Step 3: Commit**

```sh
git add internal/session/output_tty.go
git commit -m "feat(session): add AttachTTY wrapping internal/proxy"
```

---

### Task 13: `output_acp.go` — stdio bridge with mocked-`exec.Cmd` test

**Files:**
- Create: `internal/session/output_acp.go`
- Create: `internal/session/output_acp_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/session/output_acp_test.go
package session

import (
	"bytes"
	"strings"
	"testing"
)

// fakeCmdRunner replays a scripted exchange.
type fakeCmdRunner struct {
	containerStdout string // bytes the "container" sends to host
	gotStdin        bytes.Buffer
}

func (f *fakeCmdRunner) Run(stdin []byte, stdout, stderr *bytes.Buffer) error {
	f.gotStdin.Write(stdin)
	stdout.WriteString(f.containerStdout)
	return nil
}

func TestBridgeACP_PassthroughBothDirections(t *testing.T) {
	// Host sends a JSON-RPC initialize; container replies with a result.
	hostInput := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n"
	containerOutput := `{"jsonrpc":"2.0","id":1,"result":{}}` + "\n"

	in := strings.NewReader(hostInput)
	var out bytes.Buffer
	var stderr bytes.Buffer
	fake := &fakeCmdRunner{containerStdout: containerOutput}

	err := bridgeStdio(in, &out, &stderr, fake)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if got := fake.gotStdin.String(); got != hostInput {
		t.Errorf("container stdin: want %q, got %q", hostInput, got)
	}
	if got := out.String(); got != containerOutput {
		t.Errorf("host stdout: want %q, got %q", containerOutput, got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```sh
go test ./internal/session/ -run TestBridgeACP -v
```

Expected: FAIL — `bridgeStdio` undefined.

- [ ] **Step 3: Implement the bridge**

```go
// internal/session/output_acp.go
package session

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

// cmdRunner is the minimal interface we need to test the stdio bridge.
type cmdRunner interface {
	Run(stdin []byte, stdout, stderr *bytes.Buffer) error
}

// bridgeStdio reads all of in, hands it to the runner as stdin, copies the
// runner's stdout into out and stderr into stderrW. Used in tests; production
// uses bridgeACPProcess which streams concurrently.
func bridgeStdio(in io.Reader, out io.Writer, stderr io.Writer, r cmdRunner) error {
	buf, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	var so, se bytes.Buffer
	if err := r.Run(buf, &so, &se); err != nil {
		return err
	}
	if _, err := out.Write(so.Bytes()); err != nil {
		return err
	}
	if _, err := stderr.Write(se.Bytes()); err != nil {
		return err
	}
	return nil
}

// BridgeACP attaches to the running ACP container and bridges stdin/stdout
// between the host process and the container's claude-agent-acp instance.
// It returns when the container exits or the host receives SIGTERM/SIGINT.
func (h *Handle) BridgeACP(in io.Reader, out io.Writer) error {
	defer h.Cleanup()

	ctx, cancel := signalContext()
	defer cancel()

	cmd := exec.CommandContext(ctx, "docker", "attach", h.Container)
	cmd.Stdin = in
	cmd.Stdout = out
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("docker attach: %w", err)
	}

	// On host signal, gracefully stop the container before docker attach
	// returns an exit code.
	go func() {
		<-ctx.Done()
		stop := exec.Command("docker", "stop", "--time", "5", h.Container)
		stop.Stderr = os.Stderr
		_ = stop.Run()
	}()

	if err := cmd.Wait(); err != nil {
		// Don't surface a generic "exit status 137" — that's the expected
		// SIGKILL after `docker stop`.
		var ee *exec.ExitError
		if errorsAs(err, &ee) && ee.ExitCode() == 137 {
			return nil
		}
		return err
	}
	return nil
}

// signalContext returns a context cancelled on SIGINT/SIGTERM.
func signalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-ch
		cancel()
		// Give a short window for cleanup, then re-cancel hard.
		go func() { time.Sleep(8 * time.Second); cancel() }()
	}()
	return ctx, func() { signal.Stop(ch); cancel() }
}

// errorsAs is errors.As wrapped to avoid an extra import-cycle headache.
func errorsAs(err error, target any) bool {
	type asser interface{ As(any) bool }
	if a, ok := err.(asser); ok {
		return a.As(target)
	}
	return false
}
```

Replace the `errorsAs` shim with the real `errors.As`:

```go
import "errors"
// ...
		if errors.As(err, &ee) && ee.ExitCode() == 137 {
```

And remove the local `errorsAs` function.

- [ ] **Step 4: Run tests to verify they pass**

```sh
go test ./internal/session/ -v
```

Expected: PASS — `TestBridgeACP_PassthroughBothDirections` green; existing tests unaffected.

- [ ] **Step 5: Commit**

```sh
git add internal/session/output_acp.go internal/session/output_acp_test.go
git commit -m "feat(session): add ACP stdio bridge with signal handling"
```

---

### Task 14: `output_task.go` — task waiter and result parser

**Files:**
- Create: `internal/session/output_task.go`

This will reuse the existing `cmd/task.go` parsing logic in a later refactor task. For now, just provide the method shape so callers can target it.

- [ ] **Step 1: Write the file**

```go
// internal/session/output_task.go
package session

import (
	"context"
	"fmt"
	"os/exec"
)

// TaskOpts configures WaitTask.
type TaskOpts struct {
	Model    string
	MaxTurns int
}

// TaskResult is the parsed final result of a task run.
type TaskResult struct {
	Text     string // final assistant text
	ExitCode int
	Logs     string // raw container logs (stream-json)
}

// WaitTask blocks until the detached task container exits and returns the
// parsed final result. The container must have been started with task mode
// (claude -p --output-format stream-json).
func (h *Handle) WaitTask(ctx context.Context, opts TaskOpts) (TaskResult, error) {
	defer h.Cleanup()

	wait := exec.CommandContext(ctx, "docker", "wait", h.Container)
	out, err := wait.Output()
	if err != nil {
		return TaskResult{}, fmt.Errorf("docker wait: %w", err)
	}
	exitCode := 0
	fmt.Sscanf(string(out), "%d", &exitCode)

	logs := exec.CommandContext(ctx, "docker", "logs", h.Container)
	logsOut, err := logs.Output()
	if err != nil {
		return TaskResult{ExitCode: exitCode}, fmt.Errorf("docker logs: %w", err)
	}

	return TaskResult{
		Logs:     string(logsOut),
		ExitCode: exitCode,
		// Text is parsed by the caller (cmd/task.go) using its existing parser.
	}, nil
}
```

- [ ] **Step 2: Run build**

```sh
go build ./...
```

Expected: build succeeds.

- [ ] **Step 3: Commit**

```sh
git add internal/session/output_task.go
git commit -m "feat(session): add WaitTask output adapter shell"
```

---

### Task 15: `output_background.go` — detached run

**Files:**
- Create: `internal/session/output_background.go`

- [ ] **Step 1: Write the file**

```go
// internal/session/output_background.go
package session

// RunBackground returns immediately after the container has been started
// detached. The session record was already saved by Launch. Cleanup runs
// when the session is removed by `claude-container rm`, not here.
func (h *Handle) RunBackground() error {
	// Launch already started the container detached; nothing further to do.
	// The cleanup func intentionally does NOT fire here — the session
	// outlives this process.
	return nil
}
```

- [ ] **Step 2: Run build**

```sh
go build ./...
```

Expected: build succeeds.

- [ ] **Step 3: Commit**

```sh
git add internal/session/output_background.go
git commit -m "feat(session): add RunBackground no-op output adapter"
```

---

### Task 16: `session.Launch` — orchestration

**Files:**
- Create: `internal/session/launch.go`

This is the largest task. Steps below break it into write-then-build-then-commit.

- [ ] **Step 1: Write `launch.go`**

```go
// internal/session/launch.go
package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/docker"
	"github.com/joegoldin/claude-container/internal/httpproxy"
	"github.com/joegoldin/claude-container/internal/proxy"
	sandboxPkg "github.com/joegoldin/claude-container/internal/sandbox"
)

// Launch creates and starts a Claude Code container with all the requested
// scaffolding (workspace, proxy, config dir, session record) and returns a
// Handle whose method the caller invokes.
func Launch(ctx context.Context, store *config.Store, opts Opts) (*Handle, error) {
	opts.ApplyDefaults()
	if err := opts.Validate(); err != nil {
		return nil, err
	}

	// Step 1: image readiness.
	if err := docker.EnsureImage(config.DefaultDir()); err != nil {
		return nil, err
	}

	// Step 2: workspace resolution.
	cwd := opts.Cwd
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("getwd: %w", err)
		}
	}
	if opts.Name == "" {
		opts.Name = config.GenerateName(cwd)
		if opts.Mode == ModeACP {
			opts.Name = config.SanitizeName(filepath.Base(cwd) + "-acp-" + suffix())
		}
	}
	ws, err := ResolveWorkspace(cwd, opts)
	if err != nil {
		return nil, err
	}
	repoRoot := ws.RepoPath
	if repoRoot == "" {
		repoRoot = cwd
	}

	// Step 3: per-repo + per-session config dirs.
	if err := os.MkdirAll(store.RepoConfigDir(repoRoot), 0o755); err != nil {
		return nil, fmt.Errorf("create repo config dir: %w", err)
	}
	if err := store.UpsertRepo(repoRoot); err != nil {
		return nil, fmt.Errorf("update repo index: %w", err)
	}
	resumeMode := opts.Resume
	if opts.Mode == ModeACP {
		// ACP always sees the full repo history.
		resumeMode = "__picker__"
	}
	claudeConfigDir, err := store.PrepareSessionConfig(opts.Name, repoRoot, resumeMode)
	if err != nil {
		return nil, fmt.Errorf("prepare session config: %w", err)
	}

	// Step 4: write managed-settings for the chosen profile.
	prof, err := sandboxPkg.GetProfile(opts.Profile)
	if err != nil {
		return nil, err
	}
	settingsJSON, err := json.MarshalIndent(
		prof.ManagedSettingsForProxy(8080, opts.AllowPerms, opts.DenyPerms, opts.Packages),
		"", "  ",
	)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(claudeConfigDir, "managed-settings.json"), settingsJSON, 0o644); err != nil {
		return nil, fmt.Errorf("write managed-settings: %w", err)
	}

	// Step 5: per-session proxy.
	if !httpproxy.ImageExists() {
		tarball := os.Getenv("CLAUDE_PROXY_IMAGE_TARBALL")
		if tarball == "" {
			return nil, fmt.Errorf("proxy image not loaded; set CLAUDE_PROXY_IMAGE_TARBALL or run claude-container build")
		}
		load := exec.Command("docker", "load", "-i", tarball)
		load.Stdout = os.Stdout
		load.Stderr = os.Stderr
		if err := load.Run(); err != nil {
			return nil, fmt.Errorf("load proxy image: %w", err)
		}
	}
	proxyAllow := opts.AllowDomains
	if len(opts.Packages) > 0 {
		proxyAllow = append(proxyAllow,
			"cache.nixos.org", "*.cache.nixos.org",
			"channels.nixos.org", "releases.nixos.org",
			"github.com", "*.github.com", "*.githubusercontent.com",
		)
	}
	rulesJSON, err := json.MarshalIndent(prof.ProxyRules(proxyAllow), "", "  ")
	if err != nil {
		return nil, err
	}
	if err := httpproxy.EnsureSessionRules(config.DefaultDir(), opts.Name, opts.ProxySeedPreset); err != nil {
		return nil, fmt.Errorf("seed proxy rules: %w", err)
	}
	if err := httpproxy.AppendSessionRules(config.DefaultDir(), opts.Name, rulesJSON); err != nil {
		return nil, fmt.Errorf("append proxy rules: %w", err)
	}
	_, resolvedPort, err := httpproxy.EnsureRunning(httpproxy.ProxyOpts{
		Session:       opts.Name,
		ConfigDir:     config.DefaultDir(),
		DashboardPort: opts.ProxyPort,
		ForceRestart:  true,
	})
	if err != nil {
		return nil, fmt.Errorf("start proxy: %w", err)
	}
	if err := httpproxy.WaitForDashboardToken(config.DefaultDir(), opts.Name, 30*time.Second); err != nil {
		return nil, err
	}
	if err := httpproxy.WaitForCACert(config.DefaultDir()); err != nil {
		return nil, err
	}

	// Step 6: resolve extra workspaces / multi-repo worktrees.
	extra, multiRepos, err := resolveMounts(store, opts, ws.Worktree)
	if err != nil {
		return nil, err
	}

	// Step 7: build docker.RunOpts and start the container.
	runOpts := docker.RunOpts{
		Name:               opts.Name,
		Workspace:          ws.HostPath,
		ConfigDir:          claudeConfigDir,
		HostClaudeFiles:    config.HostClaudeCredentialFiles(),
		UID:                docker.ContainerUID(),
		GID:                docker.ContainerGID(),
		Yolo:               prof.Yolo,
		Prompt:             opts.Prompt,
		Resume:             opts.Resume,
		Continue:           opts.Continue && opts.Resume == "",
		ExtraWorkspaces:    extra,
		WorktreeRepos:      multiRepos,
		ProxyEnabled:       true,
		ProxyCACertDir:     httpproxy.CACertDir(config.DefaultDir()),
		ProxyDashboardPort: resolvedPort,
		Packages:           opts.Packages,
		Mode:               string(opts.Mode),
	}
	if ws.Worktree {
		runOpts.WorktreeBranch = ws.Branch
		runOpts.WorktreeFrom = opts.From
		runOpts.Workspace = "" // entrypoint creates from /mnt/repo
		if len(multiRepos) == 0 {
			// Single-repo worktree: mount cwd repo at /mnt/repo.
			runOpts.RepoPath = repoRoot
		}
	}

	var dockerArgs []string
	switch opts.Mode {
	case ModeACP:
		dockerArgs = docker.ACPRunArgs(runOpts)
	case ModeTask:
		dockerArgs = docker.TaskRunArgs(runOpts, "", 0) // model/maxTurns wired later
	default:
		dockerArgs = docker.RunArgs(runOpts, true) // detached
	}

	startCmd := exec.CommandContext(ctx, "docker", dockerArgs...)
	startCmd.Stderr = os.Stderr
	if err := startCmd.Run(); err != nil {
		return nil, fmt.Errorf("docker run: %w", err)
	}

	// Step 7: persist session record.
	sess := &config.Session{
		Name:            opts.Name,
		Branch:          ws.Branch,
		WorktreePath:    ws.HostPath, // empty for worktree mode (created in container)
		RepoPath:        repoRoot,
		ContainerName:   docker.ContainerName(opts.Name),
		Yolo:            prof.Yolo,
		AutoRemove:      opts.AutoRemove,
		CreatedAt:       time.Now(),
		Profile:         opts.Profile,
		AllowDomains:    opts.AllowDomains,
		DenyPaths:       opts.DenyPaths,
		AllowCommands:   opts.AllowCommands,
		DenyCommands:    opts.DenyCommands,
		AllowPerms:      opts.AllowPerms,
		DenyPerms:       opts.DenyPerms,
		Packages:        opts.Packages,
		ProxySeedPreset: opts.ProxySeedPreset,
		ProxyPort:       resolvedPort,
		Mode:            string(opts.Mode),
	}
	if err := store.Save(sess); err != nil {
		return nil, fmt.Errorf("save session: %w", err)
	}

	cleanup := func() {
		if opts.AutoRemove {
			_ = docker.Stop(opts.Name)
			_ = docker.Remove(opts.Name)
			_ = httpproxy.Stop(opts.Name)
			_ = httpproxy.RemoveSessionState(config.DefaultDir(), opts.Name)
			_ = httpproxy.RemoveNetwork(opts.Name)
			_ = store.Delete(opts.Name)
		}
		_ = store.SaveNewConversations(opts.Name, repoRoot)
	}

	return &Handle{
		Name:      opts.Name,
		Container: docker.ContainerName(opts.Name),
		Repo:      repoRoot,
		Branch:    ws.Branch,
		ProxyPort: resolvedPort,
		StatusBar: proxy.StatusBarInfo{
			Name:      opts.Name,
			Branch:    ws.Branch,
			Yolo:      prof.Yolo,
			ProxyPort: resolvedPort,
		},
		cleanup: cleanup,
	}, nil
}

// suffix returns a short random suffix for ACP session names.
func suffix() string {
	t := time.Now().UnixNano()
	return fmt.Sprintf("%x", t&0xfff)
}

// resolveMounts merges -W (named workspace) and -w (ad-hoc paths) into
// either ExtraWorkspaces (subdir mounts under /workspace) or WorktreeRepos
// (per-repo worktrees when worktree mode is on and the path is a git repo).
func resolveMounts(store *config.Store, opts Opts, worktreeMode bool) (extra, multiRepos []string, err error) {
	var paths []string
	if opts.WorkspaceName != "" {
		ws := config.NewWorkspaceStore(config.DefaultDir())
		wsPaths, e := ws.Get(opts.WorkspaceName)
		if e != nil {
			return nil, nil, e
		}
		paths = append(paths, wsPaths...)
	}
	for _, m := range opts.Mounts {
		abs, e := filepath.Abs(m)
		if e != nil {
			return nil, nil, fmt.Errorf("resolve mount %q: %w", m, e)
		}
		paths = append(paths, abs)
	}
	if len(paths) == 0 {
		return nil, nil, nil
	}

	// Validate existence + basename uniqueness.
	seen := make(map[string]string)
	for _, p := range paths {
		if _, e := os.Stat(p); e != nil {
			return nil, nil, fmt.Errorf("mount %q does not exist", p)
		}
		base := filepath.Base(p)
		if existing, ok := seen[base]; ok {
			return nil, nil, fmt.Errorf("basename collision: %q and %q both have %q", existing, p, base)
		}
		seen[base] = p
	}

	if worktreeMode {
		// In worktree mode, every mount that is itself a git repo becomes a
		// WorktreeRepo (own worktree under the container's /workspace tree).
		for _, p := range paths {
			if _, e := gitpkgRepoRoot(p); e != nil {
				return nil, nil, fmt.Errorf("worktree mode: %q is not a git repo", p)
			}
			multiRepos = append(multiRepos, p)
		}
		return nil, multiRepos, nil
	}
	return paths, nil, nil
}

// gitpkgRepoRoot exists so resolveMounts can call internal/git without a
// circular import; keep this thin wrapper rather than importing inline.
func gitpkgRepoRoot(p string) (string, error) {
	return gitpkg.RepoRoot(p)
}
```

Add to imports at the top of `launch.go`:

```go
import (
	// ...existing imports...
	gitpkg "github.com/joegoldin/claude-container/internal/git"
)
```

- [ ] **Step 2: Build to surface missing helpers**

```sh
go build ./...
```

If `httpproxy.RemoveSession` does not exist, look at `internal/httpproxy/` for the actual cleanup function name and substitute it.

- [ ] **Step 3: Run unit tests to make sure nothing else broke**

```sh
go test ./internal/session/ ./internal/docker/ ./internal/config/ ./internal/git/ -v
```

Expected: PASS.

- [ ] **Step 4: Commit**

```sh
git add internal/session/launch.go
git commit -m "feat(session): orchestrate Launch — image, workspace, proxy, container, record"
```

---

## Phase 3 — ACP entrypoint and `acp` subcommand

After Phase 3, `claude-container acp` works against a built image. Bare invocation still launches the TUI as before; that change comes in Phase 4.

### Task 17: Entrypoint mode dispatch + lazy install of `claude-agent-acp`

**Files:**
- Modify: `nix/image.nix` (entrypoint script)

- [ ] **Step 1: Add the install block to the entrypoint script**

Open `nix/image.nix`. Find the `# --- Exec ---` section near the bottom of the entrypoint (around line 301). Just before it, add:

```sh
    # --- ACP mode: ensure claude-agent-acp is installed ---
    if [ "''${CLAUDE_CONTAINER_MODE:-}" = "acp" ]; then
      if ! command -v claude-agent-acp >/dev/null 2>&1; then
        log "installing claude-agent-acp into persistent nix store"
        if [ "$USER_NAME" = "root" ]; then
          ${pkgs.nix}/bin/nix profile install --accept-flake-config nixpkgs#claude-agent-acp 2>&1 | ${pkgs.coreutils}/bin/tee -a "$ENTRYPOINT_LOG" >&2 || log "WARNING: claude-agent-acp install failed"
          export PATH="/root/.nix-profile/bin:$PATH"
        else
          ${suExec} "$USER_NAME" ${pkgs.nix}/bin/nix profile install --accept-flake-config nixpkgs#claude-agent-acp 2>&1 | ${pkgs.coreutils}/bin/tee -a "$ENTRYPOINT_LOG" >&2 || log "WARNING: claude-agent-acp install failed"
          export PATH="/home/$USER_NAME/.nix-profile/bin:$PATH"
        fi
      fi
    fi
```

- [ ] **Step 2: Rebuild the image**

```sh
nix build .#claude-container-image
docker load -i ./result
```

Expected: build succeeds, image loaded.

- [ ] **Step 3: Smoke-check the entrypoint**

```sh
docker run --rm -e CLAUDE_CONTAINER_MODE=acp -e USER_UID=1000 -e USER_GID=1000 \
  -v claude-nix-store:/nix/var \
  $(docker images claude-code -q | head -1) which claude-agent-acp
```

Expected: prints `/root/.nix-profile/bin/claude-agent-acp` (or the equivalent for the active user). First run takes ~1–2 minutes; subsequent runs are instant.

- [ ] **Step 4: Commit**

```sh
git add nix/image.nix
git commit -m "feat(image): lazy-install claude-agent-acp in entrypoint when MODE=acp"
```

---

### Task 18: `cmd/acp.go` — the `acp` subcommand

**Files:**
- Create: `cmd/acp.go`

- [ ] **Step 1: Write the file**

```go
// cmd/acp.go
package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/session"
	"github.com/spf13/cobra"
)

var (
	acpProfile      string
	acpName         string
	acpAllowDomains []string
	acpDenyPaths    []string
	acpAllowCmds    []string
	acpDenyCmds     []string
	acpProxyPreset  string
	acpProxyPort    int
)

var acpCmd = &cobra.Command{
	Use:   "acp",
	Short: "Run as an ACP agent (for Zed and other ACP-compatible IDEs)",
	Long: `Start a sandboxed Claude Code container that speaks the Agent Client
Protocol over stdio. The host process bridges stdin/stdout to the
container's claude-agent-acp instance. Conversations persist in the
per-repo claude-config directory and are visible to all subsequent ACP
launches in the same repo.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		store := config.NewStore(config.DefaultDir())
		if err := requireAuth(store); err != nil {
			return err
		}

		opts := session.Opts{
			Mode:          session.ModeACP,
			Name:          acpName,
			Profile:       acpProfile,
			AllowDomains:  acpAllowDomains,
			DenyPaths:     acpDenyPaths,
			AllowCommands: acpAllowCmds,
			DenyCommands:  acpDenyCmds,
			ProxySeedPreset: acpProxyPreset,
			ProxyPort:       acpProxyPort,
		}

		h, err := session.Launch(context.Background(), store, opts)
		if err != nil {
			return fmt.Errorf("launch ACP session: %w", err)
		}
		// Diagnostics already went to host stderr above. From here on,
		// stdin/stdout belong to the JSON-RPC client.
		fmt.Fprintf(os.Stderr, "claude-container acp: session=%s container=%s\n", h.Name, h.Container)
		return h.BridgeACP(os.Stdin, os.Stdout)
	},
}

func init() {
	acpCmd.Flags().StringVar(&acpName, "name", "", "Session name (auto-generated if omitted)")
	acpCmd.Flags().StringVar(&acpProfile, "profile", "", "Sandbox profile: low, default, med, high (default: med for ACP)")
	acpCmd.Flags().StringArrayVar(&acpAllowDomains, "allow-domain", nil, "Add domain to proxy allowlist")
	acpCmd.Flags().StringArrayVar(&acpDenyPaths, "deny-path", nil, "Add path to permissions deny list")
	acpCmd.Flags().StringArrayVar(&acpAllowCmds, "allow-command", nil, "Add command pattern to allow list")
	acpCmd.Flags().StringArrayVar(&acpDenyCmds, "deny-command", nil, "Add command pattern to deny list")
	acpCmd.Flags().StringVar(&acpProxyPreset, "preset", "", "Seed proxy rules from a saved preset name")
	acpCmd.Flags().IntVar(&acpProxyPort, "proxy-port", 0, "Dashboard port on host (0 = auto-assign)")
	rootCmd.AddCommand(acpCmd)
}
```

- [ ] **Step 2: Build**

```sh
go build ./...
```

Expected: build succeeds. If there's a missing `wrapCommandPerms` reference, none should exist here — the Launch path doesn't need it because `AllowCommands` flows directly into proxy rules, not into permission rules. Leave the simple wiring; later refactor tasks consolidate it.

- [ ] **Step 3: Smoke-test as a stdio bridge**

```sh
echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' | go run . acp 2>/tmp/acp-stderr.log
```

Expected: a JSON response to stdout, container created and removed (`docker ps -a | grep claude-container_` shows nothing afterward), `/tmp/acp-stderr.log` shows proxy startup and lazy-install messages.

- [ ] **Step 4: Commit**

```sh
git add cmd/acp.go
git commit -m "feat(cmd): add 'acp' subcommand for ACP-compatible IDEs"
```

---

### Task 19: Integration test for ACP round-trip

**Files:**
- Create: `internal/session/session_integration_test.go`

- [ ] **Step 1: Write the test**

```go
//go:build integration

// internal/session/session_integration_test.go
package session

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/joegoldin/claude-container/internal/config"
)

func TestACP_BridgeRoundTrip(t *testing.T) {
	store := config.NewStore(t.TempDir())
	if !store.IsAuthenticated() {
		t.Skip("requires authenticated Claude Code on host")
	}

	cwd := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	h, err := Launch(ctx, store, Opts{
		Mode: ModeACP,
		Cwd:  cwd,
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	// Send an `initialize` request.
	req := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{}}}` + "\n")
	in := bytes.NewReader(req)
	var out bytes.Buffer
	doneErr := make(chan error, 1)
	go func() { doneErr <- h.BridgeACP(in, &out) }()

	// Wait briefly for a response, then close the bridge by stopping the container.
	deadline := time.After(60 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for response; got: %q", out.String())
		default:
		}
		if out.Len() > 0 {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}

	// Validate the response is JSON-RPC.
	line := strings.SplitN(strings.TrimSpace(out.String()), "\n", 2)[0]
	var resp map[string]any
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("non-JSON response: %q (err: %v)", line, err)
	}
	if resp["jsonrpc"] != "2.0" {
		t.Errorf("expected jsonrpc=2.0, got %v", resp["jsonrpc"])
	}
	if _, ok := resp["result"]; !ok {
		t.Errorf("response missing result: %v", resp)
	}

	// Tear down.
	cancel()
	<-doneErr
}

func TestACP_ConversationPersists(t *testing.T) {
	store := config.NewStore(t.TempDir())
	if !store.IsAuthenticated() {
		t.Skip("requires authenticated Claude Code on host")
	}

	repo := t.TempDir()
	// Initialize a real git repo so per-repo storage uses a stable repo-id.
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-q", "-m", "init"}} {
		c := exec.Command("git", args...)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	// First ACP launch — send a single message, then disconnect.
	h1, err := Launch(ctx, store, Opts{Mode: ModeACP, Cwd: repo})
	if err != nil {
		t.Fatalf("first launch: %v", err)
	}
	go func() {
		_ = h1.BridgeACP(strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n"), io.Discard)
	}()
	time.Sleep(20 * time.Second) // give the agent time to write a JSONL

	// Second ACP launch — list threads, expect at least one.
	h2, err := Launch(ctx, store, Opts{Mode: ModeACP, Cwd: repo})
	if err != nil {
		t.Fatalf("second launch: %v", err)
	}

	repoConfigDir := store.RepoConfigDir(repo)
	threads, err := os.ReadDir(filepath.Join(repoConfigDir, "projects", "-workspace"))
	if err != nil {
		t.Fatalf("read projects dir: %v", err)
	}
	if len(threads) == 0 {
		t.Fatal("expected at least one .jsonl thread file in per-repo store")
	}
	_ = h2 // bridge not needed; we verified the file is on disk
}
```

Add the missing imports at the top of the file:

```go
import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
)
```

- [ ] **Step 2: Run integration tests**

```sh
go test -tags=integration -run "TestACP_BridgeRoundTrip|TestACP_ConversationPersists" -v -timeout 6m ./internal/session/
```

Expected: PASS. Slow (first ACP launch installs `claude-agent-acp`); subsequent runs are fast.

- [ ] **Step 3: Commit**

```sh
git add internal/session/session_integration_test.go
git commit -m "test(session): integration tests for ACP round-trip and persistence"
```

---

## Phase 4 — Bare invocation + TUI relocation

### Task 20: Move dashboard launch loop to `cmd/tui.go`

**Files:**
- Create: `cmd/tui.go`
- Modify: `cmd/root.go` (later in Task 21)

- [ ] **Step 1: Write `cmd/tui.go`**

Copy the entirety of the current `rootCmd.RunE` body from `cmd/root.go` (the dashboard loop, including the wizard flow and attach/recreate logic) into a new `cmd/tui.go`. The function name becomes `runTUI`. The `tuiCmd` cobra command wraps it.

```go
// cmd/tui.go
package cmd

import (
	"github.com/spf13/cobra"
)

var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Open the dashboard for browsing and managing sessions",
	Long: `Launch the Bubble Tea dashboard. Until v0.X this was bare
'claude-container' behavior; bare invocation now creates a new session.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTUI()
	},
}

func init() {
	rootCmd.AddCommand(tuiCmd)
}
```

Then create `runTUI()` in the same file, copying the body of `rootCmd.RunE` from `cmd/root.go` verbatim. Move all helper functions referenced from that body that aren't shared with other commands (none in practice; the existing helpers like `removeSession`, `saveResumeID` stay where they are). Keep the function private (`runTUI` lowercase).

- [ ] **Step 2: Build**

```sh
go build ./...
```

Expected: build succeeds. There may be duplicate symbol errors if both `cmd/root.go` and `cmd/tui.go` define `runTUI` — that's fine; we'll remove from root.go in Task 21.

- [ ] **Step 3: Commit (intermediate, code intentionally still duplicates the logic)**

Skip the commit until after Task 21 to avoid a broken middle state. Move on.

---

### Task 21: Replace `cmd/root.go` `RunE` with bare-invoke logic

**Files:**
- Modify: `cmd/root.go`

- [ ] **Step 1: Replace the dashboard `RunE` with `runDefault`**

Open `cmd/root.go`. Replace the `RunE` block (lines 23-221) with:

```go
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDefault(cmd, args)
	},
```

Add `runDefault` to `cmd/root.go`:

```go
// runDefault implements bare claude-container invocation: creates a new
// session in the current directory and attaches. Worktree-on-git, pwd
// otherwise; persistent (no --rm) by default; no yolo.
func runDefault(_ *cobra.Command, _ []string) error {
	store := config.NewStore(config.DefaultDir())
	if err := requireAuth(store); err != nil {
		return err
	}

	maybePrintBareInvokeNotice(store)

	opts := session.Opts{
		Mode:            session.ModeTTY,
		WorktreeMode:    session.WorktreeAuto,
		Name:            bareName,
		Profile:         bareProfile,
		Yolo:            bareYolo,
		Prompt:          bareprompt,
		Resume:          bareResume,
		AutoRemove:      bareAutoRemove,
		Background:      bareBackground,
		Mounts:          bareMounts,
		WorkspaceName:   bareWorkspace,
		AllowDomains:    bareAllowDomains,
		DenyPaths:       bareDenyPaths,
		AllowCommands:   bareAllowCommands,
		DenyCommands:    bareDenyCommands,
		Packages:        barePackages,
		ProxySeedPreset: bareProxyPreset,
		ProxyPort:       bareProxyPort,
		From:            bareFrom,
		NoWorktree:      bareNoWorktree,
	}

	h, err := session.Launch(context.Background(), store, opts)
	if err != nil {
		return err
	}
	if opts.Background {
		return h.RunBackground()
	}
	return h.AttachTTY()
}
```

Add the matching variables and flag declarations in `init()`:

```go
var (
	bareName            string
	bareProfile         string
	bareYolo            bool
	bareprompt          string
	bareResume          string
	bareAutoRemove      bool
	bareBackground      bool
	bareMounts          []string
	bareWorkspace       string
	bareAllowDomains    []string
	bareDenyPaths       []string
	bareAllowCommands   []string
	bareDenyCommands    []string
	barePackages        []string
	bareProxyPreset     string
	bareProxyPort       int
	bareFrom            string
	bareNoWorktree      bool
)

func init() {
	rootCmd.Flags().StringVar(&bareName, "name", "", "Session name (auto-generated)")
	rootCmd.Flags().StringVar(&bareProfile, "profile", "", "Sandbox profile: low, default, med, high")
	rootCmd.Flags().BoolVar(&bareYolo, "yolo", false, "Skip permission prompts")
	rootCmd.Flags().StringVarP(&bareprompt, "prompt", "p", "", "Initial prompt to send to Claude")
	rootCmd.Flags().StringVar(&bareResume, "resume", "", "Resume previous conversation (uuid or empty for picker)")
	rootCmd.Flags().Lookup("resume").NoOptDefVal = "__picker__"
	rootCmd.Flags().BoolVar(&bareAutoRemove, "rm", false, "Auto-remove session when it exits")
	rootCmd.Flags().BoolVarP(&bareBackground, "background", "b", false, "Don't attach after creation")
	rootCmd.Flags().StringArrayVarP(&bareMounts, "mount", "w", nil, "Additional folders to mount")
	rootCmd.Flags().StringVarP(&bareWorkspace, "workspace", "W", "", "Named workspace from workspaces.json")
	rootCmd.Flags().StringArrayVar(&bareAllowDomains, "allow-domain", nil, "Add domain to proxy allowlist")
	rootCmd.Flags().StringArrayVar(&bareDenyPaths, "deny-path", nil, "Add path to permissions deny list")
	rootCmd.Flags().StringArrayVar(&bareAllowCommands, "allow-command", nil, "Add command pattern to allow list")
	rootCmd.Flags().StringArrayVar(&bareDenyCommands, "deny-command", nil, "Add command pattern to deny list")
	rootCmd.Flags().StringSliceVar(&barePackages, "packages", nil, "Comma-separated nixpkgs to install")
	rootCmd.Flags().StringVar(&bareProxyPreset, "preset", "", "Seed proxy rules from a saved preset name")
	rootCmd.Flags().IntVar(&bareProxyPort, "proxy-port", 0, "Dashboard port on host (0 = auto-assign)")
	rootCmd.Flags().StringVar(&bareFrom, "from", "", "Base branch for worktree (default: current HEAD)")
	rootCmd.Flags().BoolVar(&bareNoWorktree, "no-worktree", false, "Use current directory directly (skip worktree)")
}
```

Adjust the existing imports of `cmd/root.go` so only what `runDefault` uses remains. Most of the heavy imports (`docker`, `httpproxy`, `proxy`, `tea`, `tui`) move to `tui.go`.

- [ ] **Step 2: Build**

```sh
go build ./...
```

Expected: build succeeds.

- [ ] **Step 3: Commit (TUI relocation + bare invoke)**

```sh
git add cmd/root.go cmd/tui.go
git commit -m "refactor(cmd): bare invocation creates a session; TUI moves to 'tui' subcommand"
```

---

### Task 22: First-time bare-invoke notice

**Files:**
- Modify: `cmd/root.go`

- [ ] **Step 1: Implement `maybePrintBareInvokeNotice`**

Append to `cmd/root.go`:

```go
import "path/filepath" // add if not already present

const bareInvokeNoticeFlag = "migrated-bare-invoke"

func maybePrintBareInvokeNotice(store *config.Store) {
	if os.Getenv("CLAUDE_CONTAINER_QUIET") == "1" {
		return
	}
	flag := filepath.Join(config.DefaultDir(), bareInvokeNoticeFlag)
	if _, err := os.Stat(flag); err == nil {
		return // already shown
	}
	sessions := store.List()
	if len(sessions) == 0 {
		// Fresh install — no notice needed.
		_ = os.WriteFile(flag, []byte("ok"), 0o644)
		return
	}
	fmt.Fprintf(os.Stderr,
		"note: bare claude-container now creates a session; run 'claude-container tui' for the dashboard (existing sessions: %d)\n",
		len(sessions),
	)
	_ = os.WriteFile(flag, []byte("ok"), 0o644)
}
```

- [ ] **Step 2: Build and run a quick smoke**

```sh
go build ./... && CLAUDE_CONTAINER_QUIET=1 ./claude-container --help
```

Expected: build succeeds, no notice printed (`--help` short-circuits before `RunE`).

- [ ] **Step 3: Commit**

```sh
git add cmd/root.go
git commit -m "feat(cmd): print bare-invoke relocation notice once"
```

---

## Phase 5 — Refactor existing commands to use the launcher

After Phase 5, the duplicate `createSession` body in `cmd/new.go` is gone, and `run`, `work`, `task`, `attach`, `new` all delegate to `session.Launch`.

### Task 23: Refactor `cmd/run.go`

**Files:**
- Modify: `cmd/run.go`

- [ ] **Step 1: Replace the body**

```go
// cmd/run.go
package cmd

import (
	"context"
	"os"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/session"
	"github.com/spf13/cobra"
)

var (
	runYolo         bool
	runPrompt       string
	runName         string
	runBackground   bool
	runAutoRemove   bool
	runMounts       []string
	runWorkspace    string
	runProfile      string
	runAllowDomains []string
	runDenyPaths    []string
	runProxyPreset string
	runProxyPort   int
	runResume         string
	runAllowCommands  []string
	runDenyCommands   []string
)

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Quick-start a session in the current directory",
	Long:  `Create a session without a worktree, using the current directory.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		store := config.NewStore(config.DefaultDir())
		if err := requireAuth(store); err != nil {
			return err
		}
		opts := session.Opts{
			Mode:            session.ModeTTY,
			WorktreeMode:    session.WorktreeNever,
			NoWorktree:      true,
			Name:            runName,
			Profile:         runProfile,
			Yolo:            runYolo,
			Prompt:          runPrompt,
			Resume:          runResume,
			AutoRemove:      runAutoRemove,
			Background:      runBackground,
			Mounts:          runMounts,
			WorkspaceName:   runWorkspace,
			AllowDomains:    runAllowDomains,
			DenyPaths:       runDenyPaths,
			AllowCommands:   runAllowCommands,
			DenyCommands:    runDenyCommands,
			ProxySeedPreset: runProxyPreset,
			ProxyPort:       runProxyPort,
		}
		h, err := session.Launch(context.Background(), store, opts)
		if err != nil {
			return err
		}
		if runBackground {
			return h.RunBackground()
		}
		return h.AttachTTY()
	},
}

func init() {
	runCmd.Flags().BoolVar(&runYolo, "yolo", false, "Skip permission prompts")
	runCmd.Flags().StringVar(&runResume, "resume", "", "Resume a previous conversation (id or empty for picker)")
	runCmd.Flags().Lookup("resume").NoOptDefVal = "__picker__"
	runCmd.Flags().StringVarP(&runPrompt, "prompt", "p", "", "Initial prompt to send to Claude")
	runCmd.Flags().StringVar(&runName, "name", "", "Session name (auto-generated)")
	runCmd.Flags().BoolVarP(&runBackground, "background", "b", false, "Don't attach after creation")
	runCmd.Flags().BoolVar(&runAutoRemove, "rm", false, "Auto-remove session when it exits")
	runCmd.Flags().StringArrayVarP(&runMounts, "mount", "w", nil, "Additional folders to mount")
	runCmd.Flags().StringVarP(&runWorkspace, "workspace", "W", "", "Named workspace from workspaces.json")
	runCmd.Flags().StringVar(&runProfile, "profile", "", "Sandbox profile: low, default, med, high")
	runCmd.Flags().StringArrayVar(&runAllowDomains, "allow-domain", nil, "Add domain to proxy allowlist")
	runCmd.Flags().StringArrayVar(&runDenyPaths, "deny-path", nil, "Add path to permissions deny list")
	runCmd.Flags().StringArrayVar(&runAllowCommands, "allow-command", nil, "Add command pattern to allow list")
	runCmd.Flags().StringArrayVar(&runDenyCommands, "deny-command", nil, "Add command pattern to deny list")
	runCmd.Flags().StringVar(&runProxyPreset, "preset", "", "Seed proxy rules from a saved preset name")
	runCmd.Flags().IntVar(&runProxyPort, "proxy-port", 0, "Dashboard port on host (0 = auto-assign)")
	rootCmd.AddCommand(runCmd)
}

var _ = os.Stderr // keep import while transitional
```

- [ ] **Step 2: Build and run unit tests**

```sh
go build ./... && go test ./...
```

Expected: PASS.

- [ ] **Step 3: Run the existing E2E test for `run`**

```sh
go test -tags=integration -run TestE2E_Run -v -timeout 5m ./cmd/
```

Expected: PASS. If a test was reading state via the old direct `createSession` path, that's the regression we're guarding against; fix any breakage by adjusting `Launch`/`Opts` mapping rather than the test.

- [ ] **Step 4: Commit**

```sh
git add cmd/run.go
git commit -m "refactor(cmd): run delegates to session.Launch"
```

---

### Task 24: Refactor `cmd/work.go`

**Files:**
- Modify: `cmd/work.go`

- [ ] **Step 1: Replace the body**

```go
// cmd/work.go
package cmd

import (
	"context"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/joegoldin/claude-container/internal/session"
	"github.com/spf13/cobra"
)

var (
	workYolo         bool
	workPrompt       string
	workName         string
	workFrom         string
	workBackground   bool
	workAutoRemove   bool
	workMounts       []string
	workWorkspace    string
	workProfile      string
	workAllowDomains []string
	workDenyPaths    []string
	workProxyPreset string
	workProxyPort   int
	workResume         string
	workAllowCommands  []string
	workDenyCommands   []string
)

var workCmd = &cobra.Command{
	Use:   "work",
	Short: "Quick-start an isolated worktree session",
	RunE: func(cmd *cobra.Command, args []string) error {
		store := config.NewStore(config.DefaultDir())
		if err := requireAuth(store); err != nil {
			return err
		}
		opts := session.Opts{
			Mode:            session.ModeTTY,
			WorktreeMode:    session.WorktreeAlways,
			Name:            workName,
			From:            workFrom,
			Profile:         workProfile,
			Yolo:            workYolo,
			Prompt:          workPrompt,
			Resume:          workResume,
			AutoRemove:      workAutoRemove,
			Background:      workBackground,
			Mounts:          workMounts,
			WorkspaceName:   workWorkspace,
			AllowDomains:    workAllowDomains,
			DenyPaths:       workDenyPaths,
			AllowCommands:   workAllowCommands,
			DenyCommands:    workDenyCommands,
			ProxySeedPreset: workProxyPreset,
			ProxyPort:       workProxyPort,
		}
		h, err := session.Launch(context.Background(), store, opts)
		if err != nil {
			return err
		}
		if workBackground {
			return h.RunBackground()
		}
		return h.AttachTTY()
	},
}

func init() {
	workCmd.Flags().BoolVar(&workYolo, "yolo", false, "Skip permission prompts")
	workCmd.Flags().StringVar(&workResume, "resume", "", "Resume a previous conversation (id or empty for picker)")
	workCmd.Flags().Lookup("resume").NoOptDefVal = "__picker__"
	workCmd.Flags().StringVarP(&workPrompt, "prompt", "p", "", "Initial prompt to send to Claude")
	workCmd.Flags().StringVar(&workName, "name", "", "Session name (auto-generated)")
	workCmd.Flags().StringVar(&workFrom, "from", "", "Base branch for worktree (default: current HEAD)")
	workCmd.Flags().BoolVarP(&workBackground, "background", "b", false, "Don't attach after creation")
	workCmd.Flags().BoolVar(&workAutoRemove, "rm", false, "Auto-remove session when it exits")
	workCmd.Flags().StringArrayVarP(&workMounts, "mount", "w", nil, "Additional folders to mount")
	workCmd.Flags().StringVarP(&workWorkspace, "workspace", "W", "", "Named workspace from workspaces.json")
	workCmd.Flags().StringVar(&workProfile, "profile", "", "Sandbox profile: low, default, med, high")
	workCmd.Flags().StringArrayVar(&workAllowDomains, "allow-domain", nil, "Add domain to proxy allowlist")
	workCmd.Flags().StringArrayVar(&workDenyPaths, "deny-path", nil, "Add path to permissions deny list")
	workCmd.Flags().StringArrayVar(&workAllowCommands, "allow-command", nil, "Add command pattern to allow list")
	workCmd.Flags().StringArrayVar(&workDenyCommands, "deny-command", nil, "Add command pattern to deny list")
	workCmd.Flags().StringVar(&workProxyPreset, "preset", "", "Seed proxy rules from a saved preset name")
	workCmd.Flags().IntVar(&workProxyPort, "proxy-port", 0, "Dashboard port on host (0 = auto-assign)")
	rootCmd.AddCommand(workCmd)
}
```

- [ ] **Step 2: Build + tests + work E2E**

```sh
go build ./... && go test ./...
go test -tags=integration -run TestE2E_Work -v -timeout 5m ./cmd/
```

Expected: PASS. Worktree path now lives in `<repo>/.worktrees/<name>/` for new sessions.

- [ ] **Step 3: Commit**

```sh
git add cmd/work.go
git commit -m "refactor(cmd): work delegates to session.Launch (in-repo .worktrees/)"
```

---

### Task 25: Refactor `cmd/task.go` to call Launch + WaitTask

**Files:**
- Modify: `cmd/task.go`

The task parser logic lives in `cmd/task_parser.go`. Keep it. Wire `cmd/task.go` to `session.Launch{Mode: ModeTask}` followed by `h.WaitTask` followed by parsing.

- [ ] **Step 1: Read `cmd/task.go`**

```sh
cat cmd/task.go
```

The parser is `parseStreamEvents(r io.Reader) taskResult` (in `cmd/task_parser.go`). Note its return type so the new `RunE` can wire its fields to stdout/stderr correctly.

- [ ] **Step 2: Rewrite the `RunE` body**

Replace the RunE of `taskCmd` with a path that:
1. Builds `session.Opts{Mode: ModeTask, AutoRemove: !taskKeep, Profile: taskProfile, ...}` with `--keep` inverted to AutoRemove.
2. Calls `session.Launch(ctx, store, opts)`.
3. Calls `h.WaitTask(ctx, session.TaskOpts{Model: taskModel, MaxTurns: taskMaxTurns})` to get the result.
4. Uses the existing parser to extract the final assistant text from `result.Logs`, prints it to stdout, prints stats to stderr.

Concrete diff (paste over the current `RunE`):

```go
RunE: func(cmd *cobra.Command, args []string) error {
	store := config.NewStore(config.DefaultDir())
	if err := requireAuth(store); err != nil {
		return err
	}
	opts := session.Opts{
		Mode:            session.ModeTask,
		Name:            taskName,
		Profile:         taskProfile,
		AutoRemove:      !taskKeep,
		Mounts:          taskMounts,
		WorkspaceName:   taskWorkspace,
		AllowDomains:    taskAllowDomains,
		DenyPaths:       taskDenyPaths,
		AllowCommands:   taskAllowCommands,
		DenyCommands:    taskDenyCommands,
		Prompt:          taskPrompt,
		ProxySeedPreset: taskProxyPreset,
		ProxyPort:       taskProxyPort,
	}
	ctx := context.Background()
	h, err := session.Launch(ctx, store, opts)
	if err != nil {
		return err
	}
	res, err := h.WaitTask(ctx, session.TaskOpts{Model: taskModel, MaxTurns: taskMaxTurns})
	if err != nil {
		return err
	}
	parsed := parseStreamEvents(strings.NewReader(res.Logs))
	fmt.Fprint(os.Stdout, parsed.FinalText)
	fmt.Fprintf(os.Stderr,
		"\nsession=%s  tokens=in:%d/out:%d  exit=%d\n",
		parsed.SessionID, parsed.InputTokens, parsed.OutputTokens, res.ExitCode)
	return nil
},
```

If the existing parser function name is different (e.g. `parseTaskOutput`), substitute it. Keep the import block in sync.

- [ ] **Step 3: Build + tests**

```sh
go build ./... && go test ./...
go test -tags=integration -run TestE2E_Task -v -timeout 5m ./cmd/
```

Expected: PASS.

- [ ] **Step 4: Commit**

```sh
git add cmd/task.go
git commit -m "refactor(cmd): task delegates to session.Launch + WaitTask"
```

---

### Task 26: Refactor `cmd/new.go` (wizard preserved, body delegates to Launch)

**Files:**
- Modify: `cmd/new.go`

The wizard flow (`tui.NewWizard`) stays. Just replace the call to `createSession` with a call that builds `session.Opts` and dispatches to `session.Launch`.

- [ ] **Step 1: Replace `createSession` body**

In `cmd/new.go`, redefine `createSession` to:

```go
func createSession(opts createOpts) error {
	store := config.NewStore(config.DefaultDir())
	if err := requireAuth(store); err != nil {
		return err
	}
	mode := session.ModeTTY
	wmode := session.WorktreeAuto
	if opts.noWorktree {
		wmode = session.WorktreeNever
	} else if opts.worktree != "" {
		wmode = session.WorktreeAlways
	}

	o := session.Opts{
		Mode:            mode,
		WorktreeMode:    wmode,
		NoWorktree:      opts.noWorktree,
		Name:            opts.name,
		WorktreeName:    opts.worktree,
		From:            opts.from,
		Profile:         opts.profile,
		Yolo:            opts.yolo,
		Prompt:          opts.prompt,
		Resume:          opts.resume,
		Continue:        opts.cont,
		AutoRemove:      opts.autoRemove,
		Background:      opts.background,
		Mounts:          opts.mounts,
		WorkspaceName:   opts.workspace,
		AllowDomains:    opts.allowDomains,
		DenyPaths:       opts.denyPaths,
		AllowCommands:   opts.allowCommands,
		DenyCommands:    opts.denyCommands,
		AllowPerms:      opts.allowPerms,
		DenyPerms:       opts.denyPerms,
		Packages:        opts.packages,
		ProxySeedPreset: opts.proxySeedPreset,
		ProxyPort:       opts.proxyPort,
	}
	h, err := session.Launch(context.Background(), store, o)
	if err != nil {
		return err
	}
	if opts.background {
		fmt.Printf("Session %q created (background).\n", h.Name)
		fmt.Printf("  Attach: claude-container attach %s\n", h.Name)
		return h.RunBackground()
	}
	return h.AttachTTY()
}
```

Strip the now-dead helpers from `cmd/new.go` that have been absorbed into `session.Launch`: `wrapCommandPerms`, `envExtraAllowCommands`, the manual proxy bring-up block. Keep `resolveWorkspaces` if Launch still relies on it (it doesn't currently — Launch handles `Mounts` directly), otherwise remove it. If unsure, leave them with a TODO and simplify in a follow-up.

- [ ] **Step 2: Build + tests**

```sh
go build ./... && go test ./...
go test -tags=integration -run TestE2E_New -v -timeout 5m ./cmd/
```

Expected: PASS.

- [ ] **Step 3: Commit**

```sh
git add cmd/new.go
git commit -m "refactor(cmd): new delegates to session.Launch (wizard preserved)"
```

---

### Task 27: Refactor `cmd/attach.go` recreate path

**Files:**
- Modify: `cmd/attach.go`

The `attach` command's tricky case is when the container is missing — we recreate it with the saved `Resume` ID. Today this is done inline in `cmd/root.go`'s dashboard loop. Move that into `attach.go` itself, calling Launch only if needed.

- [ ] **Step 1: Replace the recreate block**

Find the section in `cmd/attach.go` that re-runs `docker run` against a missing container. Replace it with:

```go
// Container missing — recreate via session.Launch using the saved opts.
sess, _ := store.Get(name)
if sess == nil {
	return fmt.Errorf("session %q not found", name)
}
opts := session.Opts{
	Mode:    session.ModeTTY,
	WorktreeMode: session.WorktreeNever, // attach never re-creates a worktree
	Name:    sess.Name,
	Profile: sess.Profile,
	Yolo:    sess.Yolo,
	Resume:  sess.ResumeID,
	Continue: sess.ResumeID == "",
	AllowDomains: sess.AllowDomains,
	DenyPaths: sess.DenyPaths,
	AllowCommands: sess.AllowCommands,
	DenyCommands: sess.DenyCommands,
	AllowPerms: sess.AllowPerms,
	DenyPerms: sess.DenyPerms,
	Packages: sess.Packages,
	ProxySeedPreset: sess.ProxySeedPreset,
	ProxyPort: sess.ProxyPort,
}
h, err := session.Launch(context.Background(), store, opts)
if err != nil {
	return err
}
return h.AttachTTY()
```

The simpler `docker.IsRunning` and `docker.Start` paths stay as-is.

- [ ] **Step 2: Build + tests**

```sh
go build ./... && go test ./...
go test -tags=integration -run TestE2E_Attach -v -timeout 5m ./cmd/
```

Expected: PASS.

- [ ] **Step 3: Commit**

```sh
git add cmd/attach.go
git commit -m "refactor(cmd): attach uses session.Launch for missing-container recreate"
```

---

## Phase 6 — Documentation polish

### Task 28: Update README.md

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Update the SYNOPSIS block**

Replace the current SYNOPSIS section to reflect the new behavior:

```
claude-container [flags]                  # bare invoke: new sandboxed session in cwd
                                            # (worktree-on-git, pwd otherwise)
claude-container acp [flags]              # ACP entry for Zed and other ACP-compatible IDEs
claude-container tui                      # dashboard for browsing/managing sessions
claude-container run [flags]              # quick-start in current dir (pwd passthrough)
claude-container work [flags]             # quick-start with worktree
claude-container task [flags]             # run a task, print result to stdout
claude-container new [flags]              # create session (interactive wizard)
claude-container ps [--json]              # list sessions
claude-container attach <session>         # attach to session
claude-container stop <session>           # stop session
claude-container rm <session>             # remove session
claude-container logs [-f] <session>      # stream logs
claude-container extract <session>        # extract conversation
claude-container build                    # load docker image
claude-container shell [workspace]        # debug shell
claude-container workspace add|list|show|rm  # manage named workspaces
claude-container conversations            # browse conversation history
claude-container auth                     # authenticate Claude Code
claude-container doctor                   # check system health
claude-container gc [--all] [--auth]      # garbage collect
```

- [ ] **Step 2: Add an "ACP" section after "TUI DASHBOARD"**

```
## ACP (AGENT CLIENT PROTOCOL)

`claude-container acp` runs a sandboxed Claude Code container and bridges
its stdio to the host process using the [Agent Client Protocol](https://agentclientprotocol.com).
This is the entry point for ACP-compatible IDEs like Zed.

To use with Zed, configure an external agent in your settings:

    "agent_servers": {
      "claude-container": {
        "command": "claude-container",
        "args": ["acp"]
      }
    }

Conversations persist in the per-repo claude-config directory and are
visible across reopens — close Zed, reopen, and prior threads are listed
and resumable.

By default, ACP sessions use the `med` sandbox profile (anthropic, github,
npmjs, pypi, nix substituters pre-allowed; unknown domains held for
browser approval). Override with `--profile=high` (anthropic-only) or
`--profile=low` (allow-all).

The ACP container is ephemeral (--rm) — when Zed disconnects, the
container exits but conversation history is preserved on disk.
```

- [ ] **Step 3: Adjust the "GETTING STARTED" example to use bare invoke**

Replace `claude-container run --yolo` with `claude-container --yolo` so the README leads with the new feel-like-claude behavior.

- [ ] **Step 4: Commit**

```sh
git add README.md
git commit -m "docs(readme): document bare invoke, acp subcommand, tui relocation"
```

---

### Task 29: Update `doctor` to surface bare-invoke + ACP discoverability

**Files:**
- Modify: `cmd/doctor.go`

- [ ] **Step 1: Append a hint to doctor's output**

Find the success path of `cmd/doctor.go` (the lines printed after all checks pass). Add at the end:

```go
fmt.Println()
fmt.Println("Quickstart:")
fmt.Println("  claude-container             # new sandboxed session in current dir")
fmt.Println("  claude-container acp         # ACP backend for Zed (and other ACP IDEs)")
fmt.Println("  claude-container tui         # dashboard for managing sessions")
```

- [ ] **Step 2: Build and run**

```sh
go build ./... && ./claude-container doctor
```

Expected: doctor output now ends with a Quickstart hint.

- [ ] **Step 3: Commit**

```sh
git add cmd/doctor.go
git commit -m "docs(doctor): add quickstart hint with bare invoke and acp"
```

---

### Task 30: Add E2E tests for bare invoke and TUI subcommand

**Files:**
- Modify: `cmd/e2e_test.go`

- [ ] **Step 1: Append the tests**

Append to `cmd/e2e_test.go`:

```go
//go:build integration

func TestE2E_BareInvoke_Persistent(t *testing.T) {
	// Mirrors TestE2E_Run but invokes the binary with no subcommand.
	cwd := makeTempGitRepo(t)
	out, err := runBinary(t, cwd, []string{"-p", "echo hello", "--yolo", "--rm"}, 60*time.Second)
	if err != nil {
		t.Fatalf("bare invoke: %v\n%s", err, out)
	}
	// Assert the session was created and removed (—rm).
	listOut, _ := runBinary(t, cwd, []string{"ps"}, 5*time.Second)
	if strings.Contains(listOut, "bare-invoke") {
		t.Errorf("expected --rm session to be cleaned up; ps:\n%s", listOut)
	}
}

func TestE2E_TUISubcommand_Smoke(t *testing.T) {
	cwd := t.TempDir()
	cmd := exec.Command(binaryPath(t), "tui")
	cmd.Dir = cwd
	cmd.Stdin = strings.NewReader("q\n") // 'q' quits immediately
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("tui: %v\n%s", err, out)
	}
}
```

If helper functions like `runBinary`, `binaryPath`, `makeTempGitRepo` don't already exist in `e2e_test.go`, locate the closest equivalent (e.g. `runCmd` or `cliBinary`) and substitute their names. The test file is large; spend the time grepping rather than creating duplicates.

- [ ] **Step 2: Run the new tests**

```sh
go test -tags=integration -run "TestE2E_BareInvoke_Persistent|TestE2E_TUISubcommand_Smoke" -v -timeout 5m ./cmd/
```

Expected: PASS.

- [ ] **Step 3: Commit**

```sh
git add cmd/e2e_test.go
git commit -m "test(cmd): E2E for bare invoke and tui subcommand"
```

---

## Final verification

After all tasks land, run the full suite:

- [ ] `go build ./...`
- [ ] `go test ./...`
- [ ] `go test -tags=integration ./...` (slow; 10–15 minutes)
- [ ] Manual smoke checklist from the spec:
  1. `cd /tmp && claude-container --yolo -p "hello"` → drops into Claude with pwd mounted.
  2. `cd <some-git-repo> && claude-container --yolo -p "hello"` → creates `.worktrees/<name>/`, prompts about `.gitignore` if not already ignored.
  3. `claude-container acp < /dev/null` from a TTY → prints session info to stderr; exits when stdin closes.
  4. Configure Zed with `command=claude-container, args=["acp"]` → start a thread, send a message, get a tool-call approval prompt in browser, approve, continue.
  5. Close Zed, reopen, see prior thread, resume, send a message, verify continuity.
- [ ] Update `flake.lock` if `nixpkgs` was bumped to pick up `claude-agent-acp`. Pin the flake input.
