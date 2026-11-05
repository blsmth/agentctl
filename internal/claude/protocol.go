package claude

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

// StreamMessage is an NDJSON line from the Claude CLI stream-json output.
type StreamMessage struct {
	Type          string         `json:"type"`
	Subtype       string         `json:"subtype,omitempty"`
	Message       *streamMsgBody `json:"message,omitempty"`
	Result        string         `json:"result,omitempty"`
	RateLimitInfo *RateLimitInfo `json:"rate_limit_info,omitempty"`
	SessionID     string         `json:"session_id,omitempty"`
}

// streamMsgBody is the nested "message" field in assistant stream messages.
type streamMsgBody struct {
	Role    string         `json:"role"`
	Content []contentBlock `json:"content"`
}

// RateLimitInfo holds rate-limit metadata emitted by the Claude CLI as a
// rate_limit_event stream message.
type RateLimitInfo struct {
	Status        string `json:"status"`
	ResetsAt      int64  `json:"resetsAt"`
	RateLimitType string `json:"rateLimitType"`
}

// userMessage is the NDJSON payload written to the subprocess stdin.
type userMessage struct {
	Type    string         `json:"type"`
	Message userMsgContent `json:"message"`
}

type userMsgContent struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// writeUserMessage writes an NDJSON user message to w.
func writeUserMessage(w io.Writer, content string) error {
	msg := userMessage{
		Type: "user",
		Message: userMsgContent{
			Role:    "user",
			Content: content,
		},
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal user message: %w", err)
	}
	data = append(data, '\n')
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("write user message: %w", err)
	}
	return nil
}

// readUntilResult reads NDJSON lines from scanner until a "result" message,
// returning all parsed messages including the result itself.
// Returns an error if the stream ends before a result message is seen.
func readUntilResult(scanner *bufio.Scanner) ([]StreamMessage, error) {
	var msgs []StreamMessage
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg StreamMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			// Skip malformed lines — the Claude CLI occasionally emits
			// progress lines that don't parse as valid stream messages.
			continue
		}
		msgs = append(msgs, msg)
		if msg.Type == "result" {
			return msgs, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return msgs, fmt.Errorf("scanner error: %w", err)
	}
	return msgs, fmt.Errorf("stream ended without result message")
}
