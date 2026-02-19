# Workspaces and Sandbox Profiles Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add multi-folder workspaces (named and ad-hoc) and tiered sandbox profiles (low/med/high) with runtime overrides to claude-container.

**Architecture:** Workspaces are stored in `workspaces.json` as name-to-paths maps, resolved at session creation and passed as extra Docker volume mounts. Sandbox profiles are hardcoded Go structs that generate managed-settings JSON overlays written to the container config dir. Both integrate via new flags on existing `run`/`work`/`new` commands.

**Tech Stack:** Go, cobra (CLI), Docker volume mounts, JSON config files

---

### Task 1: Workspace Store (config package)

Add `WorkspaceStore` to `internal/config/` for reading/writing `workspaces.json`.

**Files:**
- Create: `internal/config/workspace.go`
- Create: `internal/config/workspace_test.go`

**Step 1: Write tests for workspace store**

```go
// internal/config/workspace_test.go
package config

import (
	"path/filepath"
	"testing"
)

func TestWorkspaceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	ws := NewWorkspaceStore(dir)

	if err := ws.Add("my-work", []string{"/home/joe/code/a", "/home/joe/code/b"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	paths, err := ws.Get("my-work")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("Get: got %d paths, want 2", len(paths))
	}
	if paths[0] != "/home/joe/code/a" || paths[1] != "/home/joe/code/b" {
		t.Errorf("Get: got %v, want [/home/joe/code/a /home/joe/code/b]", paths)
	}
}

func TestWorkspaceAddAppends(t *testing.T) {
	dir := t.TempDir()
	ws := NewWorkspaceStore(dir)

	ws.Add("w", []string{"/a"})
	ws.Add("w", []string{"/b"})

	paths, _ := ws.Get("w")
	if len(paths) != 2 {
		t.Fatalf("Add should append: got %d paths, want 2", len(paths))
	}
}

func TestWorkspaceAddDeduplicates(t *testing.T) {
	dir := t.TempDir()
	ws := NewWorkspaceStore(dir)

	ws.Add("w", []string{"/a", "/b"})
	ws.Add("w", []string{"/b", "/c"})

	paths, _ := ws.Get("w")
	if len(paths) != 3 {
		t.Fatalf("Add should deduplicate: got %d paths, want 3", len(paths))
	}
}

func TestWorkspaceList(t *testing.T) {
	dir := t.TempDir()
	ws := NewWorkspaceStore(dir)

	ws.Add("alpha", []string{"/a"})
	ws.Add("beta", []string{"/b"})

	names := ws.List()
	if len(names) != 2 {
		t.Fatalf("List: got %d, want 2", len(names))
	}
}

func TestWorkspaceRemove(t *testing.T) {
	dir := t.TempDir()
	ws := NewWorkspaceStore(dir)

	ws.Add("w", []string{"/a"})
	if err := ws.Remove("w"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := ws.Get("w"); err == nil {
		t.Fatal("Get after Remove should error")
	}
}

func TestWorkspaceGetNonExistent(t *testing.T) {
	dir := t.TempDir()
	ws := NewWorkspaceStore(dir)

	_, err := ws.Get("nope")
	if err == nil {
		t.Fatal("Get non-existent should error")
	}
}

func TestWorkspaceStoreFile(t *testing.T) {
	dir := t.TempDir()
	ws := NewWorkspaceStore(dir)
	ws.Add("test", []string{"/a"})

	// Verify file was written.
	path := filepath.Join(dir, WorkspaceFile)
	if !fileExists(path) {
		t.Fatal("workspaces.json not created")
	}

	// New store instance should read the same data.
	ws2 := NewWorkspaceStore(dir)
	paths, err := ws2.Get("test")
	if err != nil {
		t.Fatalf("Get from new store: %v", err)
	}
	if len(paths) != 1 || paths[0] != "/a" {
		t.Errorf("persistence failed: got %v", paths)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
```

**Step 2: Run tests to verify they fail**

Run: `nix develop --command go test ./internal/config/ -run TestWorkspace -v`
Expected: FAIL (types/functions not defined)

**Step 3: Implement workspace store**

```go
// internal/config/workspace.go
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

const WorkspaceFile = "workspaces.json"

// WorkspaceStore provides thread-safe persistence of named workspace
// definitions (name -> list of paths) to a JSON file.
type WorkspaceStore struct {
	mu  sync.Mutex
	dir string
}

// NewWorkspaceStore returns a WorkspaceStore backed by workspaces.json in dir.
func NewWorkspaceStore(dir string) *WorkspaceStore {
	return &WorkspaceStore{dir: dir}
}

// Add creates or appends paths to a named workspace. Duplicate paths are
// silently ignored.
func (ws *WorkspaceStore) Add(name string, paths []string) error {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	all, err := ws.loadLocked()
	if err != nil {
		return err
	}

	existing := all[name]
	seen := make(map[string]bool, len(existing))
	for _, p := range existing {
		seen[p] = true
	}
	for _, p := range paths {
		if !seen[p] {
			existing = append(existing, p)
			seen[p] = true
		}
	}
	all[name] = existing
	return ws.writeLocked(all)
}

// Get returns the paths for a named workspace.
func (ws *WorkspaceStore) Get(name string) ([]string, error) {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	all, err := ws.loadLocked()
	if err != nil {
		return nil, err
	}
	paths, ok := all[name]
	if !ok {
		return nil, fmt.Errorf("workspace %q not found", name)
	}
	return paths, nil
}

// List returns all workspace names sorted alphabetically.
func (ws *WorkspaceStore) List() []string {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	all, err := ws.loadLocked()
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(all))
	for name := range all {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Remove deletes a named workspace.
func (ws *WorkspaceStore) Remove(name string) error {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	all, err := ws.loadLocked()
	if err != nil {
		return err
	}
	if _, ok := all[name]; !ok {
		return fmt.Errorf("workspace %q not found", name)
	}
	delete(all, name)
	return ws.writeLocked(all)
}

func (ws *WorkspaceStore) loadLocked() (map[string][]string, error) {
	path := filepath.Join(ws.dir, WorkspaceFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string][]string), nil
		}
		return nil, err
	}
	var m map[string][]string
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return m, nil
}

func (ws *WorkspaceStore) writeLocked(m map[string][]string) error {
	if err := os.MkdirAll(ws.dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(ws.dir, WorkspaceFile), data, 0o644)
}
```

**Step 4: Run tests to verify they pass**

Run: `nix develop --command go test ./internal/config/ -run TestWorkspace -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/config/workspace.go internal/config/workspace_test.go
git commit -m "feat: add workspace store for named folder collections"
```

---

### Task 2: Sandbox Profiles Package

Create `internal/sandbox/` with hardcoded profile definitions that generate managed-settings JSON.

**Files:**
- Create: `internal/sandbox/sandbox.go`
- Create: `internal/sandbox/sandbox_test.go`

**Step 1: Write tests**

```go
// internal/sandbox/sandbox_test.go
package sandbox

import (
	"encoding/json"
	"testing"
)

func TestProfileNames(t *testing.T) {
	for _, name := range []string{"low", "med", "high"} {
		if _, err := GetProfile(name); err != nil {
			t.Errorf("GetProfile(%q) error: %v", name, err)
		}
	}
}

func TestInvalidProfile(t *testing.T) {
	_, err := GetProfile("nonexistent")
	if err == nil {
		t.Fatal("GetProfile with invalid name should error")
	}
}

func TestLowProfileDisablesSandbox(t *testing.T) {
	p, _ := GetProfile("low")
	settings := p.ManagedSettings(nil, nil)
	sandbox, ok := settings["sandbox"].(map[string]any)
	if !ok {
		t.Fatal("low profile should have sandbox key")
	}
	if enabled, _ := sandbox["enabled"].(bool); enabled {
		t.Error("low profile sandbox.enabled should be false")
	}
}

func TestMedProfileEnablesSandbox(t *testing.T) {
	p, _ := GetProfile("med")
	settings := p.ManagedSettings(nil, nil)
	sandbox := settings["sandbox"].(map[string]any)
	if enabled, _ := sandbox["enabled"].(bool); !enabled {
		t.Error("med profile sandbox.enabled should be true")
	}
}

func TestHighProfileMinimalNetwork(t *testing.T) {
	p, _ := GetProfile("high")
	settings := p.ManagedSettings(nil, nil)
	sandbox := settings["sandbox"].(map[string]any)
	network := sandbox["network"].(map[string]any)
	domains := network["allowedDomains"].([]string)
	if len(domains) != 1 || domains[0] != "api.anthropic.com" {
		t.Errorf("high profile allowedDomains = %v, want [api.anthropic.com]", domains)
	}
}

func TestOverrideAddsDomain(t *testing.T) {
	p, _ := GetProfile("med")
	settings := p.ManagedSettings([]string{"custom.api.com"}, nil)
	sandbox := settings["sandbox"].(map[string]any)
	network := sandbox["network"].(map[string]any)
	domains := network["allowedDomains"].([]string)
	found := false
	for _, d := range domains {
		if d == "custom.api.com" {
			found = true
		}
	}
	if !found {
		t.Errorf("custom.api.com not in allowedDomains: %v", domains)
	}
}

func TestOverrideAddsDenyPath(t *testing.T) {
	p, _ := GetProfile("med")
	settings := p.ManagedSettings(nil, []string{"/secret"})
	perms := settings["permissions"].(map[string]any)
	deny := perms["deny"].([]string)
	found := false
	for _, d := range deny {
		if d == "Read(/secret)" {
			found = true
		}
	}
	if !found {
		t.Errorf("/secret not in deny list: %v", deny)
	}
}

func TestManagedSettingsJSON(t *testing.T) {
	p, _ := GetProfile("med")
	settings := p.ManagedSettings(nil, nil)
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("empty JSON output")
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `nix develop --command go test ./internal/sandbox/ -v`
Expected: FAIL (package not found)

**Step 3: Implement sandbox profiles**

```go
// internal/sandbox/sandbox.go
package sandbox

import "fmt"

// Profile defines a sandbox security profile.
type Profile struct {
	Name           string
	SandboxEnabled bool
	AllowedDomains []string
	DenyPaths      []string
}

var profiles = map[string]Profile{
	"low": {
		Name:           "low",
		SandboxEnabled: false,
		AllowedDomains: nil,
		DenyPaths:      nil,
	},
	"med": {
		Name:           "med",
		SandboxEnabled: true,
		AllowedDomains: []string{
			"api.anthropic.com",
			"statsig.anthropic.com",
			"sentry.io",
			"github.com",
			"*.github.com",
			"*.npmjs.org",
			"registry.npmjs.org",
			"registry.yarnpkg.com",
			"pypi.org",
			"*.pypi.org",
			"files.pythonhosted.org",
		},
		DenyPaths: []string{
			"Read(/etc/shadow)",
			"Read(/etc/passwd)",
			"Read(~/.ssh/**)",
			"Read(~/.aws/**)",
			"Read(~/.gnupg/**)",
		},
	},
	"high": {
		Name:           "high",
		SandboxEnabled: true,
		AllowedDomains: []string{
			"api.anthropic.com",
		},
		DenyPaths: []string{
			"Read(/etc/**)",
			"Read(/home/**)",
			"Read(/root/**)",
			"Read(/tmp/**)",
		},
	},
}

// GetProfile returns the profile with the given name, or an error if not found.
func GetProfile(name string) (Profile, error) {
	p, ok := profiles[name]
	if !ok {
		return Profile{}, fmt.Errorf("unknown sandbox profile %q (valid: low, med, high)", name)
	}
	return p, nil
}

// ManagedSettings generates a managed-settings map for this profile, with
// optional runtime overrides. extraDomains are added to allowedDomains.
// extraDenyPaths are wrapped as "Read(<path>)" and added to permissions.deny.
func (p Profile) ManagedSettings(extraDomains []string, extraDenyPaths []string) map[string]any {
	domains := make([]string, len(p.AllowedDomains))
	copy(domains, p.AllowedDomains)
	domains = append(domains, extraDomains...)

	deny := make([]string, len(p.DenyPaths))
	copy(deny, p.DenyPaths)
	for _, path := range extraDenyPaths {
		deny = append(deny, fmt.Sprintf("Read(%s)", path))
	}

	settings := map[string]any{
		"env": map[string]any{
			"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
			"DISABLE_AUTOUPDATER":                      "1",
		},
		"cleanupPeriodDays":     14,
		"alwaysThinkingEnabled": true,
		"showTurnDuration":      true,
		"spinnerTipsEnabled":    false,
		"sandbox": map[string]any{
			"enabled":                   p.SandboxEnabled,
			"autoAllowBashIfSandboxed":  true,
			"enableWeakerNestedSandbox": true,
			"allowUnsandboxedCommands":  false,
			"excludedCommands":          []string{"git"},
			"network": map[string]any{
				"allowedDomains": domains,
			},
		},
	}

	if len(deny) > 0 {
		settings["permissions"] = map[string]any{
			"deny": deny,
		}
	}

	return settings
}
```

**Step 4: Run tests to verify they pass**

Run: `nix develop --command go test ./internal/sandbox/ -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/sandbox/sandbox.go internal/sandbox/sandbox_test.go
git commit -m "feat: add sandbox profiles package with low/med/high presets"
```

---

### Task 3: Docker Extra Workspace Mounts

Add `ExtraWorkspaces` to `RunOpts` and generate additional `-v` mounts in `RunArgs()`.

**Files:**
- Modify: `internal/docker/docker.go:76-136`
- Modify: `internal/docker/docker_test.go`

**Step 1: Write tests for extra workspace mounts**

Add to `internal/docker/docker_test.go`:

```go
func TestRunArgsExtraWorkspaces(t *testing.T) {
	opts := RunOpts{
		Name:            "multi-test",
		Workspace:       "/home/user/main-project",
		ConfigDir:       "/tmp/config",
		UID:             1000,
		GID:             1000,
		ExtraWorkspaces: []string{"/home/user/code/repo-a", "/home/user/code/repo-b"},
	}
	args := RunArgs(opts, false)
	joined := strings.Join(args, " ")

	// Primary workspace still mounted at /workspace.
	if !strings.Contains(joined, "/home/user/main-project:/workspace") {
		t.Errorf("RunArgs missing primary workspace mount in %v", args)
	}

	// Extra workspaces mounted as subdirectories.
	if !strings.Contains(joined, "/home/user/code/repo-a:/workspace/repo-a") {
		t.Errorf("RunArgs missing extra workspace repo-a in %v", args)
	}
	if !strings.Contains(joined, "/home/user/code/repo-b:/workspace/repo-b") {
		t.Errorf("RunArgs missing extra workspace repo-b in %v", args)
	}
}

func TestRunArgsExtraWorkspacesOnly(t *testing.T) {
	// When ExtraWorkspaces are set but Workspace is empty, no primary mount.
	opts := RunOpts{
		Name:            "extra-only",
		ConfigDir:       "/tmp/config",
		UID:             1000,
		GID:             1000,
		ExtraWorkspaces: []string{"/home/user/code/repo-a"},
	}
	args := RunArgs(opts, false)
	joined := strings.Join(args, " ")

	if !strings.Contains(joined, "/home/user/code/repo-a:/workspace/repo-a") {
		t.Errorf("RunArgs missing extra workspace in %v", args)
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `nix develop --command go test ./internal/docker/ -run TestRunArgsExtra -v`
Expected: FAIL (ExtraWorkspaces field doesn't exist)

**Step 3: Add ExtraWorkspaces to RunOpts and RunArgs**

In `internal/docker/docker.go`, add field to `RunOpts` (after line 87):

```go
type RunOpts struct {
	Name           string
	Workspace      string
	ConfigDir      string
	HostClaudeDir  string
	HostClaudeJSON string
	UID            int
	GID            int
	Yolo           bool
	Prompt         string
	Continue       bool
	Resume         string
	ExtraWorkspaces []string // additional folders mounted as /workspace/<basename>
}
```

In `RunArgs()`, change the workspace mount logic (around line 105):

```go
	args := []string{
		"run",
		"--name", name,
		flag,
	}

	// Mount primary workspace (if set).
	if opts.Workspace != "" {
		args = append(args, "-v", opts.Workspace+":/workspace")
	}

	// Mount extra workspaces as subdirectories of /workspace.
	for _, ws := range opts.ExtraWorkspaces {
		base := filepath.Base(ws)
		args = append(args, "-v", ws+":/workspace/"+base)
	}

	args = append(args,
		"-v", opts.ConfigDir+":/claude",
		"-e", "CLAUDE_CONFIG_DIR=/claude",
		"-e", fmt.Sprintf("USER_UID=%d", opts.UID),
		"-e", fmt.Sprintf("USER_GID=%d", opts.GID),
	)
```

Add `"path/filepath"` to the imports.

**Step 4: Run all docker tests**

Run: `nix develop --command go test ./internal/docker/ -v`
Expected: ALL PASS

**Step 5: Commit**

```bash
git add internal/docker/docker.go internal/docker/docker_test.go
git commit -m "feat: support extra workspace mounts in docker RunArgs"
```

---

### Task 4: Workspace CLI Subcommand

Add `claude-container workspace add|list|show|rm`.

**Files:**
- Create: `cmd/workspace.go`

**Step 1: Implement workspace subcommands**

```go
// cmd/workspace.go
package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/joegoldin/claude-container/internal/config"
	"github.com/spf13/cobra"
)

var workspaceCmd = &cobra.Command{
	Use:   "workspace",
	Short: "Manage named workspace definitions",
}

var workspaceAddCmd = &cobra.Command{
	Use:   "add <name> <path>...",
	Short: "Create or append paths to a named workspace",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		paths := make([]string, 0, len(args)-1)
		for _, p := range args[1:] {
			abs, err := filepath.Abs(p)
			if err != nil {
				return fmt.Errorf("resolve path %q: %w", p, err)
			}
			if _, err := os.Stat(abs); err != nil {
				return fmt.Errorf("path %q does not exist", abs)
			}
			paths = append(paths, abs)
		}
		ws := config.NewWorkspaceStore(config.DefaultDir())
		if err := ws.Add(name, paths); err != nil {
			return err
		}
		fmt.Printf("Workspace %q updated (%d paths).\n", name, len(paths))
		return nil
	},
}

var workspaceListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all workspace names",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ws := config.NewWorkspaceStore(config.DefaultDir())
		names := ws.List()
		if len(names) == 0 {
			fmt.Println("No workspaces defined.")
			return nil
		}
		for _, name := range names {
			fmt.Println(name)
		}
		return nil
	},
}

var workspaceShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show paths in a workspace",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws := config.NewWorkspaceStore(config.DefaultDir())
		paths, err := ws.Get(args[0])
		if err != nil {
			return err
		}
		for _, p := range paths {
			fmt.Println(p)
		}
		return nil
	},
}

var workspaceRmCmd = &cobra.Command{
	Use:   "rm <name>",
	Short: "Remove a workspace definition",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ws := config.NewWorkspaceStore(config.DefaultDir())
		if err := ws.Remove(args[0]); err != nil {
			return err
		}
		fmt.Printf("Workspace %q removed.\n", args[0])
		return nil
	},
}

func init() {
	workspaceCmd.AddCommand(workspaceAddCmd)
	workspaceCmd.AddCommand(workspaceListCmd)
	workspaceCmd.AddCommand(workspaceShowCmd)
	workspaceCmd.AddCommand(workspaceRmCmd)
	rootCmd.AddCommand(workspaceCmd)
}
```

**Step 2: Verify compilation**

Run: `nix develop --command go build ./...`
Expected: compiles

**Step 3: Manual smoke test**

```bash
go run . workspace add test-ws /tmp /home
go run . workspace list
go run . workspace show test-ws
go run . workspace rm test-ws
```

**Step 4: Commit**

```bash
git add cmd/workspace.go
git commit -m "feat: add workspace CLI subcommand (add/list/show/rm)"
```

---

### Task 5: Session Struct Updates

Add profile name, extra workspaces, and sandbox overrides to the session for resume/reattach.

**Files:**
- Modify: `internal/config/config.go:25-35`

**Step 1: Update Session struct**

Add new fields to `Session` in `internal/config/config.go`:

```go
type Session struct {
	Name            string    `json:"name"`
	Branch          string    `json:"branch"`
	WorktreePath    string    `json:"worktree_path"`
	RepoPath        string    `json:"repo_path"`
	ContainerName   string    `json:"container_name"`
	Yolo            bool      `json:"yolo"`
	AutoRemove      bool      `json:"auto_remove,omitempty"`
	ResumeID        string    `json:"resume_id,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	Profile         string    `json:"profile,omitempty"`
	ExtraWorkspaces []string  `json:"extra_workspaces,omitempty"`
	AllowDomains    []string  `json:"allow_domains,omitempty"`
	DenyPaths       []string  `json:"deny_paths,omitempty"`
}
```

**Step 2: Run existing tests to verify nothing breaks**

Run: `nix develop --command go test ./internal/config/ -v`
Expected: ALL PASS (new fields are `omitempty`, backward compatible)

**Step 3: Commit**

```bash
git add internal/config/config.go
git commit -m "feat: add profile and workspace fields to Session struct"
```

---

### Task 6: CLI Flags on run/work/new

Add `-w`, `-W`, `--profile`, `--allow-domain`, `--deny-path` flags to `run`, `work`, and `new` commands.

**Files:**
- Modify: `cmd/run.go`
- Modify: `cmd/work.go`
- Modify: `cmd/new.go:31-42` (createOpts struct)

**Step 1: Update createOpts and add flags**

In `cmd/new.go`, add to `createOpts`:

```go
type createOpts struct {
	name           string
	worktree       string
	from           string
	noWorktree     bool
	yolo           bool
	prompt         string
	cont           bool
	background     bool
	autoRemove     bool
	mounts         []string // -w flag: ad-hoc folder paths
	workspace      string   // -W flag: named workspace
	profile        string   // --profile flag
	allowDomains   []string // --allow-domain flag
	denyPaths      []string // --deny-path flag
}
```

In `cmd/run.go`, add flag variables and register them:

```go
var (
	runYolo         bool
	runPrompt       string
	runName         string
	runBackground   bool
	runAutoRemove   bool
	runMounts       []string
	runWorkspace    string
	runProfile      string
	runAllowDomains []string
	runDenyPaths    []string
)
```

In `init()`, add:

```go
	runCmd.Flags().StringArrayVarP(&runMounts, "mount", "w", nil, "Additional folders to mount (repeatable)")
	runCmd.Flags().StringVarP(&runWorkspace, "workspace", "W", "", "Named workspace from workspaces.json")
	runCmd.Flags().StringVar(&runProfile, "profile", "", "Sandbox profile: low, med, high (default \"med\")")
	runCmd.Flags().StringArrayVar(&runAllowDomains, "allow-domain", nil, "Add domain to sandbox allowlist")
	runCmd.Flags().StringArrayVar(&runDenyPaths, "deny-path", nil, "Add path to sandbox deny list")
```

Pass them to `createOpts` in `RunE`.

Do the same for `cmd/work.go`. For `cmd/new.go` add the same flag vars and registration.

**Step 2: Verify compilation**

Run: `nix develop --command go build ./...`
Expected: compiles

**Step 3: Verify help output shows new flags**

Run: `go run . run --help`
Expected: shows `-w`, `-W`, `--profile`, `--allow-domain`, `--deny-path`

**Step 4: Commit**

```bash
git add cmd/run.go cmd/work.go cmd/new.go
git commit -m "feat: add workspace and profile flags to run/work/new commands"
```

---

### Task 7: Wire Workspaces and Profiles into createSession

Integrate workspace resolution, profile generation, and extra mounts into the `createSession()` function.

**Files:**
- Modify: `cmd/new.go:105-234` (createSession function)

**Step 1: Add imports and helper**

Add a helper function to resolve and validate workspace paths:

```go
// resolveWorkspaces merges named workspace paths with ad-hoc mount paths,
// validates all paths exist, and checks for basename collisions.
func resolveWorkspaces(workspace string, mounts []string) ([]string, error) {
	var paths []string

	// Resolve named workspace.
	if workspace != "" {
		ws := config.NewWorkspaceStore(config.DefaultDir())
		wsPaths, err := ws.Get(workspace)
		if err != nil {
			return nil, err
		}
		paths = append(paths, wsPaths...)
	}

	// Add ad-hoc mounts.
	for _, m := range mounts {
		abs, err := filepath.Abs(m)
		if err != nil {
			return nil, fmt.Errorf("resolve path %q: %w", m, err)
		}
		paths = append(paths, abs)
	}

	if len(paths) == 0 {
		return nil, nil
	}

	// Validate paths exist and check for basename collisions.
	seen := make(map[string]string) // basename -> full path
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			return nil, fmt.Errorf("workspace path %q does not exist", p)
		}
		base := filepath.Base(p)
		if existing, ok := seen[base]; ok {
			return nil, fmt.Errorf("basename collision: %q and %q both have basename %q", existing, p, base)
		}
		seen[base] = p
	}

	return paths, nil
}
```

**Step 2: Update createSession**

Key changes to `createSession()`:

1. At the top, resolve workspaces:
```go
	extraWorkspaces, err := resolveWorkspaces(opts.workspace, opts.mounts)
	if err != nil {
		return err
	}
```

2. Validate `--yolo` vs `--profile` conflict:
```go
	profile := opts.profile
	if opts.yolo && profile != "" && profile != "low" {
		return fmt.Errorf("--yolo and --profile=%s conflict; --yolo is equivalent to --profile=low", profile)
	}
	if opts.yolo {
		profile = "low"
	}
	if profile == "" {
		profile = "med"
	}
```

3. When `extraWorkspaces` is set, don't use cwd as workspace:
```go
	workspace := cwd
	if len(extraWorkspaces) > 0 {
		workspace = "" // no primary mount, all via ExtraWorkspaces
	}
```

4. For worktree mode with extra workspaces, create worktrees for each:
```go
	if opts.worktree != "" && !opts.noWorktree && len(extraWorkspaces) > 0 {
		// Multi-repo worktree mode.
		resolvedWorktrees := make([]string, 0, len(extraWorkspaces))
		for _, ws := range extraWorkspaces {
			base := filepath.Base(ws)
			wtDir := filepath.Join(store.WorktreeDir(), name, base)
			branch := name + "/" + base
			repoRoot, err := gitpkg.RepoRoot(ws)
			if err != nil {
				return fmt.Errorf("path %q is not a git repository (required for worktree mode)", ws)
			}
			if opts.from != "" {
				if err := gitpkg.CreateWorktreeFromBranch(repoRoot, wtDir, branch, opts.from); err != nil {
					return fmt.Errorf("create worktree for %s: %w", base, err)
				}
			} else {
				if err := gitpkg.CreateWorktree(repoRoot, wtDir, branch); err != nil {
					return fmt.Errorf("create worktree for %s: %w", base, err)
				}
			}
			resolvedWorktrees = append(resolvedWorktrees, wtDir)
		}
		extraWorkspaces = resolvedWorktrees
		workspace = "" // all via extra workspaces
	}
```

5. Generate and write managed settings from profile:
```go
	prof, err := sandboxPkg.GetProfile(profile)
	if err != nil {
		return err
	}
	settingsJSON, err := json.MarshalIndent(
		prof.ManagedSettings(opts.allowDomains, opts.denyPaths), "", "  ")
	if err != nil {
		return err
	}
	settingsPath := filepath.Join(claudeConfigDir, "managed-settings.json")
	if err := os.WriteFile(settingsPath, settingsJSON, 0o644); err != nil {
		return fmt.Errorf("write managed settings: %w", err)
	}
```

6. Set `Yolo` from profile (low = yolo):
```go
	runOpts := docker.RunOpts{
		Name:            name,
		Workspace:       workspace,
		ConfigDir:       claudeConfigDir,
		...
		Yolo:            profile == "low",
		ExtraWorkspaces: extraWorkspaces,
	}
```

7. Save extra fields to session:
```go
	sess := &config.Session{
		...
		Profile:         profile,
		ExtraWorkspaces: extraWorkspaces,
		AllowDomains:    opts.allowDomains,
		DenyPaths:       opts.denyPaths,
	}
```

**Step 3: Add imports**

Add to `cmd/new.go` imports:
```go
	"encoding/json"
	sandboxPkg "github.com/joegoldin/claude-container/internal/sandbox"
```

**Step 4: Verify compilation and tests pass**

Run: `nix develop --command go build ./... && nix develop --command go test ./...`
Expected: compiles and ALL PASS

**Step 5: Commit**

```bash
git add cmd/new.go
git commit -m "feat: integrate workspaces and sandbox profiles into session creation"
```

---

### Task 8: Update attach.go for Reattach

Use stored profile and extra workspaces when recreating a container.

**Files:**
- Modify: `cmd/attach.go:76-99` (ensureRunning recreate block)

**Step 1: Update ensureRunning to pass stored fields**

In the `default:` case of `ensureRunning()`, add `ExtraWorkspaces`:

```go
	default:
		// ... existing recreate logic ...
		detachedArgs := docker.RunArgs(docker.RunOpts{
			Name:            name,
			Workspace:       sess.WorktreePath,
			ConfigDir:       store.ClaudeConfigDir(),
			HostClaudeDir:   config.HostClaudeDir(),
			HostClaudeJSON:  config.HostClaudeJSON(),
			UID:             os.Getuid(),
			GID:             os.Getgid(),
			Yolo:            sess.Yolo,
			Resume:          sess.ResumeID,
			Continue:        sess.ResumeID == "",
			ExtraWorkspaces: sess.ExtraWorkspaces,
		}, true)
```

Also regenerate managed settings from the stored profile before recreating:

```go
	default:
		// Regenerate managed settings from stored profile.
		profile := sess.Profile
		if profile == "" {
			profile = "med"
		}
		prof, err := sandboxPkg.GetProfile(profile)
		if err == nil {
			settingsJSON, _ := json.MarshalIndent(
				prof.ManagedSettings(sess.AllowDomains, sess.DenyPaths), "", "  ")
			configDir := store.ClaudeConfigDir()
			os.WriteFile(filepath.Join(configDir, "managed-settings.json"), settingsJSON, 0o644)
		}
		// ... rest of recreate logic
```

**Step 2: Add imports**

Add to `cmd/attach.go`:
```go
	"encoding/json"
	"path/filepath"
	sandboxPkg "github.com/joegoldin/claude-container/internal/sandbox"
```

**Step 3: Verify compilation**

Run: `nix develop --command go build ./...`
Expected: compiles

**Step 4: Commit**

```bash
git add cmd/attach.go
git commit -m "feat: restore workspaces and profile on session reattach"
```

---

### Task 9: Update README and Verify

Update documentation and run full verification.

**Files:**
- Modify: `README.md`

**Step 1: Update README sections**

Add to SYNOPSIS:
```
claude-container workspace add <name> <path>...  # define workspace
claude-container workspace list                   # list workspaces
claude-container workspace show <name>            # show workspace paths
claude-container workspace rm <name>              # remove workspace
```

Add new section after COMMANDS:

```markdown
### workspace

Manage named workspace definitions (collections of folder paths).

```
Usage:
  claude-container workspace [command]

Available Commands:
  add         Create or append paths to a named workspace
  list        List all workspace names
  show        Show paths in a workspace
  rm          Remove a workspace definition
```

Update `run` and `work` flag sections to include new flags.

Add SANDBOX PROFILES section:
```markdown
## SANDBOX PROFILES

Three built-in profiles control sandbox security:

    low        sandbox off, unrestricted network, full filesystem
    med        sandbox on, allowlisted domains, deny sensitive paths (default)
    high       sandbox on, Anthropic API only, /workspace only

Use `--profile` to select, `--allow-domain` and `--deny-path` to customize:

    claude-container run --profile=high --allow-domain=github.com
    claude-container work -w ~/code/a --profile=low
```

**Step 2: Run full test suite**

Run: `nix develop --command go test ./...`
Expected: ALL PASS

**Step 3: Run build**

Run: `nix develop --command go build ./...`
Expected: compiles

**Step 4: Commit**

```bash
git add README.md
git commit -m "docs: add workspaces and sandbox profiles to README"
```
