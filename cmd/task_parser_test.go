package cmd

import (
	"strings"
	"testing"
)

func TestParseStreamEvents(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"abc-123"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"I'll fix the tests now."}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Done. Here's what I changed."}]}}`,
		`{"type":"result","duration_ms":5000,"usage":{"input_tokens":1200,"output_tokens":450}}`,
	}, "\n")

	result := parseStreamEvents(strings.NewReader(input))

	if result.SessionID != "abc-123" {
		t.Errorf("SessionID = %q, want abc-123", result.SessionID)
	}
	if result.FinalText != "Done. Here's what I changed." {
		t.Errorf("FinalText = %q, want last assistant message", result.FinalText)
	}
	if result.InputTokens != 1200 {
		t.Errorf("InputTokens = %d, want 1200", result.InputTokens)
	}
	if result.OutputTokens != 450 {
		t.Errorf("OutputTokens = %d, want 450", result.OutputTokens)
	}
}

func TestParseStreamEventsEmpty(t *testing.T) {
	result := parseStreamEvents(strings.NewReader(""))
	if result.FinalText != "" {
		t.Errorf("FinalText = %q, want empty", result.FinalText)
	}
}

func TestParseStreamEventsMultiContent(t *testing.T) {
	input := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit"},{"type":"text","text":"All done."}]}}`

	result := parseStreamEvents(strings.NewReader(input))
	if result.FinalText != "All done." {
		t.Errorf("FinalText = %q, want 'All done.'", result.FinalText)
	}
}

func TestParseStreamEventsMalformedLines(t *testing.T) {
	input := strings.Join([]string{
		`not json`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"works"}]}}`,
		`{"broken`,
	}, "\n")

	result := parseStreamEvents(strings.NewReader(input))
	if result.FinalText != "works" {
		t.Errorf("FinalText = %q, want 'works'", result.FinalText)
	}
}

func TestParseStreamEventsResultTokensNested(t *testing.T) {
	input := `{"type":"result","usage":{"input_tokens":500,"output_tokens":200}}`
	result := parseStreamEvents(strings.NewReader(input))

	if result.InputTokens != 500 {
		t.Errorf("InputTokens = %d, want 500", result.InputTokens)
	}
	if result.OutputTokens != 200 {
		t.Errorf("OutputTokens = %d, want 200", result.OutputTokens)
	}
}
