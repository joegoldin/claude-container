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
