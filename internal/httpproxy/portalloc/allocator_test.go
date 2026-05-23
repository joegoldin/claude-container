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
