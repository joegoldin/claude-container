# Transparent Binary — Implementation Plan

**Status:** Active. Supersedes
`2026-05-07-acp-and-transparent-binary-implementation.md` (ACP scope removed).

## Reading order

1. `docs/plans/2026-05-17-transparent-binary-design.md` (design)
2. This file (tasks)
3. `docs/plans/2026-05-17-transparent-binary-handoff.md` (execution state)

## Execution model

Subagent-driven development per the `subagent-driven-development` skill:
implementer subagent → spec-compliance reviewer → code-quality reviewer →
commit → next task. The user has consented to working directly on `main`
with phase-boundary check-ins.

Per-task: run the touched package's tests + `go build ./...`. At each phase
boundary: `go test ./...` (and optionally `-tags=integration ./...`).

Dev shell is **devenv**: every Go command is `devenv shell -- go ...`.

## Phase summary

| Phase | Tasks    | Description                                | Status        |
|-------|----------|--------------------------------------------|---------------|
| 1     | 1–3      | Foundation utilities                       | DONE          |
| 2     | 4–13     | Session launcher core (`internal/session`) | Tasks 4–6 done; 7–13 remain |
| 3     | 14–16    | Bare-invoke + TUI relocation               | Not started   |
| 4     | 17–21    | Refactor `run`/`work`/`task`/`new`/`attach` to `session.Launch` | Not started   |
| 5     | 22–25    | Docs + E2E + final review                  | Not started   |

(Task 4 in the old plan — `docker.ACPRunArgs` — was reverted with the ACP
scope; it is not in this plan. Old task numbers 5/6/7 are renumbered to
4/5/6 here.)

## Phase 1 — Foundation utilities (DONE)

| Task | Description | Commit |
|------|-------------|--------|
| 1 | `internal/git/EnsureIgnored` helper | `2cbf6f3` + fix `9b39121` |
| 2 | `Mode` field on `config.Session` (defaults `"tty"` on old-record load) | `d150c28` |
| 3 | `Mode` field on `docker.RunOpts`; `CLAUDE_CONTAINER_MODE` env exported by `RunArgs`/`TaskRunArgs` | `0f5f49c` |

## Phase 2 — Session launcher core

After Phase 2, `internal/session/` exists with unit tests but no `cmd/*.go`
calls it yet. Existing commands are untouched.

### Task 4: `session.Opts` + per-mode defaults — DONE

Commit `63261ed`. Added `internal/session/options.go` and
`internal/session/options_test.go` with:
- `Mode` enum (`ModeTTY`, `ModeTask`, `ModeBackground`)
- `WorktreeMode` enum (`WorktreeAuto`, `WorktreeAlways`, `WorktreeNever`)
- `Opts` struct (all session config fields)
- `ApplyDefaults()` and `Validate()`
- 4 tests covering TTY / Task / Background defaults and explicit-profile
  override.

(The ACP enum, ACP defaults branch, and ACP test were removed in commit
`5c5b98c`.)

### Task 5: `ResolveWorkspace` non-git pwd passthrough — DONE

Commits `cf4c73f` + `877c3ea`. Added `internal/session/workspace.go` and
`workspace_test.go` with the `Workspace` struct and the non-git +
`WorktreeNever` branches.

(The ACP-mode branch and `TestResolveWorkspace_Git_ACPMode_ForcesPwd` were
removed in commit `5c5b98c`.)

### Task 6: `ResolveWorkspace` git + ignored `.worktrees` — DONE

Commits `949d556` + `b90da76`. Added `ensureWorktreeBase` helper, the
git+worktree branch, and `TestResolveWorkspace_Git_DotWorktreesAlreadyIgnored`
+ `TestResolveWorkspace_Git_CreatesWorktreesDirWhenMissing`.

### Task 7: `ResolveWorkspace` — `.gitignore` mutation test

**Files:** modify `internal/session/workspace_test.go`.

The implementation already handles this via `gitpkg.EnsureIgnored` from
Phase 1. We just need a test to lock it down.

**Step 1: Write the test.** Append to `workspace_test.go`:

```go
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

Add `"strings"` to the imports if it isn't already present.

**Step 2: Run tests.**

```sh
devenv shell -- go test ./internal/session/ -v
```

Expected: PASS — relies on `EnsureIgnored` from Phase 1.

**Step 3: Commit.**

```sh
git add internal/session/workspace_test.go
git commit -m "test(session): assert .gitignore append for ResolveWorkspace"
```

### Task 8: `ResolveWorkspace` — read-only `.gitignore` fallback

**Files:**
- Modify: `internal/session/workspace.go`
- Modify: `internal/session/workspace_test.go`

**Step 1: Write the failing test.** Append to `workspace_test.go`:

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
	want := filepath.Join(tmpHome, "claude-container", "worktrees")
	if !strings.HasPrefix(ws.HostPath, want) {
		t.Fatalf("expected fallback under %q, got %q", want, ws.HostPath)
	}
	if ws.Branch != "fallback-foo" {
		t.Errorf("Branch: want %q, got %q", "fallback-foo", ws.Branch)
	}
}
```

**Step 2: Run test to verify it fails.**

```sh
devenv shell -- go test ./internal/session/ -run TestResolveWorkspace_Git_GitignoreReadOnly -v
```

Expected: FAIL — currently the read-only branch returns the
`"ensure ignored: ... (fallback not implemented)"` error.

**Step 3: Implement the fallback in `workspace.go`.** Replace the existing
`EnsureIgnored` block with:

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

Add the helper at the bottom of `workspace.go`:

```go
// globalWorktreeDir returns $XDG_DATA_HOME/claude-container/worktrees/<repo-id>/<branch>
// (defaulting XDG_DATA_HOME to ~/.local/share), creating the parent if missing.
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

Add to imports: `crypto/sha256`, `encoding/hex`.

**Step 4: Run tests.**

```sh
devenv shell -- go test ./internal/session/ -v
```

Expected: PASS — all workspace tests green (5 from Task 5 + 3 from Task 6 +
1 from Task 7 + 1 new from Task 8 = 10 total).

**Step 5: Commit.**

```sh
git add internal/session/workspace.go internal/session/workspace_test.go
git commit -m "feat(session): fallback worktree dir when .gitignore is read-only"
```

### Task 9: `session.Handle` skeleton

**Files:** create `internal/session/handle.go`.

**Step 1: Write the file.**

```go
// internal/session/handle.go
package session

import (
	"sync"

	"github.com/joegoldin/claude-container/internal/proxy"
)

// CleanupFunc runs once when the session ends (container removed, proxy
// torn down, session record deleted, etc.). The session-launcher composes
// these in Launch.
type CleanupFunc func()

// Handle is returned by Launch. The caller picks one of AttachTTY,
// WaitTask, RunBackground based on intent.
type Handle struct {
	Name      string
	Container string
	Repo      string
	Branch    string
	ProxyPort int
	StatusBar proxy.StatusBarInfo

	cleanupOnce sync.Once
	cleanup     CleanupFunc
}

// Cleanup runs the cleanup function (idempotent).
func (h *Handle) Cleanup() {
	if h.cleanup == nil {
		return
	}
	h.cleanupOnce.Do(h.cleanup)
}
```

**Step 2: Build and write a small test** to lock the idempotency contract.
Create `internal/session/handle_test.go`:

```go
package session

import "testing"

func TestHandle_CleanupIdempotent(t *testing.T) {
	calls := 0
	h := &Handle{cleanup: func() { calls++ }}
	h.Cleanup()
	h.Cleanup()
	h.Cleanup()
	if calls != 1 {
		t.Fatalf("Cleanup called %d times, want 1", calls)
	}
}

func TestHandle_CleanupNil(t *testing.T) {
	h := &Handle{} // no cleanup function
	h.Cleanup()    // must not panic
}
```

**Step 3: Run tests + build.**

```sh
devenv shell -- go test ./internal/session/ -v
devenv shell -- go build ./...
```

**Step 4: Commit.**

```sh
git add internal/session/handle.go internal/session/handle_test.go
git commit -m "feat(session): add Handle skeleton with idempotent Cleanup"
```

### Task 10: `output_tty.go` — wraps existing `proxy.Run`

**Files:** create `internal/session/output_tty.go`.

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

Verify the `proxy.Opts` struct field names by reading `internal/proxy/proxy.go`.
If `DockerArgs` / `ContainerName` / `StatusBar` differ, match the real names.

**Build + commit.**

```sh
devenv shell -- go build ./...
git add internal/session/output_tty.go
git commit -m "feat(session): add AttachTTY wrapping internal/proxy"
```

### Task 11: `output_task.go` — task waiter

**Files:** create `internal/session/output_task.go`.

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
	}, nil
}
```

`Model` and `MaxTurns` fields are unused here — they go to `Launch` via
`Opts`. They live on `TaskOpts` so the caller has a single struct to pass
to the eventual task helper if needed.

**Build + commit.**

```sh
devenv shell -- go build ./...
git add internal/session/output_task.go
git commit -m "feat(session): add WaitTask output adapter shell"
```

### Task 12: `output_background.go`

**Files:** create `internal/session/output_background.go`.

```go
// internal/session/output_background.go
package session

// RunBackground returns immediately after Launch has started the container
// detached. The session record was already saved. Cleanup intentionally
// does NOT fire here — the session outlives this process and is removed
// by `claude-container rm` or `gc`.
func (h *Handle) RunBackground() error {
	return nil
}
```

**Build + commit.**

```sh
devenv shell -- go build ./...
git add internal/session/output_background.go
git commit -m "feat(session): add RunBackground no-op output adapter"
```

### Task 13: `session.Launch` — orchestration

The largest task. Writes the single source of truth for "create a sandboxed
Claude session."

**Files:** create `internal/session/launch.go`.

**Step 1: Write the file.**

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
	gitpkg "github.com/joegoldin/claude-container/internal/git"
	"github.com/joegoldin/claude-container/internal/httpproxy"
	"github.com/joegoldin/claude-container/internal/proxy"
	sandboxPkg "github.com/joegoldin/claude-container/internal/sandbox"
)

// Launch creates and starts a Claude Code container with all the
// requested scaffolding (workspace, proxy, config dir, session record)
// and returns a Handle whose method the caller invokes.
func Launch(ctx context.Context, store *config.Store, opts Opts) (*Handle, error) {
	opts.ApplyDefaults()
	if err := opts.Validate(); err != nil {
		return nil, err
	}

	// Step 1: image readiness.
	if err := docker.EnsureImage(config.DefaultDir()); err != nil {
		return nil, err
	}

	// Step 2: workspace resolution and session name.
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
	claudeConfigDir, err := store.PrepareSessionConfig(opts.Name, repoRoot, opts.Resume)
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
	proxyAllow := append([]string(nil), opts.AllowDomains...)
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

	// Step 6: resolve extra mounts (-w paths and -W named workspace).
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
			runOpts.RepoPath = repoRoot
		}
	}

	var dockerArgs []string
	switch opts.Mode {
	case ModeTask:
		dockerArgs = docker.TaskRunArgs(runOpts, "", 0)
	default:
		dockerArgs = docker.RunArgs(runOpts, true) // detached
	}

	startCmd := exec.CommandContext(ctx, "docker", dockerArgs...)
	startCmd.Stderr = os.Stderr
	if err := startCmd.Run(); err != nil {
		return nil, fmt.Errorf("docker run: %w", err)
	}

	// Step 8: persist session record.
	sess := &config.Session{
		Name:            opts.Name,
		Branch:          ws.Branch,
		WorktreePath:    ws.HostPath,
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
		for _, p := range paths {
			if _, e := gitpkg.RepoRoot(p); e != nil {
				return nil, nil, fmt.Errorf("worktree mode: %q is not a git repo", p)
			}
			multiRepos = append(multiRepos, p)
		}
		return nil, multiRepos, nil
	}
	return paths, nil, nil
}
```

**Step 2: Build to surface helper signature mismatches.**

```sh
devenv shell -- go build ./...
```

Likely fix-ups:
- `httpproxy` cleanup function names — check `internal/httpproxy/` and
  swap in the real symbols (`RemoveSession` vs `RemoveSessionState` vs
  whatever exists).
- `config.GenerateName` / `config.HostClaudeCredentialFiles` — confirm
  they exist; otherwise inline what `cmd/run.go` does today.
- `sandboxPkg.GetProfile` and `Profile.ManagedSettingsForProxy` /
  `Profile.ProxyRules` — read `internal/sandbox/` for the real API.

Adjust to match. This is the integration task — expect 2–3 fix-up edits
before it compiles.

**Step 3: Run unit tests across affected packages.**

```sh
devenv shell -- go test ./internal/session/ ./internal/docker/ ./internal/config/ ./internal/git/
```

Expected: PASS.

**Step 4: Commit.**

```sh
git add internal/session/launch.go
git commit -m "feat(session): orchestrate Launch — image, workspace, proxy, container, record"
```

**Phase 2 boundary check:**

```sh
devenv shell -- go test ./...
devenv shell -- go build ./...
```

Pause for user check-in before Phase 3.

## Phase 3 — Bare invoke + TUI relocation

### Task 14: `cmd/tui.go` — relocate dashboard

**Files:**
- Create: `cmd/tui.go`
- Modify: `cmd/root.go` (remove dashboard `RunE`, leave `runDefault` as a
  TODO stub returning an error like "not implemented — landing in Task 15")

**Step 1:** Read the current `cmd/root.go` to identify the dashboard
launch code (the body of the root command's `RunE`). Move it verbatim
into a new `cmd/tui.go`:

```go
// cmd/tui.go
package cmd

import (
	"github.com/spf13/cobra"
)

var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Open the dashboard",
	Long:  `Open the Bubble Tea dashboard. This is what bare 'claude-container' used to do.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTUI(cmd, args)
	},
}

func init() {
	rootCmd.AddCommand(tuiCmd)
}

// runTUI is the body of the old root command's RunE. Pasted verbatim.
func runTUI(cmd *cobra.Command, args []string) error {
	// ... copy from old root.go RunE ...
}
```

In `cmd/root.go`, replace the `RunE` body with a temporary stub:

```go
RunE: func(cmd *cobra.Command, args []string) error {
	return fmt.Errorf("bare-invoke not yet wired up; use 'claude-container tui' for the dashboard")
},
```

**Step 2:** Build, run the binary manually with `claude-container tui` and
confirm the dashboard still launches.

**Step 3:** Add a smoke E2E test in `cmd/e2e_test.go`:

```go
func TestE2E_TUISubcommand_Smoke(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	// Just verify the binary exits cleanly when given 'tui --help'.
	out, err := exec.Command(binaryPath(t), "tui", "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("tui --help failed: %v\n%s", err, out)
	}
	if !bytes.Contains(out, []byte("dashboard")) {
		t.Errorf("expected help to mention dashboard; got: %s", out)
	}
}
```

(Adapt `binaryPath(t)` to whatever helper exists in `cmd/e2e_test.go`.)

**Step 4: Commit.**

```sh
git add cmd/tui.go cmd/root.go cmd/e2e_test.go
git commit -m "feat(cmd): add 'tui' subcommand, stub bare invoke"
```

### Task 15: `cmd/root.go` — bare-invoke session creation

**Files:** modify `cmd/root.go`.

**Step 1: Declare the bare-invoke flag set.** Use cobra's `Flags()` on
`rootCmd`, not `PersistentFlags()` (avoid leaking flags to subcommands).
Union of `run` + `work` flags:

```go
func init() {
	f := rootCmd.Flags()
	f.Bool("yolo", false, "skip Claude Code permission prompts")
	f.StringP("prompt", "p", "", "initial prompt to send")
	f.String("name", "", "session name (auto-generated if empty)")
	f.BoolP("background", "b", false, "run detached")
	f.Bool("rm", false, "remove container on exit")
	f.StringArrayP("mount", "w", nil, "extra host path to mount")
	f.StringP("workspace", "W", "", "named workspace to use")
	f.String("profile", "default", "sandbox profile (low|default|med|high)")
	f.StringArray("allow-domain", nil, "domains the proxy should allow")
	f.StringArray("deny-path", nil, "filesystem paths to deny")
	f.StringArray("allow-command", nil, "shell commands to allow")
	f.StringArray("deny-command", nil, "shell commands to deny")
	f.String("preset", "", "proxy seed preset")
	f.Int("proxy-port", 0, "host port for the proxy dashboard")
	f.String("from", "", "base branch/ref for new worktree")
	f.Bool("no-worktree", false, "pwd passthrough even in git repos")
	f.String("resume", "", "resume mode (picker, last, <id>)")
}
```

**Step 2: Replace the stub `RunE` with `runDefault`.**

```go
RunE: func(cmd *cobra.Command, args []string) error {
	return runDefault(cmd)
},
```

```go
// runDefault is what bare 'claude-container' does: create a session in the
// current directory and attach to it.
func runDefault(cmd *cobra.Command) error {
	ctx := cmd.Context()

	opts := session.Opts{
		Mode:         session.ModeTTY,
		WorktreeMode: session.WorktreeAuto,
	}
	// Read flags into opts...
	if v, _ := cmd.Flags().GetBool("yolo"); v {
		opts.Yolo = v
	}
	// (continue for each declared flag)

	store := config.NewStore(config.DefaultDir())
	h, err := session.Launch(ctx, store, opts)
	if err != nil {
		return err
	}
	if opts.Background {
		return h.RunBackground()
	}
	return h.AttachTTY()
}
```

Be explicit and methodical with each flag — don't skip any from the list in
Task 15 Step 1.

**Step 3: Verify** by building and running `claude-container` from a
non-git temp dir, then a git temp dir, and from this repo:

```sh
devenv shell -- go build -o /tmp/cc ./...
cd /tmp && mkdir -p non-git-test && cd non-git-test && /tmp/cc --help
# (smoke; full session needs docker)
```

**Step 4: Add an E2E case** if docker is available locally; otherwise
defer to Task 24.

**Step 5: Commit.**

```sh
git add cmd/root.go
git commit -m "feat(cmd): bare invoke creates a session in the current dir"
```

### Task 16: First-time bare-invoke notice

**Files:** modify `cmd/root.go`.

When bare invoke runs and `sessions.json` is non-empty AND a flag file
`~/.config/claude-container/migrated-bare-invoke` is missing AND the env
var `CLAUDE_CONTAINER_QUIET` is unset:

1. Print to stderr:
   ```
   note: bare claude-container now creates a session; run 'claude-container tui' for the dashboard (existing sessions: <count>)
   ```
2. Touch the flag file so the notice does not repeat.

Add a small helper:

```go
func maybePrintBareInvokeNotice(store *config.Store) {
	if os.Getenv("CLAUDE_CONTAINER_QUIET") != "" {
		return
	}
	flagPath := filepath.Join(config.DefaultDir(), "migrated-bare-invoke")
	if _, err := os.Stat(flagPath); err == nil {
		return
	}
	sessions, _ := store.List()
	if len(sessions) == 0 {
		// no prior state, no migration needed
		_ = os.WriteFile(flagPath, []byte(""), 0o644)
		return
	}
	fmt.Fprintf(os.Stderr,
		"note: bare claude-container now creates a session; run 'claude-container tui' for the dashboard (existing sessions: %d)\n",
		len(sessions),
	)
	_ = os.WriteFile(flagPath, []byte(""), 0o644)
}
```

Call from `runDefault` before `session.Launch`.

**Add a unit test** that uses a temp config dir, seeds `sessions.json`
with one entry, captures stderr, calls the helper, asserts the message
and the flag file. Place in `cmd/root_test.go` (new file).

**Commit:**

```sh
git add cmd/root.go cmd/root_test.go
git commit -m "feat(cmd): first-time bare-invoke migration notice"
```

**Phase 3 boundary check:**

```sh
devenv shell -- go test ./...
devenv shell -- go build ./...
```

Pause for user check-in before Phase 4.

## Phase 4 — Refactor existing commands to `session.Launch`

Each task in this phase converges one existing command onto the new
launcher. The pattern is:

1. Identify the command's current launch sequence (most live in
   `cmd/run.go` / `cmd/work.go` etc.).
2. Replace with: flag parsing → build `session.Opts` → `session.Launch` →
   appropriate `Handle` method.
3. Keep behavior identical — run the matching E2E case after.

For each task: run the package-level test (`devenv shell -- go test
./cmd/`) and the matching E2E test if docker is available.

### Task 17: refactor `cmd/run.go`

`run` is pwd-passthrough always, persistent by default.

- `session.Opts{Mode: ModeTTY, WorktreeMode: WorktreeNever}` (or set
  `NoWorktree: true` — ApplyDefaults coerces).
- Honor existing flags by reading them into `Opts`.
- Call `Launch` → `AttachTTY` (or `RunBackground` if `--background`).
- Delete the previously duplicated launch code from `cmd/run.go`.

Run `cmd/e2e_test.go`'s `run` cases to confirm parity.

**Commit:** `refactor(cmd): run delegates to session.Launch`

### Task 18: refactor `cmd/work.go`

`work` is explicit worktree mode, persistent.

- `session.Opts{Mode: ModeTTY, WorktreeMode: WorktreeAlways}`.
- New sessions land in `<repo>/.worktrees/<name>/` (resolution handled
  by `ResolveWorkspace`). Existing sessions reattach via `cmd/attach.go`
  (refactored in Task 21) and continue to use their old paths.
- Add a one-line stderr notice when an existing session's `WorktreePath`
  is outside the repo: `"note: this session uses a legacy worktree path; new sessions land in .worktrees/"`.
- Call `Launch` → `AttachTTY`.

Run `work` E2E cases.

**Commit:** `refactor(cmd): work delegates to session.Launch, new worktree location`

### Task 19: refactor `cmd/task.go`

`task` is ephemeral by default, parses stream-json output.

- `session.Opts{Mode: ModeTask, AutoRemove: !keep}`.
- Call `Launch` → `WaitTask`.
- Parse `TaskResult.Logs` with the existing parser to produce the final
  assistant text + stats output.

Run `task` E2E cases.

**Commit:** `refactor(cmd): task delegates to session.Launch + WaitTask`

### Task 20: refactor `cmd/new.go`

`new` is the wizard. Keep the entire wizard UI; the only change is that
the final "launch the session" step builds `session.Opts` and calls
`Launch` instead of duplicating the launch sequence.

**Commit:** `refactor(cmd): new wizard delegates final launch to session.Launch`

### Task 21: refactor `cmd/attach.go`

`attach` resumes an existing session. If the container is missing
(stopped/removed), it currently recreates the session. Move that recreate
path onto `session.Launch`:

- Read the existing `Session` record.
- Build `Opts` from the record (Mode, Profile, AllowDomains, etc.).
- Call `Launch` (which creates a fresh container) → `AttachTTY`.

Other attach paths (container exists and is running) keep their existing
short-circuit.

Run `attach` E2E cases.

**Commit:** `refactor(cmd): attach recreate path uses session.Launch`

**Phase 4 boundary check:**

```sh
devenv shell -- go test ./...
devenv shell -- go test -tags=integration ./...   # if Docker is available
devenv shell -- go build ./...
```

Pause for user check-in before Phase 5.

## Phase 5 — Docs, E2E, final review

### Task 22: README updates

Update `README.md`:
- SYNOPSIS shows bare `claude-container` as the primary entry point.
- A new short "Quickstart" section: `cd <project> && claude-container`.
- Note that the dashboard moved to `claude-container tui`.
- Drop any references to ACP / Zed.

**Commit:** `docs: bare invoke is the primary entry point`

### Task 23: `doctor` quickstart hint

Modify `cmd/doctor.go` to print a one-liner at the end of a successful
`doctor` run:

```
Quickstart: cd <your repo> && claude-container
Dashboard:  claude-container tui
```

Add a unit test (or assert via the existing doctor test) that the
quickstart hint is included.

**Commit:** `feat(doctor): print quickstart hint after a successful run`

### Task 24: E2E coverage for bare invoke + TUI subcommand

Add to `cmd/e2e_test.go`:

```go
func TestE2E_BareInvoke_GitRepo_CreatesWorktree(t *testing.T) {
	if testing.Short() || !dockerAvailable(t) {
		t.Skip()
	}
	// Create a temp git repo, run claude-container -p "hello" in it, exit,
	// assert .worktrees/<name>/ exists and is a directory.
	// Cleanup: docker rm -f the container, remove sessions.json entry.
}

func TestE2E_BareInvoke_NonGit_PwdMount(t *testing.T) {
	if testing.Short() || !dockerAvailable(t) {
		t.Skip()
	}
	// Same as above but in a non-git temp dir.
}

func TestE2E_TUISubcommand(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	// 'claude-container tui --help' returns 0 and mentions "dashboard".
}
```

Use the existing `dockerAvailable` and binary-path helpers from
`cmd/e2e_test.go`.

**Commit:** `test(cmd): E2E coverage for bare invoke and tui subcommand`

### Task 25: Final code review

Dispatch a final code-quality reviewer over the entire phase 2–5 diff
(base SHA from the start of phase 2 to HEAD). Address any
critical/important findings; mark minor findings as follow-ups.

When clean: tag and ship.

## Per-task workflow (the playbook)

For each task in phase 2 and beyond:

1. Capture base SHA: `git rev-parse HEAD`.
2. Dispatch implementer subagent (`Agent` tool, `general-purpose`, model
   `haiku` for mechanical tasks, `sonnet` for integration tasks like
   Task 13). Paste the full task text. Tell the implementer to follow
   TDD, run package-level tests, build, and commit. Be explicit: "Do NOT
   run `go test ./...` — deferred to phase boundary." Be explicit: "Use
   `devenv shell --` for all Go commands."
3. Spec-compliance review (model `sonnet`): paste task text + base/head
   SHAs.
4. Code-quality review (model `sonnet`): same SHAs, ask for
   strengths/issues (Critical/Important/Minor)/assessment.
5. If reviewers raise blockers, dispatch a fix subagent with targeted
   instructions and re-review.
6. Mark the task complete and proceed.

If a subagent's edits land in the working tree but the commit was
interrupted, verify the working-tree diff matches the plan, run package
tests + build, and commit yourself.

## Conventions

- Errors lowercase, no trailing punctuation: `fmt.Errorf("open config: %w", err)`.
- Cobra commands register themselves in `init()`.
- Don't auto-format files you aren't touching (no global `gofmt`).
- Commit prefixes: `feat:`, `fix:`, `refactor:`, `test:`, `docs:`,
  `chore:`. No LLM marker phrases in commit messages.
- Keep files small and focused; one responsibility per file.
- Don't commit `.gitignore` mutations as part of unrelated work.
- Don't `git add -A` — always stage specific files. The working tree has
  untracked dotfiles (`.bash_profile`, `.idea`, etc.) that must NOT be
  committed.
