// internal/session/handle_test.go
package session

import "testing"

func TestHandle_CleanupIdempotent(t *testing.T) {
	calls := 0
	h := &Handle{cleanup: func() { calls++ }}
	h.Cleanup()
	h.Cleanup()
	h.Cleanup()
	if calls != 1 {
		t.Fatalf("Cleanup called %d times, want 1", calls)
	}
}

func TestHandle_CleanupNil(t *testing.T) {
	h := &Handle{} // no cleanup function
	h.Cleanup()    // must not panic
}
