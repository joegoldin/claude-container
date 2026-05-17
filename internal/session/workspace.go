package session

import (
	"fmt"
	"os"
	"path/filepath"

	gitpkg "github.com/joegoldin/claude-container/internal/git"
)

// Workspace is the result of resolving how a session's working directory
// should be wired up into the container.
type Workspace struct {
	HostPath string // host path mounted as /workspace; empty in worktree mode
	RepoPath string // git toplevel; empty if not a git repo
	Worktree bool   // true when entrypoint should create a worktree
	Branch   string // worktree branch name; empty for pwd passthrough
}

// ResolveWorkspace decides whether to create a worktree or pwd-passthrough.
// See spec at docs/plans/2026-05-07-acp-and-transparent-binary-design.md
// (Workspace resolution).
func ResolveWorkspace(cwd string, opts Opts) (Workspace, error) {
	repoRoot, repoErr := gitpkg.RepoRoot(cwd)
	inGit := repoErr == nil && repoRoot != ""

	// 1. Forced pwd passthrough.
	if opts.WorktreeMode == WorktreeNever || opts.Mode == ModeACP {
		ws := Workspace{HostPath: cwd}
		if inGit {
			ws.RepoPath = repoRoot
		}
		return ws, nil
	}

	if !inGit {
		return Workspace{HostPath: cwd}, nil
	}

	// 2. Git repo + worktree mode.
	base, err := ensureWorktreeBase(repoRoot)
	if err != nil {
		return Workspace{}, fmt.Errorf("pick worktree base: %w", err)
	}

	added, ignErr := gitpkg.EnsureIgnored(repoRoot, base)
	if ignErr != nil {
		// .gitignore not writable — fall back to global location (Task 9).
		return Workspace{}, fmt.Errorf("ensure ignored: %w (fallback not implemented)", ignErr)
	}
	if added {
		fmt.Fprintf(os.Stderr, "note: added %s/ to .gitignore — commit when convenient\n", base)
	}

	branch := opts.WorktreeName
	if branch == "" {
		branch = opts.Name
	}
	if branch == "" {
		return Workspace{}, fmt.Errorf("worktree mode requires a session name")
	}

	return Workspace{
		RepoPath: repoRoot,
		Worktree: true,
		Branch:   branch,
	}, nil
}

// ensureWorktreeBase returns the base directory name (relative to repoRoot)
// where new worktrees should live, creating it on disk if it doesn't exist.
// Priority: existing .worktrees, then existing worktrees, then create
// .worktrees as the default.
func ensureWorktreeBase(repoRoot string) (string, error) {
	candidates := []string{".worktrees", "worktrees"}
	for _, c := range candidates {
		if info, err := os.Stat(filepath.Join(repoRoot, c)); err == nil && info.IsDir() {
			return c, nil
		}
	}
	// Create the hidden default.
	if err := os.MkdirAll(filepath.Join(repoRoot, ".worktrees"), 0o755); err != nil {
		return "", err
	}
	return ".worktrees", nil
}
