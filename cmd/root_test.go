package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joegoldin/claude-container/internal/config"
)

func TestMaybePrintBareInvokeNotice_FirstRunWithSessions(t *testing.T) {
	dir := t.TempDir()
	store := config.NewStore(dir)
	if err := store.Save(&config.Session{Name: "s1", CreatedAt: time.Now()}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	var buf bytes.Buffer
	t.Setenv("CLAUDE_CONTAINER_QUIET", "")
	maybePrintBareInvokeNotice(store, dir, &buf)

	if !strings.Contains(buf.String(), "now creates a session") {
		t.Errorf("expected notice about session creation, got %q", buf.String())
	}
	if !strings.Contains(buf.String(), "existing sessions: 1") {
		t.Errorf("expected session count in notice, got %q", buf.String())
	}
	if _, err := os.Stat(filepath.Join(dir, bareInvokeNoticeFlagFile)); err != nil {
		t.Errorf("flag file should be created, got %v", err)
	}

	// Second call must NOT print again.
	var buf2 bytes.Buffer
	maybePrintBareInvokeNotice(store, dir, &buf2)
	if buf2.Len() != 0 {
		t.Errorf("second call should not print, got %q", buf2.String())
	}
}

func TestMaybePrintBareInvokeNotice_NoSessionsSilentlyMarks(t *testing.T) {
	dir := t.TempDir()
	store := config.NewStore(dir)

	var buf bytes.Buffer
	t.Setenv("CLAUDE_CONTAINER_QUIET", "")
	maybePrintBareInvokeNotice(store, dir, &buf)

	if buf.Len() != 0 {
		t.Errorf("no-sessions case should print nothing, got %q", buf.String())
	}
	if _, err := os.Stat(filepath.Join(dir, bareInvokeNoticeFlagFile)); err != nil {
		t.Errorf("flag file should be created even with no sessions, got %v", err)
	}
}

func TestMaybePrintBareInvokeNotice_QuietEnvSuppresses(t *testing.T) {
	dir := t.TempDir()
	store := config.NewStore(dir)
	if err := store.Save(&config.Session{Name: "s1", CreatedAt: time.Now()}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	t.Setenv("CLAUDE_CONTAINER_QUIET", "1")
	var buf bytes.Buffer
	maybePrintBareInvokeNotice(store, dir, &buf)

	if buf.Len() != 0 {
		t.Errorf("CLAUDE_CONTAINER_QUIET should suppress notice, got %q", buf.String())
	}
	// Flag file should NOT be created — the notice was just suppressed,
	// not "shown." Future calls without QUIET still need to display it.
	if _, err := os.Stat(filepath.Join(dir, bareInvokeNoticeFlagFile)); err == nil {
		t.Error("flag file should not be created when notice is suppressed by env var")
	}
}
