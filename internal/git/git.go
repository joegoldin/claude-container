package git

import (
	"fmt"
	"os/exec"
	"strings"
)

// RepoRoot returns the top-level directory of the git repository
// containing dir.
func RepoRoot(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --show-toplevel: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// CurrentBranch returns the name of the currently checked-out branch in
// the given directory.
func CurrentBranch(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --abbrev-ref HEAD: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// HeadCommit returns the full SHA of the HEAD commit in the given directory.
func HeadCommit(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// CreateWorktree creates a new git worktree at worktreeDir with a new
// branch. This is equivalent to: git worktree add -b <branch> <dir>
func CreateWorktree(repoDir, worktreeDir, branch string) error {
	cmd := exec.Command("git", "worktree", "add", "-b", branch, worktreeDir)
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree add -b %s %s: %w\n%s", branch, worktreeDir, err, out)
	}
	return nil
}

// CreateWorktreeFromBranch creates a new git worktree at worktreeDir with
// newBranch, starting from baseBranch. It first tries the local baseBranch,
// then falls back to origin/<baseBranch>.
func CreateWorktreeFromBranch(repoDir, worktreeDir, newBranch, baseBranch string) error {
	// Try local branch first.
	ref := baseBranch
	cmd := exec.Command("git", "rev-parse", "--verify", ref)
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		// Fall back to origin/<baseBranch>.
		ref = "origin/" + baseBranch
		cmd2 := exec.Command("git", "rev-parse", "--verify", ref)
		cmd2.Dir = repoDir
		if err2 := cmd2.Run(); err2 != nil {
			return fmt.Errorf("branch %q not found locally or as origin/%s", baseBranch, baseBranch)
		}
	}

	cmd = exec.Command("git", "worktree", "add", "-b", newBranch, worktreeDir, ref)
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree add -b %s %s %s: %w\n%s", newBranch, worktreeDir, ref, err, out)
	}
	return nil
}

// CheckoutWorktree creates a new git worktree at worktreeDir using an
// existing branch. This is equivalent to: git worktree add <dir> <branch>
func CheckoutWorktree(repoDir, worktreeDir, existingBranch string) error {
	cmd := exec.Command("git", "worktree", "add", worktreeDir, existingBranch)
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree add %s %s: %w\n%s", worktreeDir, existingBranch, err, out)
	}
	return nil
}

// RemoveWorktree removes a git worktree and optionally deletes the branch.
// Errors from individual steps are ignored (best effort cleanup).
func RemoveWorktree(repoDir, worktreeDir, branch string) error {
	// Force-remove the worktree.
	cmd := exec.Command("git", "worktree", "remove", "-f", worktreeDir)
	cmd.Dir = repoDir
	cmd.CombinedOutput() // best effort

	// Prune stale worktree entries.
	cmd = exec.Command("git", "worktree", "prune")
	cmd.Dir = repoDir
	cmd.CombinedOutput() // best effort

	// Delete the branch (best effort).
	if branch != "" {
		cmd = exec.Command("git", "branch", "-D", branch)
		cmd.Dir = repoDir
		cmd.CombinedOutput() // best effort
	}

	return nil
}

// ListBranches returns all local branch names in the repository.
func ListBranches(dir string) ([]string, error) {
	cmd := exec.Command("git", "branch", "--format=%(refname:short)")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git branch: %w", err)
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil, nil
	}
	return strings.Split(raw, "\n"), nil
}

// Diff returns the output of git diff HEAD. If the repo has no commits,
// it falls back to plain git diff.
func Diff(dir string) (string, error) {
	cmd := exec.Command("git", "diff", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		// Fallback: no commits yet.
		cmd = exec.Command("git", "diff")
		cmd.Dir = dir
		out, err = cmd.Output()
		if err != nil {
			return "", fmt.Errorf("git diff: %w", err)
		}
	}
	return strings.TrimSpace(string(out)), nil
}

// Status returns the short-form status output for the given directory.
func Status(dir string) (string, error) {
	cmd := exec.Command("git", "status", "--short")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git status --short: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// IsBranchCheckedOut returns true if branch is the currently checked-out
// branch in repoDir.
func IsBranchCheckedOut(repoDir, branch string) bool {
	current, err := CurrentBranch(repoDir)
	if err != nil {
		return false
	}
	return current == branch
}
