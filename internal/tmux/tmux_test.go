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
	args := NewSessionArgs("dev", "/home/user/project", []string{"docker", "run", "-it", "claude-code"})

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

	// Must contain the command to run.
	if !strings.Contains(joined, "docker") {
		t.Errorf("NewSessionArgs missing command in %v", args)
	}
}

func TestNewSessionArgsMouseMode(t *testing.T) {
	args := NewSessionArgs("dev", "/tmp", []string{"echo", "hello"})
	joined := strings.Join(args, " ")

	// Must enable mouse mode via set-option.
	if !strings.Contains(joined, "set-option -g mouse on") {
		t.Errorf("NewSessionArgs should enable mouse mode, got %v", args)
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
