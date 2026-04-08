package transcript

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sampleJSONL returns a minimal but realistic Claude Code transcript in the
// same shape as the real file: one file-history-snapshot (to be skipped),
// a user message, an assistant thinking+text response, and a tool_use +
// tool_result pair.
func sampleJSONL() string {
	return strings.Join([]string{
		`{"type":"file-history-snapshot","messageId":"x"}`,
		`{"type":"user","timestamp":"2026-04-07T12:00:00Z","message":{"role":"user","content":"list the files"}}`,
		`{"type":"assistant","timestamp":"2026-04-07T12:00:01Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"I should run ls"},{"type":"text","text":"Sure, running ls."}]}}`,
		`{"type":"assistant","timestamp":"2026-04-07T12:00:02Z","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}`,
		`{"type":"user","timestamp":"2026-04-07T12:00:03Z","message":{"role":"user","content":[{"type":"tool_result","content":"README.md\ngo.mod"}]}}`,
		``,
	}, "\n")
}

func TestRenderMarkdown_BasicShape(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderMarkdown(strings.NewReader(sampleJSONL()), &buf, RenderOptions{}); err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	out := buf.String()

	for _, want := range []string{
		"# Claude Code transcript",
		"## user",
		"list the files",
		"## assistant",
		"Sure, running ls.",
		"### tool_use: Bash",
		"\"command\": \"ls\"",
		"### tool_result",
		"README.md",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected output to contain %q, got:\n%s", want, out)
		}
	}

	// Thinking should be OMITTED by default.
	if strings.Contains(out, "I should run ls") {
		t.Error("thinking block should be excluded by default")
	}
	// file-history-snapshot must be skipped entirely.
	if strings.Contains(out, "file-history-snapshot") {
		t.Error("file-history-snapshot should not appear in rendered output")
	}
}

func TestRenderMarkdown_IncludeThinking(t *testing.T) {
	var buf bytes.Buffer
	err := RenderMarkdown(strings.NewReader(sampleJSONL()), &buf,
		RenderOptions{IncludeThinking: true})
	if err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	if !strings.Contains(buf.String(), "I should run ls") {
		t.Error("thinking block should appear when IncludeThinking is true")
	}
}

func TestRenderMarkdown_StripsANSI(t *testing.T) {
	// An assistant text block with ANSI color codes. JSON string literals
	// must escape control bytes, so 0x1b becomes \u001b on the wire.
	line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"\u001b[31mred\u001b[0m text"}]}}`
	var buf bytes.Buffer
	if err := RenderMarkdown(strings.NewReader(line+"\n"), &buf, RenderOptions{}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, "\x1b") {
		t.Errorf("ANSI escapes should be stripped, got: %q", out)
	}
	if !strings.Contains(out, "red text") {
		t.Errorf("plain text should remain, got: %q", out)
	}
}

func TestRenderMarkdown_MalformedLineSkipped(t *testing.T) {
	input := "not json at all\n" +
		`{"type":"user","message":{"role":"user","content":"hi"}}` + "\n"
	var buf bytes.Buffer
	if err := RenderMarkdown(strings.NewReader(input), &buf, RenderOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "hi") {
		t.Error("good line should still render after a malformed one")
	}
}

func TestFindTranscript_ExactID(t *testing.T) {
	tmp := t.TempDir()
	// Simulate <claudeCfgDir>/projects/-workspace/<id>.jsonl
	dir := filepath.Join(tmp, "projects", "-workspace")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	id := "abc123"
	want := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(want, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := FindTranscript(FindOptions{ClaudeConfigDir: tmp, ResumeID: id})
	if err != nil {
		t.Fatalf("FindTranscript: %v", err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFindTranscript_FallbackNewest(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "projects", "-workspace")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	older := filepath.Join(dir, "older.jsonl")
	newer := filepath.Join(dir, "newer.jsonl")
	if err := os.WriteFile(older, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newer, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Force the mtime on "newer" to be strictly later.
	future := time.Now().Add(1 * time.Minute)
	if err := os.Chtimes(newer, future, future); err != nil {
		t.Fatal(err)
	}

	got, err := FindTranscript(FindOptions{ClaudeConfigDir: tmp})
	if err != nil {
		t.Fatalf("FindTranscript: %v", err)
	}
	if got != newer {
		t.Errorf("got %q, want %q (most-recently modified)", got, newer)
	}
}

func TestFindTranscript_MissingDir(t *testing.T) {
	tmp := t.TempDir()
	_, err := FindTranscript(FindOptions{ClaudeConfigDir: tmp})
	if err == nil {
		t.Error("expected error when projects/ doesn't exist")
	}
}

func TestEncodeHostCwd(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"/workspace", "-workspace"},
		{"/home/joe/Development/claude-container", "-home-joe-Development-claude-container"},
		{"", ""},
	}
	for _, c := range cases {
		if got := EncodeHostCwd(c.in); got != c.want {
			t.Errorf("EncodeHostCwd(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
