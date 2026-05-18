# Claude Code Instructions

## Project

`claude-container` is a Go CLI for running multiple Claude Code instances in
isolated Docker containers with per-session HTTP proxy (mitmproxy) and a TUI
dashboard.

## Build & test commands

The dev shell is managed by **devenv** (config in `devenv.nix` and `devenv.yaml`).
It provides `go`, `gopls`, `dlv`, `git`, and `docker`. `nix build`, `nix flake
check`, etc. still work — only the dev shell entry changed from `nix develop`.

```bash
devenv shell                                 # enter dev shell
devenv shell -- go build ./...               # one-shot: compile entire workspace
devenv shell -- go test ./<package>/         # one-shot: run one package's tests
devenv shell -- go test ./<package>/ -run TestName -v   # one test
devenv shell -- go test ./...                # run every package (slow)
devenv shell -- go test -tags=integration ./...         # real-docker integration tests
devenv shell -- go vet ./...                 # static analysis
nix build                                    # full build with completions + wrapping
```

## Testing protocol

When working through a multi-task implementation plan, run tests at two
granularities:

- **Per task** — run the package-level test (`go test ./<package>/ -v`) and
  `go build ./...`. These are cheap and surface regressions immediately.
- **Per phase boundary** — run `go test ./...` once at the end of a phase to
  catch cross-package regressions in one sweep, then `go test -tags=integration
  ./...` if Docker is available. Do not run the full suite between every task.

Integration tests (`-tags=integration`) require a running Docker daemon and
take 10–15 minutes; reserve them for phase boundaries and pre-release checks.

## Architecture

- `cmd/` — Cobra subcommands (root, new, ps, attach, stop, rm, logs, build,
  shell, run, work, task, auth, doctor, gc, extract, conversations, workspace)
- `internal/config/` — Session and per-repo Claude config state persistence at
  `~/.config/claude-container/`
- `internal/docker/` — Docker container lifecycle and `RunArgs`/`TaskRunArgs`
- `internal/git/` — Git worktree create/remove/diff helpers; `.gitignore`
  mutation via `EnsureIgnored`
- `internal/httpproxy/` — Per-session mitmproxy sidecar lifecycle, rules,
  dashboard token
- `internal/proxy/` — Host-side PTY proxy that owns the user's terminal during
  attach (Ctrl+B chord, status bar)
- `internal/sandbox/` — Sandbox profile definitions (low/default/med/high)
- `internal/session/` — Session launcher: workspace resolution, container
  lifecycle, output adapters (TTY/task/background) — added by the
  transparent-binary plan
- `internal/transcript/` — JSONL transcript parsing for `extract` and the TUI
- `internal/tui/` — Bubble Tea dashboard, session list, wizard, conversations
- `nix/` — Image derivations (`image.nix`, `proxy-image.nix`) and the managed
  Claude settings (`managed-settings.nix`)
- `proxy/` — Python (mitmproxy) addon code for the proxy container
- `docs/plans/` — Design and implementation plan documents

## Conventions

- Keep functions small and focused; prefer files with one clear responsibility.
- Use `internal/` for all non-cmd packages.
- Error messages are lowercase, no trailing punctuation (`fmt.Errorf("open
  config: %w", err)`).
- Cobra commands register themselves in `init()`.
- Don't commit `.gitignore` mutations as part of unrelated work — leave them
  for the user to commit.
- Do not auto-format with `gofmt`/`goimports` unless touching the file.
- Commits use imperative-style prefixes: `feat:`, `fix:`, `refactor:`, `test:`,
  `docs:`, `chore:`. No LLM marker phrases.
