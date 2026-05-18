# Transparent Binary — Execution Handoff (Mid-Phase-2)

Live state of the transparent-binary refactor. Captures what's done and
what's next so a fresh agent can pick up cleanly.

## Source documents (read these first)

- **Design:** `docs/plans/2026-05-17-transparent-binary-design.md`
- **Plan:** `docs/plans/2026-05-17-transparent-binary-implementation.md`
- **This handoff:** `docs/plans/2026-05-17-transparent-binary-handoff.md`

## Scope change (2026-05-17): ACP dropped

The original plan combined transparent-binary refactor with ACP support
(Zed integration). ACP was dropped because `claude-agent-acp` uses the
Claude Code SDK, which bills outside the Claude Max subscription. All
ACP-specific code, tests, and tasks were removed in commit `5c5b98c`.

The transparent-binary work continues: `claude-container` becomes a
drop-in safe replacement for `claude`, with the dashboard relocated to
`claude-container tui`.

## Execution model

Subagent-driven development per `subagent-driven-development` skill:
- Dispatch an implementer subagent per task with full task text
- Run spec-compliance reviewer (model: sonnet)
- Run code-quality reviewer (model: sonnet)
- Implementer fixes any reviewer-blocking issues; reviewers re-review
- Mark complete and move to next task

**User-imposed adjustments:**
- Run on `main`, not in a worktree (explicit user consent).
- One phase at a time, with user check-in at each phase boundary.
- Defer cross-package `go test ./...` to phase boundaries; per-task, only
  the touched package's tests run.
- Dev shell is `devenv shell --`, not `nix develop`.

## Progress summary

| Phase | Status   |
|-------|----------|
| 1     | DONE     |
| 2     | Tasks 4–6 done; 7–13 remain |
| 3     | Not started |
| 4     | Not started |
| 5     | Not started |

## Done (commits, in order)

| Commit    | Task                                           |
|-----------|------------------------------------------------|
| `2cbf6f3` | Task 1: `internal/git/EnsureIgnored` helper    |
| `9b39121` | Task 1 follow-up fix                           |
| `d150c28` | Task 2: `Mode` field on `config.Session`       |
| `0f5f49c` | Task 3: `Mode` on `docker.RunOpts` + env       |
| `63261ed` | Task 4: `session.Opts` + per-mode defaults     |
| `cf4c73f` | Task 5: `ResolveWorkspace` non-git case        |
| `877c3ea` | Task 5 fix: drop dead stubs, cover forced-pwd  |
| `949d556` | Task 6: `ResolveWorkspace` git + ignored case  |
| `b90da76` | Task 6 fix: rename + cover mkdir path          |
| `5c5b98c` | Scrub ACP: drop ModeACP, ACPRunArgs, etc.      |

## Phase 2 remaining (tasks 7–13)

| Task | File(s) | Purpose |
|------|---------|---------|
| 7  | `internal/session/workspace_test.go` | `.gitignore` mutation lock-down test |
| 8  | `internal/session/workspace.go` + test | Read-only `.gitignore` fallback (`$XDG_DATA_HOME/claude-container/worktrees/<hash>/<branch>`) |
| 9  | `internal/session/handle.go` + test | `Handle` struct + idempotent `Cleanup()` |
| 10 | `internal/session/output_tty.go` | `Handle.AttachTTY()` wrapping `proxy.Run()` |
| 11 | `internal/session/output_task.go` | `Handle.WaitTask()` (docker wait + logs) |
| 12 | `internal/session/output_background.go` | `Handle.RunBackground()` no-op |
| 13 | `internal/session/launch.go` | `Launch(ctx, store, opts) -> *Handle` orchestration |

Full text of each task lives in the implementation plan.

After Phase 2: no caller exists yet — the launcher is dead code that
compiles and tests cleanly. Phase 3 wires it up.

## Phases 3–5 (NOT STARTED) at a glance

- **Phase 3** (tasks 14–16) — `cmd/tui.go` relocates the dashboard;
  `cmd/root.go` bare-invoke becomes `runDefault` calling `session.Launch`;
  first-time bare-invoke notice.
- **Phase 4** (tasks 17–21) — refactor `cmd/run.go`, `cmd/work.go`,
  `cmd/task.go`, `cmd/new.go`, `cmd/attach.go` to delegate to
  `session.Launch`.
- **Phase 5** (tasks 22–25) — README, `doctor` quickstart hint, E2E tests
  for bare invoke + `tui` subcommand, final code review.

## Dev shell

`devenv shell` enters the dev shell. Per-task commands:

```sh
devenv shell -- go test ./internal/session/ -v
devenv shell -- go build ./...
```

Phase-boundary checks:

```sh
devenv shell -- go test ./...
devenv shell -- go test -tags=integration ./...   # if Docker is available
```

## Permission model

`.claude/settings.json` carries narrow allow-list patterns:
- `go test|build|vet|fmt|doc *`
- `devenv shell -- go test|build|vet|fmt *`
- `nix build`, `nix flake check`
- read-only `git` (rev-parse/diff/log/show/status/branch/worktree
  list/ls-files)
- mutating-but-safe `git add|commit|stash|checkout *`

**Not** broadly authorized: `docker *` (sub-commands must be added
piecemeal), `nix run *`, `nix shell *`, `go install|get|run`.

## Coding conventions (from CLAUDE.md)

- Errors lowercase, no trailing punctuation
  (`fmt.Errorf("open config: %w", err)`).
- Cobra commands register themselves in `init()`.
- Don't auto-format files you aren't touching (no global `gofmt`).
- Commit prefixes: `feat:`, `fix:`, `refactor:`, `test:`, `docs:`,
  `chore:`. No LLM marker phrases in commit messages.
- Keep files small and focused; one responsibility per file.
- Don't commit `.gitignore` mutations as part of unrelated work.
- Don't `git add -A` — explicit files only. There are untracked dotfiles
  (`.bash_profile`, `.idea`, `.zshrc`, etc.) in the working tree that
  must NOT be committed.

## Architecture quick reference

```
cmd/                Cobra subcommands (root=bare invoke after Phase 3, otherwise TUI)
internal/config/    Session + per-repo Claude config state
internal/docker/    Docker container args + lifecycle (RunArgs, TaskRunArgs)
internal/git/       Worktree helpers; EnsureIgnored
internal/httpproxy/ Per-session mitmproxy sidecar
internal/proxy/     Host-side PTY proxy (Ctrl+B chord, status bar)
internal/sandbox/   Sandbox profiles (low/default/med/high)
internal/session/   Session launcher (Phase 2, in progress)
internal/transcript/ JSONL transcript parsing
internal/tui/       Bubble Tea dashboard
nix/                Image derivations, managed-settings
proxy/              Python mitmproxy addon
docs/plans/         Design & implementation plans
```

## Per-task workflow (the playbook)

For each Phase 2+ task:

1. **Capture base SHA:** `git rev-parse HEAD`.
2. **Dispatch implementer subagent** (`Agent` tool, `general-purpose`,
   model `haiku` for mechanical tasks, `sonnet` for integration tasks
   like Task 13). Paste full task text. Be explicit:
   - "Do NOT run `go test ./...` — deferred to phase boundary."
   - "Use `devenv shell --` for all Go commands."
3. **Spec-compliance review** (`Agent`, model `sonnet`): paste task spec,
   base/head SHAs, instruct verification by reading the diff.
4. **Code-quality review** (`Agent`, model `sonnet`): same SHAs, ask for
   strengths + issues (Critical/Important/Minor) + assessment.
5. If reviewers raise blockers, dispatch a **fix subagent**.
6. Mark complete in `TodoWrite`/`TaskUpdate` and proceed.

If a subagent's edits land in the working tree but commit was
interrupted, verify the diff matches the plan, run package tests + build,
commit yourself.

## Resuming in a fresh agent

1. Read this handoff.
2. Skim the design doc for the why behind each piece.
3. Open the implementation plan for the exact step-by-step task text.
4. Run `git status` and `git log --oneline -10` to confirm state.
5. Continue from Phase 2 Task 7 (`.gitignore` mutation test).
6. After Task 13, run the Phase 2 boundary check and pause for user
   check-in before Phase 3.

## Notable lessons from earlier phases

- **Interrupts leave dirty working trees.** On restart, always `git
  status` first and reconcile pending changes against the current task
  spec before dispatching a new implementer.
- **CLAUDE.md and `.claude/settings.json` updates sneak into task
  commits.** Explicitly `git add` only the task's files; verify with
  `git show --stat <sha>` after the commit.
- **Pre-existing integration test failures.**
  `TestIntegrationE2EClaudeProxyAllow/Deny` fail because of a leftover
  container name. Unrelated to this work; requires a `docker rm -f`.
- **Sandbox/bash interruptions.** The Bash tool can intermittently fail
  in this environment. Retry; if persistent, the user may need to adjust
  sandbox config.
