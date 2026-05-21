package session

// RunBackground returns immediately after Launch has started the container
// detached. The session record was already saved. Cleanup intentionally
// does NOT fire here — the session outlives this process and is removed
// by `claude-container rm` or `gc`.
func (h *Handle) RunBackground() error {
	return nil
}
