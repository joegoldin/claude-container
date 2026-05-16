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
	HostPath string // path mounted as /workspace; empty when worktree mode (entrypoint creates from /mnt/repo)
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

	// 2. Git repo + worktree mode (auto or always). Implemented in Task 7.
	return Workspace{}, fmt.Errorf("worktree resolution not implemented yet")
}

// ensureDir is a small helper used by worktree path picking.
func ensureDir(p string) error {
	if _, err := os.Stat(p); err == nil {
		return nil
	}
	return os.MkdirAll(p, 0o755)
}

var _ = filepath.Base // keep import; used by later tasks
