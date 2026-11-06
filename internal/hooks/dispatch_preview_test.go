package hooks

import (
	"strings"
	"testing"

	ctxstore "github.com/blsmth/agentctl/internal/context"
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

// isolateContextStore points the context store at a temp dir and returns a
// function that seeds an entry.
func isolateContextStore(t *testing.T) func(key, content string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_DATA_HOME", dir+"/data")
	t.Setenv("XDG_CONFIG_HOME", dir+"/config")
	t.Setenv("XDG_STATE_HOME", dir+"/state")
	return func(key, content string) {
		t.Helper()
		ctxstore.Init()
		if err := ctxstore.Set(key, ctxstore.Entry{Content: content}); err != nil {
			t.Fatalf("seeding store: %v", err)
		}
	}
}

func TestResolvePromptKey(t *testing.T) {
	tests := []struct {
		name    string
		seed    map[string]string
		command string
		want    string
		wantOK  bool
	}{
		{
			name:    "chained set resolves",
			command: `agentctl context set k "plain text" && agentctl dispatch --agent a --prompt-key k`,
			want:    "plain text", wantOK: true,
		},
		{
			name:    "single-quoted multi-line",
			command: "agentctl context set k 'line one\nline two $HOME' && agentctl dispatch --agent a --prompt-key k",
			want:    "line one\nline two $HOME", wantOK: true,
		},
		{
			name:    "double-quoted command substitution is unresolved",
			command: `agentctl context set k "see $(cat x)" && agentctl dispatch --agent a --prompt-key k`,
		},
		{
			name:    "double-quoted variable is unresolved",
			command: `agentctl context set k "hi $VAR" && agentctl dispatch --agent a --prompt-key k`,
		},
		{
			name:    "backtick is unresolved",
			command: "agentctl context set k \"hi `id`\" && agentctl dispatch --agent a --prompt-key k",
		},
		{
			name:    "inline set unresolved does not fall back to the store",
			seed:    map[string]string{"k": "stored"},
			command: `agentctl context set k "$X" && agentctl dispatch --agent a --prompt-key k`,
		},
		{
			name:    "store used when no inline set",
			seed:    map[string]string{"k": "stored"},
			command: `agentctl dispatch --agent a --prompt-key k`,
			want:    "stored", wantOK: true,
		},
		{
			name:    "inline set wins over store",
			seed:    map[string]string{"k": "stored"},
			command: `agentctl context set k "fresh" && agentctl dispatch --agent a --prompt-key k`,
			want:    "fresh", wantOK: true,
		},
		{
			name:    "missing everywhere",
			command: `agentctl dispatch --agent a --prompt-key k`,
		},
		{
			name:    "different key in the chain is not used",
			command: `agentctl context set other "x" && agentctl dispatch --agent a --prompt-key k`,
		},
		{
			name:    "set that is only an argument of another command is not used",
			command: `echo agentctl context set k "x" && agentctl dispatch --agent a --prompt-key k`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seed := isolateContextStore(t)
			for k, v := range tt.seed {
				seed(k, v)
			}
			got, ok := resolvePromptKey("k", tt.command)
			if ok != tt.wantOK || got != tt.want {
				t.Errorf("resolvePromptKey = (%q, %v), want (%q, %v)", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestDispatchPreviewGate_UnresolvedPromptKeyDenies(t *testing.T) {
	isolateContextStore(t)
	t.Setenv("TMUX", "/tmp/fake-tmux-socket")
	t.Setenv("AGENTCTL_SESSION_ID", "sess-promptkey-deny")
	command := "agentctl dispatch --agent go-engineer --prompt-key nokey"
	removeDispatchLockForTest(t, command)
	failIfDispatchPopupRunnerCalled(t)

	approved, decided, reason := dispatchPreviewGate("lead", command)
	if approved || decided {
		t.Errorf("got approved=%v decided=%v, want false, false", approved, decided)
	}
	for _, want := range []string{"nokey", "not found", "separate Bash call"} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason %q missing %q", reason, want)
		}
	}
}
