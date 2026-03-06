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

// Workspace holds configuration for a named workspace.
type Workspace struct {
	Paths      []string `json:"paths"`
	AllowPerms []string `json:"allow_perms,omitempty"`
	DenyPerms  []string `json:"deny_perms,omitempty"`
	Packages   []string `json:"packages,omitempty"`
	Profile    string   `json:"profile,omitempty"`
}

type WorkspaceStore struct {
	mu  sync.Mutex
	dir string
}

func NewWorkspaceStore(dir string) *WorkspaceStore {
	return &WorkspaceStore{dir: dir}
}

func (ws *WorkspaceStore) Add(name string, paths []string) error {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	all, err := ws.loadLocked()
	if err != nil {
		return err
	}

	existing := all[name]
	seen := make(map[string]bool, len(existing.Paths))
	for _, p := range existing.Paths {
		seen[p] = true
	}
	for _, p := range paths {
		if !seen[p] {
			existing.Paths = append(existing.Paths, p)
			seen[p] = true
		}
	}
	all[name] = existing
	return ws.writeLocked(all)
}

// Save writes a complete workspace config, replacing any existing entry.
func (ws *WorkspaceStore) Save(name string, w Workspace) error {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	all, err := ws.loadLocked()
	if err != nil {
		return err
	}
	all[name] = w
	return ws.writeLocked(all)
}

// GetWorkspace returns the full Workspace config for a named workspace.
func (ws *WorkspaceStore) GetWorkspace(name string) (Workspace, error) {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	all, err := ws.loadLocked()
	if err != nil {
		return Workspace{}, err
	}
	w, ok := all[name]
	if !ok {
		return Workspace{}, fmt.Errorf("workspace %q not found", name)
	}
	return w, nil
}

func (ws *WorkspaceStore) Get(name string) ([]string, error) {
	w, err := ws.GetWorkspace(name)
	if err != nil {
		return nil, err
	}
	return w.Paths, nil
}

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

// ListAll returns all workspace paths (for backward compat with TUI).
func (ws *WorkspaceStore) ListAll() map[string][]string {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	all, err := ws.loadLocked()
	if err != nil {
		return nil
	}
	result := make(map[string][]string, len(all))
	for name, w := range all {
		result[name] = w.Paths
	}
	return result
}

// ListAllWorkspaces returns the full Workspace config for every workspace.
func (ws *WorkspaceStore) ListAllWorkspaces() map[string]Workspace {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	all, err := ws.loadLocked()
	if err != nil {
		return nil
	}
	return all
}

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

// loadLocked reads the workspaces file, handling both the new format
// (map[string]Workspace) and the legacy format (map[string][]string).
func (ws *WorkspaceStore) loadLocked() (map[string]Workspace, error) {
	path := filepath.Join(ws.dir, WorkspaceFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]Workspace), nil
		}
		return nil, err
	}

	// Try new format first.
	var m map[string]Workspace
	if err := json.Unmarshal(data, &m); err == nil {
		// Validate: if any entry has a non-empty Paths field, it's the new format.
		for _, w := range m {
			if len(w.Paths) > 0 || w.Profile != "" || len(w.AllowPerms) > 0 {
				return m, nil
			}
		}
	}

	// Try legacy format: map[string][]string.
	var legacy map[string][]string
	if err := json.Unmarshal(data, &legacy); err == nil {
		result := make(map[string]Workspace, len(legacy))
		for name, paths := range legacy {
			result[name] = Workspace{Paths: paths}
		}
		return result, nil
	}

	// If neither worked, return the original new-format error.
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return m, nil
}

func (ws *WorkspaceStore) writeLocked(m map[string]Workspace) error {
	if err := os.MkdirAll(ws.dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(ws.dir, WorkspaceFile), data, 0o644)
}
