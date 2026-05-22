package cmd

import (
	"io"
	"os"
	"strings"
	"testing"
)

// TestPrintQuickstartHint asserts the hint wording survives refactors.
// Captures stdout via an os.Pipe so the helper's *os.File signature is
// exercised directly.
func TestPrintQuickstartHint(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	printQuickstartHint(w)
	w.Close()

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	got := string(out)

	for _, want := range []string{
		"Quickstart:",
		"cd <your repo> && claude-container",
		"Dashboard:",
		"claude-container tui",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("printQuickstartHint missing %q in output: %q", want, got)
		}
	}
}
