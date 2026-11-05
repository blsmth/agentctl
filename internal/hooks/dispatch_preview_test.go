package hooks

import (
	"testing"
)

func TestIsAgentctlDispatch(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    bool
	}{
		{
			name:    "bare agentctl dispatch",
			command: "agentctl dispatch --issue 42",
			want:    true,
		},
		{
			name:    "absolute path agentctl dispatch",
			command: "/home/user/.local/bin/agentctl dispatch --issue 42",
			want:    true,
		},
		{
			name:    "home-relative path",
			command: "/Users/alice/.local/bin/agentctl dispatch --agent go-engineer",
			want:    true,
		},
		{
			name:    "agentctl dispatch no flags",
			command: "agentctl dispatch",
			want:    true,
		},
		{
			name:    "not a dispatch command",
			command: "agentctl monitor",
			want:    false,
		},
		{
			name:    "empty command",
			command: "",
			want:    false,
		},
		{
			name:    "some other bash command",
			command: "git push origin main",
			want:    false,
		},
		{
			name:    "agentctl but not dispatch",
			command: "agentctl signal done",
			want:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isAgentctlDispatch(tt.command)
			if got != tt.want {
				t.Errorf("isAgentctlDispatch(%q) = %v, want %v", tt.command, got, tt.want)
			}
		})
	}
}

func TestTokenizeCommand(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "simple tokens",
			input: "agentctl dispatch --issue 42",
			want:  []string{"agentctl", "dispatch", "--issue", "42"},
		},
		{
			name:  "single-quoted value",
			input: "agentctl dispatch --prompt 'do something cool'",
			want:  []string{"agentctl", "dispatch", "--prompt", "do something cool"},
		},
		{
			name:  "double-quoted value",
			input: `agentctl dispatch --prompt "fix the bug"`,
			want:  []string{"agentctl", "dispatch", "--prompt", "fix the bug"},
		},
		{
			name:  "empty string",
			input: "",
			want:  nil,
		},
		{
			name:  "multiple spaces between tokens",
			input: "agentctl  dispatch   --issue  42",
			want:  []string{"agentctl", "dispatch", "--issue", "42"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tokenizeCommand(tt.input)
			if len(got) != len(tt.want) {
				t.Errorf("tokenizeCommand(%q) = %v, want %v", tt.input, got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("tokenizeCommand(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestParseDispatchFlags(t *testing.T) {
	tests := []struct {
		name          string
		command       string
		wantAgent     string
		wantIssue     int
		wantPrompt    string
		wantPromptKey string
	}{
		{
			name:      "long flags with issue and agent",
			command:   "agentctl dispatch --issue 42 --agent go-engineer",
			wantAgent: "go-engineer",
			wantIssue: 42,
		},
		{
			name:      "short flags with issue and agent",
			command:   "agentctl dispatch -i 42 -a python-engineer",
			wantAgent: "python-engineer",
			wantIssue: 42,
		},
		{
			name:       "prompt only",
			command:    "agentctl dispatch --prompt 'fix the login bug'",
			wantPrompt: "fix the login bug",
		},
		{
			name:       "prompt with double quotes",
			command:    `agentctl dispatch --prompt "fix the login bug"`,
			wantPrompt: "fix the login bug",
		},
		{
			name:      "equal-sign form for agent",
			command:   "agentctl dispatch --agent=go-engineer --issue=99",
			wantAgent: "go-engineer",
			wantIssue: 99,
		},
		{
			name:      "absolute path binary",
			command:   "/home/user/.local/bin/agentctl dispatch --issue 10 --agent architect",
			wantAgent: "architect",
			wantIssue: 10,
		},
		{
			name:    "no flags",
			command: "agentctl dispatch",
		},
		{
			name:       "issue and prompt together",
			command:    "agentctl dispatch --issue 7 --agent go-engineer --prompt 'extra context'",
			wantAgent:  "go-engineer",
			wantIssue:  7,
			wantPrompt: "extra context",
		},
		{
			name:          "long flag prompt-key",
			command:       "agentctl dispatch --prompt-key spec-42 --agent go-engineer",
			wantAgent:     "go-engineer",
			wantPromptKey: "spec-42",
		},
		{
			name:          "short flag prompt-key",
			command:       "agentctl dispatch -k spec-42 -a go-engineer",
			wantAgent:     "go-engineer",
			wantPromptKey: "spec-42",
		},
		{
			name:          "equal-sign form for prompt-key",
			command:       "agentctl dispatch --prompt-key=spec-42 --agent go-engineer",
			wantAgent:     "go-engineer",
			wantPromptKey: "spec-42",
		},
		{
			name:          "prompt-key equal-sign form does not populate prompt",
			command:       "agentctl dispatch --prompt-key=x --agent go-engineer",
			wantAgent:     "go-engineer",
			wantPrompt:    "",
			wantPromptKey: "x",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotAgent, gotIssue, gotPrompt, gotPromptKey := parseDispatchFlags(tt.command)
			if gotAgent != tt.wantAgent {
				t.Errorf("agent = %q, want %q", gotAgent, tt.wantAgent)
			}
			if gotIssue != tt.wantIssue {
				t.Errorf("issue = %d, want %d", gotIssue, tt.wantIssue)
			}
			if gotPrompt != tt.wantPrompt {
				t.Errorf("prompt = %q, want %q", gotPrompt, tt.wantPrompt)
			}
			if gotPromptKey != tt.wantPromptKey {
				t.Errorf("promptKey = %q, want %q", gotPromptKey, tt.wantPromptKey)
			}
		})
	}
}

func TestIsAgentctlDispatch_Segments(t *testing.T) {
	tests := []struct {
		command string
		want    bool
	}{
		{"agentctl dispatch --help", false},
		{"agentctl dispatch -h", false},
		{"agentctl dispatch --help 2>&1 | head -40", false},
		{"agentctl dispatch --help 2>&1 | head -40; git -C /x log", false},
		{"agentctl dispatch --help; git log", false},
		{"agentctl dispatch --agent go-engineer --prompt foo", true},
		{"/abs/path/agentctl dispatch --issue 3", true},
		{"git pull && agentctl dispatch --issue 3", true},
		{"agentctl dispatch", true},
		{`agentctl dispatch --prompt "--help is a flag"`, true},
		{`agentctl dispatch --prompt --help`, true},
		{"agentctl dispatch --issue 3; git log -h", true},
		{"agentctl dispatch --help; agentctl dispatch --issue 3", true},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			if got := isAgentctlDispatch(tt.command); got != tt.want {
				t.Errorf("isAgentctlDispatch(%q) = %v, want %v", tt.command, got, tt.want)
			}
		})
	}
}

func TestParseDispatchFlags_Segment(t *testing.T) {
	tests := []struct {
		name       string
		command    string
		wantAgent  string
		wantIssue  int
		wantPrompt string
	}{
		{"after semicolon", "agentctl dispatch --issue 5; git log -p foo", "", 5, ""},
		{"after &&", "agentctl dispatch --issue 5 && git log -p foo", "", 5, ""},
		{"after pipe", "agentctl dispatch --issue 5 | grep -a x", "", 5, ""},
		{"attached terminator", "agentctl dispatch --issue 5; git log", "", 5, ""},
		{"attached terminator on value", "agentctl dispatch --agent foo; x", "foo", 0, ""},
		{"flags before dispatch ignored", "git log -p x && agentctl dispatch --issue 2", "", 2, ""},
		{"redirection ignored", "agentctl dispatch --issue 5 2>&1", "", 5, ""},
		{"quoted semicolon kept", `agentctl dispatch --prompt "a; b" --agent x`, "x", 0, "a; b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, i, p, _ := parseDispatchFlags(tt.command)
			if a != tt.wantAgent || i != tt.wantIssue || p != tt.wantPrompt {
				t.Errorf("got (%q,%d,%q), want (%q,%d,%q)", a, i, p, tt.wantAgent, tt.wantIssue, tt.wantPrompt)
			}
		})
	}
}
