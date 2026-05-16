package session

import (
	"os"
	"os/exec"
	"path/filepath"
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

func TestResolveWorkspace_Git_ACPMode_ForcesPwd(t *testing.T) {
	repo := setupGitRepo(t)
	ws, err := ResolveWorkspace(repo, Opts{Mode: ModeACP, WorktreeMode: WorktreeAuto, Name: "x"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if ws.HostPath != repo {
		t.Errorf("HostPath: want %q, got %q", repo, ws.HostPath)
	}
	if ws.RepoPath != repo {
		t.Errorf("RepoPath should be set for git repo, got %q", ws.RepoPath)
	}
	if ws.Worktree {
		t.Error("Worktree should be false for ACP mode")
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
