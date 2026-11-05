package toolinput

import (
	"strings"
	"testing"
)

func TestSummarize(t *testing.T) {
	longValue := strings.Repeat("é", 130) // multi-byte rune, to catch byte-slice truncation bugs

	tests := []struct {
		name     string
		toolName string
		input    string
		maxLen   int
		want     string
	}{
		{
			name:     "bash_single_line",
			toolName: "Bash",
			input:    `{"command":"go test ./..."}`,
			maxLen:   120,
			want:     "go test ./...",
		},
		{
			name:     "bash_multi_line_first_line_only",
			toolName: "Bash",
			input:    `{"command":"echo one\necho two\necho three"}`,
			maxLen:   120,
			want:     "echo one",
		},
		{
			name:     "read_file_path",
			toolName: "Read",
			input:    `{"file_path":"/tmp/foo.go"}`,
			maxLen:   120,
			want:     "/tmp/foo.go",
		},
		{
			name:     "write_file_path",
			toolName: "Write",
			input:    `{"file_path":"/tmp/bar.go","content":"package bar"}`,
			maxLen:   120,
			want:     "/tmp/bar.go",
		},
		{
			name:     "edit_file_path",
			toolName: "Edit",
			input:    `{"file_path":"/tmp/baz.go","old_string":"a","new_string":"b"}`,
			maxLen:   120,
			want:     "/tmp/baz.go",
		},
		{
			name:     "notebook_edit_file_path",
			toolName: "NotebookEdit",
			input:    `{"file_path":"/tmp/nb.ipynb"}`,
			maxLen:   120,
			want:     "/tmp/nb.ipynb",
		},
		{
			name:     "glob_pattern",
			toolName: "Glob",
			input:    `{"pattern":"**/*.go"}`,
			maxLen:   120,
			want:     "**/*.go",
		},
		{
			name:     "grep_pattern",
			toolName: "Grep",
			input:    `{"pattern":"TODO"}`,
			maxLen:   120,
			want:     "TODO",
		},
		{
			name:     "webfetch_url",
			toolName: "WebFetch",
			input:    `{"url":"https://example.com","prompt":"summarize"}`,
			maxLen:   120,
			want:     "https://example.com",
		},
		{
			name:     "websearch_query",
			toolName: "WebSearch",
			input:    `{"query":"golang generics"}`,
			maxLen:   120,
			want:     "golang generics",
		},
		{
			name:     "task_subagent_and_description",
			toolName: "Task",
			input:    `{"subagent_type":"go-engineer","description":"fix the bug","prompt":"..."}`,
			maxLen:   120,
			want:     "go-engineer: fix the bug",
		},
		{
			name:     "task_subagent_only",
			toolName: "Task",
			input:    `{"subagent_type":"go-engineer"}`,
			maxLen:   120,
			want:     "go-engineer",
		},
		{
			name:     "task_description_only",
			toolName: "Task",
			input:    `{"description":"fix the bug"}`,
			maxLen:   120,
			want:     "fix the bug",
		},
		{
			name:     "agent_alias_subagent_and_description",
			toolName: "Agent",
			input:    `{"subagent_type":"go-engineer","description":"fix the bug"}`,
			maxLen:   120,
			want:     "go-engineer: fix the bug",
		},
		{
			name:     "unknown_tool",
			toolName: "SomeFutureTool",
			input:    `{"anything":"value"}`,
			maxLen:   120,
			want:     "",
		},
		{
			name:     "empty_input",
			toolName: "Bash",
			input:    ``,
			maxLen:   120,
			want:     "",
		},
		{
			name:     "malformed_json",
			toolName: "Bash",
			input:    `{not valid json`,
			maxLen:   120,
			want:     "",
		},
		{
			name:     "missing_field",
			toolName: "Read",
			input:    `{"other_field":"x"}`,
			maxLen:   120,
			want:     "",
		},
		{
			name:     "long_value_truncated_rune_safe",
			toolName: "Grep",
			input:    `{"pattern":"` + longValue + `"}`,
			maxLen:   120,
			want:     strings.Repeat("é", 120),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Summarize(tt.toolName, []byte(tt.input), tt.maxLen)
			if got != tt.want {
				t.Errorf("Summarize(%q, %q, %d) = %q, want %q", tt.toolName, tt.input, tt.maxLen, got, tt.want)
			}
			if n := len([]rune(got)); n > tt.maxLen {
				t.Errorf("Summarize(%q, %q, %d) returned %d runes, want <= %d", tt.toolName, tt.input, tt.maxLen, n, tt.maxLen)
			}
		})
	}
}
