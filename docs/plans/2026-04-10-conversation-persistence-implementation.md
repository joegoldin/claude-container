# Conversation Persistence & Per-Repo Isolation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Isolate conversation history per-repo, fix the credential mount security issue, add `--resume` passthrough, and build TUI conversation management.

**Architecture:** Replace shared `claude-config/` with per-repo config dirs keyed by SHA-256 of repo path. Mount individual credential files instead of `~/.claude/` directory. Add `--resume` flag that passes through to Claude Code. TUI gets a conversations sub-page for browsing/copying history between repos.

**Tech Stack:** Go, Bubble Tea (TUI), Docker volume mounts, JSONL transcript parsing

---

## File Structure

| File | Responsibility |
|------|---------------|
| `internal/config/config.go` | `RepoConfigDir()`, `RepoIndex` type, `HostClaudeCredentialFiles()`, repo index CRUD |
| `internal/config/migrate.go` | One-time migration from shared to per-repo config dirs |
| `internal/docker/docker.go` | Individual credential file mounts, `RunOpts.HostClaudeFiles` |
| `cmd/new.go` | Per-repo config dir resolution, `--resume` flag |
| `cmd/work.go` | `--resume` flag |
| `cmd/run.go` | `--resume` flag |
| `cmd/attach.go` | Per-repo config dir on recreation |
| `cmd/conversations.go` | CLI subcommand: list, copy, rm |
| `internal/tui/conversations.go` | Bubble Tea model for conversation management |
| `internal/tui/dashboard.go` | Navigation key to conversations view |
| `internal/transcript/transcript.go` | `ScanConversations()` helper for listing transcripts with metadata |

---

## Phase 1: Security Fix — Mount Individual Credential Files

### Task 1.1: Replace `HostClaudeDir()` with `HostClaudeCredentialFiles()`

- [ ] Read `internal/config/config.go` lines 83-96 (`HostClaudeDir()`)
- [ ] Add new function `HostClaudeCredentialFiles()` that returns a `[]string` of existing credential file paths:
  ```go
  func HostClaudeCredentialFiles() []string {
      home, err := os.UserHomeDir()
      if err != nil { return nil }
      candidates := []string{".credentials.json", "settings.json", ".claude.json"}
      var files []string
      for _, name := range candidates {
          p := filepath.Join(home, ".claude", name)
          if _, err := os.Stat(p); err == nil {
              files = append(files, p)
          }
      }
      return files
  }
  ```
- [ ] Keep `HostClaudeDir()` for now (remove in task 1.3 after callers are updated)
- [ ] Run `go build ./...` — verify it compiles
- [ ] Commit: "feat: add HostClaudeCredentialFiles for individual file mounts"

### Task 1.2: Update `RunOpts` and `RunArgs()` in docker.go

- [ ] Read `internal/docker/docker.go` lines 82-106 (`RunOpts`) and lines 210-216 (host credential mount section)
- [ ] Replace `HostClaudeDir string` field with `HostClaudeFiles []string` in `RunOpts` (line ~87)
- [ ] Update `RunArgs()` (lines 210-216): instead of mounting the directory, mount each file individually:
  ```go
  // Mount host Claude credential files read-only (individual files, not whole directory).
  for _, f := range opts.HostClaudeFiles {
      base := filepath.Base(f)
      args = append(args, "-v", f+":/mnt/claude-host/"+base+":ro")
  }
  ```
- [ ] Find and update `TaskRunArgs()` the same way (search for `HostClaudeDir` in that function)
- [ ] Run `go build ./...` — expect compile errors (callers still pass old field)
- [ ] Commit: "refactor: docker mount individual credential files instead of directory"

### Task 1.3: Update all callers

- [ ] Search for `HostClaudeDir` across the codebase: `grep -r "HostClaudeDir" cmd/ internal/`
- [ ] Update each caller (likely `cmd/new.go` `createSession()` ~line 462-493, and `cmd/attach.go` `ensureRunning()` ~line 129-145) to use `HostClaudeFiles: config.HostClaudeCredentialFiles()`
- [ ] Remove old `HostClaudeDir()` function from config.go
- [ ] Run `go build ./...` — verify clean compile
- [ ] Run `go test ./...` — verify existing tests pass
- [ ] Commit: "fix: stop exposing entire ~/.claude directory to containers"

---

## Phase 2: Per-Repo Config Directories

### Task 2.1: Add `RepoConfigDir()` and `RepoIndex` to config.go

- [ ] Read `internal/config/config.go` to understand Store methods
- [ ] Add imports: `"crypto/sha256"`, `"encoding/hex"`
- [ ] Add `RepoEntry` struct and `RepoIndex` type:
  ```go
  type RepoEntry struct {
      Path     string    `json:"path"`
      Name     string    `json:"name"`
      LastUsed time.Time `json:"last_used"`
  }
  ```
- [ ] Add `RepoConfigDir(repoPath string) string` method on Store:
  - Compute SHA-256 of `repoPath`, take first 12 hex chars
  - Return `filepath.Join(s.dir, "claude-config", repoID)`
- [ ] Add `RepoID(repoPath string) string` helper (pure function, no Store needed)
- [ ] Add `repoIndexPath()` helper: returns `filepath.Join(s.dir, "claude-config", "repos.json")`
- [ ] Run `go build ./...`
- [ ] Commit: "feat: add RepoConfigDir and RepoID for per-repo config isolation"

### Task 2.2: Add repo index CRUD methods

- [ ] Add `UpsertRepo(repoPath string) error` on Store:
  - Read repos.json (create if missing)
  - Compute repo ID
  - Set/update entry with path, basename as name, current time as last_used
  - Write repos.json back (use mutex)
- [ ] Add `ListRepos() (map[string]RepoEntry, error)` on Store:
  - Read and parse repos.json, return empty map if file doesn't exist
- [ ] Add `DeleteRepo(repoID string) error` on Store:
  - Remove from repos.json and optionally delete the config dir
- [ ] Write test `internal/config/config_test.go` (or add to existing):
  - Test `RepoID` produces stable 12-char hex
  - Test `UpsertRepo` creates repos.json and directory
  - Test `ListRepos` returns upserted repos
  - Test `RepoConfigDir` returns correct path
- [ ] Run tests, verify pass
- [ ] Commit: "feat: repo index CRUD for per-repo conversation storage"

### Task 2.3: Add `RepoPath` field to Session and update `createSession()`

- [ ] Read `cmd/new.go` `createSession()` lines 264-559
- [ ] The Session struct already has `RepoPath string` (line ~30). This is set when worktree mode is used. For non-worktree mode, it's empty. Update `createSession()` to always resolve and store the repo path:
  - After resolving CWD and repo root (line ~265), store the repo root (even in non-worktree mode)
  - If not in a git repo, use CWD as the "repo path"
- [ ] After resolving repo path, compute per-repo config dir:
  ```go
  repoConfigDir := store.RepoConfigDir(repoPath)
  os.MkdirAll(repoConfigDir, 0o700)
  store.UpsertRepo(repoPath)
  ```
- [ ] Replace the `store.ClaudeConfigDir()` call (~line 427-434) with `repoConfigDir`
- [ ] Pass `repoConfigDir` as `opts.ConfigDir` in RunOpts (~line 462)
- [ ] Run `go build ./...`
- [ ] Commit: "feat: use per-repo config dir for session creation"

### Task 2.4: Update `attach.go` to use per-repo config dir

- [ ] Read `cmd/attach.go` `ensureRunning()` lines 97-152
- [ ] When recreating a container, it needs the per-repo config dir. The session already stores `RepoPath`. Use `store.RepoConfigDir(sess.RepoPath)` as the `ConfigDir` in RunOpts (line ~133)
- [ ] Also update the managed-settings write path (~line 118-122) to use per-repo config dir
- [ ] Run `go build ./...`
- [ ] Commit: "fix: attach uses per-repo config dir when recreating container"

### Task 2.5: Write migration logic

- [ ] Create `internal/config/migrate.go`
- [ ] Add `MigrateToPerRepo(store *Store) error`:
  1. Check if old `claude-config/projects/` exists
  2. If no JSONL files found, return nil (nothing to migrate)
  3. Load sessions.json, build map of ResumeID → RepoPath
  4. For each JSONL file in `claude-config/projects/-workspace/`:
     - Extract UUID from filename
     - Look up RepoPath from sessions map
     - If found: move to `RepoConfigDir(repoPath)/projects/-workspace/`
     - If not found: move to `RepoConfigDir("_orphaned")/projects/-workspace/`
  5. Upsert repos.json entries for each destination
  6. Return count of migrated files (caller prints message)
- [ ] Write test: create temp dir with fake shared config, fake sessions.json, run migration, verify files moved correctly
- [ ] Run tests
- [ ] Commit: "feat: migration from shared to per-repo conversation storage"

### Task 2.6: Call migration on startup

- [ ] Read `cmd/new.go` or `cmd/root.go` to find a good place for one-time migration
- [ ] Add migration call in `createSession()` before per-repo dir resolution (or in root command PersistentPreRun if one exists):
  ```go
  if n, err := config.MigrateToPerRepo(store); err != nil {
      fmt.Fprintf(os.Stderr, "warning: migration failed: %v\n", err)
  } else if n > 0 {
      fmt.Fprintf(os.Stderr, "Migrated %d conversations to per-repo storage\n", n)
  }
  ```
- [ ] Run `go build ./...`
- [ ] Manual test: create a fake shared config dir, run a command, verify migration happens
- [ ] Commit: "feat: auto-migrate conversation history on first run"

---

## Phase 3: Resume Flag Passthrough

### Task 3.1: Add `--resume` flag to `new`, `work`, `run`

- [ ] Read `cmd/new.go` flag definitions (lines 23-45 and 192-220)
- [ ] Add `newResume string` flag variable and register:
  ```go
  newCmd.Flags().StringVar(&newResume, "resume", "", "resume a previous conversation (pass ID or empty for picker)")
  ```
  Note: Cobra doesn't distinguish "flag present with no value" from "flag absent" for string flags. Use `NoOptDefVal` to handle `--resume` with no value:
  ```go
  newCmd.Flags().Lookup("resume").NoOptDefVal = "__picker__"
  ```
  When `newResume == "__picker__"`, pass `--resume` with no value to Claude. When it has a UUID value, pass `--resume <id>`.
- [ ] Add same flag to `cmd/work.go` and `cmd/run.go`
- [ ] Add `resume string` field to `createOpts` struct
- [ ] Wire through: `createOpts.resume` → `RunOpts.Resume`
- [ ] The existing `RunArgs()` already handles `opts.Resume != ""` (docker.go line ~227). Verify it appends `"--resume", opts.Resume` to the claude command args. For the picker case (`__picker__`), append just `"--resume"` with no value.
- [ ] Update `RunArgs()` to handle the picker sentinel:
  ```go
  if opts.Resume == "__picker__" {
      args = append(args, "--resume")
  } else if opts.Resume != "" {
      args = append(args, "--resume", opts.Resume)
  }
  ```
- [ ] Run `go build ./...`
- [ ] Commit: "feat: --resume flag passthrough to Claude Code"

### Task 3.2: Store resume preference in Session

- [ ] The Session struct already has `ResumeID string`. This is extracted from logs after a session runs. The new `--resume` flag is different — it's a user preference at creation time.
- [ ] Actually, reconsider: the `--resume` flag is only meaningful on first launch. On `attach` (container recreation), we already use `sess.ResumeID` (extracted from logs). So we don't need to persist the flag — just pass it through on initial `docker run`.
- [ ] Verify `attach.go` `ensureRunning()` already handles `sess.ResumeID` correctly (it does, lines 124-139)
- [ ] No Session struct changes needed. Remove this from the spec's file changes table.
- [ ] Commit: (skip, no changes)

---

## Phase 4: CLI Conversations Subcommand

### Task 4.1: Create `cmd/conversations.go` with `list` subcommand

- [ ] Create `cmd/conversations.go`
- [ ] Define `conversationsCmd` as a Cobra command with `Use: "conversations"`, `Aliases: []string{"convos"}`
- [ ] Define `conversationsListCmd`:
  - Load repo index via `store.ListRepos()`
  - For each repo, scan `RepoConfigDir(repoPath)/projects/-workspace/*.jsonl`
  - Print table: repo name, path, conversation count, last modified date
  - Sort by last_used descending
- [ ] Register `conversationsCmd` under root, `conversationsListCmd` under conversations
- [ ] Run `go build ./...`
- [ ] Manual test: run `claude-container conversations list`
- [ ] Commit: "feat: conversations list subcommand"

### Task 4.2: Add `copy` subcommand

- [ ] Add `conversationsCopyCmd` to `cmd/conversations.go`:
  - Args: `<source-repo-name> <target-repo-name>` (match by repo name from index)
  - Optional `--conversation <uuid>` flag to copy one specific conversation
  - Resolve source/target repo IDs from names
  - Copy JSONL files from source's `projects/-workspace/` to target's `projects/-workspace/`
  - Print count of copied files
- [ ] Write test: create two repo config dirs with fake JSONL files, run copy, verify files exist in both
- [ ] Run tests
- [ ] Commit: "feat: conversations copy subcommand"

### Task 4.3: Add `rm` subcommand

- [ ] Add `conversationsRmCmd` to `cmd/conversations.go`:
  - Args: `<repo-name>`
  - Optional `--conversation <uuid>` to delete one
  - Without `--conversation`: delete all JSONL files in the repo's project dir + remove repo from index
  - With `--conversation`: delete just that JSONL file
  - Confirm before deleting (unless `--force`)
- [ ] Run `go build ./...`
- [ ] Commit: "feat: conversations rm subcommand"

---

## Phase 5: TUI Conversation Management

### Task 5.1: Add `ScanConversations()` to transcript.go

- [ ] Read `internal/transcript/transcript.go`
- [ ] Add `ConversationInfo` struct:
  ```go
  type ConversationInfo struct {
      ID        string    // UUID from filename
      Path      string    // full path to JSONL file
      ModTime   time.Time // file modification time
      Size      int64     // file size in bytes
      Preview   string    // first user message (truncated)
  }
  ```
- [ ] Add `ScanConversations(projectDir string) ([]ConversationInfo, error)`:
  - Glob `projectDir/projects/-workspace/*.jsonl`
  - For each file: stat for mod time/size, read first few lines to extract first user message as preview
  - Sort by ModTime descending
- [ ] Write test with temp dir and fake JSONL files
- [ ] Run tests
- [ ] Commit: "feat: ScanConversations helper for listing conversation metadata"

### Task 5.2: Create conversations Bubble Tea model

- [ ] Create `internal/tui/conversations.go`
- [ ] Define `ConversationsModel` struct:
  ```go
  type ConversationsModel struct {
      store       *config.Store
      repos       []repoRow         // repo index entries with conversation counts
      convos      []transcript.ConversationInfo  // conversations for selected repo
      repoCursor  int
      convoCursor int
      inRepo      bool              // true = viewing conversations within a repo
      width, height int
      err         error
  }
  ```
- [ ] Implement `Init()`: load repo index, scan conversation counts
- [ ] Implement `Update()`:
  - `j/k` or `up/down`: navigate repos or conversations
  - `enter`: expand repo to show conversations / select conversation for action
  - `c`: copy conversation (prompt for target repo)
  - `d`: delete conversation (with confirmation)
  - `esc`/`backspace`: go back from conversation list to repo list
  - `q`: return to dashboard
- [ ] Implement `View()`: render repo list or conversation list depending on state
- [ ] Run `go build ./...`
- [ ] Commit: "feat: TUI conversations model for browsing repo history"

### Task 5.3: Wire conversations into dashboard

- [ ] Read `internal/tui/dashboard.go` `Update()` method (lines 102-251)
- [ ] Add a key binding (e.g., `h` for "history" or `c` for "conversations") that transitions to the conversations model
- [ ] The dashboard needs to be able to switch between its own view and the conversations view. Add a `showConversations bool` and `convosModel ConversationsModel` field to `DashboardModel`
- [ ] In `Update()`: if `showConversations`, delegate to `convosModel.Update()`. If conversations model signals "back", set `showConversations = false`
- [ ] In `View()`: if `showConversations`, return `convosModel.View()`
- [ ] Add `c` key hint to the dashboard footer/help text
- [ ] Run `go build ./...`
- [ ] Manual test: launch TUI, press `c`, verify conversations view appears
- [ ] Commit: "feat: wire conversations view into TUI dashboard"

---

## Phase 6: Final Integration Testing

### Task 6.1: End-to-end test

- [ ] Create a test repo in /tmp
- [ ] Run `claude-container run --name test1` in the repo, verify per-repo config dir created
- [ ] Stop session, verify JSONL transcript exists in per-repo dir
- [ ] Run `claude-container run --name test2 --resume` in the same repo, verify Claude shows resume picker with only that repo's conversations
- [ ] Run `claude-container conversations list`, verify repo appears with conversation count
- [ ] Create second test repo, run a session, verify separate config dir
- [ ] Run `claude-container conversations copy repo1 repo2`, verify JSONL copied
- [ ] Commit: "test: end-to-end conversation persistence verification"
