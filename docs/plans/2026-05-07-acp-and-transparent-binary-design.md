# Transparent Binary + ACP Integration

> **SUPERSEDED 2026-05-17:** ACP scope was dropped (`claude-agent-acp`
> bills outside Claude Max). See
> `docs/plans/2026-05-17-transparent-binary-design.md` for the active
> design. This file is preserved for historical context.

## Summary

Reshape `claude-container` so the binary feels like a drop-in replacement for `claude` while preserving every existing command. Two new entry points:

1. **Bare `claude-container`** — creates a sandboxed Claude session in the current dir. Worktree at `<repo>/.worktrees/<name>/` if in a git repo, pwd passthrough otherwise. Persistent (resumable later).
2. **`claude-container acp`** — ACP entry point so editors like Zed can use claude-container as their agent backend. Pwd passthrough; ephemeral container; persistent conversations via per-repo JSONL store; full proxy + approval UI.

The TUI dashboard, today the bare-invocation behavior, moves to `claude-container tui`. All other subcommands (`run`, `work`, `task`, `new`, `ps`, `attach`, `extract`, `gc`, `auth`, `doctor`, `build`, `shell`, `stop`, `rm`, `logs`, `workspace`, `conversations`) are preserved with identical CLI surfaces.

Internally, the change introduces an `internal/session/` package as the single source of truth for "create a sandboxed Claude session." Existing `cmd/*.go` files become thin wrappers around `session.Launch()`.

## Goals

- Zed (and any ACP-compatible IDE) can use `claude-container` as an agent backend with a single command-line config: `command=claude-container, args=["acp"]`.
- Reopening Zed resumes prior threads from the per-repo conversation store with no special user action.
- Bare `claude-container` invocation feels indistinguishable from running `claude` — no flags required, no TUI to navigate first.
- All current functionality preserved: every existing subcommand, flag, file path, env var, and behavior continues to work.
- Per-session proxy with approval UI works in ACP mode just like it does for interactive sessions.

## Non-goals

- No Zed-specific code. We test against the ACP wire protocol directly.
- No new persistence layer for ACP — conversations go through the existing per-repo `claude-config/<repo-id>/projects/` store.
- No `cc-install` wrapper or skill bundled in v1. The agent uses `nix profile install nixpkgs#<pkg>` directly via the existing persistent `/nix/var` volume.
- No reworked TUI or new dashboard features.

## Architecture

A new `internal/session/` package owns the session lifecycle. Today this logic is duplicated across `cmd/run.go`, `cmd/work.go`, `cmd/new.go`, `cmd/task.go`, and `cmd/root.go`.

```
internal/session/
  options.go     // Opts struct + per-mode defaults
  workspace.go   // Resolve workspace: git → worktree, else → pwd
  launch.go      // Launch(ctx, opts) -> Handle
  handle.go      // Handle: AttachTTY, BridgeACP, WaitTask, RunBackground
  outputs/
    tty.go       // attach to interactive TTY (wraps internal/proxy)
    acp.go       // stdio bridge for ACP
    task.go      // detached run + collect final stream-json result
    background.go// detached run, return immediately
```

`session.Launch(opts)` does, in order:

1. Ensure the docker image is loaded (existing `docker.EnsureImage`).
2. Resolve workspace path (worktree if git + worktree mode, else pwd).
3. Compute repo-id, ensure per-repo claude-config dir, write `managed-settings.json` to a per-session config dir, set up `projects/` symlink as appropriate for the resume mode.
4. Ensure the per-session proxy is up (existing `internal/httpproxy`).
5. `docker run` the container with the right command (`claude` / `claude-agent-acp` / `claude -p`) based on `opts.Mode`.
6. Persist the `Session` record to `sessions.json` (with new `Mode` field).
7. Return a `Handle` whose method (`AttachTTY`, `BridgeACP`, `WaitTask`, `RunBackground`) the caller picks.

Each `cmd/*.go` shrinks to: flag parsing → build `session.Opts` → call `session.Launch` → invoke the appropriate `Handle` method. The `acp` subcommand is ~50 lines of caller code.

The existing `internal/docker`, `internal/httpproxy`, `internal/git`, `internal/proxy` (PTY proxy), and `internal/config` packages stay where they are. `session/` glues them together.

## Workspace resolution

`session.ResolveWorkspace(cwd, opts)` returns:

```go
type Workspace struct {
    HostPath string  // path mounted into container as /workspace (or used to mount /mnt/repo)
    RepoPath string  // git toplevel; empty if not a git repo
    Worktree bool    // true if HostPath is a worktree (created by container entrypoint)
    Branch   string  // branch name; empty if pwd passthrough
}
```

Decision tree:

1. **`opts.NoWorktree` set, or `opts.Mode == ACP`** → pwd passthrough.
   - `HostPath = cwd`, `Worktree = false`. If cwd is in a git repo, set `RepoPath` informationally.

2. **In a git repo and worktree mode is on** (default for bare invoke, `work`, explicit `--worktree`):
   - `RepoPath = git rev-parse --show-toplevel`.
   - Pick a worktree base directory inside the repo, in priority order:
     - `<repo>/.worktrees` if it exists.
     - `<repo>/worktrees` if it exists.
     - Otherwise create `<repo>/.worktrees`.
   - **Verify the dir is gitignored.** Run `git check-ignore -q <repo>/<base>`. If it fails, append `<base>/` to `<repo>/.gitignore`. **Do not auto-commit** — leave the modification in the working tree, print a one-line notice (`note: added .worktrees/ to .gitignore — commit when convenient`).
   - Branch name = session name (e.g. `myproject-calm-reef`). If `--from <ref>` was passed, branch off that; else off current HEAD.
   - Worktree path = `<base>/<branch>`.
   - The actual `git worktree add` continues to happen inside the container's entrypoint (existing `nix/image.nix` behavior). The host computes the path and passes `WORKTREE_BRANCH` / `WORKTREE_FROM` env. The host mounts the repo at `/mnt/repo`.
   - **Fallback if `.gitignore` is read-only:** create the worktree at `~/.local/share/claude-container/worktrees/<repo-id>/<branch>/`. Print a notice.

3. **Not in a git repo** → pwd passthrough as in (1).

**Multi-repo workspaces (`-W my-repos`).** Same logic per repo; each repo with worktree mode gets its own `<repo>/.worktrees/<branch>/`. Container mounts each at `/mnt/repos/<basename>` and the entrypoint creates the worktree, exactly as today.

**Migration of existing worktrees.** Sessions stored in `sessions.json` keep their existing `~/.config/claude-container/worktrees/<session>/` paths. Only **new** sessions use the new in-repo location. No migration script.

## Output mode adapters

The four output adapters live under `internal/session/outputs/`:

```go
type Handle struct {
    Name      string
    Container string
    Repo      string
    Branch    string
    ProxyPort int
    StatusBar StatusBarInfo
    cleanup   func()
}

func (h *Handle) AttachTTY(opts TTYOpts) error
func (h *Handle) BridgeACP(in io.Reader, out io.Writer) error
func (h *Handle) WaitTask(opts TaskOpts) (TaskResult, error)
func (h *Handle) RunBackground() error
```

**`AttachTTY`** wraps the existing `internal/proxy.Run()` (PTY proxy). Used by bare invoke, `run`, `work`, `attach`. Same status bar, Ctrl+B chord, auto-restart on stopped containers. Zero behavior change vs. today.

**`BridgeACP`** runs `docker attach` (no `-t`) against a container started with `claude-agent-acp` as its command. Stdin/stdout pipe through byte-for-byte; ACP is line-delimited JSON-RPC, no PTY needed. When Zed sends EOF or SIGTERM, we `docker stop` (short grace period) and exit. The container has `--rm`, so it disappears on exit.

**`WaitTask`** runs `claude -p --output-format stream-json` detached, waits for exit, parses the final JSON from logs, returns result + stats. Behavior identical to today's `cmd/task.go`.

**`RunBackground`** does `docker run -d`, persists session record, returns. Caller can later attach via the dashboard or `attach` command.

**Stderr handling for ACP.** ACP keeps stdin/stdout strictly for JSON-RPC. All claude-container diagnostics (proxy startup, image load progress) go to host stderr before `BridgeACP` starts. Once the bridge is running, container stderr passes through to host stderr. Zed shows agent stderr in its log panel — exactly where we want it.

**Signal handling for ACP.** SIGTERM (from Zed) and SIGINT (host Ctrl+C): `docker stop` with short grace period, then exit.

## ACP bridge details

**Per-session config dir** (existing `PrepareSessionConfig` infrastructure):

```
~/.config/claude-container/containers/<session-name>/
    managed-settings.json        # real file, per-profile
    projects/                    # symlink → ../../claude-config/<repo-id>/projects/
```

For ACP, this code path runs unconditionally with `resumeMode = "__picker__"`, regardless of any `--resume` flag. Effects:

- **Thread listing works.** SDK walks `projects/-workspace/*.jsonl` → sees every thread for this repo.
- **Resume works.** When Zed says "open thread X", SDK loads `projects/-workspace/<X>.jsonl` from the per-repo store via the symlink.
- **New conversations save live.** Writes go through the symlink to the real per-repo dir as they happen. No copy-back step on container exit.
- **Concurrent ACP sessions don't conflict.** Each Zed conversation gets its own thread UUID → its own JSONL file → no contention.

**Container command:**

```
docker run --rm -i \
  -v <session-config-dir>:/claude \
  -e CLAUDE_CONFIG_DIR=/claude \
  -e CLAUDE_CONTAINER_MODE=acp \
  ... (mounts, proxy network, ca cert, credentials, packages) ...
  <image> \
  claude-agent-acp
```

Note `-i` not `-it` — no PTY. Stdin from Zed is binary JSON-RPC. The host's `docker attach` plumbs stdin/stdout through.

**Cleanup on exit:**

1. Container has `--rm`, so it's gone when `claude-agent-acp` exits.
2. Tear down the per-session proxy sidecar; remove its rules file.
3. Remove the per-session config dir. Go's `os.RemoveAll` removes the symlink, not its target — per-repo store is safe.
4. Remove the session record from `sessions.json`.

**Session naming.** Each ACP launch gets `<repo-basename>-acp-<adj>-<noun>` so multiple Zed conversations in the same repo coexist. The `acp-` infix distinguishes them in `ps`.

**Visibility in `ps`.** ACP sessions are recorded in `sessions.json` so they show up in `ps` while connected. Records are removed on container exit. Stale records from host crashes are cleaned by `gc`.

## Nix image additions

The image already has `nodejs` (transitively via `claude-code`) and a writable `/nix/var` volume for runtime installs. No image-level new dependencies.

**Lazy install of `claude-agent-acp`.** The package is in nixpkgs (`pkgs.claude-agent-acp`). Rather than baking it into every image at build time, the entrypoint installs it on first ACP launch:

```sh
# In nix/image.nix entrypoint, when CLAUDE_CONTAINER_MODE=acp
if ! command -v claude-agent-acp >/dev/null 2>&1; then
  log "installing claude-agent-acp into persistent nix store"
  nix profile install nixpkgs#claude-agent-acp 2>&1 | tee -a "$ENTRYPOINT_LOG" >&2
fi
```

This persists in the `claude-nix-store` Docker volume, so first ACP launch is slow (substituters need to be reachable, which they are because they're in the proxy's `med` profile allow-list); all subsequent ACP launches are instant. No image rebuild needed when nixpkgs ships a new version — first launch after `nix profile upgrade` picks it up.

**No `cc-install` wrapper.** The agent uses `nix profile install nixpkgs#<pkg>` directly when it needs to install a tool mid-conversation. The existing nixpkgs registry pin in `/etc/nix/registry.json` makes this work offline (substituters aside).

**Entrypoint mode dispatch.** A new env var `CLAUDE_CONTAINER_MODE` (one of `tty`, `acp`, `task`, `background`) lets the entrypoint know which path to take. Today the entrypoint runs the same setup regardless of mode. With this var, the ACP path can do the lazy install before exec'ing `claude-agent-acp`.

## Bare-invocation behavior change

Today: bare `claude-container` launches the TUI dashboard. After this change: bare `claude-container` creates and attaches to a session in the current dir.

**New behavior matrix:**

```
claude-container                 → bare invoke: smart default
                                   - git repo, no flags  → worktree at .worktrees/<name>/, persistent
                                   - non-git, no flags    → pwd passthrough, persistent
                                   - --no-worktree        → pwd passthrough even in git
                                   - flags (--yolo, -p, --profile, etc.) honored as on `run`/`work`
claude-container run [flags]     → preserved; pwd-passthrough always
claude-container work [flags]    → preserved; explicit worktree
claude-container acp [flags]     → new; ACP entry point
claude-container tui             → new; the dashboard that bare-invoke used to launch
```

All other subcommands (`new`, `ps`, `attach`, `task`, `extract`, `gc`, `auth`, `doctor`, `build`, `shell`, `stop`, `rm`, `logs`, `workspace`, `conversations`, `completion`) are preserved verbatim.

**`cmd/root.go` change.** Replace the dashboard-launching `RunE` with `runDefault`, which builds `session.Opts{Mode: TTY, WorktreeMode: Auto, Persistent: true, Yolo: false}` and calls `Launch` + `AttachTTY`.

**`cmd/tui.go` (new).** Registers `tui` as a subcommand whose body is the current `cmd/root.go` dashboard loop verbatim. Becomes the new home for everything that today happens "on bare invoke."

**Flag set on bare invoke.** Union of `run` and `work` flags: `--yolo`, `--prompt`/-p, `--name`, `--background`/-b, `--rm`, `--mount`/-w, `--workspace`/-W, `--profile`, `--allow-domain`, `--deny-path`, `--allow-command`, `--deny-command`, `--preset`, `--proxy-port`, `--from`, `--no-worktree`, `--resume`. Declared directly on the root cobra command — no flag inheritance to avoid global-flag surprises.

## Configuration defaults summary

| Mode                   | Workspace                          | Persistence       | Yolo | Profile    | Conversation visibility           |
|------------------------|------------------------------------|-------------------|------|------------|-----------------------------------|
| `claude-container`     | git → `<repo>/.worktrees/<name>/`  <br> non-git → pwd | persistent (no `--rm`) | off | `default` | per-repo, isolated by default |
| `run`                  | pwd passthrough                    | persistent        | off  | `default`  | per-repo, isolated by default     |
| `work`                 | git worktree (existing location for old sessions, `<repo>/.worktrees/<name>/` for new) | persistent | off | `default` | per-repo, isolated by default |
| `task`                 | as flag specifies                  | ephemeral (default), `--keep` to persist | n/a | `default` | per-repo, isolated by default |
| `acp`                  | pwd passthrough                    | ephemeral (`--rm`) | off | `med`     | full repo history (symlink to per-repo `projects/`) |
| `tui` / `ps` / `attach`| (operates on existing sessions)    | n/a               | n/a  | n/a        | n/a                               |

**Profile override.** All session-creating commands accept `--profile {low,default,med,high}` plus `--allow-domain`, `--allow-command`, `--deny-command`, `--deny-path`. No new flag surface.

**ACP profile = `med`.** Anthropic, github, npmjs, pypi pre-allowed. Substituters (cache.nixos.org, devenv.cachix.org) need to be reachable for the lazy `claude-agent-acp` install — they're already in the `med` allow list. Override available via `claude-container acp --profile=high` (anthropic-only) or `--profile=low` (allow-all).

**Conversation visibility.** Default-isolated for non-ACP modes (existing semantics from commit 45a6135). Full-repo-history for ACP via the unconditional `__picker__` symlink mode.

## Migration & backward compatibility

**Sessions in `sessions.json`.** Zero migration. The `Session` struct gains a `Mode string` field (one of `tty`, `acp`, `task`, `background`); old records default to `tty` on unmarshal. Old records continue to work with `attach` because `attach` reads `WorktreePath` directly from the record.

**Conversation history.** The existing per-repo migration from commit 2e7951f stays. ACP just adds a new symlink-projects-from-per-repo flow that doesn't conflict.

**Bare-invocation break.** Only user-visible break. Mitigation: on first run after upgrade, if `sessions.json` is non-empty, print a one-line notice to stderr before launching the session:

```
note: bare claude-container now creates a session; run 'claude-container tui' for the dashboard (existing sessions: <count>)
```

Suppress with `CLAUDE_CONTAINER_QUIET=1` or after first display (write a flag file `~/.config/claude-container/migrated-bare-invoke`). Update README and `doctor` output to make the relocation discoverable.

**Image rebuild.** ACP launches require an entrypoint that knows about `CLAUDE_CONTAINER_MODE=acp`. Old image tarballs without the new entrypoint logic continue working for non-ACP modes. ACP launches against an old image fail with: `error: image is older than your claude-container CLI; run 'claude-container build' to rebuild`.

**Scripts that bare-invoke.** A CI script running `claude-container` with no args expecting the dashboard would now create a session. Documented as breaking. Workaround: `claude-container tui`.

**`--rm` semantics.** `task` defaults ephemeral (`--keep` to persist). `acp` defaults ephemeral. Bare invoke and `run`/`work` default persistent. Each command's `--rm` flag continues to override.

## Testing strategy

Existing test surface (`internal/docker/docker_test.go`, `docker_integration_test.go`, `cmd/e2e_test.go`) is preserved. New tests target new code paths.

**Unit tests for `internal/session/`:**

- `workspace_test.go` — `ResolveWorkspace()` table-driven: non-git → pwd; git + worktree mode + ignored `.worktrees` → uses it; git + worktree mode + un-ignored `.worktrees` → mutates `.gitignore`; git + un-writable `.gitignore` → falls back to `~/.local/share/...`; `--from <ref>` → branch off ref; ACP mode → ignores worktree mode.
- `options_test.go` — defaults per-mode (yolo, profile, rm, persistence) match the defaults table.

**Unit tests for the ACP bridge:**

- `outputs/acp_test.go` — feed canned JSON-RPC frames through the bridge with a mocked `docker attach`. Assert byte-for-byte passthrough in both directions, EOF handling, signal handling.

**Integration tests** (build tag `integration`, real docker required) in `internal/session/session_integration_test.go`:

- `TestACP_BridgeRoundTrip` — `session.Launch{Mode: ACP}` → write a real `initialize` JSON-RPC request to handle stdin → assert valid response on stdout.
- `TestACP_ConversationPersists` — start ACP container in a temp git repo, send "hello", exit. Start a second ACP container, list threads, assert prior thread present.
- `TestBareInvoke_GitRepo_CreatesWorktree` — bare in temp git repo → `.worktrees/<name>/` exists, `.gitignore` includes the entry.
- `TestBareInvoke_NonGit_PwdMount` — bare in non-git temp dir → workspace mount is the dir itself.

**`cmd/e2e_test.go` additions:**

- `TestE2E_BareInvoke_Persistent` — bare claude-container, exit, `ps` shows it, `attach` works.
- `TestE2E_ACP_HelloWorld` — `claude-container acp` with a scripted JSON-RPC client.
- `TestE2E_TUISubcommand` — `claude-container tui` smoke test.

**Refactor regression coverage.** Existing E2E cases for `run`, `work`, `task`, `attach`, etc. become the regression suite for the session-launcher refactor. Do not delete or weaken any existing E2E case.

**Out of scope:**

- Zed itself — we test against the ACP wire protocol directly.
- The `claude-agent-acp` package's behavior — upstream nixpkgs dep; assume correct; pin version via flake.

**Manual smoke checklist** (saved alongside this doc):

1. Build, run bare in non-git dir → drops into Claude.
2. Build, run bare in git repo → creates worktree, prompts about `.gitignore` if not already ignored.
3. `claude-container acp` from a TTY → bridges nothing until stdin closes; doesn't crash.
4. Configure Zed with `command=claude-container, args=["acp"]` → start a thread, send a message, get a tool-call approval prompt in browser, approve, continue.
5. Close Zed, reopen, see prior thread in list, resume, send a message, verify continuity.

## File changes

| File | Change |
|------|--------|
| `internal/session/options.go` | New — `Opts` struct, per-mode defaults, validation |
| `internal/session/workspace.go` | New — `ResolveWorkspace()`, `.gitignore` mutation, fallback path |
| `internal/session/launch.go` | New — `Launch(ctx, opts) -> Handle`; orchestrates docker, proxy, config |
| `internal/session/handle.go` | New — `Handle` type and method routing |
| `internal/session/outputs/tty.go` | New — wraps `internal/proxy.Run()` |
| `internal/session/outputs/acp.go` | New — stdio bridge using `docker attach` |
| `internal/session/outputs/task.go` | New — task waiter and result parser |
| `internal/session/outputs/background.go` | New — detached run + record persistence |
| `internal/session/session_integration_test.go` | New — integration tests |
| `internal/session/workspace_test.go` | New — unit tests |
| `internal/session/options_test.go` | New — unit tests |
| `internal/session/outputs/acp_test.go` | New — ACP bridge unit tests |
| `cmd/root.go` | Replace dashboard-launching `RunE` with `runDefault`; declare bare-invoke flag set; print first-time notice |
| `cmd/tui.go` | New — relocate dashboard launch loop here |
| `cmd/acp.go` | New — `acp` subcommand; ~50 lines |
| `cmd/run.go` | Refactor body to `session.Launch` + `AttachTTY` |
| `cmd/work.go` | Refactor body to `session.Launch` + `AttachTTY`; new worktree location for fresh sessions |
| `cmd/task.go` | Refactor body to `session.Launch` + `WaitTask` |
| `cmd/new.go` | Refactor body; wizard remains, just calls `session.Launch` at the end |
| `cmd/attach.go` | Use `session.Launch` for the recreate-on-missing-container path |
| `internal/config/config.go` | Add `Mode` field to `Session` (default `tty` on unmarshal) |
| `internal/docker/docker.go` | Add `Mode` field to `RunOpts`; pass `CLAUDE_CONTAINER_MODE` env; ACP variant of `RunArgs` (no `-t`, `--rm`) |
| `nix/image.nix` | Add lazy `claude-agent-acp` install in entrypoint when `CLAUDE_CONTAINER_MODE=acp` |
| `internal/git/git.go` | Add helper for `.gitignore` mutation + check |
| `cmd/e2e_test.go` | Add bare-invoke + ACP + TUI E2E cases |
| `README.md` | Update SYNOPSIS, COMMANDS, EXAMPLES; add ACP section |

## Risks

- **`buildNpmPackage`/nixpkgs version drift for `claude-agent-acp`.** The lazy install path means we depend on nixpkgs always tracking a working version. Mitigation: pin nixpkgs in the flake; add an integration test that exercises the install path so CI catches regressions.
- **Docker `attach` stdio framing.** ACP is line-delimited JSON; if Docker injects framing bytes (e.g. multiplexed stream prefix), the bridge breaks. Mitigation: the test plan includes a real round-trip check via `TestACP_BridgeRoundTrip`. If Docker's default `attach` has framing issues, switch to `docker exec -i` against a long-lived dummy process or a named pipe — both are tested-known-good for raw stdio.
- **Concurrent gitignore mutation.** Two simultaneous bare invocations in the same fresh repo could race on appending to `.gitignore`. Mitigation: file-lock around the mutation (already a small surface); a duplicate `.worktrees/` line in `.gitignore` is harmless.
- **First-time bare-invoke notice noise.** If users object to the notice, leaving it on by default is a paper cut. Mitigation: env var dismissal + flag file means it shows at most twice (once per machine after upgrade).
