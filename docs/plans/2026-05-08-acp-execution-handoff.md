# ACP Implementation — Execution Handoff (Mid-Phase-2)

> **SUPERSEDED 2026-05-17:** ACP scope was dropped. See
> `docs/plans/2026-05-17-transparent-binary-handoff.md` for the active
> handoff. This file is preserved for historical context.

This document captures the live state of the ACP + transparent-binary
implementation so a fresh agent can pick up cleanly without replaying the
preceding conversation.

## Source documents (read these first)

- **Spec**: `docs/plans/2026-05-07-acp-and-transparent-binary-design.md`
- **Plan**: `docs/plans/2026-05-07-acp-and-transparent-binary-implementation.md`
- **This handoff**: `docs/plans/2026-05-08-acp-execution-handoff.md`

The execution mode is **subagent-driven development**:
- Dispatch an implementer subagent per task with the task's full text
- Run a **spec-compliance** reviewer (model: sonnet)
- Run a **code-quality** reviewer (model: sonnet)
- Implementer fixes any reviewer-blocking issues; reviewers re-review
- Mark complete and move to next task
- Phase boundaries pause for user confirmation before continuing

User-imposed adjustments to the skill defaults:
- **Run on `main`**, not in a worktree (explicit user consent).
- **One phase at a time**, with a user check-in at each phase boundary.
- **Defer cross-package `go test ./...` runs to phase boundaries**; per-task,
  only the touched package's tests are run.
- The **dev shell is now `devenv shell --`**, not `nix develop`. See "Dev shell
  migration" below.

## Phase 1 status — DONE pending boundary check

Phase 1 is "Foundation utilities" — four self-contained additions that the
session-launcher built in Phase 2 will consume. All four tasks landed with
spec + code-quality review approval.

| Task | Description | Commit |
|------|-------------|--------|
| 1 | `internal/git/EnsureIgnored` helper for `.gitignore` mutation | `2cbf6f3` + fix `9b39121` |
| 2 | `Mode` field on `config.Session` (defaults to `"tty"` on old-record load) | `d150c28` |
| 3 | `Mode` field on `docker.RunOpts`; `CLAUDE_CONTAINER_MODE` env exported by `RunArgs`/`TaskRunArgs` | `0f5f49c` |
| 4 | `docker.ACPRunArgs` for ephemeral no-PTY ACP container | `6dc530e` |

### Outstanding items inside Phase 1

1. **Task 4 follow-up tests are written but not yet committed.** Two test
   functions were added directly via Edit (not a subagent) after the code-quality
   reviewer flagged minor coverage gaps. Located at the bottom of
   `internal/docker/docker_test.go`:
   - `TestACPRunArgs_DefaultModeIsACP` — exercises the `Mode == ""` default path
   - `TestACPRunArgs_ProxyEnabledEmitsNetworkAndCA` — exercises the `ProxyEnabled` branch
   They are uncommitted in the working tree. **Next agent should run them, then
   commit them** with a message like:
   `test(docker): cover ACPRunArgs default mode and proxy block`

2. **Phase 1 boundary check not yet run.** Need to run, once Bash works:
   ```
   devenv shell -- go test ./...
   devenv shell -- go build ./...
   ```
   And, if Docker is available and the user wants the integration sweep:
   ```
   devenv shell -- go test -tags=integration ./...
   ```
   Pre-existing failures observed during per-task runs (not introduced by these
   tasks): `TestIntegrationE2EClaudeProxyAllow` / `TestIntegrationE2EClaudeProxyDeny`
   fail due to an orphaned proxy container name conflict — these are independent
   of Phase 1.

3. **Phase 1 → Phase 2 user check-in.** The user asked for a checkpoint at the
   end of each phase. Surface the Phase 1 summary (tasks done, follow-up tests,
   any integration test surprises) and confirm whether to proceed into Phase 2.

## Phase 2 — Session launcher core (Tasks 5–16, NOT STARTED)

Full task text lives in
`docs/plans/2026-05-07-acp-and-transparent-binary-implementation.md` (Phase 2
section). Quick reference:

| Task | File | Purpose |
|------|------|---------|
| 5  | `internal/session/options.go` + test | `Mode`/`WorktreeMode` enums; `Opts` struct; per-mode `ApplyDefaults()`; `Validate()` |
| 6  | `internal/session/workspace.go` + test | `Workspace` struct; `ResolveWorkspace()` non-git case |
| 7  | (same files) | `ResolveWorkspace` git + ignored `.worktrees` case |
| 8  | (same test file) | Test for `.gitignore` mutation when `.worktrees` not ignored |
| 9  | (same files) | Fallback to `~/.local/share/claude-container/worktrees/<repo-id>/<branch>/` when `.gitignore` is read-only |
| 10 | (same test file) | Tests for ACP-mode and `--no-worktree` overrides forcing pwd |
| 11 | `internal/session/handle.go` | `Handle` struct skeleton + `Cleanup()` |
| 12 | `internal/session/output_tty.go` | `Handle.AttachTTY()` wrapping `proxy.Run()` |
| 13 | `internal/session/output_acp.go` + test | `Handle.BridgeACP()` stdio bridge with signal handling |
| 14 | `internal/session/output_task.go` | `Handle.WaitTask()` waiter + result shell |
| 15 | `internal/session/output_background.go` | `Handle.RunBackground()` no-op |
| 16 | `internal/session/launch.go` | `Launch(ctx, store, opts) -> *Handle` orchestration (image, workspace, proxy, container, record) + `resolveMounts` for multi-repo/extra-workspace handling |

After Phase 2, no caller exists yet — the launcher is dead code that compiles
and tests cleanly. Phase 3 wires it up.

## Phases 3–6 (NOT STARTED) — at a glance

- **Phase 3** (Tasks 17–19) — `nix/image.nix` entrypoint lazy-installs
  `claude-agent-acp` when `CLAUDE_CONTAINER_MODE=acp`; `cmd/acp.go` subcommand;
  ACP integration tests (round-trip + conversation persistence).
- **Phase 4** (Tasks 20–22) — `cmd/tui.go` relocates the dashboard;
  `cmd/root.go` bare-invoke becomes `runDefault` calling `session.Launch`;
  first-time bare-invoke notice.
- **Phase 5** (Tasks 23–27) — Refactor `run`, `work`, `task`, `new`, `attach`
  to delegate to `session.Launch`.
- **Phase 6** (Tasks 28–30) — README, `doctor` quickstart hint, E2E tests for
  bare invoke + `tui` subcommand. Final code review.

## Dev shell migration

The user migrated the dev shell from `nix develop` (flake `devShells.default`)
to **devenv** during this session. New commands:

| Old | New |
|-----|-----|
| `nix develop` | `devenv shell` |
| `nix develop --command <cmd>` | `devenv shell -- <cmd>` |
| `nix develop -c <cmd>` | `devenv shell -- <cmd>` |

`nix build` / `nix flake check` are unchanged. The dev-shell tools are unchanged:
`go`, `gopls`, `dlv`, `git`, `docker`. Config lives in `devenv.nix` + `devenv.yaml`
at the repo root.

CLAUDE.md is already updated to reflect this.

## Permission model

`.claude/settings.json` carries narrow allow-list patterns: `go test|build|vet|fmt|doc *`,
`devenv shell -- go test|build|vet|fmt *`, `nix build`, `nix flake check`,
read-only `git` (rev-parse/diff/log/show/status/branch/worktree list/ls-files), and
mutating-but-safe `git add|commit|stash|checkout *`. **Not** broadly authorized:
`docker *` (sub-commands must be added piecemeal), `nix run *`, `nix shell *`,
`go install|get|run`.

## Coding conventions (project-specific)

From `CLAUDE.md`:
- Errors are lowercase, no trailing punctuation (`fmt.Errorf("open config: %w", err)`).
- Cobra commands register themselves in `init()`.
- Don't auto-format files you aren't touching (no global `gofmt`).
- Commit prefixes: `feat:`, `fix:`, `refactor:`, `test:`, `docs:`, `chore:`.
  **No** LLM marker phrases in commit messages.
- Keep files small and focused; one responsibility per file.
- Don't commit `.gitignore` mutations as part of unrelated work.

## Architecture quick reference

```
cmd/                Cobra subcommands (root=bare invoke after Phase 4, otherwise TUI)
internal/config/    Session + per-repo Claude config state
internal/docker/    Docker container args + lifecycle (RunArgs, TaskRunArgs, ACPRunArgs)
internal/git/       Worktree helpers; EnsureIgnored
internal/httpproxy/ Per-session mitmproxy sidecar
internal/proxy/     Host-side PTY proxy (Ctrl+B chord, status bar)
internal/sandbox/   Sandbox profiles (low/default/med/high)
internal/session/   Session launcher (NEW, Phase 2)
internal/transcript/ JSONL transcript parsing
internal/tui/       Bubble Tea dashboard
nix/                Image derivations, managed-settings
proxy/              Python mitmproxy addon
docs/plans/         Design & implementation plans
```

## Per-task TDD workflow (the playbook)

For each Phase 2+ task:

1. **Capture base SHA**:
   ```
   git rev-parse HEAD
   ```
2. **Dispatch implementer subagent** (`Agent` tool, `general-purpose`, model
   `haiku` for mechanical tasks, `sonnet` for integration tasks like Task 16):
   - Paste the full task text from the implementation plan.
   - Tell the implementer to follow TDD, run package-level tests, build, and commit.
   - Be explicit: "Do NOT run `go test ./...` — deferred to phase boundary."
   - Be explicit: "Use `devenv shell --` for all Go commands."
3. **Spec-compliance review** (`Agent` tool, model `sonnet`): paste the task
   spec, the implementer's SHAs (base/head), and instruct verification by
   reading the diff. Skill template:
   `subagent-driven-development/spec-reviewer-prompt.md`.
4. **Code-quality review** (`Agent` tool, model `sonnet`): same SHAs, asking for
   strengths + issues (Critical/Important/Minor) + assessment. Skill template:
   `subagent-driven-development/code-quality-reviewer-prompt.md`.
5. If either reviewer raises blockers, dispatch a **fix subagent** with
   targeted instructions and re-review the relevant stage only.
6. Mark the task complete in `TodoWrite` and proceed.

If a subagent's edits land in the working tree but the commit was interrupted
(observed twice during Phase 1), verify the working-tree diff matches the
plan exactly, run the package test, build, and commit yourself.

## Notable lessons from Phase 1

- **Interrupts leave dirty working trees.** When you restart, always
  `git status` first and reconcile any pending changes against the current
  task spec before dispatching a new implementer.
- **CLAUDE.md and `.claude/settings.json` updates sneak into task commits.**
  When committing a task, explicitly `git add` only the task's files; check
  `git show --stat <sha>` afterwards to confirm scope.
- **Pre-existing integration test failures**: `TestIntegrationE2EClaudeProxyAllow/Deny`
  fail because of a leftover container name. They are unrelated to this work
  and require a `docker rm -f` of the conflicting container or a different
  cleanup approach in the test.
- **Sandbox failures** can hit the Bash tool intermittently (observed at the
  end of Phase 1). Retry; if persistent, the user may need to adjust
  permissions or sandbox config.

## Open questions / decisions still pending

None blocking Phase 2 start. The design has resolved all open clarifications.

## Resuming in a fresh agent

Steps for a fresh agent picking this up:

1. Read this handoff document.
2. Skim `docs/plans/2026-05-07-acp-and-transparent-binary-design.md` for the
   why behind each piece.
3. Open `docs/plans/2026-05-07-acp-and-transparent-binary-implementation.md`
   for the exact step-by-step task text.
4. Run `git status` and `git log --oneline -10` to confirm state.
5. Run the Phase 1 boundary check (full unit suite + build) before starting Phase 2.
6. **If the two pending Task 4 tests in the working tree are still there**:
   verify they pass, then commit with
   `test(docker): cover ACPRunArgs default mode and proxy block`.
7. Check in with the user before kicking off Phase 2.
8. Then dispatch Task 5's implementer per the playbook above.
