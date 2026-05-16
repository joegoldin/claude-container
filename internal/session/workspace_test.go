package session

import (
	"testing"
)

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
