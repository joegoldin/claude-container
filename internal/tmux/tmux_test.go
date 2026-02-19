package tmux

import (
	"slices"
	"strings"
	"testing"
)

func TestSessionName(t *testing.T) {
	got := SessionName("my-session")
	want := "claude-container_my-session"
	if got != want {
		t.Errorf("SessionName(%q) = %q, want %q", "my-session", got, want)
	}
}

func TestSessionNamePrefix(t *testing.T) {
	got := SessionName("foo")
	if !strings.HasPrefix(got, prefix) {
		t.Errorf("SessionName(%q) = %q, should start with prefix %q", "foo", got, prefix)
	}
}

func TestNewSessionArgs(t *testing.T) {
	args := NewSessionArgs("dev", "/home/user/project")

	if len(args) == 0 {
		t.Fatal("NewSessionArgs returned empty slice")
	}

	// First arg must be "new-session".
	if args[0] != "new-session" {
		t.Errorf("args[0] = %q, want %q", args[0], "new-session")
	}

	// Must contain -d (detached).
	if !slices.Contains(args, "-d") {
		t.Errorf("NewSessionArgs missing -d flag in %v", args)
	}

	// Must contain the full session name.
	name := SessionName("dev")
	if !slices.Contains(args, name) {
		t.Errorf("NewSessionArgs missing session name %q in %v", name, args)
	}

	// Must contain -s flag for session naming.
	if !slices.Contains(args, "-s") {
		t.Errorf("NewSessionArgs missing -s flag in %v", args)
	}

	joined := strings.Join(args, " ")

	// Must reference the working directory.
	if !strings.Contains(joined, "/home/user/project") {
		t.Errorf("NewSessionArgs missing working directory in %v", args)
	}
}

func TestNewSessionArgsNoCommand(t *testing.T) {
	args := NewSessionArgs("dev", "/tmp")

	// NewSessionArgs should NOT contain any command — the command is
	// sent separately via send-keys in Create().
	for _, a := range args {
		if a == "docker" || a == "sh" {
			t.Errorf("NewSessionArgs should not contain command, got %v", args)
		}
	}
}

func TestCapturePaneArgs(t *testing.T) {
	args := CapturePaneArgs("test")

	if len(args) == 0 {
		t.Fatal("CapturePaneArgs returned empty slice")
	}

	// First arg must be "capture-pane".
	if args[0] != "capture-pane" {
		t.Errorf("args[0] = %q, want %q", args[0], "capture-pane")
	}

	// Must contain the session name via -t flag.
	name := SessionName("test")
	if !slices.Contains(args, name) {
		t.Errorf("CapturePaneArgs missing session name %q in %v", name, args)
	}
	if !slices.Contains(args, "-t") {
		t.Errorf("CapturePaneArgs missing -t flag in %v", args)
	}

	// Must contain -p (print to stdout) and -e (escape sequences).
	if !slices.Contains(args, "-p") {
		t.Errorf("CapturePaneArgs missing -p flag in %v", args)
	}
	if !slices.Contains(args, "-e") {
		t.Errorf("CapturePaneArgs missing -e flag in %v", args)
	}
}

func TestNewSessionArgsWorkDir(t *testing.T) {
	args := NewSessionArgs("test", "/home/user/myproject")

	// Verify -c flag is present with the correct working directory.
	foundC := false
	for i, arg := range args {
		if arg == "-c" && i+1 < len(args) {
			foundC = true
			if args[i+1] != "/home/user/myproject" {
				t.Errorf("working directory after -c = %q, want %q", args[i+1], "/home/user/myproject")
			}
			break
		}
	}
	if !foundC {
		t.Errorf("NewSessionArgs missing -c flag for working directory in %v", args)
	}
}

func TestNewSessionArgsLength(t *testing.T) {
	args := NewSessionArgs("cmd-test", "/tmp")

	// Should contain: new-session -d -s <name> -c /tmp = 6 args
	if len(args) != 6 {
		t.Errorf("NewSessionArgs returned %d args, want 6: %v", len(args), args)
	}
}

func TestCapturePaneArgsFlags(t *testing.T) {
	args := CapturePaneArgs("flag-test")

	// Verify -p (print to stdout) and -e (escape sequences) are present.
	if !slices.Contains(args, "-p") {
		t.Errorf("CapturePaneArgs missing -p flag in %v", args)
	}
	if !slices.Contains(args, "-e") {
		t.Errorf("CapturePaneArgs missing -e flag in %v", args)
	}

	// Verify -t (target) is present.
	if !slices.Contains(args, "-t") {
		t.Errorf("CapturePaneArgs missing -t flag in %v", args)
	}

	// Verify the first arg is "capture-pane".
	if args[0] != "capture-pane" {
		t.Errorf("args[0] = %q, want %q", args[0], "capture-pane")
	}
}

func TestSessionNameConsistency(t *testing.T) {
	// SessionName should use the same prefix pattern as config.Prefix.
	// config.Prefix is "claude-container_"
	expectedPrefix := "claude-container_"

	sessions := []string{"alpha", "beta", "my-session", "test123"}
	for _, s := range sessions {
		got := SessionName(s)
		if !strings.HasPrefix(got, expectedPrefix) {
			t.Errorf("SessionName(%q) = %q, should start with %q", s, got, expectedPrefix)
		}
		suffix := strings.TrimPrefix(got, expectedPrefix)
		if suffix != s {
			t.Errorf("SessionName(%q) suffix = %q, want %q", s, suffix, s)
		}
	}
}

// TestAllFunctionsAcceptPlainName verifies that every public function in the
// tmux package expects a plain session name (NOT prefixed). This prevents
// callers from double-prefixing, which was the root cause of a bug where
// tmux.Attach(ctx, tmux.SessionName(name)) produced
// "claude-container_claude-container_name".
func TestAllFunctionsAcceptPlainName(t *testing.T) {
	session := "test-session"
	expected := "claude-container_test-session"

	// NewSessionArgs: verify the -s flag value uses single prefix.
	args := NewSessionArgs(session, "/tmp")
	sIdx := -1
	for i, a := range args {
		if a == "-s" {
			sIdx = i
			break
		}
	}
	if sIdx == -1 || sIdx+1 >= len(args) {
		t.Fatal("NewSessionArgs missing -s flag")
	}
	if args[sIdx+1] != expected {
		t.Errorf("NewSessionArgs -s value = %q, want %q", args[sIdx+1], expected)
	}

	// CapturePaneArgs: verify the -t flag value uses single prefix.
	cpArgs := CapturePaneArgs(session)
	tIdx := -1
	for i, a := range cpArgs {
		if a == "-t" {
			tIdx = i
			break
		}
	}
	if tIdx == -1 || tIdx+1 >= len(cpArgs) {
		t.Fatal("CapturePaneArgs missing -t flag")
	}
	if cpArgs[tIdx+1] != expected {
		t.Errorf("CapturePaneArgs -t value = %q, want %q", cpArgs[tIdx+1], expected)
	}
}

// TestSessionNameIdempotency verifies that passing an already-prefixed name
// through SessionName produces a DOUBLE prefix. This is intentional -- it
// confirms that callers must NOT call SessionName() before passing to other
// tmux functions (which call SessionName internally).
func TestSessionNameIdempotency(t *testing.T) {
	plain := "my-session"
	prefixed := SessionName(plain)
	doublePrefixed := SessionName(prefixed)

	// The double prefix should be detectable.
	if doublePrefixed == prefixed {
		t.Error("SessionName is unexpectedly idempotent; double-prefix bug would be hidden")
	}
	if doublePrefixed != "claude-container_claude-container_my-session" {
		t.Errorf("double-prefixed = %q, want %q", doublePrefixed, "claude-container_claude-container_my-session")
	}
}

// TestNewSessionAndCapturePaneUseSamePrefix verifies Create and CapturePane
// target the same tmux session name for the same input.
func TestNewSessionAndCapturePaneUseSamePrefix(t *testing.T) {
	session := "consistency-test"

	createArgs := NewSessionArgs(session, "/tmp")
	captureArgs := CapturePaneArgs(session)

	// Extract session names from args.
	var createName, captureName string
	for i, a := range createArgs {
		if a == "-s" && i+1 < len(createArgs) {
			createName = createArgs[i+1]
			break
		}
	}
	for i, a := range captureArgs {
		if a == "-t" && i+1 < len(captureArgs) {
			captureName = captureArgs[i+1]
			break
		}
	}

	if createName == "" {
		t.Fatal("could not extract session name from NewSessionArgs")
	}
	if captureName == "" {
		t.Fatal("could not extract session name from CapturePaneArgs")
	}
	if createName != captureName {
		t.Errorf("NewSessionArgs name %q != CapturePaneArgs name %q", createName, captureName)
	}
}

func TestShellJoin(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "simple args",
			args: []string{"echo", "hello"},
			want: "echo hello",
		},
		{
			name: "args with spaces",
			args: []string{"echo", "hello world"},
			want: `echo 'hello world'`,
		},
		{
			name: "args with single quotes",
			args: []string{"echo", "it's"},
			want: `echo 'it'"'"'s'`,
		},
		{
			name: "empty args",
			args: []string{},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shellJoin(tt.args)
			if got != tt.want {
				t.Errorf("shellJoin(%v) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}
