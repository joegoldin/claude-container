package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runCmd executes a command in the given directory and fails the test on error.
func runCmd(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v failed: %v\n%s", name, args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// initTestRepo creates a temporary git repo with one commit and returns
// its path. Separate from setupTestRepo to avoid name collision.
func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	runCmd(t, dir, "git", "init")
	runCmd(t, dir, "git", "config", "user.email", "integration@test.com")
	runCmd(t, dir, "git", "config", "user.name", "Integration Test")

	err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Integration Test\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	runCmd(t, dir, "git", "add", ".")
	runCmd(t, dir, "git", "commit", "-m", "initial commit")

	return dir
}

func TestFullWorktreeLifecycle(t *testing.T) {
	repo := initTestRepo(t)

	// 1. Create a worktree.
	wtDir := filepath.Join(t.TempDir(), "lifecycle-wt")
	branch := "lifecycle-branch"
	err := CreateWorktree(repo, wtDir, branch)
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	// Verify the worktree exists and has the correct branch.
	gotBranch, err := CurrentBranch(wtDir)
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}
	if gotBranch != branch {
		t.Errorf("branch = %q, want %q", gotBranch, branch)
	}

	// 2. Make changes in the worktree.
	err = os.WriteFile(filepath.Join(wtDir, "feature.txt"), []byte("new feature\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	// 3. Verify status shows the change.
	status, err := Status(wtDir)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !strings.Contains(status, "feature.txt") {
		t.Errorf("Status should mention feature.txt, got %q", status)
	}

	// 4. Stage and check diff.
	runCmd(t, wtDir, "git", "add", "feature.txt")
	diff, err := Diff(wtDir)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(diff, "new feature") {
		t.Errorf("Diff should contain 'new feature', got %q", diff)
	}

	// 5. Commit the change.
	runCmd(t, wtDir, "git", "commit", "-m", "add feature")

	// 6. Verify the main repo does not have the feature file.
	if _, err := os.Stat(filepath.Join(repo, "feature.txt")); !os.IsNotExist(err) {
		t.Error("feature.txt should not exist in main repo")
	}

	// 7. Remove the worktree.
	err = RemoveWorktree(repo, wtDir, branch)
	if err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}

	// 8. Verify worktree directory is gone.
	if _, err := os.Stat(wtDir); !os.IsNotExist(err) {
		t.Error("worktree directory should not exist after removal")
	}

	// 9. Verify branch is deleted.
	branches, err := ListBranches(repo)
	if err != nil {
		t.Fatalf("ListBranches: %v", err)
	}
	for _, b := range branches {
		if b == branch {
			t.Errorf("branch %q should be deleted after RemoveWorktree", branch)
		}
	}
}

func TestWorktreeFromBranchWithCommits(t *testing.T) {
	repo := initTestRepo(t)

	// Create a branch with additional commits.
	runCmd(t, repo, "git", "checkout", "-b", "feature-commits")
	err := os.WriteFile(filepath.Join(repo, "feature-a.txt"), []byte("feature A\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	runCmd(t, repo, "git", "add", "feature-a.txt")
	runCmd(t, repo, "git", "commit", "-m", "add feature A")

	err = os.WriteFile(filepath.Join(repo, "feature-b.txt"), []byte("feature B\n"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	runCmd(t, repo, "git", "add", "feature-b.txt")
	runCmd(t, repo, "git", "commit", "-m", "add feature B")

	// Get the commit SHA for verification.
	featureSHA, err := HeadCommit(repo)
	if err != nil {
		t.Fatalf("HeadCommit: %v", err)
	}

	// Switch back to the default branch.
	defaultBranch, _ := CurrentBranch(repo)
	// The current branch is feature-commits; we need to find the original.
	branches, _ := ListBranches(repo)
	origBranch := ""
	for _, b := range branches {
		if b == "main" || b == "master" {
			origBranch = b
			break
		}
	}
	if origBranch == "" {
		// fallback: just use whatever isn't feature-commits
		for _, b := range branches {
			if b != "feature-commits" && b != defaultBranch {
				origBranch = b
				break
			}
		}
		if origBranch == "" {
			origBranch = branches[0]
		}
	}
	runCmd(t, repo, "git", "checkout", origBranch)

	// Create a worktree from the feature-commits branch.
	wtDir := filepath.Join(t.TempDir(), "wt-from-commits")
	err = CreateWorktreeFromBranch(repo, wtDir, "derived-from-feature", "feature-commits")
	if err != nil {
		t.Fatalf("CreateWorktreeFromBranch: %v", err)
	}

	// Verify the worktree has the commits from the feature branch.
	wtSHA, err := HeadCommit(wtDir)
	if err != nil {
		t.Fatalf("HeadCommit on worktree: %v", err)
	}
	if wtSHA != featureSHA {
		t.Errorf("worktree HEAD = %q, want %q (should match feature-commits)", wtSHA, featureSHA)
	}

	// Verify both feature files exist.
	for _, f := range []string{"feature-a.txt", "feature-b.txt"} {
		if _, err := os.Stat(filepath.Join(wtDir, f)); os.IsNotExist(err) {
			t.Errorf("file %q should exist in worktree derived from feature-commits", f)
		}
	}
}

func TestMultipleWorktrees(t *testing.T) {
	repo := initTestRepo(t)

	const count = 3
	type wt struct {
		dir    string
		branch string
	}

	worktrees := make([]wt, count)
	for i := 0; i < count; i++ {
		w := wt{
			dir:    filepath.Join(t.TempDir(), "wt-multi-"+string(rune('a'+i))),
			branch: "multi-branch-" + string(rune('a'+i)),
		}
		err := CreateWorktree(repo, w.dir, w.branch)
		if err != nil {
			t.Fatalf("CreateWorktree %d: %v", i, err)
		}
		worktrees[i] = w
	}

	// Verify each worktree has the correct branch.
	for i, w := range worktrees {
		branch, err := CurrentBranch(w.dir)
		if err != nil {
			t.Fatalf("CurrentBranch wt %d: %v", i, err)
		}
		if branch != w.branch {
			t.Errorf("wt %d branch = %q, want %q", i, branch, w.branch)
		}
	}

	// Add unique files to each worktree.
	for i, w := range worktrees {
		filename := "unique-" + string(rune('a'+i)) + ".txt"
		content := "content for worktree " + string(rune('a'+i)) + "\n"
		err := os.WriteFile(filepath.Join(w.dir, filename), []byte(content), 0o644)
		if err != nil {
			t.Fatal(err)
		}
		runCmd(t, w.dir, "git", "add", filename)
		runCmd(t, w.dir, "git", "commit", "-m", "add "+filename)
	}

	// Verify each worktree only has its own unique file, not the others'.
	for i, w := range worktrees {
		ownFile := "unique-" + string(rune('a'+i)) + ".txt"
		if _, err := os.Stat(filepath.Join(w.dir, ownFile)); os.IsNotExist(err) {
			t.Errorf("wt %d: missing its own file %q", i, ownFile)
		}

		for j := range worktrees {
			if i == j {
				continue
			}
			otherFile := "unique-" + string(rune('a'+j)) + ".txt"
			if _, err := os.Stat(filepath.Join(w.dir, otherFile)); !os.IsNotExist(err) {
				t.Errorf("wt %d: should not have file %q from wt %d", i, otherFile, j)
			}
		}
	}

	// Verify the main repo doesn't have any of the unique files.
	for i := 0; i < count; i++ {
		filename := "unique-" + string(rune('a'+i)) + ".txt"
		if _, err := os.Stat(filepath.Join(repo, filename)); !os.IsNotExist(err) {
			t.Errorf("main repo should not have file %q", filename)
		}
	}
}
