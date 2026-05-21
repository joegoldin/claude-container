// internal/session/handle.go
package session

import (
	"sync"

	"github.com/joegoldin/claude-container/internal/proxy"
)

// CleanupFunc runs once when the session ends (container removed, proxy
// torn down, session record deleted, etc.). The session-launcher composes
// these in Launch.
type CleanupFunc func()

// Handle is returned by Launch. The caller picks one of AttachTTY,
// WaitTask, RunBackground based on intent.
type Handle struct {
	Name      string
	Container string
	Repo      string
	Branch    string
	ProxyPort int
	StatusBar proxy.StatusBarInfo

	cleanupOnce sync.Once
	cleanup     CleanupFunc
}

// Cleanup runs the cleanup function (idempotent).
func (h *Handle) Cleanup() {
	if h.cleanup == nil {
		return
	}
	h.cleanupOnce.Do(h.cleanup)
}
