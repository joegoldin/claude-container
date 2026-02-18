package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSessionRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	now := time.Now().Truncate(time.Second)
	sess := &Session{
		Name:          "test-session",
		Branch:        "feature-auth",
		WorktreePath:  "/tmp/worktrees/feature-auth",
		RepoPath:      "/home/user/project",
		ContainerName: Prefix + "test-session",
		TmuxSession:   Prefix + "test-session",
		Yolo:          true,
		CreatedAt:     now,
	}

	if err := store.Save(sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.Get("test-session")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got.Name != sess.Name {
		t.Errorf("Name = %q, want %q", got.Name, sess.Name)
	}
	if got.Branch != sess.Branch {
		t.Errorf("Branch = %q, want %q", got.Branch, sess.Branch)
	}
	if got.WorktreePath != sess.WorktreePath {
		t.Errorf("WorktreePath = %q, want %q", got.WorktreePath, sess.WorktreePath)
	}
	if got.RepoPath != sess.RepoPath {
		t.Errorf("RepoPath = %q, want %q", got.RepoPath, sess.RepoPath)
	}
	if got.ContainerName != sess.ContainerName {
		t.Errorf("ContainerName = %q, want %q", got.ContainerName, sess.ContainerName)
	}
	if got.TmuxSession != sess.TmuxSession {
		t.Errorf("TmuxSession = %q, want %q", got.TmuxSession, sess.TmuxSession)
	}
	if got.Yolo != sess.Yolo {
		t.Errorf("Yolo = %v, want %v", got.Yolo, sess.Yolo)
	}
	if !got.CreatedAt.Equal(sess.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, sess.CreatedAt)
	}
}

func TestListSessions(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Empty store returns empty list.
	if got := store.List(); len(got) != 0 {
		t.Fatalf("List on empty store: got %d, want 0", len(got))
	}

	now := time.Now().Truncate(time.Second)

	sess1 := &Session{
		Name:      "alpha",
		CreatedAt: now,
	}
	sess2 := &Session{
		Name:      "beta",
		CreatedAt: now.Add(time.Second),
	}

	if err := store.Save(sess1); err != nil {
		t.Fatalf("Save sess1: %v", err)
	}
	if err := store.Save(sess2); err != nil {
		t.Fatalf("Save sess2: %v", err)
	}

	list := store.List()
	if len(list) != 2 {
		t.Fatalf("List: got %d sessions, want 2", len(list))
	}

	// Should be sorted by CreatedAt (earliest first).
	if list[0].Name != "alpha" {
		t.Errorf("list[0].Name = %q, want %q", list[0].Name, "alpha")
	}
	if list[1].Name != "beta" {
		t.Errorf("list[1].Name = %q, want %q", list[1].Name, "beta")
	}
}

func TestDeleteSession(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	sess := &Session{
		Name:      "to-delete",
		CreatedAt: time.Now().Truncate(time.Second),
	}

	if err := store.Save(sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := store.Get("to-delete"); err != nil {
		t.Fatalf("Get before delete: %v", err)
	}

	if err := store.Delete("to-delete"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := store.Get("to-delete"); err == nil {
		t.Fatal("Get after delete: expected error, got nil")
	}

	if list := store.List(); len(list) != 0 {
		t.Fatalf("List after delete: got %d, want 0", len(list))
	}
}

func TestStoreCreatesDir(t *testing.T) {
	base := t.TempDir()
	nested := filepath.Join(base, "a", "b", "c")
	store := NewStore(nested)

	sess := &Session{
		Name:      "nested",
		CreatedAt: time.Now().Truncate(time.Second),
	}

	if err := store.Save(sess); err != nil {
		t.Fatalf("Save with nested dir: %v", err)
	}

	// Verify the file was actually written.
	path := filepath.Join(nested, SessionFile)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("sessions.json not created: %v", err)
	}
}

func TestGetNonExistent(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	_, err := store.Get("does-not-exist")
	if err == nil {
		t.Fatal("Get on non-existent session: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got %q", err.Error())
	}
}

func TestSaveOverwrite(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	now := time.Now().Truncate(time.Second)

	// Save the first version.
	sess1 := &Session{
		Name:      "overwrite-me",
		Branch:    "branch-v1",
		CreatedAt: now,
	}
	if err := store.Save(sess1); err != nil {
		t.Fatalf("Save v1: %v", err)
	}

	// Save a second version with the same name but different branch.
	sess2 := &Session{
		Name:      "overwrite-me",
		Branch:    "branch-v2",
		CreatedAt: now.Add(time.Second),
	}
	if err := store.Save(sess2); err != nil {
		t.Fatalf("Save v2: %v", err)
	}

	got, err := store.Get("overwrite-me")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Branch != "branch-v2" {
		t.Errorf("Branch = %q, want %q (latest should win)", got.Branch, "branch-v2")
	}

	// There should be exactly one session, not two.
	if list := store.List(); len(list) != 1 {
		t.Errorf("List: got %d sessions, want 1 after overwrite", len(list))
	}
}

func TestNamesOrder(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	now := time.Now().Truncate(time.Second)
	// Insert in reverse-alphabetical order but with ascending timestamps.
	for i, name := range []string{"charlie", "alpha", "bravo"} {
		sess := &Session{
			Name:      name,
			CreatedAt: now.Add(time.Duration(i) * time.Second),
		}
		if err := store.Save(sess); err != nil {
			t.Fatalf("Save %q: %v", name, err)
		}
	}

	names := store.Names()
	if len(names) != 3 {
		t.Fatalf("Names: got %d, want 3", len(names))
	}
	// Names() returns names from List() which sorts by CreatedAt.
	// Insertion order: charlie(t+0), alpha(t+1), bravo(t+2)
	want := []string{"charlie", "alpha", "bravo"}
	for i, w := range want {
		if names[i] != w {
			t.Errorf("Names[%d] = %q, want %q", i, names[i], w)
		}
	}
}

func TestDefaultDir(t *testing.T) {
	// Set XDG_CONFIG_HOME and verify DefaultDir respects it.
	original := os.Getenv("XDG_CONFIG_HOME")
	defer os.Setenv("XDG_CONFIG_HOME", original)

	os.Setenv("XDG_CONFIG_HOME", "/custom/config")
	got := DefaultDir()
	want := filepath.Join("/custom/config", "claude-container")
	if got != want {
		t.Errorf("DefaultDir() with XDG_CONFIG_HOME = %q, want %q", got, want)
	}

	// When XDG_CONFIG_HOME is empty, fall back to ~/.config.
	os.Setenv("XDG_CONFIG_HOME", "")
	got = DefaultDir()
	home, _ := os.UserHomeDir()
	want = filepath.Join(home, ".config", "claude-container")
	if got != want {
		t.Errorf("DefaultDir() without XDG_CONFIG_HOME = %q, want %q", got, want)
	}
}

func TestWorktreeDir(t *testing.T) {
	store := NewStore("/tmp/test-config")
	got := store.WorktreeDir()
	want := filepath.Join("/tmp/test-config", "worktrees")
	if got != want {
		t.Errorf("WorktreeDir() = %q, want %q", got, want)
	}
}

func TestSanitizeName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"feature/auth", "feature-auth"},
		{"fix payments", "fix-payments"},
		{"simple", "simple"},
		{"a/b/c", "a-b-c"},
		{"hello world/test", "hello-world-test"},
		{"tabs\there", "tabs-here"},
		{"multiple   spaces", "multiple-spaces"},
	}

	for _, tt := range tests {
		got := SanitizeName(tt.input)
		if got != tt.want {
			t.Errorf("SanitizeName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
