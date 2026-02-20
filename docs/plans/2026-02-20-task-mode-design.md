# Task Mode Design

## Problem

claude-container currently only supports interactive sessions (foreground with PTY proxy) or background sessions (detached, requires manual attach). There is no way to run a one-shot task that returns Claude's final output to stdout for scripting and piping, while printing metadata (changed files, duration, tokens) to stderr.

## Solution

A new `claude-container task` subcommand that runs Claude non-interactively, emits the final answer on stdout, and prints a summary to stderr.

## Command Interface

```
claude-container task -p "fix the failing tests" [flags]
```

### Flags

Carried over from `run`:
- `-p/--prompt` (required) -- the task prompt
- `--name` -- session name (auto-generated if omitted)
- `--profile` -- sandbox profile (default/med/high)
- `--allow-command`, `--deny-command`, `--allow-domain`, `--deny-path` -- security overrides
- `-w/--mount`, `-W/--workspace` -- additional folder mounts
- `--proxy-profile`, `--proxy-port` -- proxy config

Task-specific:
- `--keep` -- persist session after completion (default: ephemeral, auto-removed)
- `--model` -- passed through to Claude CLI
- `--max-turns` -- passed through to Claude CLI

### Lifecycle

1. Same setup as `createSession` (proxy, image, managed settings, auth)
2. Container starts **detached** with `claude -p --output-format stream-json --dangerously-skip-permissions "prompt"`
3. `docker logs -f` streams NDJSON events
4. Go parser extracts events, shows spinner on stderr (TTY only)
5. Container exits when Claude finishes
6. `docker exec <container> git diff --name-status HEAD` captures changed files
7. Final assistant text goes to stdout
8. Summary block goes to stderr
9. Container and session cleaned up (unless `--keep`)

### Exit Code

Propagates Claude's exit code from `docker wait`. Returns 0 on success, non-zero on failure.

## Output Design

### Stdout (pipeable)

Only the final assistant text. This is the last text content block from the final assistant message in the stream-json output. No metadata, no formatting.

### Stderr

Spinner while running (TTY only, suppressed when piped):
```
Working... 45s
```

Summary after completion:
```
--- Task Complete ---
Changed files:
  M src/auth.go
  M src/auth_test.go
  A src/middleware.go
Duration: 1m 34s
Tokens:   12,450 in / 3,200 out
Session:  myproject-calm-reef  (kept)
```

The "Session" line only appears with `--keep`. Changed files section is skipped if the workspace is not a git repo.

## Stream-JSON Parsing

Claude's `--output-format stream-json` emits NDJSON (one JSON object per line). Key event types:

- `{"type": "system", "subtype": "init", "session_id": "..."}` -- capture session ID for `--keep`
- `{"type": "assistant", "message": {"content": [{"type": "text", "text": "..."}]}}` -- accumulate text
- `{"type": "result", ...}` -- final metadata (tokens, etc.)

The parser maintains a rolling "last assistant text" since Claude produces multiple assistant messages during a task. The final text is the last text content block from the final assistant message.

## Files

| File | Change |
|------|--------|
| `cmd/task.go` (new) | Subcommand with flags, orchestration |
| `cmd/task_parser.go` (new) | NDJSON stream parser, event types, text extraction |
| `cmd/task_parser_test.go` (new) | Unit tests for parser |
| `internal/docker/docker.go` | `TaskRunArgs()` builder, `ExecGitDiff()` helper |

### Key Decisions

- **Separate `TaskRunArgs()`** rather than conditionals in `RunArgs()`. Task needs different Claude CLI flags (`-p`, `--output-format stream-json`, no `-it`).
- **Parser in separate file** for independent testability without Docker.
- **`docker logs -f`** streams output from detached container. Terminates when container exits.
- **Git diff via `docker exec`** after Claude exits but before container removal. Fails gracefully if no git.
- **Ephemeral by default** -- container removed after completion unless `--keep`.
