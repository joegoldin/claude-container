package git

import (
	"errors"
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
	if err := cmd.Run(); err == nil {
		return false, nil
	} else {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			// not ignored — fall through to append
		} else {
			return false, fmt.Errorf("git check-ignore: %w", err)
		}
	}

	gitignore := filepath.Join(repoDir, ".gitignore")

	// Read existing content (if any) to make sure we add a leading newline
	// when the file does not end with one.
	prefix := ""
	if data, readErr := os.ReadFile(gitignore); readErr == nil {
		if len(data) > 0 && data[len(data)-1] != '\n' {
			prefix = "\n"
		}
	} else if !os.IsNotExist(readErr) {
		return false, fmt.Errorf("read .gitignore: %w", readErr)
	}

	f, err := os.OpenFile(gitignore, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return false, fmt.Errorf("open .gitignore: %w", err)
	}
	if _, err := f.WriteString(prefix + entry + "\n"); err != nil {
		f.Close()
		return false, fmt.Errorf("append .gitignore: %w", err)
	}
	if err := f.Close(); err != nil {
		return false, fmt.Errorf("close .gitignore: %w", err)
	}
	return true, nil
}
