# claude-container Design

## Overview

`claude-container` is a CLI tool for running multiple Claude Code instances in
isolated, sandboxed Docker containers with git worktree separation. It provides a
TUI dashboard for monitoring and switching between sessions, and manages the full
lifecycle of sessions (create, attach, detach, stop, remove).

## Problem

Running multiple Claude Code agents on the same repository causes conflicts --
competing file edits, dirty git state, broken builds. You need isolated workspaces,
but managing worktrees + containers + terminal sessions by hand is tedious.

## Solution

Combine three isolation layers:

- **Git worktrees** -- each session gets its own branch and working directory
- **Docker containers** -- sandboxed execution with controlled permissions
- **Tmux sessions** -- persistent terminal sessions with attach/detach support

Wrap them in a Go CLI with an interactive TUI dashboard.

## Command Structure

```
claude-container                   # TUI dashboard (no args)
claude-container new               # Interactive wizard to create session
claude-container ps                # List all sessions
claude-container attach <session>  # Attach to a running session
claude-container stop <session>    # Stop a session (keep worktree)
claude-container rm <session>      # Stop + remove worktree + branch
claude-container logs <session>    # Stream session logs
claude-container build             # Build the docker image
claude-container shell [workspace] # Debug shell in container
```

### `new` flags (skip wizard)

```
--name <name>        Session name
--worktree <branch>  Create worktree on new branch
--from <branch>      Base branch for worktree (default: current HEAD)
--no-worktree        Use current directory directly
--yolo               Skip permission prompts
-p, --prompt <text>  Initial prompt to send to Claude
-c, --continue       Resume previous conversation in directory
```

### `ps` flags

```
--json               Machine-readable output
```

### `logs` flags

```
-f, --follow         Stream logs continuously (like docker logs -f)
```

## Interactive Wizard

When `claude-container new` is invoked without flags, an interactive wizard asks:

1. **Session name** -- free text, defaults to auto-generated from branch name
2. **Worktree?** -- Yes (new branch) / Yes (from existing branch, fuzzy picker) / No (current dir)
3. **Mode** -- Normal / Yolo (skip permissions)
4. **Initial prompt** -- optional text to send to Claude on startup

All wizard steps are skippable via the corresponding CLI flags.

## Architecture

### Stack

- **Go** -- CLI and TUI
- **Cobra** -- command routing, flag parsing, shell completions
- **Bubble Tea** -- TUI dashboard and wizard
- **tmux** -- session persistence, attach/detach, pane capture
- **Docker** -- sandboxed container execution
- **Git** -- worktree creation, branch management, diff

### Session Lifecycle

```
new  --> create worktree (optional)
     --> create tmux session (named: claude-container_<name>)
     --> start docker container inside tmux (named, persistent)
     --> container runs `claude` (or `claude --dangerously-skip-permissions`)

ps / TUI --> queries tmux + docker state, reconciles with sessions.json

attach --> attaches to tmux session via Go PTY layer
       --> Ctrl+Q intercepted to detach back to TUI/terminal

stop --> stops docker container, preserves tmux session metadata
     --> worktree and branch preserved for resume

rm   --> stops container, kills tmux session
     --> removes worktree directory, deletes branch

logs --> streams docker logs from the named container
```

### State Persistence

Session metadata stored in `~/.config/claude-container/sessions.json`:

```json
{
  "sessions": {
    "feature-auth": {
      "name": "feature-auth",
      "branch": "feature-auth",
      "worktree_path": "/home/user/.config/claude-container/worktrees/feature-auth",
      "repo_path": "/home/user/myproject",
      "container_name": "claude-container_feature-auth",
      "tmux_session": "claude-container_feature-auth",
      "yolo": false,
      "created_at": "2026-02-18T10:00:00Z"
    }
  }
}
```

On TUI startup, stored state is reconciled with actual tmux/docker state to handle
crashed containers, manually killed processes, etc.

### Worktree Layout

```
~/.config/claude-container/
  sessions.json                        # session metadata
  worktrees/
    feature-auth/                      # git worktree for session
    fix-payments/                      # git worktree for session
```

Worktree directories are mounted into the container as `/workspace`.

### Tmux Session Naming

All tmux sessions prefixed with `claude-container_` to avoid collisions with user's
own tmux sessions. The TUI queries tmux with this prefix filter.

## TUI Dashboard

```
+-----------------------------+------------------------------+
|  Sessions                   |  Preview                     |
|                             |                              |
|  * feature-auth  [running]  |  > Analyzing auth module...  |
|    fix-payments  [running]  |  > Reading src/auth.ts       |
|    refactor-db   [stopped]  |  > ...                       |
|                             |                              |
|                             |                              |
+-----------------------------+------------------------------+
| n:new  enter:attach  d:stop  x:rm  tab:diff  q:quit       |
+------------------------------------------------------------+
```

### Preview Pane

Polls `tmux capture-pane` on the selected session every 100ms. Shows live terminal
output from the running Claude Code instance.

### Diff View

Toggle with Tab. Replaces preview with `git diff` of the session's worktree,
showing what the agent has changed.

### Key Bindings

| Key       | Action                                          |
|-----------|-------------------------------------------------|
| j/k       | Navigate session list                           |
| Enter     | Attach to selected session                      |
| n         | New session (wizard)                            |
| d         | Stop selected session                           |
| x         | Remove selected session (with confirmation)     |
| Tab       | Toggle preview/diff view                        |
| /         | Filter sessions                                 |
| q         | Quit TUI (sessions keep running)                |

### Attach Mode

When attached, the user is in the tmux session's terminal. Tmux mouse mode is
enabled for scrollback and text selection. Ctrl+Q is intercepted in the Go PTY layer
to detach back to the TUI (not tmux's native detach).

## Docker Integration

### Container Configuration

Containers are created with:

- Named container (`claude-container_<session>`) -- no `--rm`
- Worktree directory mounted as `/workspace`
- Config directory mounted as `/claude`
- UID/GID passthrough for file ownership
- The existing Dockerfile, entrypoint.sh, and managed-settings.json

### Image Management

`claude-container build` builds the image. The `new` wizard checks if the image
exists and prompts to build if missing.

## Project Structure

```
claude-container/
  flake.nix
  go.mod / go.sum
  main.go
  cmd/
    root.go            # no args -> TUI, subcommand dispatch
    new.go             # wizard + flags
    ps.go              # list sessions
    attach.go          # attach to session
    stop.go            # stop session
    rm.go              # remove session + worktree
    logs.go            # stream session logs
    build.go           # build docker image
    shell.go           # debug shell
  internal/
    config/            # sessions.json persistence, XDG paths
    docker/            # container create/start/stop/rm/logs, image check
    tmux/              # session create/kill, PTY attach/detach, capture-pane
    git/               # worktree create/remove, branch list/delete, diff
    tui/               # Bubble Tea dashboard, wizard, preview, diff view
  docker/
    Dockerfile
    entrypoint.sh
    managed-settings.json
```

## Nix Packaging

### flake.nix

Inputs: `nixpkgs` (unstable), `flake-utils`.

Outputs per system:
- `packages.default` -- `buildGoModule` with `postInstall` for completions
- `overlays.default` -- makes `pkgs.claude-container` available
- `devShells.default` -- Go, gopls, tmux, docker, git

### Build

`postInstall` generates shell completions:

```bash
$out/bin/claude-container completion bash > claude-container.bash
$out/bin/claude-container completion fish > claude-container.fish
$out/bin/claude-container completion zsh  > _claude-container
installShellCompletion claude-container.{bash,fish} _claude-container
```

Binary wrapped with `wrapProgram` to put `tmux`, `git`, `docker` on PATH.

### Dotfiles Integration

```nix
# inputs.nix
claude-container = {
  url = "github:joegoldin/claude-container";
  inputs.nixpkgs.follows = "nixpkgs";
};

# overlays
claude-container-packages = inputs.claude-container.overlays.default;

# packages.nix
claude-container
```

## Key Decisions

- **No daemon.** TUI is a viewer. Sessions are tmux+docker processes. Kill the TUI,
  sessions keep running. Relaunch, it reconciles state.
- **Ctrl+Q detach.** Intercepted in Go PTY layer, not tmux. Tmux prefix key stays
  independent.
- **Session resume.** Attaching a stopped session restarts the container in the same
  worktree with `claude --continue`.
- **Cleanup is explicit.** `stop` preserves everything. `rm` destroys everything.
  No auto-cleanup.
- **No orchestration.** Sessions are independent. Worktree isolation prevents
  conflicts. No cross-session coordination.

## Out of Scope (v1)

- Remote/SSH sessions
- Sending prompts to detached sessions
- Auto-merging worktrees
- Monitoring/alerting on session completion
- Multiple repos in one TUI view
