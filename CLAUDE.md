# Claude Code Instructions

## Project

`claude-container` is a Go CLI for running multiple Claude Code instances in isolated Docker containers with git worktree separation and a TUI dashboard.

## Build & Test

```bash
nix develop          # enter dev shell with go, tmux, git, docker
go build ./...       # compile
go test ./...        # run tests
nix build            # full nix build with completions + wrapping
```

## Architecture

- `cmd/` -- Cobra commands (root=TUI, new, ps, attach, stop, rm, logs, build, shell)
- `internal/config/` -- Session state persistence (~/.config/claude-container/)
- `internal/docker/` -- Docker container lifecycle
- `internal/tmux/` -- Tmux session management + PTY attach/detach
- `internal/git/` -- Git worktree create/remove/diff
- `internal/tui/` -- Bubble Tea dashboard + wizard
- `docker/` -- Dockerfile, entrypoint.sh, managed-settings.json

## Conventions

- Keep functions small and focused
- Use `internal/` for all non-cmd packages
- Error messages should be lowercase, no trailing punctuation
- Cobra commands register themselves in `init()`
