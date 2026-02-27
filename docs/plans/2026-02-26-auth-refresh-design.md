# Auth Refresh Design

## Problem

When host credentials get refreshed (token rotation, re-login), running containers have stale copies. There's no way to update them without restarting the container.

## Solution

Add `claude-container auth refresh` command that re-copies credentials from the existing read-only mounts into all running containers.

## Design

### Command

`claude-container auth refresh` — subcommand of `auth`.

### Flow

1. Load all sessions from the store
2. Filter to running containers (`docker.IsRunning()`)
3. For each, `docker exec` a shell snippet that mirrors the entrypoint copy logic:
   - Copy `.credentials.json`, `settings.json`, `.claude.json` from `/mnt/claude-host/` to `/claude/`
   - Copy `/mnt/claude-host-json` to `/claude/.claude.json`
   - Set permissions to 600
4. Print success/failure per container

### Implementation

- `internal/docker/docker.go`: Add `RefreshAuth(session string) error` — runs `docker exec` with the copy script
- `cmd/auth.go`: Add `authRefreshCmd` as subcommand of `authCmd`
- No image/entrypoint changes needed

### Why `docker exec` over `docker cp`

The host credentials are already mounted read-only at `/mnt/claude-host/` inside running containers. Exec'ing to re-copy reuses the same mechanism as the entrypoint — no need to push files from outside.
