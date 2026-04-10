# Conversation Persistence & Per-Repo Isolation

## Summary

Three changes: (1) security fix to stop mounting all of `~/.claude` into containers, (2) per-repo CLAUDE_CONFIG_DIR so conversations are isolated by repository and Claude's `/resume` only shows relevant history, (3) `--resume` flag passthrough and TUI conversation management.

## Problem

- `~/.claude/` is mounted read-only into every container, exposing all host conversation history (JSONL files, projects, etc.) even though only credential files are needed
- All containers share one CLAUDE_CONFIG_DIR with the same `/workspace` CWD encoding, so conversations from every project are jumbled together
- No way to resume a previous conversation when starting a new session in the same repo
- No way to browse or manage conversation history across repos

## Design

### 1. Security Fix: Mount Only Credential Files

**Current** (docker.go):
```
-v ~/.claude:/mnt/claude-host:ro    # exposes entire directory tree
```

**New**:
```
-v ~/.claude/.credentials.json:/mnt/claude-host/.credentials.json:ro
-v ~/.claude/settings.json:/mnt/claude-host/settings.json:ro
-v ~/.claude/.claude.json:/mnt/claude-host/.claude.json:ro
```

Each file mounted individually. If a file doesn't exist on the host, skip it (same as current behavior for the directory). The entrypoint already only copies these three files, so no entrypoint changes needed.

### 2. Per-Repo CLAUDE_CONFIG_DIR

Instead of one shared `claude-config/` directory, create per-repo config directories:

**Path**: `~/.config/claude-container/claude-config/<repo-id>/`

**repo-id**: First 12 hex chars of SHA-256 of the canonical repo path (resolved via `git rev-parse --show-toplevel`). Example:
- Repo at `/home/joe/Development/claude-container`
- SHA-256 prefix: `a1b2c3d4e5f6`
- Config dir: `~/.config/claude-container/claude-config/a1b2c3d4e5f6/`

**Repo index file**: `~/.config/claude-container/claude-config/repos.json`
```json
{
  "a1b2c3d4e5f6": {
    "path": "/home/joe/Development/claude-container",
    "name": "claude-container",
    "last_used": "2026-04-10T12:00:00Z"
  }
}
```

This allows the TUI and CLI to map repo IDs back to human-readable names/paths.

**What lives in each per-repo config dir**:
- `.credentials.json` — copied from host mount on startup (same auth everywhere)
- `.claude.json` — copied from host mount on startup
- `settings.json` — copied from host mount on startup
- `managed-settings.json` — written by claude-container per sandbox profile
- `projects/-workspace/` — Claude Code conversation transcripts (JSONL)

**Session creation flow** (changes to `createSession()`):
1. Resolve repo root from CWD (or from `--worktree` source repo)
2. Compute repo-id hash
3. Create per-repo config dir if it doesn't exist
4. Upsert entry in `repos.json`
5. Pass per-repo config dir as `opts.ConfigDir` to `docker.RunArgs()`

**Non-repo directories**: If CWD is not inside a git repo, use a hash of the CWD path itself. The repo index entry would have `"name"` set to the directory basename.

### 3. Resume Flag Passthrough

Add `--resume` flag to `new`, `work`, and `run` commands:

- `--resume` (no value): Pass `--resume` to Claude Code. Claude shows its own session picker UI, which now only lists conversations from the same repo.
- `--resume <id>`: Pass `--resume <id>` to Claude Code. Resumes that exact conversation.
- No flag (default): Fresh conversation. User can still use Claude's `/resume` slash command interactively inside the container.

The `--resume` flag is stored in `Session.Resume` (new field) so that `attach` can re-pass it when recreating a destroyed container.

**Changes to docker.go `RunArgs()`**: The existing `Resume` and `Continue` fields on `RunOpts` already support this. Wire the new CLI flag through `createOpts` → `RunOpts.Resume`.

### 4. TUI Conversation Management

Add a "Conversations" sub-page to the TUI dashboard accessible from the main session list.

**Conversations view**:
- Lists repos with conversation history, sorted by last used
- Each repo row shows: repo name, path, conversation count, last active date
- Selecting a repo expands to show its conversations (date, first user message preview, message count)

**Actions available**:
- **Copy/merge**: Copy conversation history from one repo's config dir to another. This copies JSONL files from `source/projects/-workspace/` into `target/projects/-workspace/`. Use case: "I was working in repoA but need to reference that conversation context in repoB."
- **Delete**: Remove individual conversations or all conversations for a repo
- **Browse**: View conversation transcript (reuse `transcript.RenderMarkdown()` from extract command)

**Copy/merge semantics**:
- Copies selected JSONL files (not moves)
- No deduplication needed — Claude Code identifies sessions by UUID filename
- After copying, the conversation appears in the target repo's `/resume` list

**Implementation**: New Bubble Tea model in `internal/tui/conversations.go` that reads `repos.json` and scans per-repo `projects/-workspace/*.jsonl` files.

**CLI alternative**: `claude-container conversations` subcommand with:
- `claude-container conversations list` — list repos and conversation counts
- `claude-container conversations copy <source-repo> <target-repo> [--conversation <id>]` — copy conversations between repos
- `claude-container conversations rm <repo> [--conversation <id>]` — delete conversations

## Security Model

The container's filesystem isolation comes from Docker, not bubblewrap. Claude Code's built-in bwrap sandbox is intentionally weakened inside containers (`enableWeakerNestedSandbox=true` in managed-settings) because full bwrap-in-Docker nesting is unreliable. This means any path mounted into the container is directly readable by Claude Code.

This is why the security fix (section 1) is critical: the current `-v ~/.claude:/mnt/claude-host:ro` mount exposes all host conversation history to Claude Code inside the container. After the fix, only individual credential files are mounted — no conversation transcripts leak between host and container.

The per-repo config dir (section 2) also improves isolation: each container only sees conversations from its own repo, not from all repos. A compromised session in repoA cannot read repoB's conversation history.

## Migration

Existing installations have conversations in `~/.config/claude-container/claude-config/projects/-workspace/`. On first run after upgrade:

1. Check if old shared `claude-config/projects/` exists with JSONL files
2. If yes, check `sessions.json` for repo paths associated with each ResumeID
3. Move each transcript to the appropriate per-repo config dir
4. Transcripts with no matching session record go to an `_orphaned/` repo-id
5. Print a one-time message: "Migrated N conversations to per-repo storage"

If no sessions.json records exist (clean state or all removed), move everything to `_orphaned/`.

## File Changes

| File | Change |
|------|--------|
| `internal/config/config.go` | Replace `HostClaudeDir()` with `HostClaudeCredentialFiles()` returning individual file paths; add `RepoConfigDir(repoPath)`, `RepoIndex` type, `ListRepos()`, `UpsertRepo()` |
| `internal/docker/docker.go` | Mount individual credential files instead of `~/.claude/` directory; update `RunArgs()` and `TaskRunArgs()` |
| `cmd/new.go` | Compute repo-id, pass per-repo config dir, add `--resume` flag |
| `cmd/work.go` | Add `--resume` flag |
| `cmd/run.go` | Add `--resume` flag |
| `cmd/attach.go` | Persist resume preference for container recreation |
| `cmd/conversations.go` | New — CLI subcommand for conversation management |
| `internal/tui/conversations.go` | New — Bubble Tea model for conversation management view |
| `internal/tui/dashboard.go` | Add navigation to conversations view |
| `internal/config/migrate.go` | New — one-time migration from shared to per-repo config dirs |
