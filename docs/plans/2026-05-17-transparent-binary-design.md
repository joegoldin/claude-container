# Transparent Binary — Design

**Status:** Active design. Supersedes `2026-05-07-acp-and-transparent-binary-design.md`
(ACP support dropped — `claude-agent-acp` requires SDK billing outside Claude Max).

## Summary

Reshape `claude-container` so the binary feels like a drop-in safe replacement for
`claude` while preserving every existing command. Bare `claude-container`
creates and attaches to a sandboxed Claude session in the current directory;
the TUI dashboard that bare invocation launches today moves to
`claude-container tui`. All other subcommands (`run`, `work`, `task`, `new`,
`ps`, `attach`, `extract`, `gc`, `auth`, `doctor`, `build`, `shell`, `stop`,
`rm`, `logs`, `workspace`, `conversations`, `completion`) keep their exact
CLI surfaces.

Internally, a new `internal/session/` package becomes the single source of
truth for "create a sandboxed Claude session." Existing `cmd/*.go` files
shrink to thin wrappers around `session.Launch()`.

## Goals

- Bare `claude-container` feels indistinguishable from running `claude`: no
  flags required, no TUI to navigate first.
- Worktree-by-default in a git repo, pwd-passthrough otherwise — match the
  ergonomics users already expect from `work` and `run` without forcing them
  to choose.
- All current functionality preserved: every existing subcommand, flag, file
  path, env var, and behavior continues to work.
- Per-session proxy with approval UI is automatic on bare invoke.
- Refactor convergence: `run`, `work`, `task`, `new`, `attach` all share one
  launch code path (`session.Launch`) and one cleanup code path.

## Non-goals

- ACP / Zed integration. Deferred indefinitely (cost model conflict).
- New persistence layer — existing per-repo `claude-config/<repo-id>/` store
  is unchanged.
- Reworked TUI or new dashboard features.

## Architecture

```
internal/session/
  options.go             // Opts struct + per-mode defaults + Validate
  workspace.go           // ResolveWorkspace: git → worktree, else → pwd
  handle.go              // Handle struct + Cleanup
  output_tty.go          // Handle.AttachTTY  (wraps internal/proxy)
  output_task.go         // Handle.WaitTask   (docker wait + logs)
  output_background.go   // Handle.RunBackground (no-op, container outlives caller)
  launch.go              // Launch(ctx, store, opts) -> *Handle
```

`session.Launch(ctx, store, opts)` does, in order:

1. Apply per-mode defaults; validate.
2. Ensure the docker image is loaded (existing `docker.EnsureImage`).
3. Resolve workspace path (worktree if git + worktree mode, else pwd).
4. Ensure per-repo claude-config dir, write `managed-settings.json` to a
   per-session config dir, set up `projects/` symlink per the resume mode.
5. Ensure the per-session proxy is up (existing `internal/httpproxy`).
6. `docker run` the container with the right command (`claude` /
   `claude -p`) based on `opts.Mode`.
7. Persist the `Session` record to `sessions.json` (with `Mode` field).
8. Return a `Handle` whose method the caller picks: `AttachTTY` /
   `WaitTask` / `RunBackground`.

Each `cmd/*.go` shrinks to: flag parsing → build `session.Opts` → call
`session.Launch` → invoke the right `Handle` method. The bare-invoke path in
`cmd/root.go` is the same pattern.

The existing `internal/docker`, `internal/httpproxy`, `internal/git`,
`internal/proxy` (PTY proxy), and `internal/config` packages keep their
current responsibilities. `session/` glues them together.

## Modes

```go
const (
    ModeTTY        Mode = "tty"        // interactive, PTY attach (bare invoke, run, work, attach)
    ModeTask       Mode = "task"       // claude -p one-shot, parse stream-json result
    ModeBackground Mode = "background" // detached run, no attach
)
```

The mode value is also exposed to the container as `CLAUDE_CONTAINER_MODE`
so the entrypoint can branch behavior if needed (currently informational).

## Workspace resolution

`session.ResolveWorkspace(cwd, opts)` returns:

```go
type Workspace struct {
    HostPath string  // host path mounted as /workspace; empty in worktree mode
    RepoPath string  // git toplevel; empty if not a git repo
    Worktree bool    // true → entrypoint creates a worktree from /mnt/repo
    Branch   string  // worktree branch; empty for pwd passthrough
}
```

Decision tree:

1. **`opts.WorktreeMode == WorktreeNever`** (set by `--no-worktree`, `run`,
   and `task` by default) → pwd passthrough.
   - `HostPath = cwd`, `Worktree = false`. If cwd is in a git repo,
     populate `RepoPath` informationally.

2. **In a git repo and worktree mode is on** (default for bare invoke and
   `work`):
   - `RepoPath = git rev-parse --show-toplevel`.
   - Pick a worktree base directory inside the repo, in priority order:
     - `<repo>/.worktrees` if it exists.
     - `<repo>/worktrees` if it exists.
     - Otherwise create `<repo>/.worktrees`.
   - **Verify the dir is gitignored** via `gitpkg.EnsureIgnored`. If not,
     append `<base>/` to `<repo>/.gitignore`. **Do not auto-commit** —
     leave the modification in the working tree, print a one-line stderr
     notice (`note: added .worktrees/ to .gitignore — commit when convenient`).
   - **Fallback if `.gitignore` is not writable:** create the worktree at
     `$XDG_DATA_HOME/claude-container/worktrees/<repo-id>/<branch>/`
     (defaulting `XDG_DATA_HOME` to `~/.local/share`). Print a stderr notice.
   - Branch name = `opts.WorktreeName` if set, else `opts.Name` (session
     name, e.g. `myproject-calm-reef`). If both empty, return an error.
   - The actual `git worktree add` continues to happen inside the
     container's entrypoint (existing `nix/image.nix` behavior). The host
     computes the path and passes `WORKTREE_BRANCH` / `WORKTREE_FROM` env.
     The host mounts the repo at `/mnt/repo`.

3. **Not in a git repo** → pwd passthrough as in (1).

**Multi-repo workspaces (`-W my-repos`).** Same logic per repo; each repo
with worktree mode gets its own `<repo>/.worktrees/<branch>/`. Container
mounts each at `/mnt/repos/<basename>` and the entrypoint creates the
worktree, exactly as today.

**Migration of existing worktrees.** Sessions in `sessions.json` keep their
existing `~/.config/claude-container/worktrees/<session>/` paths. Only **new**
sessions use the new in-repo location. No migration script.

## Output adapters

Three adapters on `*Handle`, each chosen by the caller after `Launch`
returns:

```go
type Handle struct {
    Name      string
    Container string
    Repo      string
    Branch    string
    ProxyPort int
    StatusBar proxy.StatusBarInfo
    cleanup   func()
}

func (h *Handle) AttachTTY() error
func (h *Handle) WaitTask(ctx context.Context, opts TaskOpts) (TaskResult, error)
func (h *Handle) RunBackground() error
func (h *Handle) Cleanup()  // idempotent
```

**`AttachTTY`** wraps the existing `internal/proxy.Run()` (PTY proxy).
Used by bare invoke, `run`, `work`, `attach`. Same status bar, Ctrl+B
chord, auto-restart on stopped containers. Zero behavior change vs. today.

**`WaitTask`** runs `docker wait` against the container, then `docker logs`,
returning a `TaskResult` with the raw stream-json logs and exit code. The
caller (`cmd/task.go`) parses the final assistant text from the logs using
its existing parser.

**`RunBackground`** is a no-op: `Launch` already started the container
detached with `--detach`. The session record was already saved. Cleanup
intentionally does NOT fire here — the session outlives this process and
is cleaned up by `claude-container rm` / `gc`.

## Bare-invocation behavior change

Today: bare `claude-container` launches the TUI dashboard. After this
change: bare `claude-container` creates and attaches to a session in the
current dir.

**New behavior matrix:**

```
claude-container                 → bare invoke: smart default
                                   - git repo, no flags  → worktree at .worktrees/<name>/, persistent
                                   - non-git, no flags    → pwd passthrough, persistent
                                   - --no-worktree        → pwd passthrough even in git
                                   - flags (--yolo, -p, --profile, etc.) honored as on `run`/`work`
claude-container run [flags]     → preserved; pwd-passthrough always
claude-container work [flags]    → preserved; explicit worktree
claude-container tui             → new; the dashboard that bare-invoke used to launch
```

All other subcommands (`new`, `ps`, `attach`, `task`, `extract`, `gc`,
`auth`, `doctor`, `build`, `shell`, `stop`, `rm`, `logs`, `workspace`,
`conversations`, `completion`) are preserved verbatim.

**`cmd/root.go` change.** Replace the dashboard-launching `RunE` with
`runDefault`, which builds `session.Opts{Mode: TTY, WorktreeMode: Auto,
Persistent: true, Yolo: false}` and calls `Launch` + `AttachTTY`.

**`cmd/tui.go` (new).** Registers `tui` as a subcommand whose body is the
current `cmd/root.go` dashboard loop verbatim. Becomes the new home for
everything that today happens "on bare invoke."

**Flag set on bare invoke.** Union of `run` and `work` flags: `--yolo`,
`--prompt`/`-p`, `--name`, `--background`/`-b`, `--rm`, `--mount`/`-w`,
`--workspace`/`-W`, `--profile`, `--allow-domain`, `--deny-path`,
`--allow-command`, `--deny-command`, `--preset`, `--proxy-port`, `--from`,
`--no-worktree`, `--resume`. Declared directly on the root cobra command —
no flag inheritance to avoid global-flag surprises.

## Configuration defaults summary

| Mode                       | Workspace                          | Persistence       | Yolo | Profile    |
|----------------------------|------------------------------------|-------------------|------|------------|
| `claude-container`         | git → `<repo>/.worktrees/<name>/`  <br> non-git → pwd | persistent (no `--rm`) | off | `default` |
| `run`                      | pwd passthrough                    | persistent        | off  | `default`  |
| `work`                     | git worktree (`<repo>/.worktrees/<name>/`) | persistent | off | `default` |
| `task`                     | as flag specifies                  | ephemeral (default), `--keep` to persist | n/a | `default` |
| `tui` / `ps` / `attach`    | (operates on existing sessions)    | n/a               | n/a  | n/a        |

**Profile override.** All session-creating commands accept
`--profile {low,default,med,high}` plus `--allow-domain`,
`--allow-command`, `--deny-command`, `--deny-path`. No new flag surface.

## Migration & backward compatibility

**Sessions in `sessions.json`.** The `Session` struct already has a `Mode
string` field (added by Phase 1). Old records default to `tty` on
unmarshal. `attach` reads `WorktreePath` directly from the record, so
existing sessions work unchanged.

**Conversation history.** The existing per-repo migration from commit
2e7951f stays unchanged.

**Bare-invocation break.** The only user-visible break. Mitigation: on
first run after upgrade, if `sessions.json` is non-empty, print a one-line
notice to stderr before launching the session:

```
note: bare claude-container now creates a session; run 'claude-container tui' for the dashboard (existing sessions: <count>)
```

Suppress with `CLAUDE_CONTAINER_QUIET=1` or after first display (write a
flag file `~/.config/claude-container/migrated-bare-invoke`). Update README
and `doctor` output to make the relocation discoverable.

**Image rebuild.** `CLAUDE_CONTAINER_MODE` env was added in Phase 1
(commit `0f5f49c`); old image entrypoints simply ignore it. No image
rebuild required for this work.

**Scripts that bare-invoke.** A CI script running `claude-container` with
no args expecting the dashboard would now create a session. Documented as
breaking. Workaround: `claude-container tui`.

**`--rm` semantics.** `task` defaults ephemeral (`--keep` to persist).
Bare invoke and `run`/`work` default persistent. Each command's `--rm`
flag continues to override.

## Testing strategy

**Unit tests for `internal/session/`:**

- `options_test.go` — defaults per-mode (yolo, profile, rm, persistence)
  match the defaults table. DONE (Task 5).
- `workspace_test.go` — table-driven: non-git → pwd; git + worktree mode +
  ignored `.worktrees` → uses it; git + worktree mode + un-ignored
  `.worktrees` → mutates `.gitignore`; git + un-writable `.gitignore` →
  falls back to `$XDG_DATA_HOME/...`; `--no-worktree` forces pwd. Partial
  (Tasks 6–7 done, 8–9 pending).
- `handle_test.go` — `Cleanup` idempotent.

**Integration tests** (build tag `integration`, real docker required) in
`internal/session/session_integration_test.go`:

- `TestBareInvoke_GitRepo_CreatesWorktree` — bare in temp git repo →
  `.worktrees/<name>/` exists, `.gitignore` includes the entry.
- `TestBareInvoke_NonGit_PwdMount` — bare in non-git temp dir →
  workspace mount is the dir itself.

**`cmd/e2e_test.go` additions:**

- `TestE2E_BareInvoke_Persistent` — bare claude-container, exit, `ps`
  shows it, `attach` works.
- `TestE2E_TUISubcommand` — `claude-container tui` smoke test.

**Refactor regression coverage.** Existing E2E cases for `run`, `work`,
`task`, `attach`, etc. become the regression suite for the session-launcher
refactor. Do not delete or weaken any existing E2E case.

## File changes

| File | Change |
|------|--------|
| `internal/session/options.go` | Done — `Opts`, `Mode`, `WorktreeMode`, `ApplyDefaults`, `Validate` |
| `internal/session/workspace.go` | In progress — `ResolveWorkspace` + fallback (Tasks 6–9) |
| `internal/session/handle.go` | New — `Handle` type and `Cleanup` |
| `internal/session/output_tty.go` | New — wraps `internal/proxy.Run()` |
| `internal/session/output_task.go` | New — `docker wait`/`logs` shell |
| `internal/session/output_background.go` | New — no-op shell |
| `internal/session/launch.go` | New — orchestrates docker, proxy, config, record |
| `cmd/root.go` | Replace dashboard-launching `RunE` with `runDefault`; declare bare-invoke flag set; print first-time notice |
| `cmd/tui.go` | New — relocate dashboard launch loop here |
| `cmd/run.go` | Refactor body to `session.Launch` + `AttachTTY` |
| `cmd/work.go` | Refactor body to `session.Launch` + `AttachTTY`; new worktree location for fresh sessions |
| `cmd/task.go` | Refactor body to `session.Launch` + `WaitTask` |
| `cmd/new.go` | Refactor body; wizard remains, just calls `session.Launch` at the end |
| `cmd/attach.go` | Use `session.Launch` for the recreate-on-missing-container path |
| `internal/config/config.go` | Done — `Mode` field on `Session` (defaults `tty` on old records) |
| `internal/docker/docker.go` | Done — `Mode` field on `RunOpts`, `CLAUDE_CONTAINER_MODE` env |
| `internal/git/git.go` | Done — `EnsureIgnored` helper |
| `cmd/e2e_test.go` | Add bare-invoke + TUI E2E cases |
| `README.md` | Update SYNOPSIS, COMMANDS, EXAMPLES |

## Risks

- **Bare-invoke breaking scripts.** Mitigation: stderr notice + env var
  dismissal + flag file means it shows at most twice (once per machine
  after upgrade). Documented in CHANGELOG.
- **Concurrent gitignore mutation.** Two simultaneous bare invocations in
  the same fresh repo could race on appending to `.gitignore`. Mitigation:
  `gitpkg.EnsureIgnored` already file-locks the mutation. A duplicate
  `.worktrees/` line in `.gitignore` is harmless even without the lock.
- **Refactor regression risk.** Moving live code paths (`run`, `work`,
  `task`, `new`, `attach`) onto `session.Launch` is the largest change.
  Mitigation: keep the existing E2E suite as the regression spec, refactor
  one command at a time, verify the matching E2E case after each.
