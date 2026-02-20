package cmd

import (
	"bufio"
	"encoding/json"
	"io"
)

// taskResult holds parsed data from a Claude stream-json session.
type taskResult struct {
	SessionID    string
	FinalText    string
	InputTokens  int
	OutputTokens int
}

// parseStreamEvents reads NDJSON lines from r and extracts the final
// assistant text, session ID, and token usage.
func parseStreamEvents(r io.Reader) taskResult {
	var res taskResult

	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var event struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`

			// system init event
			SessionID string `json:"session_id"`

			// assistant event
			Message struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`

			// result event
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}

		if err := json.Unmarshal(line, &event); err != nil {
			continue // skip malformed lines
		}

		switch event.Type {
		case "system":
			if event.Subtype == "init" && event.SessionID != "" {
				res.SessionID = event.SessionID
			}
		case "assistant":
			// Extract last text content block from this message.
			for i := len(event.Message.Content) - 1; i >= 0; i-- {
				if event.Message.Content[i].Type == "text" {
					res.FinalText = event.Message.Content[i].Text
					break
				}
			}
		case "result":
			if event.Usage.InputTokens > 0 {
				res.InputTokens = event.Usage.InputTokens
			}
			if event.Usage.OutputTokens > 0 {
				res.OutputTokens = event.Usage.OutputTokens
			}
		}
	}

	return res
}
