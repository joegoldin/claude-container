// internal/session/output_tty.go
package session

import (
	"github.com/joegoldin/claude-container/internal/proxy"
)

// AttachTTY runs the existing PTY proxy against the launched container.
// It blocks until the user detaches (Ctrl+B d) or the container exits.
// Cleanup runs after proxy.Run returns.
func (h *Handle) AttachTTY() error {
	defer h.Cleanup()
	return proxy.Run(proxy.Opts{
		DockerArgs:    []string{"attach", h.Container},
		ContainerName: h.Container,
		StatusBar:     h.StatusBar,
	})
}
