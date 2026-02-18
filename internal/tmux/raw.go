package tmux

import "golang.org/x/sys/unix"

// termState holds a saved terminal state so it can be restored later.
type termState struct {
	termios unix.Termios
}

// makeRaw sets the terminal identified by fd to raw mode and returns the
// previous state so it can be restored with restore(). In raw mode the
// terminal does not echo or buffer input, allowing byte-by-byte reads.
func makeRaw(fd uintptr) (*termState, error) {
	old, err := unix.IoctlGetTermios(int(fd), unix.TCGETS)
	if err != nil {
		return nil, err
	}
	saved := &termState{termios: *old}

	raw := *old
	// Input flags: disable break, CR-to-NL, parity, strip, flow control.
	raw.Iflag &^= unix.BRKINT | unix.ICRNL | unix.INPCK | unix.ISTRIP | unix.IXON
	// Output flags: disable post-processing.
	raw.Oflag &^= unix.OPOST
	// Control flags: set 8-bit characters.
	raw.Cflag |= unix.CS8
	// Local flags: disable echo, canonical mode, extensions, signals.
	raw.Lflag &^= unix.ECHO | unix.ICANON | unix.IEXTEN | unix.ISIG
	// Read returns after 1 byte with no timeout.
	raw.Cc[unix.VMIN] = 1
	raw.Cc[unix.VTIME] = 0

	if err := unix.IoctlSetTermios(int(fd), unix.TCSETS, &raw); err != nil {
		return nil, err
	}
	return saved, nil
}

// restore resets the terminal identified by fd to the state captured by
// a previous call to makeRaw.
func restore(fd uintptr, state *termState) error {
	return unix.IoctlSetTermios(int(fd), unix.TCSETS, &state.termios)
}
