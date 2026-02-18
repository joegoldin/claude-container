package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

const (
	// Prefix is prepended to container and tmux session names.
	Prefix = "claude-container_"

	// SessionFile is the JSON file where sessions are persisted.
	SessionFile = "sessions.json"
)

// Session represents a single Claude Code container session.
type Session struct {
	Name          string    `json:"name"`
	Branch        string    `json:"branch"`
	WorktreePath  string    `json:"worktree_path"`
	RepoPath      string    `json:"repo_path"`
	ContainerName string    `json:"container_name"`
	TmuxSession   string    `json:"tmux_session"`
	Yolo          bool      `json:"yolo"`
	CreatedAt     time.Time `json:"created_at"`
}

// Store provides thread-safe persistence of sessions to a JSON file.
type Store struct {
	mu  sync.Mutex
	dir string
}

// NewStore returns a Store that reads and writes sessions in dir.
func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

// DefaultDir returns the default configuration directory, respecting
// $XDG_CONFIG_HOME when set.
func DefaultDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "claude-container")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "claude-container")
}

// WorktreeDir returns the path where git worktrees are stored.
func (s *Store) WorktreeDir() string {
	return filepath.Join(s.dir, "worktrees")
}

// Save persists sess into the store, creating the directory and file if
// needed. If a session with the same name exists it is overwritten.
func (s *Store) Save(sess *Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	sessions, err := s.loadLocked()
	if err != nil {
		return err
	}

	// Upsert: replace existing or append.
	found := false
	for i, existing := range sessions {
		if existing.Name == sess.Name {
			sessions[i] = sess
			found = true
			break
		}
	}
	if !found {
		sessions = append(sessions, sess)
	}

	return s.writeLocked(sessions)
}

// Get returns the session with the given name, or an error if not found.
func (s *Store) Get(name string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sessions, err := s.loadLocked()
	if err != nil {
		return nil, err
	}

	for _, sess := range sessions {
		if sess.Name == name {
			return sess, nil
		}
	}
	return nil, fmt.Errorf("session %q not found", name)
}

// List returns all sessions sorted by CreatedAt (earliest first).
func (s *Store) List() []*Session {
	s.mu.Lock()
	defer s.mu.Unlock()

	sessions, err := s.loadLocked()
	if err != nil {
		return nil
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].CreatedAt.Before(sessions[j].CreatedAt)
	})
	return sessions
}

// Delete removes the session with the given name. It returns an error if
// the session does not exist.
func (s *Store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	sessions, err := s.loadLocked()
	if err != nil {
		return err
	}

	filtered := make([]*Session, 0, len(sessions))
	found := false
	for _, sess := range sessions {
		if sess.Name == name {
			found = true
			continue
		}
		filtered = append(filtered, sess)
	}
	if !found {
		return fmt.Errorf("session %q not found", name)
	}

	return s.writeLocked(filtered)
}

// Names returns the names of all sessions, useful for shell tab
// completion.
func (s *Store) Names() []string {
	sessions := s.List()
	names := make([]string, len(sessions))
	for i, sess := range sessions {
		names[i] = sess.Name
	}
	return names
}

// sanitizeRe matches slashes and whitespace characters.
var sanitizeRe = regexp.MustCompile(`[/\s]+`)

// SanitizeName replaces slashes and whitespace runs with a single hyphen.
func SanitizeName(name string) string {
	return sanitizeRe.ReplaceAllString(name, "-")
}

// loadLocked reads the sessions file. Must be called with s.mu held.
// Returns an empty slice if the file does not exist.
func (s *Store) loadLocked() ([]*Session, error) {
	path := filepath.Join(s.dir, SessionFile)

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var sessions []*Session
	if err := json.Unmarshal(data, &sessions); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return sessions, nil
}

// writeLocked writes sessions to the file, creating directories as needed.
// Must be called with s.mu held.
func (s *Store) writeLocked(sessions []*Session) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(sessions, "", "  ")
	if err != nil {
		return err
	}

	path := filepath.Join(s.dir, SessionFile)
	return os.WriteFile(path, data, 0o644)
}
