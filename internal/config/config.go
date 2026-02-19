package config

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// Prefix is prepended to container names.
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
	Yolo          bool      `json:"yolo"`
	AutoRemove    bool      `json:"auto_remove,omitempty"`
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

// CredentialsFile returns the path to the Claude Code credentials file
// on the host, or "" if not found. It checks CLAUDE_CONFIG_DIR, then
// ~/.claude/.credentials.json.
func CredentialsFile() string {
	// Check CLAUDE_CONFIG_DIR first.
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		p := filepath.Join(dir, ".credentials.json")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	// Default location.
	home, _ := os.UserHomeDir()
	p := filepath.Join(home, ".claude", ".credentials.json")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

// ContainerConfigDir returns the per-session directory that gets mounted
// into the Docker container for Claude Code's own config files. Each
// session gets an isolated config so the host sessions.json isn't
// clobbered by container writes.
func (s *Store) ContainerConfigDir(name string) string {
	return filepath.Join(s.dir, "containers", name)
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

// adjectives used for random name generation.
var adjectives = []string{
	"bold", "calm", "cool", "dark", "deep",
	"fast", "keen", "loud", "neat", "pale",
	"pure", "rare", "slim", "soft", "tall",
	"warm", "wise", "wild", "firm", "glad",
	"epic", "fair", "fine", "free", "gold",
	"grim", "hazy", "icy", "jade", "kind",
	"lazy", "mild", "neon", "odd", "pink",
	"red", "safe", "tidy", "vast", "zinc",
}

// nouns used for random name generation.
var nouns = []string{
	"arch", "beam", "bolt", "cape", "cave",
	"claw", "core", "cube", "dawn", "disk",
	"dome", "dune", "echo", "edge", "fern",
	"flux", "foam", "fork", "gate", "glow",
	"grid", "haze", "helm", "hill", "iris",
	"jade", "knot", "lake", "leaf", "link",
	"loom", "mist", "moon", "nest", "node",
	"opal", "orb", "palm", "peak", "pine",
	"pond", "reef", "rune", "sage", "seed",
	"shard", "star", "tide", "vale", "vine",
	"wave", "well", "wing", "yard", "zone",
}

// GenerateName creates a readable session name from the directory basename
// plus a random adjective-noun pair. Example: "myproject-calm-reef".
func GenerateName(dir string) string {
	base := filepath.Base(dir)
	// Clean the base name: lowercase, replace non-alphanumeric with hyphen.
	base = strings.ToLower(base)
	base = sanitizeRe.ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	if base == "" || base == "." {
		base = "session"
	}

	adj := adjectives[rand.Intn(len(adjectives))]
	noun := nouns[rand.Intn(len(nouns))]
	return fmt.Sprintf("%s-%s-%s", base, adj, noun)
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
