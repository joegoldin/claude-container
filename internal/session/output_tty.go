// internal/session/output_tty.go
package session

import (
	"github.com/joegoldin/claude-container/internal/proxy"
)

// AttachTTY runs the existing PTY proxy against the launched container.
// It blocks until the user detaches (Ctrl+B d) or the container exits.
//
// proxy.Opts.{AutoRemove,Cleanup} are intentionally left unset: defer
// h.Cleanup() runs on every return path (including detach), which is
// broader coverage than proxy's own teardown hook.
func (h *Handle) AttachTTY() error {
	defer h.Cleanup()
	return proxy.Run(proxy.Opts{
		DockerArgs:    []string{"attach", h.Container},
		ContainerName: h.Container,
		StatusBar:     h.StatusBar,
	})
}
