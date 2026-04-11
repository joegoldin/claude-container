package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMigrateToPerRepo(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Create shared workspace dir with 2 fake JSONL files.
	workspaceDir := filepath.Join(dir, "claude-config", "projects", "-workspace")
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceDir, "uuid1.jsonl"), []byte(`{"test":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceDir, "uuid2.jsonl"), []byte(`{"test":2}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a session with ResumeID matching uuid1.
	sess := &Session{
		Name:      "test-session",
		ResumeID:  "uuid1",
		RepoPath:  "/fake/repo",
		CreatedAt: time.Now(),
	}
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}

	// Run migration.
	n, err := MigrateToPerRepo(store)
	if err != nil {
		t.Fatalf("MigrateToPerRepo failed: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 migrated files, got %d", n)
	}

	// Verify uuid1.jsonl moved to per-repo dir for /fake/repo.
	repoDir := store.RepoConfigDir("/fake/repo")
	uuid1Dest := filepath.Join(repoDir, "projects", "-workspace", "uuid1.jsonl")
	if _, err := os.Stat(uuid1Dest); err != nil {
		t.Errorf("uuid1.jsonl not found at %s: %v", uuid1Dest, err)
	}

	// Verify uuid2.jsonl moved to orphaned dir.
	orphanDir := store.RepoConfigDir("_orphaned")
	uuid2Dest := filepath.Join(orphanDir, "projects", "-workspace", "uuid2.jsonl")
	if _, err := os.Stat(uuid2Dest); err != nil {
		t.Errorf("uuid2.jsonl not found at %s: %v", uuid2Dest, err)
	}

	// Verify old shared workspace dir is removed.
	if _, err := os.Stat(workspaceDir); !os.IsNotExist(err) {
		t.Errorf("old workspace dir should be removed, but still exists")
	}

	// Verify idempotency: running again returns (0, nil).
	n2, err2 := MigrateToPerRepo(store)
	if err2 != nil {
		t.Fatalf("second MigrateToPerRepo failed: %v", err2)
	}
	if n2 != 0 {
		t.Fatalf("expected 0 migrated files on second run, got %d", n2)
	}
}
