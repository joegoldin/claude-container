package portalloc

import (
	"path/filepath"
	"testing"
)

func TestNew_CreatesEmptyAllocator(t *testing.T) {
	dir := t.TempDir()
	a, err := New(filepath.Join(dir, "alloc.json"), 30000, 30099, 10)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a == nil {
		t.Fatal("expected non-nil allocator")
	}
}

func TestClaim_FirstSessionGetsPoolStart(t *testing.T) {
	dir := t.TempDir()
	a, _ := New(filepath.Join(dir, "alloc.json"), 30000, 30099, 10)
	alloc, err := a.Claim("session-a", 10)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if alloc.Base != 30000 || alloc.Size != 10 {
		t.Errorf("got %+v, want base=30000 size=10", alloc)
	}
}

func TestClaim_SecondSessionGetsNextRange(t *testing.T) {
	dir := t.TempDir()
	a, _ := New(filepath.Join(dir, "alloc.json"), 30000, 30099, 10)
	_, _ = a.Claim("session-a", 10)
	alloc, err := a.Claim("session-b", 10)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if alloc.Base != 30010 || alloc.Size != 10 {
		t.Errorf("got %+v, want base=30010 size=10", alloc)
	}
}

func TestClaim_ExistingSessionReturnsSameRange(t *testing.T) {
	dir := t.TempDir()
	a, _ := New(filepath.Join(dir, "alloc.json"), 30000, 30099, 10)
	first, _ := a.Claim("session-a", 10)
	again, _ := a.Claim("session-a", 10)
	if again != first {
		t.Errorf("re-claim returned %+v, want same as first %+v", again, first)
	}
}

func TestClaim_PoolExhaustionErrors(t *testing.T) {
	dir := t.TempDir()
	// pool size 20 ports, 10 each → 2 sessions max
	a, _ := New(filepath.Join(dir, "alloc.json"), 30000, 30019, 10)
	_, _ = a.Claim("s1", 10)
	_, _ = a.Claim("s2", 10)
	_, err := a.Claim("s3", 10)
	if err == nil {
		t.Fatal("expected exhaustion error, got nil")
	}
}

func TestRelease_FreesTheRange(t *testing.T) {
	dir := t.TempDir()
	a, _ := New(filepath.Join(dir, "alloc.json"), 30000, 30099, 10)
	_, _ = a.Claim("session-a", 10)   // 30000-30009
	_, _ = a.Claim("session-b", 10)   // 30010-30019
	if err := a.Release("session-a"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	// New session should now get the freed range.
	got, _ := a.Claim("session-c", 10)
	if got.Base != 30000 {
		t.Errorf("after Release, next Claim got base=%d, want 30000", got.Base)
	}
}

func TestRelease_UnknownSessionIsNoop(t *testing.T) {
	dir := t.TempDir()
	a, _ := New(filepath.Join(dir, "alloc.json"), 30000, 30099, 10)
	if err := a.Release("never-existed"); err != nil {
		t.Errorf("Release of unknown session should be a no-op, got %v", err)
	}
}
