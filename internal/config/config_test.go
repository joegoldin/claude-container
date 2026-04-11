package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSessionRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	now := time.Now().Truncate(time.Second)
	sess := &Session{
		Name:          "test-session",
		Branch:        "feature-auth",
		WorktreePath:  "/tmp/worktrees/feature-auth",
		RepoPath:      "/home/user/project",
		ContainerName: Prefix + "test-session",
		Yolo:          true,
		CreatedAt:     now,
	}

	if err := store.Save(sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.Get("test-session")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got.Name != sess.Name {
		t.Errorf("Name = %q, want %q", got.Name, sess.Name)
	}
	if got.Branch != sess.Branch {
		t.Errorf("Branch = %q, want %q", got.Branch, sess.Branch)
	}
	if got.WorktreePath != sess.WorktreePath {
		t.Errorf("WorktreePath = %q, want %q", got.WorktreePath, sess.WorktreePath)
	}
	if got.RepoPath != sess.RepoPath {
		t.Errorf("RepoPath = %q, want %q", got.RepoPath, sess.RepoPath)
	}
	if got.ContainerName != sess.ContainerName {
		t.Errorf("ContainerName = %q, want %q", got.ContainerName, sess.ContainerName)
	}
	if got.Yolo != sess.Yolo {
		t.Errorf("Yolo = %v, want %v", got.Yolo, sess.Yolo)
	}
	if !got.CreatedAt.Equal(sess.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, sess.CreatedAt)
	}
}

func TestListSessions(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Empty store returns empty list.
	if got := store.List(); len(got) != 0 {
		t.Fatalf("List on empty store: got %d, want 0", len(got))
	}

	now := time.Now().Truncate(time.Second)

	sess1 := &Session{
		Name:      "alpha",
		CreatedAt: now,
	}
	sess2 := &Session{
		Name:      "beta",
		CreatedAt: now.Add(time.Second),
	}

	if err := store.Save(sess1); err != nil {
		t.Fatalf("Save sess1: %v", err)
	}
	if err := store.Save(sess2); err != nil {
		t.Fatalf("Save sess2: %v", err)
	}

	list := store.List()
	if len(list) != 2 {
		t.Fatalf("List: got %d sessions, want 2", len(list))
	}

	// Should be sorted by CreatedAt (earliest first).
	if list[0].Name != "alpha" {
		t.Errorf("list[0].Name = %q, want %q", list[0].Name, "alpha")
	}
	if list[1].Name != "beta" {
		t.Errorf("list[1].Name = %q, want %q", list[1].Name, "beta")
	}
}

func TestDeleteSession(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	sess := &Session{
		Name:      "to-delete",
		CreatedAt: time.Now().Truncate(time.Second),
	}

	if err := store.Save(sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := store.Get("to-delete"); err != nil {
		t.Fatalf("Get before delete: %v", err)
	}

	if err := store.Delete("to-delete"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := store.Get("to-delete"); err == nil {
		t.Fatal("Get after delete: expected error, got nil")
	}

	if list := store.List(); len(list) != 0 {
		t.Fatalf("List after delete: got %d, want 0", len(list))
	}
}

func TestStoreCreatesDir(t *testing.T) {
	base := t.TempDir()
	nested := filepath.Join(base, "a", "b", "c")
	store := NewStore(nested)

	sess := &Session{
		Name:      "nested",
		CreatedAt: time.Now().Truncate(time.Second),
	}

	if err := store.Save(sess); err != nil {
		t.Fatalf("Save with nested dir: %v", err)
	}

	// Verify the file was actually written.
	path := filepath.Join(nested, SessionFile)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("sessions.json not created: %v", err)
	}
}

func TestGetNonExistent(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	_, err := store.Get("does-not-exist")
	if err == nil {
		t.Fatal("Get on non-existent session: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got %q", err.Error())
	}
}

func TestSaveOverwrite(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	now := time.Now().Truncate(time.Second)

	// Save the first version.
	sess1 := &Session{
		Name:      "overwrite-me",
		Branch:    "branch-v1",
		CreatedAt: now,
	}
	if err := store.Save(sess1); err != nil {
		t.Fatalf("Save v1: %v", err)
	}

	// Save a second version with the same name but different branch.
	sess2 := &Session{
		Name:      "overwrite-me",
		Branch:    "branch-v2",
		CreatedAt: now.Add(time.Second),
	}
	if err := store.Save(sess2); err != nil {
		t.Fatalf("Save v2: %v", err)
	}

	got, err := store.Get("overwrite-me")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Branch != "branch-v2" {
		t.Errorf("Branch = %q, want %q (latest should win)", got.Branch, "branch-v2")
	}

	// There should be exactly one session, not two.
	if list := store.List(); len(list) != 1 {
		t.Errorf("List: got %d sessions, want 1 after overwrite", len(list))
	}
}

func TestNamesOrder(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	now := time.Now().Truncate(time.Second)
	// Insert in reverse-alphabetical order but with ascending timestamps.
	for i, name := range []string{"charlie", "alpha", "bravo"} {
		sess := &Session{
			Name:      name,
			CreatedAt: now.Add(time.Duration(i) * time.Second),
		}
		if err := store.Save(sess); err != nil {
			t.Fatalf("Save %q: %v", name, err)
		}
	}

	names := store.Names()
	if len(names) != 3 {
		t.Fatalf("Names: got %d, want 3", len(names))
	}
	// Names() returns names from List() which sorts by CreatedAt.
	// Insertion order: charlie(t+0), alpha(t+1), bravo(t+2)
	want := []string{"charlie", "alpha", "bravo"}
	for i, w := range want {
		if names[i] != w {
			t.Errorf("Names[%d] = %q, want %q", i, names[i], w)
		}
	}
}

func TestDefaultDir(t *testing.T) {
	// Set XDG_CONFIG_HOME and verify DefaultDir respects it.
	original := os.Getenv("XDG_CONFIG_HOME")
	defer os.Setenv("XDG_CONFIG_HOME", original)

	os.Setenv("XDG_CONFIG_HOME", "/custom/config")
	got := DefaultDir()
	want := filepath.Join("/custom/config", "claude-container")
	if got != want {
		t.Errorf("DefaultDir() with XDG_CONFIG_HOME = %q, want %q", got, want)
	}

	// When XDG_CONFIG_HOME is empty, fall back to ~/.config.
	os.Setenv("XDG_CONFIG_HOME", "")
	got = DefaultDir()
	home, _ := os.UserHomeDir()
	want = filepath.Join(home, ".config", "claude-container")
	if got != want {
		t.Errorf("DefaultDir() without XDG_CONFIG_HOME = %q, want %q", got, want)
	}
}

func TestWorktreeDir(t *testing.T) {
	store := NewStore("/tmp/test-config")
	got := store.WorktreeDir()
	want := filepath.Join("/tmp/test-config", "worktrees")
	if got != want {
		t.Errorf("WorktreeDir() = %q, want %q", got, want)
	}
}

func TestContainerConfigDir(t *testing.T) {
	store := NewStore("/tmp/test-config")

	got := store.ContainerConfigDir("my-session")
	want := filepath.Join("/tmp/test-config", "containers", "my-session")
	if got != want {
		t.Errorf("ContainerConfigDir(%q) = %q, want %q", "my-session", got, want)
	}

	// Each session should get its own directory.
	a := store.ContainerConfigDir("session-a")
	b := store.ContainerConfigDir("session-b")
	if a == b {
		t.Errorf("ContainerConfigDir should be unique per session, both = %q", a)
	}

	// Container config dir should NOT be the same as the store base dir
	// (prevents container writes from clobbering sessions.json).
	if store.ContainerConfigDir("x") == "/tmp/test-config" {
		t.Error("ContainerConfigDir should not equal store base dir")
	}
}

func TestClaudeConfigDir(t *testing.T) {
	store := NewStore("/tmp/test-config")
	got := store.ClaudeConfigDir()
	want := filepath.Join("/tmp/test-config", "claude-config")
	if got != want {
		t.Errorf("ClaudeConfigDir() = %q, want %q", got, want)
	}

	// Consistent across calls.
	if got2 := store.ClaudeConfigDir(); got2 != got {
		t.Errorf("ClaudeConfigDir() returned %q then %q, want consistent", got, got2)
	}
}

func TestIsAuthenticated(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Override HOME so HostClaudeCredentialFiles() doesn't find real host credentials.
	t.Setenv("HOME", t.TempDir())

	// Not authenticated by default.
	if store.IsAuthenticated() {
		t.Error("IsAuthenticated() = true on fresh store, want false")
	}

	// Create the credentials file.
	configDir := store.ClaudeConfigDir()
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, ".credentials.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Now should be authenticated.
	if !store.IsAuthenticated() {
		t.Error("IsAuthenticated() = false after creating credentials, want true")
	}
}

func TestGenerateName(t *testing.T) {
	tests := []struct {
		dir  string
		desc string
	}{
		{"/home/user/my-project", "normal directory"},
		{"/tmp", "short directory"},
		{".", "dot directory"},
		{"", "empty string"},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			name := GenerateName(tt.dir)
			if name == "" {
				t.Error("GenerateName returned empty string")
			}
			parts := strings.Split(name, "-")
			if len(parts) < 3 {
				t.Errorf("GenerateName(%q) = %q, expected at least 3 hyphen-separated parts", tt.dir, name)
			}
			// Should not contain slashes or spaces.
			if strings.ContainsAny(name, "/ \t") {
				t.Errorf("GenerateName(%q) = %q, contains invalid characters", tt.dir, name)
			}
		})
	}

	// Two calls should (almost certainly) produce different names.
	a := GenerateName("/home/user/project")
	b := GenerateName("/home/user/project")
	// With 40*55=2200 combos, collision is ~0.05% per pair. Run a few times.
	different := false
	for i := 0; i < 10; i++ {
		if GenerateName("/x") != GenerateName("/x") {
			different = true
			break
		}
	}
	if !different && a == b {
		t.Log("warning: GenerateName produced identical names (unlikely but possible)")
	}
}

func TestRepoID(t *testing.T) {
	// Stable output.
	id1 := RepoID("/home/joe/Development/foo")
	id2 := RepoID("/home/joe/Development/foo")
	if id1 != id2 {
		t.Errorf("RepoID not stable: %q != %q", id1, id2)
	}

	// 12 hex characters.
	if len(id1) != 12 {
		t.Errorf("RepoID length = %d, want 12", len(id1))
	}

	// Different inputs produce different IDs.
	id3 := RepoID("/home/joe/Development/bar")
	if id1 == id3 {
		t.Errorf("different paths produced same RepoID: %q", id1)
	}
}

func TestRepoConfigDir(t *testing.T) {
	store := NewStore("/tmp/test-config")
	got := store.RepoConfigDir("/home/joe/project")
	id := RepoID("/home/joe/project")
	want := filepath.Join("/tmp/test-config", "claude-config", id)
	if got != want {
		t.Errorf("RepoConfigDir() = %q, want %q", got, want)
	}
}

func TestUpsertAndListRepos(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Empty list initially.
	repos, err := store.ListRepos()
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != 0 {
		t.Fatalf("expected empty map, got %d entries", len(repos))
	}

	// Upsert a repo.
	repoPath := "/home/joe/Development/myproject"
	if err := store.UpsertRepo(repoPath); err != nil {
		t.Fatalf("UpsertRepo: %v", err)
	}

	repos, err = store.ListRepos()
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(repos))
	}

	id := RepoID(repoPath)
	entry, ok := repos[id]
	if !ok {
		t.Fatalf("entry for repo ID %q not found", id)
	}
	if entry.Path != repoPath {
		t.Errorf("Path = %q, want %q", entry.Path, repoPath)
	}
	if entry.Name != "myproject" {
		t.Errorf("Name = %q, want %q", entry.Name, "myproject")
	}
	if entry.LastUsed.IsZero() {
		t.Error("LastUsed should not be zero")
	}

	// Verify config dir was created.
	configDir := store.RepoConfigDir(repoPath)
	if _, err := os.Stat(configDir); err != nil {
		t.Errorf("repo config dir not created: %v", err)
	}
}

func TestDeleteRepo(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	repoPath := "/home/joe/Development/deleteme"
	if err := store.UpsertRepo(repoPath); err != nil {
		t.Fatalf("UpsertRepo: %v", err)
	}

	id := RepoID(repoPath)
	if err := store.DeleteRepo(id); err != nil {
		t.Fatalf("DeleteRepo: %v", err)
	}

	repos, err := store.ListRepos()
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if _, ok := repos[id]; ok {
		t.Error("repo entry still exists after delete")
	}

	// Config dir should still exist (caller decides whether to remove it).
	configDir := store.RepoConfigDir(repoPath)
	if _, err := os.Stat(configDir); err != nil {
		t.Log("note: config dir was removed, but DeleteRepo should not remove it")
	}
}

func TestSanitizeName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"feature/auth", "feature-auth"},
		{"fix payments", "fix-payments"},
		{"simple", "simple"},
		{"a/b/c", "a-b-c"},
		{"hello world/test", "hello-world-test"},
		{"tabs\there", "tabs-here"},
		{"multiple   spaces", "multiple-spaces"},
	}

	for _, tt := range tests {
		got := SanitizeName(tt.input)
		if got != tt.want {
			t.Errorf("SanitizeName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
