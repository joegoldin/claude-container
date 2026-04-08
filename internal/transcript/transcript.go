// Package transcript renders Claude Code session transcripts (JSONL) into
// human-readable text, and resolves where those transcripts live on disk
// for a given claude-container session.
//
// Claude Code writes one JSONL file per session to
//
//	<CLAUDE_CONFIG_DIR>/projects/<encoded-cwd>/<session-uuid>.jsonl
//
// where <encoded-cwd> is the container's working directory with `/` replaced
// by `-` (e.g. `/workspace` becomes `-workspace`). Each line is a JSON
// object describing one turn: a user message, an assistant message, a tool
// call, a tool result, or a Claude Code internal entry like a file-history
// snapshot that we skip.
//
// Rendering is deliberately self-contained: no LLM involved. We decode each
// line, walk the message content, strip ANSI escape sequences, and emit
// plain markdown. This keeps the text mode of `claude-container extract`
// safe from any prompt injection that may have landed in tool output — the
// bytes are just copied through as quoted markdown code blocks.
package transcript

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ansiRe matches ANSI escape sequences. We strip these when rendering text
// so copy/paste into other tools doesn't smuggle cursor movement or color
// codes. It's the same pattern used in internal/docker and internal/proxy.
var ansiRe = regexp.MustCompile(`\x1b(?:\[[0-9;?]*[a-zA-Z]|\][^\x07]*\x07|\([A-Z])`)

// stripANSI removes ANSI escape sequences from s.
func stripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

// FindOptions controls how FindTranscript locates a .jsonl file.
type FindOptions struct {
	// ClaudeConfigDir is the host-side claude config dir the container
	// mounts at /claude (i.e. <configDir>/claude-config).
	ClaudeConfigDir string
	// ResumeID is the session UUID. If empty, FindTranscript falls back
	// to scanning projects/ for .jsonl files and returning the most
	// recently modified one — useful when saveResumeID never ran.
	ResumeID string
}

// FindTranscript returns the path to the JSONL transcript file for the
// given session, or an error if it cannot be located.
func FindTranscript(opts FindOptions) (string, error) {
	projectsDir := filepath.Join(opts.ClaudeConfigDir, "projects")
	if _, err := os.Stat(projectsDir); err != nil {
		return "", fmt.Errorf("transcript: no projects dir at %s", projectsDir)
	}

	// Preferred path: exact match on the session UUID anywhere under
	// projects/*/<uuid>.jsonl.
	if opts.ResumeID != "" {
		var found string
		err := filepath.WalkDir(projectsDir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil // swallow per-entry errors; best-effort search
			}
			if d.IsDir() {
				return nil
			}
			if filepath.Base(path) == opts.ResumeID+".jsonl" {
				found = path
				return filepath.SkipAll
			}
			return nil
		})
		if err == nil && found != "" {
			return found, nil
		}
		return "", fmt.Errorf("transcript: no file %s.jsonl under %s", opts.ResumeID, projectsDir)
	}

	// Fallback: pick the most-recently modified .jsonl file.
	var best string
	var bestMod int64
	err := filepath.WalkDir(projectsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.ModTime().UnixNano() > bestMod {
			best = path
			bestMod = info.ModTime().UnixNano()
		}
		return nil
	})
	if err != nil || best == "" {
		return "", fmt.Errorf("transcript: no .jsonl files found under %s", projectsDir)
	}
	return best, nil
}

// RenderOptions controls markdown rendering behavior.
type RenderOptions struct {
	// IncludeThinking includes assistant "thinking" content blocks in the
	// output. Default off because thinking is usually noisy and contains
	// the model's internal deliberation rather than the conversation.
	IncludeThinking bool
}

// RenderMarkdown reads a JSONL transcript from r and writes a markdown
// transcript to w. It is tolerant of unknown record shapes: anything it
// doesn't understand is skipped rather than producing an error.
func RenderMarkdown(r io.Reader, w io.Writer, opts RenderOptions) error {
	scanner := bufio.NewScanner(r)
	// Claude Code transcripts can contain very long lines (large tool
	// outputs); bump the scanner's buffer accordingly.
	scanner.Buffer(make([]byte, 1<<20), 32<<20)

	fmt.Fprintln(w, "# Claude Code transcript")
	fmt.Fprintln(w)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal(line, &entry); err != nil {
			// Malformed line — skip, don't fail the whole render.
			continue
		}
		renderEntry(w, entry, opts)
	}
	return scanner.Err()
}

func renderEntry(w io.Writer, entry map[string]any, opts RenderOptions) {
	// Skip internal record types that aren't part of the user-visible
	// conversation (file-history snapshots, etc.). We could whitelist
	// known types, but blacklisting is easier to keep up to date.
	switch entry["type"] {
	case "file-history-snapshot", "tool-cancel":
		return
	}

	ts, _ := entry["timestamp"].(string)

	msg, ok := entry["message"].(map[string]any)
	if !ok {
		return
	}
	role, _ := msg["role"].(string)
	if role == "" {
		return
	}

	fmt.Fprintf(w, "## %s", role)
	if ts != "" {
		fmt.Fprintf(w, " — %s", ts)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w)

	// content is either a bare string (simple user messages) or an array
	// of content blocks (assistant responses, tool use/result).
	switch content := msg["content"].(type) {
	case string:
		fmt.Fprintln(w, stripANSI(content))
		fmt.Fprintln(w)
	case []any:
		for _, block := range content {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}
			renderBlock(w, b, opts)
		}
	}
}

func renderBlock(w io.Writer, block map[string]any, opts RenderOptions) {
	blockType, _ := block["type"].(string)
	switch blockType {
	case "text":
		if text, ok := block["text"].(string); ok {
			fmt.Fprintln(w, stripANSI(text))
			fmt.Fprintln(w)
		}
	case "thinking":
		if !opts.IncludeThinking {
			return
		}
		if text, ok := block["thinking"].(string); ok {
			fmt.Fprintln(w, "> _thinking:_")
			for _, line := range strings.Split(stripANSI(text), "\n") {
				fmt.Fprintf(w, "> %s\n", line)
			}
			fmt.Fprintln(w)
		}
	case "tool_use":
		name, _ := block["name"].(string)
		fmt.Fprintf(w, "### tool_use: %s\n\n", name)
		if input, ok := block["input"]; ok {
			inputJSON, err := json.MarshalIndent(input, "", "  ")
			if err == nil {
				fmt.Fprintln(w, "```json")
				fmt.Fprintln(w, stripANSI(string(inputJSON)))
				fmt.Fprintln(w, "```")
				fmt.Fprintln(w)
			}
		}
	case "tool_result":
		fmt.Fprintln(w, "### tool_result")
		fmt.Fprintln(w)
		// tool_result.content is either a string, or an array of
		// {type:"text", text:"..."} blocks.
		switch rc := block["content"].(type) {
		case string:
			fmt.Fprintln(w, "```")
			fmt.Fprintln(w, stripANSI(rc))
			fmt.Fprintln(w, "```")
			fmt.Fprintln(w)
		case []any:
			for _, inner := range rc {
				m, ok := inner.(map[string]any)
				if !ok {
					continue
				}
				if text, ok := m["text"].(string); ok {
					fmt.Fprintln(w, "```")
					fmt.Fprintln(w, stripANSI(text))
					fmt.Fprintln(w, "```")
					fmt.Fprintln(w)
				}
			}
		}
	}
}

// EncodeHostCwd returns the claude-code-style encoding of a host cwd, which
// is the string with every `/` replaced by `-`. This is the directory name
// under ~/.claude/projects/ that a host `claude --resume` expects for
// sessions started in that cwd. The function exists to help the extract
// command tell the user where to drop the .jsonl if they want to resume.
func EncodeHostCwd(cwd string) string {
	return strings.ReplaceAll(cwd, string(filepath.Separator), "-")
}
