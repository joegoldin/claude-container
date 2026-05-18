package config

import (
	"crypto/sha256"
	"encoding/hex"
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
	Name            string    `json:"name"`
	Branch          string    `json:"branch"`
	WorktreePath    string    `json:"worktree_path"`
	RepoPath        string    `json:"repo_path"`
	ContainerName   string    `json:"container_name"`
	Yolo            bool      `json:"yolo"`
	AutoRemove      bool      `json:"auto_remove,omitempty"`
	Mode            string    `json:"mode,omitempty"` // tty, task, background; default tty for old records
	ResumeID        string    `json:"resume_id,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	Profile         string    `json:"profile,omitempty"`
	ExtraWorkspaces []string  `json:"extra_workspaces,omitempty"`
	WorktreeRepos   []string  `json:"worktree_repos,omitempty"` // extra repos with container-created worktrees (for cleanup)
	AllowDomains    []string  `json:"allow_domains,omitempty"`
	DenyPaths       []string  `json:"deny_paths,omitempty"`
	AllowCommands   []string  `json:"allow_commands,omitempty"`
	DenyCommands    []string  `json:"deny_commands,omitempty"`
	AllowPerms      []string  `json:"allow_perms,omitempty"` // raw permission rules (e.g. "Bash(docker *)")
	DenyPerms       []string  `json:"deny_perms,omitempty"`  // raw deny rules (e.g. "Read(/etc/**)")
	Packages        []string  `json:"packages,omitempty"`
	NetworkSandbox  string    `json:"network_sandbox,omitempty"` // deprecated: always "proxy"
	ProxySeedPreset string    `json:"proxy_seed_preset,omitempty"`
	ProxyPort       int       `json:"proxy_port,omitempty"`
}

// RepoEntry represents a tracked repository in the repo index.
type RepoEntry struct {
	Path     string    `json:"path"`
	Name     string    `json:"name"`
	LastUsed time.Time `json:"last_used"`
}

// RepoID returns a stable 12-character hex identifier for a repository path.
func RepoID(repoPath string) string {
	h := sha256.Sum256([]byte(repoPath))
	return hex.EncodeToString(h[:])[:12]
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

// ClaudeConfigDir returns the path to the shared Claude Code config
// directory that gets mounted as CLAUDE_CONFIG_DIR inside containers.
// Claude Code manages its own auth and settings in this directory.
func (s *Store) ClaudeConfigDir() string {
	return filepath.Join(s.dir, "claude-config")
}

// RepoConfigDir returns the per-repo config directory path.
func (s *Store) RepoConfigDir(repoPath string) string {
	return filepath.Join(s.dir, "claude-config", RepoID(repoPath))
}

// repoIndexPath returns the path to the repos.json index file.
func (s *Store) repoIndexPath() string {
	return filepath.Join(s.dir, "claude-config", "repos.json")
}

// loadReposLocked reads the repo index. Must be called with s.mu held.
func (s *Store) loadReposLocked() (map[string]RepoEntry, error) {
	data, err := os.ReadFile(s.repoIndexPath())
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]RepoEntry), nil
		}
		return nil, err
	}
	var repos map[string]RepoEntry
	if err := json.Unmarshal(data, &repos); err != nil {
		return nil, fmt.Errorf("parse repos.json: %w", err)
	}
	return repos, nil
}

// writeReposLocked writes the repo index. Must be called with s.mu held.
func (s *Store) writeReposLocked(repos map[string]RepoEntry) error {
	dir := filepath.Dir(s.repoIndexPath())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(repos, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.repoIndexPath(), data, 0o644)
}

// UpsertRepo adds or updates a repository in the repo index and creates
// its per-repo config directory.
func (s *Store) UpsertRepo(repoPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	repos, err := s.loadReposLocked()
	if err != nil {
		return err
	}

	id := RepoID(repoPath)
	repos[id] = RepoEntry{
		Path:     repoPath,
		Name:     filepath.Base(repoPath),
		LastUsed: time.Now(),
	}

	// Create the per-repo config directory.
	repoDir := filepath.Join(s.dir, "claude-config", id)
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		return err
	}

	return s.writeReposLocked(repos)
}

// ListRepos returns all tracked repositories keyed by repo ID.
func (s *Store) ListRepos() (map[string]RepoEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadReposLocked()
}

// DeleteRepo removes a repository from the repo index by its ID.
// It does not delete the per-repo config directory.
func (s *Store) DeleteRepo(repoID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	repos, err := s.loadReposLocked()
	if err != nil {
		return err
	}

	delete(repos, repoID)
	return s.writeReposLocked(repos)
}

// HostClaudeCredentialFiles returns the paths of individual credential
// files from the host's ~/.claude/ directory that actually exist.
// Only known credential files are included (.credentials.json,
// settings.json, .claude.json) — conversation history and other data
// are deliberately excluded for security.
func HostClaudeCredentialFiles() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	dir := filepath.Join(home, ".claude")
	candidates := []string{".credentials.json", "settings.json", ".claude.json"}
	var files []string
	for _, name := range candidates {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			files = append(files, p)
		}
	}
	return files
}

// IsAuthenticated reports whether Claude Code credentials exist either
// in the shared config directory or in the host's ~/.claude/ directory.
func (s *Store) IsAuthenticated() bool {
	// Check shared config dir.
	p := filepath.Join(s.ClaudeConfigDir(), ".credentials.json")
	if _, err := os.Stat(p); err == nil {
		return true
	}
	// Check host ~/.claude/ dir.
	for _, f := range HostClaudeCredentialFiles() {
		if filepath.Base(f) == ".credentials.json" {
			return true
		}
	}
	return false
}

// ContainerConfigDir returns the per-session directory that gets mounted
// into the Docker container for Claude Code's own config files. Each
// session gets an isolated config so the host sessions.json isn't
// clobbered by container writes.
func (s *Store) ContainerConfigDir(name string) string {
	return filepath.Join(s.dir, "containers", name)
}

// PrepareSessionConfig creates a per-session config directory and populates
// it based on the resume mode. Returns the path to the session config dir.
//
// resumeMode values:
//   - ""           : fresh session, no old conversations exposed
//   - "__picker__" : symlink projects/ from per-repo dir (all conversations)
//   - "<uuid>"     : symlink only that specific JSONL file
func (s *Store) PrepareSessionConfig(sessionName, repoPath, resumeMode string) (string, error) {
	sessionDir := s.ContainerConfigDir(sessionName)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return "", fmt.Errorf("create session config dir: %w", err)
	}

	repoDir := s.RepoConfigDir(repoPath)

	switch resumeMode {
	case "__picker__":
		// Symlink projects/ from per-repo dir so all conversations are visible.
		repoProjects := filepath.Join(repoDir, "projects")
		if _, err := os.Stat(repoProjects); err == nil {
			sessionProjects := filepath.Join(sessionDir, "projects")
			os.Remove(sessionProjects) // remove stale symlink if exists
			if err := os.Symlink(repoProjects, sessionProjects); err != nil {
				return "", fmt.Errorf("symlink projects dir: %w", err)
			}
		}
	case "":
		// Fresh session: no projects/ dir, conversations are hidden.
	default:
		// Specific resume ID: expose only that one JSONL file.
		repoWS := filepath.Join(repoDir, "projects", "-workspace")
		jsonlFile := resumeMode + ".jsonl"
		src := filepath.Join(repoWS, jsonlFile)
		if _, err := os.Stat(src); err == nil {
			sessionWS := filepath.Join(sessionDir, "projects", "-workspace")
			if err := os.MkdirAll(sessionWS, 0o755); err != nil {
				return "", fmt.Errorf("create session workspace dir: %w", err)
			}
			dst := filepath.Join(sessionWS, jsonlFile)
			os.Remove(dst) // remove stale symlink
			if err := os.Symlink(src, dst); err != nil {
				return "", fmt.Errorf("symlink conversation: %w", err)
			}
		}
	}

	return sessionDir, nil
}

// SaveNewConversations copies any new JSONL files from the session config
// dir back to the per-repo config dir for long-term storage.
func (s *Store) SaveNewConversations(sessionName, repoPath string) error {
	sessionWS := filepath.Join(s.ContainerConfigDir(sessionName), "projects", "-workspace")
	repoWS := filepath.Join(s.RepoConfigDir(repoPath), "projects", "-workspace")

	entries, err := os.ReadDir(sessionWS)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no conversations created
		}
		return err
	}

	if err := os.MkdirAll(repoWS, 0o755); err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}
		src := filepath.Join(sessionWS, entry.Name())
		// Skip symlinks (these are resumed conversations, already in repo dir)
		info, err := os.Lstat(src)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		dst := filepath.Join(repoWS, entry.Name())
		if _, err := os.Stat(dst); err == nil {
			continue // already exists in repo dir
		}
		// Copy the file
		data, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("read conversation %s: %w", entry.Name(), err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return fmt.Errorf("save conversation %s: %w", entry.Name(), err)
		}
	}
	return nil
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
	for _, sess := range sessions {
		if sess.Mode == "" {
			sess.Mode = "tty"
		}
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
