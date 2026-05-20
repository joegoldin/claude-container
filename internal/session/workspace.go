package session

import (
	"crypto/sha256"
	"encoding/hex"
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
// See spec at docs/plans/2026-05-07-transparent-binary-design.md
// (Workspace resolution).
func ResolveWorkspace(cwd string, opts Opts) (Workspace, error) {
	repoRoot, repoErr := gitpkg.RepoRoot(cwd)
	inGit := repoErr == nil && repoRoot != ""

	// 1. Forced pwd passthrough.
	if opts.WorktreeMode == WorktreeNever {
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
		// .gitignore not writable — fall back to global location.
		fallback, ferr := globalWorktreeDir(repoRoot, opts)
		if ferr != nil {
			return Workspace{}, fmt.Errorf("ensure ignored: %w; fallback failed: %v", ignErr, ferr)
		}
		fmt.Fprintf(os.Stderr, "note: %s/.gitignore not writable; using fallback %s\n", repoRoot, fallback)
		branch := opts.WorktreeName
		if branch == "" {
			branch = opts.Name
		}
		return Workspace{
			HostPath: fallback,
			RepoPath: repoRoot,
			Worktree: true,
			Branch:   branch,
		}, nil
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

// globalWorktreeDir returns $XDG_DATA_HOME/claude-container/worktrees/<repo-id>/<branch>
// (defaulting XDG_DATA_HOME to ~/.local/share), creating the parent directory if missing.
func globalWorktreeDir(repoRoot string, opts Opts) (string, error) {
	xdg := os.Getenv("XDG_DATA_HOME")
	if xdg == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		xdg = filepath.Join(home, ".local", "share")
	}
	hash := sha256.Sum256([]byte(repoRoot))
	id := hex.EncodeToString(hash[:])[:12]
	branch := opts.WorktreeName
	if branch == "" {
		branch = opts.Name
	}
	dir := filepath.Join(xdg, "claude-container", "worktrees", id, branch)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	return dir, nil
}
