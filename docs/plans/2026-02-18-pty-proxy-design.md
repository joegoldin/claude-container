# PTY Proxy with Status Bar + Auth Improvements

## Problem

Containers use Docker's native Ctrl+P,Ctrl+Q for detach. This is invisible, undiscoverable, and doesn't support richer actions. The `auth` command requires manual exit after login and doesn't skip permission prompts.

## Approach: Go PTY Proxy

Replace all `syscall.Exec` (process replacement) and `docker.Attach` subprocess paths with a single PTY proxy that intercepts stdin, renders a status bar, and handles lifecycle.

## Architecture

### PTY Proxy (`internal/proxy/`)

Single entry point:

```go
type Opts struct {
    DockerArgs  []string
    Session     *config.Session
    Store       *config.Store
    StatusBar   StatusBarInfo
    AutoRemove  bool
}

type StatusBarInfo struct {
    Name   string
    Branch string
    Yolo   bool
}

func Run(opts Opts) error
```

**Byte flow:**

```
Host Terminal (raw mode)
    ↓ stdin
Proxy goroutine: intercept Ctrl+B prefix
    ↓ (if not prefix: forward byte)
Docker subprocess stdin
    ↓
Container (Claude Code)
    ↓ stdout/stderr
Docker subprocess stdout/stderr
    ↓
Proxy goroutine: write to terminal (inside scroll region)
    ↓
Host Terminal
```

### Prefix Key State Machine

```
NORMAL → (Ctrl+B) → PREFIX_WAIT
PREFIX_WAIT → d → detach (stop proxying, return)
PREFIX_WAIT → q → quit (docker stop + remove session, return)
PREFIX_WAIT → Ctrl+B → forward literal Ctrl+B to container
PREFIX_WAIT → (anything else) → ignore, back to NORMAL
PREFIX_WAIT → (200ms timeout) → ignore, back to NORMAL
```

### Status Bar

- Reserve bottom terminal row via ANSI scroll region: `\033[1;{h-1}r`
- Redraw on startup and SIGWINCH (terminal resize)
- Content: `│ session-name │ branch │ yolo │ ^B d:detach q:quit │`
- Styled with ANSI colors matching existing lipgloss palette

### Exit / Cleanup Handling

- **Ctrl+C**: always forwarded to container (Claude uses it to cancel operations)
- **Container exits** (subprocess exits): if `AutoRemove`, cleanup (remove container + delete session + remove worktree)
- **Ctrl+B,q**: docker stop, then same cleanup if `AutoRemove`
- **Ctrl+B,d**: stop proxying, return to caller. Container keeps running.

### Terminal Setup/Teardown

- On entry: save terminal state, set raw mode, set scroll region
- On exit (any path): restore terminal state, clear scroll region

## Caller Changes

### `cmd/new.go`

Foreground mode calls `proxy.Run()` instead of `docker.ExecForeground()`.

### `cmd/attach.go`

All three cases (running, stopped, missing container) call `proxy.Run()` with appropriate docker args (`docker attach`, `docker start -ai`, `docker run`).

### `cmd/root.go`

Dashboard calls `proxy.Run()` instead of `docker.Attach()`. Auto-remove logic moves into the proxy.

### `cmd/shell.go`

Debug shell goes through proxy (gets status bar and keybindings).

### Functions Removed from `internal/docker/`

- `ExecAttach()` — replaced by proxy
- `ExecStartAttach()` — replaced by proxy
- `ExecForeground()` — replaced by proxy
- `Attach()` — replaced by proxy

Keep: `RunDetached()`, `Build()`, `Stop`, `Remove`, `Start`, `IsRunning`, etc.

## Auth Improvements (`cmd/auth.go`)

### Pass `--dangerously-skip-permissions`

Auth container runs `claude --dangerously-skip-permissions` so login flow isn't interrupted by tool permission prompts.

### Auto-exit on successful login

A goroutine polls `store.ClaudeConfigDir()/.credentials.json` every 500ms while the auth container runs. When the file appears, send SIGTERM to the docker subprocess for graceful exit.

Auth does NOT use the PTY proxy — stays as a simple subprocess with stdio connected, plus the polling goroutine.
