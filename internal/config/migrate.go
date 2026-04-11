package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MigrateToPerRepo moves JSONL conversation files from the old shared
// claude-config/projects/-workspace/ directory into per-repo directories.
// It returns the number of files migrated and any error encountered.
func MigrateToPerRepo(store *Store) (int, error) {
	sharedProjectsDir := filepath.Join(store.ClaudeConfigDir(), "projects")
	workspaceDir := filepath.Join(sharedProjectsDir, "-workspace")

	// Check if old shared projects dir exists.
	if _, err := os.Stat(sharedProjectsDir); os.IsNotExist(err) {
		return 0, nil
	}

	// Glob for JSONL files.
	matches, err := filepath.Glob(filepath.Join(workspaceDir, "*.jsonl"))
	if err != nil {
		return 0, fmt.Errorf("glob jsonl files: %w", err)
	}
	if len(matches) == 0 {
		return 0, nil
	}

	// Build a map of ResumeID → RepoPath from existing sessions.
	sessions := store.List()
	resumeMap := make(map[string]string, len(sessions))
	for _, sess := range sessions {
		if sess.ResumeID != "" {
			resumeMap[sess.ResumeID] = sess.RepoPath
		}
	}

	count := 0
	for _, src := range matches {
		base := filepath.Base(src)
		uuid := strings.TrimSuffix(base, ".jsonl")

		// Determine destination repo path.
		repoPath, found := resumeMap[uuid]
		if !found || repoPath == "" {
			repoPath = "_orphaned"
		}

		destDir := filepath.Join(store.RepoConfigDir(repoPath), "projects", "-workspace")
		if err := os.MkdirAll(destDir, 0o755); err != nil {
			return count, fmt.Errorf("create destination dir: %w", err)
		}

		dest := filepath.Join(destDir, base)
		if err := os.Rename(src, dest); err != nil {
			return count, fmt.Errorf("move %s: %w", base, err)
		}

		// Register the repo in the index (skip for orphaned).
		if repoPath != "_orphaned" {
			if err := store.UpsertRepo(repoPath); err != nil {
				return count, fmt.Errorf("upsert repo for %s: %w", base, err)
			}
		}

		count++
	}

	// Clean up empty shared directories.
	_ = os.Remove(workspaceDir)
	_ = os.Remove(sharedProjectsDir)

	return count, nil
}
