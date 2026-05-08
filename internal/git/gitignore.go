package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// EnsureIgnored makes sure path (relative to repoDir, e.g. ".worktrees") is
// ignored by git. Returns added=true if it appended a new line to .gitignore,
// false if the path was already ignored. Does not commit the change.
//
// If .gitignore exists but is read-only, returns the underlying error so
// callers can fall back to an alternate location.
func EnsureIgnored(repoDir, path string) (added bool, err error) {
	entry := strings.TrimRight(path, "/") + "/"

	// Quick check: is it already ignored?
	cmd := exec.Command("git", "check-ignore", "-q", entry)
	cmd.Dir = repoDir
	if cmd.Run() == nil {
		return false, nil
	}

	gitignore := filepath.Join(repoDir, ".gitignore")

	// Read existing content (if any) to make sure we add a leading newline
	// when the file does not end with one.
	prefix := ""
	if data, err := os.ReadFile(gitignore); err == nil {
		if len(data) > 0 && data[len(data)-1] != '\n' {
			prefix = "\n"
		}
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("read .gitignore: %w", err)
	}

	f, err := os.OpenFile(gitignore, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return false, fmt.Errorf("open .gitignore: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(prefix + entry + "\n"); err != nil {
		return false, fmt.Errorf("append .gitignore: %w", err)
	}
	return true, nil
}
