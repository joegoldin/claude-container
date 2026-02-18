package config

import (
	"os"
	"path/filepath"
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
