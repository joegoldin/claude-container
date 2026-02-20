package config

import (
	"os"
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

	path := filepath.Join(dir, WorkspaceFile)
	if _, err := os.Stat(path); err != nil {
		t.Fatal("workspaces.json not created")
	}

	ws2 := NewWorkspaceStore(dir)
	paths, err := ws2.Get("test")
	if err != nil {
		t.Fatalf("Get from new store: %v", err)
	}
	if len(paths) != 1 || paths[0] != "/a" {
		t.Errorf("persistence failed: got %v", paths)
	}
}
