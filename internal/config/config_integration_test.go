package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestConcurrentAccess(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	const goroutines = 10
	var wg sync.WaitGroup
	errs := make(chan error, goroutines*2)

	now := time.Now().Truncate(time.Second)

	// Launch goroutines that save sessions concurrently.
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sess := &Session{
				Name:      fmt.Sprintf("concurrent-%d", idx),
				Branch:    fmt.Sprintf("branch-%d", idx),
				CreatedAt: now.Add(time.Duration(idx) * time.Millisecond),
			}
			if err := store.Save(sess); err != nil {
				errs <- fmt.Errorf("Save concurrent-%d: %w", idx, err)
			}
		}(i)
	}

	wg.Wait()

	// Launch goroutines that read sessions concurrently.
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			name := fmt.Sprintf("concurrent-%d", idx)
			got, err := store.Get(name)
			if err != nil {
				errs <- fmt.Errorf("Get %s: %w", name, err)
				return
			}
			if got.Name != name {
				errs <- fmt.Errorf("Get %s: Name = %q", name, got.Name)
			}
		}(i)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}

	// Verify all sessions exist.
	list := store.List()
	if len(list) != goroutines {
		t.Errorf("List: got %d sessions, want %d", len(list), goroutines)
	}
}

func TestCorruptedFile(t *testing.T) {
	dir := t.TempDir()

	// Write garbage to sessions.json.
	path := filepath.Join(dir, SessionFile)
	err := os.WriteFile(path, []byte("this is not json{{{"), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	store := NewStore(dir)

	// Get should return an error.
	_, err = store.Get("anything")
	if err == nil {
		t.Error("Get on corrupted file: expected error, got nil")
	}

	// List should return nil (graceful failure).
	list := store.List()
	if list != nil {
		t.Errorf("List on corrupted file: expected nil, got %v", list)
	}

	// Save should overwrite the corrupted file and succeed.
	sess := &Session{
		Name:      "recovery",
		CreatedAt: time.Now().Truncate(time.Second),
	}
	// Save calls loadLocked which will fail on corrupted JSON, so it should error.
	err = store.Save(sess)
	if err == nil {
		t.Error("Save on corrupted file: expected error, got nil")
	}
}

func TestLargeNumberOfSessions(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	const count = 100
	now := time.Now().Truncate(time.Second)

	for i := 0; i < count; i++ {
		sess := &Session{
			Name:      fmt.Sprintf("session-%03d", i),
			Branch:    fmt.Sprintf("branch-%03d", i),
			CreatedAt: now.Add(time.Duration(i) * time.Millisecond),
		}
		if err := store.Save(sess); err != nil {
			t.Fatalf("Save session-%03d: %v", i, err)
		}
	}

	list := store.List()
	if len(list) != count {
		t.Fatalf("List: got %d sessions, want %d", len(list), count)
	}

	// Verify sorted by CreatedAt.
	for i := 1; i < len(list); i++ {
		if list[i].CreatedAt.Before(list[i-1].CreatedAt) {
			t.Errorf("List not sorted: session %q (at %v) before session %q (at %v)",
				list[i-1].Name, list[i-1].CreatedAt, list[i].Name, list[i].CreatedAt)
			break
		}
	}

	// Verify Names() returns all names.
	names := store.Names()
	if len(names) != count {
		t.Fatalf("Names: got %d, want %d", len(names), count)
	}

	// Verify each session can be retrieved.
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("session-%03d", i)
		got, err := store.Get(name)
		if err != nil {
			t.Errorf("Get %s: %v", name, err)
			continue
		}
		if got.Branch != fmt.Sprintf("branch-%03d", i) {
			t.Errorf("Get %s: Branch = %q, want %q", name, got.Branch, fmt.Sprintf("branch-%03d", i))
		}
	}
}
