package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func setupRepo(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "config", "user.email", "t@t")
	runGit(t, dir, "config", "user.name", "t")
}

func TestEnsureIgnored_AlreadyIgnored(t *testing.T) {
	dir := t.TempDir()
	setupRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".worktrees/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	added, err := EnsureIgnored(dir, ".worktrees")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if added {
		t.Fatal("expected added=false (already ignored)")
	}
}

func TestEnsureIgnored_NotIgnored_AppendsAndReturnsTrue(t *testing.T) {
	dir := t.TempDir()
	setupRepo(t, dir)
	added, err := EnsureIgnored(dir, ".worktrees")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !added {
		t.Fatal("expected added=true (newly ignored)")
	}
	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if !strings.Contains(string(data), ".worktrees/") {
		t.Fatalf(".gitignore missing entry, got: %q", string(data))
	}
}

func TestEnsureIgnored_GitignoreExistsButMissingEntry(t *testing.T) {
	dir := t.TempDir()
	setupRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("node_modules/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	added, err := EnsureIgnored(dir, ".worktrees")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !added {
		t.Fatal("expected added=true")
	}
	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "node_modules/") || !strings.Contains(got, ".worktrees/") {
		t.Fatalf("expected both entries; got %q", got)
	}
}
