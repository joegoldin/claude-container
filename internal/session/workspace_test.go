package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func setupGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-q", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func TestResolveWorkspace_NonGit_PwdPassthrough(t *testing.T) {
	dir := t.TempDir() // not a git repo
	ws, err := ResolveWorkspace(dir, Opts{Mode: ModeTTY, WorktreeMode: WorktreeAuto, Name: "s1"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if ws.HostPath != dir {
		t.Errorf("HostPath: want %q, got %q", dir, ws.HostPath)
	}
	if ws.RepoPath != "" {
		t.Errorf("RepoPath should be empty for non-git, got %q", ws.RepoPath)
	}
	if ws.Worktree {
		t.Error("Worktree should be false for non-git")
	}
	if ws.Branch != "" {
		t.Error("Branch should be empty for non-git")
	}
}

func TestResolveWorkspace_Git_WorktreeNever_ForcesPwd(t *testing.T) {
	repo := setupGitRepo(t)
	ws, err := ResolveWorkspace(repo, Opts{Mode: ModeTTY, WorktreeMode: WorktreeNever, Name: "x"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if ws.HostPath != repo {
		t.Errorf("HostPath: want %q, got %q", repo, ws.HostPath)
	}
	if ws.RepoPath != repo {
		t.Errorf("RepoPath should be set informationally for git repo, got %q", ws.RepoPath)
	}
	if ws.Worktree {
		t.Error("Worktree should be false for forced pwd")
	}
}

func TestResolveWorkspace_Git_DotWorktreesAlreadyIgnored(t *testing.T) {
	repo := setupGitRepo(t)
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".worktrees/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, err := ResolveWorkspace(repo, Opts{Mode: ModeTTY, WorktreeMode: WorktreeAuto, Name: "myrepo-foo-bar"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if ws.RepoPath != repo {
		t.Errorf("RepoPath: want %q, got %q", repo, ws.RepoPath)
	}
	if !ws.Worktree {
		t.Error("Worktree should be true")
	}
	if ws.Branch != "myrepo-foo-bar" {
		t.Errorf("Branch: want session name, got %q", ws.Branch)
	}
	// HostPath is empty in worktree mode (entrypoint creates from /mnt/repo).
	if ws.HostPath != "" {
		t.Errorf("HostPath should be empty for worktree, got %q", ws.HostPath)
	}
}

func TestResolveWorkspace_Git_CreatesWorktreesDirWhenMissing(t *testing.T) {
	repo := setupGitRepo(t)
	// Pre-ignore .worktrees so ResolveWorkspace doesn't need to mutate
	// .gitignore — this test focuses on the directory creation path.
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".worktrees/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Confirm .worktrees does not exist yet.
	if _, err := os.Stat(filepath.Join(repo, ".worktrees")); err == nil {
		t.Fatal("precondition: .worktrees should not exist yet")
	}

	ws, err := ResolveWorkspace(repo, Opts{Mode: ModeTTY, WorktreeMode: WorktreeAuto, Name: "mkdir-test"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !ws.Worktree {
		t.Error("Worktree should be true")
	}
	// The directory should now exist on disk.
	info, err := os.Stat(filepath.Join(repo, ".worktrees"))
	if err != nil {
		t.Fatalf(".worktrees was not created: %v", err)
	}
	if !info.IsDir() {
		t.Error(".worktrees was created but is not a directory")
	}
}

func TestResolveWorkspace_Git_AppendsGitignore(t *testing.T) {
	repo := setupGitRepo(t)
	// .gitignore exists but does NOT include .worktrees.
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("node_modules/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, err := ResolveWorkspace(repo, Opts{Mode: ModeTTY, WorktreeMode: WorktreeAuto, Name: "s1"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !ws.Worktree {
		t.Fatal("expected worktree mode")
	}
	data, err := os.ReadFile(filepath.Join(repo, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if !strings.Contains(string(data), ".worktrees/") {
		t.Fatalf(".gitignore was not updated: %q", string(data))
	}
}

func TestResolveWorkspace_Git_GitignoreReadOnly_FallsBackToGlobal(t *testing.T) {
	repo := setupGitRepo(t)
	gi := filepath.Join(repo, ".gitignore")
	if err := os.WriteFile(gi, []byte("# placeholder\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(gi, 0o644) // so t.TempDir cleanup works

	tmpHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmpHome)

	ws, err := ResolveWorkspace(repo, Opts{Mode: ModeTTY, WorktreeMode: WorktreeAuto, Name: "fallback-foo"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !ws.Worktree {
		t.Fatal("expected Worktree=true")
	}
	want := filepath.Join(tmpHome, "claude-container", "worktrees")
	if !strings.HasPrefix(ws.HostPath, want) {
		t.Fatalf("expected fallback under %q, got %q", want, ws.HostPath)
	}
	if !strings.Contains(ws.HostPath, "fallback-foo") {
		t.Errorf("expected branch component %q in HostPath, got %q", "fallback-foo", ws.HostPath)
	}
	if ws.Branch != "fallback-foo" {
		t.Errorf("Branch: want %q, got %q", "fallback-foo", ws.Branch)
	}
}
