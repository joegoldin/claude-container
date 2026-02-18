package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// run executes a command in the given directory and fails the test on error.
func run(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v failed: %v\n%s", name, args, err, out)
	}
}

// setupTestRepo creates a temporary git repo with one commit and returns
// its path.
func setupTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	run(t, dir, "git", "init")
	run(t, dir, "git", "config", "user.email", "test@test.com")
	run(t, dir, "git", "config", "user.name", "Test User")

	// Create a file, add, and commit.
	err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", "initial commit")

	return dir
}

func TestRepoRoot(t *testing.T) {
	repo := setupTestRepo(t)

	// Create a subdirectory.
	sub := filepath.Join(repo, "subdir", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := RepoRoot(sub)
	if err != nil {
		t.Fatalf("RepoRoot(%q) error: %v", sub, err)
	}

	// Normalize both paths through EvalSymlinks to handle /tmp symlinks.
	wantReal, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	gotReal, err := filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatal(err)
	}
	if gotReal != wantReal {
		t.Errorf("RepoRoot = %q, want %q", gotReal, wantReal)
	}
}

func TestCreateWorktree(t *testing.T) {
	repo := setupTestRepo(t)

	worktreeDir := filepath.Join(t.TempDir(), "wt-feature")
	err := CreateWorktree(repo, worktreeDir, "feature-branch")
	if err != nil {
		t.Fatalf("CreateWorktree error: %v", err)
	}

	// Verify the worktree directory exists.
	if _, err := os.Stat(worktreeDir); os.IsNotExist(err) {
		t.Fatalf("worktree directory %q does not exist", worktreeDir)
	}

	// Verify the correct branch is checked out.
	branch, err := CurrentBranch(worktreeDir)
	if err != nil {
		t.Fatalf("CurrentBranch error: %v", err)
	}
	if branch != "feature-branch" {
		t.Errorf("CurrentBranch = %q, want %q", branch, "feature-branch")
	}
}

func TestRemoveWorktree(t *testing.T) {
	repo := setupTestRepo(t)

	worktreeDir := filepath.Join(t.TempDir(), "wt-remove")
	err := CreateWorktree(repo, worktreeDir, "remove-branch")
	if err != nil {
		t.Fatalf("CreateWorktree error: %v", err)
	}

	// Verify worktree exists before removal.
	if _, err := os.Stat(worktreeDir); os.IsNotExist(err) {
		t.Fatal("worktree should exist before removal")
	}

	err = RemoveWorktree(repo, worktreeDir, "remove-branch")
	if err != nil {
		t.Fatalf("RemoveWorktree error: %v", err)
	}

	// Verify worktree directory is gone.
	if _, err := os.Stat(worktreeDir); !os.IsNotExist(err) {
		t.Errorf("worktree directory %q should not exist after removal", worktreeDir)
	}
}

func TestCreateWorktreeFromBranch(t *testing.T) {
	repo := setupTestRepo(t)

	// Create a base branch with extra content.
	run(t, repo, "git", "branch", "base-branch")

	worktreeDir := filepath.Join(t.TempDir(), "wt-from-base")
	err := CreateWorktreeFromBranch(repo, worktreeDir, "derived-branch", "base-branch")
	if err != nil {
		t.Fatalf("CreateWorktreeFromBranch error: %v", err)
	}

	// Verify the worktree directory exists.
	if _, err := os.Stat(worktreeDir); os.IsNotExist(err) {
		t.Fatalf("worktree directory %q does not exist", worktreeDir)
	}

	// Verify the new branch name.
	branch, err := CurrentBranch(worktreeDir)
	if err != nil {
		t.Fatalf("CurrentBranch error: %v", err)
	}
	if branch != "derived-branch" {
		t.Errorf("CurrentBranch = %q, want %q", branch, "derived-branch")
	}
}

func TestListBranches(t *testing.T) {
	repo := setupTestRepo(t)

	// Create additional branches.
	run(t, repo, "git", "branch", "branch-a")
	run(t, repo, "git", "branch", "branch-b")

	branches, err := ListBranches(repo)
	if err != nil {
		t.Fatalf("ListBranches error: %v", err)
	}

	// We should have at least 3 branches: the default branch + branch-a + branch-b.
	if len(branches) < 3 {
		t.Errorf("ListBranches returned %d branches, want at least 3: %v", len(branches), branches)
	}

	// Verify our created branches are present.
	found := map[string]bool{"branch-a": false, "branch-b": false}
	for _, b := range branches {
		if _, ok := found[b]; ok {
			found[b] = true
		}
	}
	for name, ok := range found {
		if !ok {
			t.Errorf("ListBranches missing branch %q in %v", name, branches)
		}
	}
}

func TestDiff(t *testing.T) {
	repo := setupTestRepo(t)

	worktreeDir := filepath.Join(t.TempDir(), "wt-diff")
	err := CreateWorktree(repo, worktreeDir, "diff-branch")
	if err != nil {
		t.Fatalf("CreateWorktree error: %v", err)
	}

	// Write a new file in the worktree.
	err = os.WriteFile(filepath.Join(worktreeDir, "new-file.txt"), []byte("new content\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	// Check status first (untracked files won't show in diff).
	status, err := Status(worktreeDir)
	if err != nil {
		t.Fatalf("Status error: %v", err)
	}
	if status == "" {
		t.Error("Status should be non-empty after adding a new file")
	}
	if !strings.Contains(status, "new-file.txt") {
		t.Errorf("Status should mention new-file.txt, got %q", status)
	}

	// Stage the file and check diff.
	run(t, worktreeDir, "git", "add", "new-file.txt")
	diff, err := Diff(worktreeDir)
	if err != nil {
		t.Fatalf("Diff error: %v", err)
	}
	if diff == "" {
		t.Error("Diff should be non-empty after staging a new file")
	}
}

func TestCheckoutWorktree(t *testing.T) {
	repo := setupTestRepo(t)

	// Create a branch to checkout.
	run(t, repo, "git", "branch", "existing-branch")

	worktreeDir := filepath.Join(t.TempDir(), "wt-checkout")
	err := CheckoutWorktree(repo, worktreeDir, "existing-branch")
	if err != nil {
		t.Fatalf("CheckoutWorktree error: %v", err)
	}

	// Verify the worktree directory exists.
	if _, err := os.Stat(worktreeDir); os.IsNotExist(err) {
		t.Fatalf("worktree directory %q does not exist", worktreeDir)
	}

	// Verify the correct branch is checked out.
	branch, err := CurrentBranch(worktreeDir)
	if err != nil {
		t.Fatalf("CurrentBranch error: %v", err)
	}
	if branch != "existing-branch" {
		t.Errorf("CurrentBranch = %q, want %q", branch, "existing-branch")
	}
}

func TestHeadCommit(t *testing.T) {
	repo := setupTestRepo(t)

	sha, err := HeadCommit(repo)
	if err != nil {
		t.Fatalf("HeadCommit error: %v", err)
	}

	// SHA should be 40 hex characters.
	if len(sha) != 40 {
		t.Errorf("HeadCommit returned %q (len %d), want 40 hex chars", sha, len(sha))
	}
	for _, c := range sha {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("HeadCommit returned %q, contains non-hex char %c", sha, c)
			break
		}
	}
}

func TestCurrentBranch(t *testing.T) {
	repo := setupTestRepo(t)

	branch, err := CurrentBranch(repo)
	if err != nil {
		t.Fatalf("CurrentBranch error: %v", err)
	}

	// Default branch is usually "main" or "master" depending on git config.
	if branch != "main" && branch != "master" {
		t.Errorf("CurrentBranch = %q, want %q or %q", branch, "main", "master")
	}
}

func TestCreateWorktreeDuplicateBranch(t *testing.T) {
	repo := setupTestRepo(t)

	// Create first worktree with a branch.
	wt1 := filepath.Join(t.TempDir(), "wt-dup-1")
	err := CreateWorktree(repo, wt1, "dup-branch")
	if err != nil {
		t.Fatalf("CreateWorktree first: %v", err)
	}

	// Attempting to create a second worktree with the same branch name should fail.
	wt2 := filepath.Join(t.TempDir(), "wt-dup-2")
	err = CreateWorktree(repo, wt2, "dup-branch")
	if err == nil {
		t.Fatal("CreateWorktree with duplicate branch: expected error, got nil")
	}
}

func TestWorktreeIsolation(t *testing.T) {
	repo := setupTestRepo(t)

	// Create two worktrees.
	wt1 := filepath.Join(t.TempDir(), "wt-iso-1")
	wt2 := filepath.Join(t.TempDir(), "wt-iso-2")

	if err := CreateWorktree(repo, wt1, "iso-branch-1"); err != nil {
		t.Fatalf("CreateWorktree 1: %v", err)
	}
	if err := CreateWorktree(repo, wt2, "iso-branch-2"); err != nil {
		t.Fatalf("CreateWorktree 2: %v", err)
	}

	// Add a file in worktree 1.
	err := os.WriteFile(filepath.Join(wt1, "only-in-wt1.txt"), []byte("wt1\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	run(t, wt1, "git", "add", "only-in-wt1.txt")
	run(t, wt1, "git", "commit", "-m", "add file in wt1")

	// Verify file does NOT exist in worktree 2.
	if _, err := os.Stat(filepath.Join(wt2, "only-in-wt1.txt")); !os.IsNotExist(err) {
		t.Error("file added in wt1 should not appear in wt2 (worktrees should be isolated)")
	}

	// Verify file does NOT exist in the main repo.
	if _, err := os.Stat(filepath.Join(repo, "only-in-wt1.txt")); !os.IsNotExist(err) {
		t.Error("file added in wt1 should not appear in main repo")
	}
}

func TestRemoveWorktreeCleansBranch(t *testing.T) {
	repo := setupTestRepo(t)

	worktreeDir := filepath.Join(t.TempDir(), "wt-clean-branch")
	branchName := "clean-branch"
	err := CreateWorktree(repo, worktreeDir, branchName)
	if err != nil {
		t.Fatalf("CreateWorktree error: %v", err)
	}

	// Branch should exist before removal.
	branches, err := ListBranches(repo)
	if err != nil {
		t.Fatalf("ListBranches: %v", err)
	}
	found := false
	for _, b := range branches {
		if b == branchName {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("branch %q should exist before removal", branchName)
	}

	// Remove the worktree.
	err = RemoveWorktree(repo, worktreeDir, branchName)
	if err != nil {
		t.Fatalf("RemoveWorktree error: %v", err)
	}

	// Branch should be deleted after removal.
	branches, err = ListBranches(repo)
	if err != nil {
		t.Fatalf("ListBranches after removal: %v", err)
	}
	for _, b := range branches {
		if b == branchName {
			t.Errorf("branch %q should not exist after RemoveWorktree", branchName)
		}
	}
}

func TestIsBranchCheckedOut(t *testing.T) {
	repo := setupTestRepo(t)

	// The default branch (master or main) should be checked out.
	defaultBranch, err := CurrentBranch(repo)
	if err != nil {
		t.Fatalf("CurrentBranch error: %v", err)
	}

	if !IsBranchCheckedOut(repo, defaultBranch) {
		t.Errorf("IsBranchCheckedOut(%q) = false, want true", defaultBranch)
	}

	// A non-existent branch should not be checked out.
	if IsBranchCheckedOut(repo, "nonexistent-branch") {
		t.Error("IsBranchCheckedOut(nonexistent-branch) = true, want false")
	}
}
