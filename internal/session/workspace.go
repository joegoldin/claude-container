package session

import (
	"fmt"

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

	// 2. Git repo + worktree mode (auto or always). Implemented in Task 7.
	return Workspace{}, fmt.Errorf("worktree resolution not implemented yet")
}
