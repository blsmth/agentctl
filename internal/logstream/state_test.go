package logstream

import (
	"strings"
	"testing"
)

// ── StateTracker ──────────────────────────────────────────────────────────────

func TestStateTracker_ToolUse(t *testing.T) {
	tracker := &StateTracker{}
	entry := Entry{
		Type: "assistant",
		Content: []ContentBlock{
			{Type: "tool_use", ToolName: "Bash", ToolInput: map[string]any{"command": "go build ./..."}},
		},
	}
	tracker.OnEntry(entry)

	if tracker.ToolName != "Bash" {
		t.Errorf("ToolName = %q, want %q", tracker.ToolName, "Bash")
	}
	if tracker.ToolDetail != "go build ./..." {
		t.Errorf("ToolDetail = %q, want %q", tracker.ToolDetail, "go build ./...")
	}
	if tracker.LastText != "" {
		t.Errorf("LastText = %q, want empty", tracker.LastText)
	}
	if tracker.LastType != "assistant" {
		t.Errorf("LastType = %q, want %q", tracker.LastType, "assistant")
	}
}

func TestStateTracker_TextBlock(t *testing.T) {
	tracker := &StateTracker{}
	entry := Entry{
		Type: "assistant",
		Content: []ContentBlock{
			{Type: "text", Text: "I'll help you with that task."},
		},
	}
	tracker.OnEntry(entry)

	if tracker.ToolName != "" {
		t.Errorf("ToolName = %q, want empty", tracker.ToolName)
	}
	if tracker.ToolDetail != "" {
		t.Errorf("ToolDetail = %q, want empty", tracker.ToolDetail)
	}
	if tracker.LastText != "I'll help you with that task." {
		t.Errorf("LastText = %q, want non-empty", tracker.LastText)
	}
}

func TestStateTracker_UserEntry(t *testing.T) {
	tracker := &StateTracker{}

	// First an assistant entry to set ToolName.
	tracker.OnEntry(Entry{
		Type: "assistant",
		Content: []ContentBlock{
			{Type: "tool_use", ToolName: "Read", ToolInput: map[string]any{"file_path": "/foo/bar.go"}},
		},
	})

	// Then a user entry — should update LastType but leave ToolName intact.
	tracker.OnEntry(Entry{Type: "user"})

	if tracker.LastType != "user" {
		t.Errorf("LastType = %q, want %q", tracker.LastType, "user")
	}
	// ToolName should be unchanged (not reset by a user entry).
	if tracker.ToolName != "Read" {
		t.Errorf("ToolName = %q, want %q", tracker.ToolName, "Read")
	}
}

func TestStateTracker_LastContentBlockWins(t *testing.T) {
	// If an assistant message has multiple blocks, the last one wins.
	tracker := &StateTracker{}
	entry := Entry{
		Type: "assistant",
		Content: []ContentBlock{
			{Type: "tool_use", ToolName: "Edit", ToolInput: map[string]any{"file_path": "/a.go"}},
			{Type: "text", Text: "Done editing."},
		},
	}
	tracker.OnEntry(entry)

	// The last block is text, so ToolName should be empty and LastText set.
	if tracker.ToolName != "" {
		t.Errorf("ToolName = %q, want empty (last block was text)", tracker.ToolName)
	}
	if tracker.LastText != "Done editing." {
		t.Errorf("LastText = %q, want %q", tracker.LastText, "Done editing.")
	}
}

func TestStateTracker_OnReset(t *testing.T) {
	tracker := &StateTracker{}
	tracker.OnEntry(Entry{
		Type: "assistant",
		Content: []ContentBlock{
			{Type: "tool_use", ToolName: "Bash", ToolInput: map[string]any{"command": "ls"}},
		},
	})

	tracker.OnReset()

	if tracker.ToolName != "" || tracker.ToolDetail != "" || tracker.LastText != "" || tracker.LastType != "" {
		t.Errorf("after OnReset, expected all fields empty; got ToolName=%q ToolDetail=%q LastText=%q LastType=%q",
			tracker.ToolName, tracker.ToolDetail, tracker.LastText, tracker.LastType)
	}
}

func TestStateTracker_ToolDetailTruncated(t *testing.T) {
	tracker := &StateTracker{}
	longCmd := strings.Repeat("a", 200)
	tracker.OnEntry(Entry{
		Type: "assistant",
		Content: []ContentBlock{
			{Type: "tool_use", ToolName: "Bash", ToolInput: map[string]any{"command": longCmd}},
		},
	})

	if len(tracker.ToolDetail) > 80 {
		t.Errorf("ToolDetail len = %d, want ≤80", len(tracker.ToolDetail))
	}
}

func TestStateTracker_LastTextTruncated(t *testing.T) {
	tracker := &StateTracker{}
	longText := strings.Repeat("b", 200)
	tracker.OnEntry(Entry{
		Type: "assistant",
		Content: []ContentBlock{
			{Type: "text", Text: longText},
		},
	})

	if len(tracker.LastText) > 100 {
		t.Errorf("LastText len = %d, want ≤100", len(tracker.LastText))
	}
}

func TestStateTracker_ToolDetailByToolName(t *testing.T) {
	tests := []struct {
		toolName  string
		input     map[string]any
		wantField string
	}{
		{"Bash", map[string]any{"command": "echo hi"}, "echo hi"},
		{"Edit", map[string]any{"file_path": "/a/b.go"}, "/a/b.go"},
		{"Read", map[string]any{"file_path": "/c/d.go"}, "/c/d.go"},
		{"Write", map[string]any{"file_path": "/e/f.go"}, "/e/f.go"},
		{"Grep", map[string]any{"pattern": "func.*Error"}, "func.*Error"},
		{"Agent", map[string]any{"description": "do something"}, "Agent"}, // default: tool name
		{"TodoRead", map[string]any{}, "TodoRead"},                        // default: tool name
	}

	for _, tt := range tests {
		t.Run(tt.toolName, func(t *testing.T) {
			tracker := &StateTracker{}
			tracker.OnEntry(Entry{
				Type: "assistant",
				Content: []ContentBlock{
					{Type: "tool_use", ToolName: tt.toolName, ToolInput: tt.input},
				},
			})
			if tracker.ToolDetail != tt.wantField {
				t.Errorf("ToolDetail = %q, want %q", tracker.ToolDetail, tt.wantField)
			}
		})
	}
}

// ── flattenWhitespace / truncateState ────────────────────────────────────────

func TestFlattenWhitespace(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"embedded newline", "Done. Summary:\n\n1. **Task 1** — did the thing", "Done. Summary: 1. **Task 1** — did the thing"},
		{"crlf", "line one\r\nline two\r\nline three", "line one line two line three"},
		{"tabs", "col1\tcol2\tcol3", "col1 col2 col3"},
		{"runs of multiple spaces", "a    b     c", "a b c"},
		{"leading and trailing whitespace", "   trim me   ", "trim me"},
		{"only whitespace yields empty not a lone space", "   \n\t\r\n  ", ""},
		{"empty string", "", ""},
		{"already flat", "single line text", "single line text"},
		{"mixed whitespace kinds", "a\n\tb \r\n c", "a b c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := flattenWhitespace(tt.in)
			if got != tt.want {
				t.Errorf("flattenWhitespace(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestTruncateState(t *testing.T) {
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"under limit unchanged", "short", 80, "short"},
		{"exact limit unchanged", "12345", 5, "12345"},
		{"over limit truncated", "123456789", 5, "12345"},
		{"empty string", "", 10, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateState(tt.in, tt.n)
			if got != tt.want {
				t.Errorf("truncateState(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
			}
		})
	}
}

// TestFlattenThenTruncate_Ordering guards the "flatten first, then truncate"
// rule from the ticket: a long multi-line string must flatten to a single
// line BEFORE truncation, not after — otherwise truncating first collapses
// nearly all 100 chars into leading whitespace/newlines and the flattened
// result is far shorter than the limit.
func TestFlattenThenTruncate_Ordering(t *testing.T) {
	// 150 newlines followed by 150 'x' characters. If truncated first (to say
	// 100 bytes) the result would be 100 newlines, which flattens to "". The
	// correct order — flatten then truncate — collapses the newlines to a
	// single leading space, trims it, and truncates the 'x' run to the limit.
	longMultiline := strings.Repeat("\n", 150) + strings.Repeat("x", 150)

	tracker := &StateTracker{}
	tracker.OnEntry(Entry{
		Type: "assistant",
		Content: []ContentBlock{
			{Type: "text", Text: longMultiline},
		},
	})

	if tracker.LastText == "" {
		t.Fatalf("LastText is empty; flatten must have run before truncate, not after")
	}
	if strings.ContainsAny(tracker.LastText, "\n\r\t") {
		t.Errorf("LastText contains raw whitespace: %q", tracker.LastText)
	}
	if len(tracker.LastText) > 100 {
		t.Errorf("LastText len = %d, want ≤100", len(tracker.LastText))
	}
	want := strings.Repeat("x", 100)
	if tracker.LastText != want {
		t.Errorf("LastText = %q, want %q", tracker.LastText, want)
	}
}

// TestToolDetail_FlattensEmbeddedNewline covers the ticket's explicit
// callout: a Bash command with an embedded newline must not break the
// one-row-per-agent layout either.
func TestToolDetail_FlattensEmbeddedNewline(t *testing.T) {
	tracker := &StateTracker{}
	tracker.OnEntry(Entry{
		Type: "assistant",
		Content: []ContentBlock{
			{Type: "tool_use", ToolName: "Bash", ToolInput: map[string]any{"command": "echo one\necho two\necho three"}},
		},
	})

	want := "echo one echo two echo three"
	if tracker.ToolDetail != want {
		t.Errorf("ToolDetail = %q, want %q", tracker.ToolDetail, want)
	}
}

func TestStateTracker_ViaStream(t *testing.T) {
	// Verify that StateTracker integrates correctly with Stream.
	line := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"x","name":"Bash","input":{"command":"go test ./..."}}],"usage":{"input_tokens":1000,"output_tokens":10,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`

	f := writeTempFile(t, line)

	stream := NewStream(f)
	tracker := &StateTracker{}
	stream.Subscribe(tracker)
	stream.Tick()

	if tracker.ToolName != "Bash" {
		t.Errorf("ToolName = %q, want %q", tracker.ToolName, "Bash")
	}
	if tracker.ToolDetail != "go test ./..." {
		t.Errorf("ToolDetail = %q, want %q", tracker.ToolDetail, "go test ./...")
	}
}
