package harness

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/logstream"
	"github.com/blsmth/agentctl/internal/panestate"
)

func TestPiCommand(t *testing.T) {
	tests := []struct {
		name    string
		inv     Invocation
		want    []string
		wantErr bool
	}{
		{
			name: "dispatch launch",
			inv: Invocation{
				Agent: "go-engineer", Name: "go-engineer-proj-1", SessionID: "sid",
				Model: "anthropic/claude-sonnet-4-5", Effort: "high",
				ExtensionPath: "/rt/launch/agentctl-pi.ts", SystemPromptFile: "/rt/launch/adhoc-sys-1.txt",
				Message: "say hi",
			},
			want: []string{
				"pi", "--session-id", "sid", "-n", "go-engineer-proj-1",
				"--model", "anthropic/claude-sonnet-4-5", "--thinking", "high",
				"-e", "/rt/launch/agentctl-pi.ts", "--append-system-prompt", "/rt/launch/adhoc-sys-1.txt",
				"--", "say hi",
			},
		},
		{
			name: "bare launch: no message, no model",
			inv:  Invocation{Agent: "a", Name: "a-p-1", SessionID: "sid", ExtensionPath: "/e.ts"},
			want: []string{"pi", "--session-id", "sid", "-n", "a-p-1", "-e", "/e.ts"},
		},
		{
			name: "resume reuses --session-id",
			inv:  Invocation{Agent: "a", Resume: "old", ExtensionPath: "/e.ts"},
			want: []string{"pi", "--session-id", "old", "-e", "/e.ts"},
		},
		{
			name: "fork keeps the new id and forks the old",
			inv:  Invocation{Agent: "a", SessionID: "new", Resume: "old", ForkSession: true},
			want: []string{"pi", "--session-id", "new", "--fork", "old"},
		},
		{
			name:    "fork without a source",
			inv:     Invocation{Agent: "a", ForkSession: true},
			wantErr: true,
		},
		{
			name: "auto and unknown efforts leave pi's default",
			inv:  Invocation{Agent: "a", SessionID: "s", Effort: "auto"},
			want: []string{"pi", "--session-id", "s"},
		},
		{
			name: "claude-only fields are ignored",
			inv:  Invocation{Agent: "a", SessionID: "s", PermissionMode: "auto", SettingsPath: "/s.json"},
			want: []string{"pi", "--session-id", "s"},
		},
		{
			name: "dash-led and hostile message stays one argument",
			inv:  Invocation{Agent: "a", SessionID: "s", Message: "-rf $(touch /tmp/pwned) 'x'"},
			want: []string{"pi", "--session-id", "s", "--", "-rf $(touch /tmp/pwned) 'x'"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := For(Pi).Command(tt.inv)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Command = %q, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if argv := fields(t, got); !slices.Equal(argv, tt.want) {
				t.Errorf("Command() = %s\n tokenizes to %q\n want         %q", got, argv, tt.want)
			}
		})
	}
}

func TestPiThinkingCoversEfforts(t *testing.T) {
	for _, e := range []string{"low", "medium", "high", "xhigh", "max"} {
		if piThinking[e] == "" {
			t.Errorf("effort %q has no pi thinking level", e)
		}
	}
}

func TestPiRestoreCommand(t *testing.T) {
	inv := Invocation{Agent: "a", Name: "n", ExtensionPath: "/e.ts"}
	got, err := For(Pi).RestoreCommand(inv, "sid", true)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"pi", "--session-id", "sid", "-n", "n", "-e", "/e.ts"}; !slices.Equal(fields(t, got), want) {
		t.Errorf("resume = %q, want %q", fields(t, got), want)
	}
	fresh, err := For(Pi).RestoreCommand(inv, "sid", false)
	if err != nil {
		t.Fatal(err)
	}
	argv := fields(t, fresh)
	if len(argv) < 3 || argv[1] != "--session-id" || argv[2] == "sid" || argv[2] == "" {
		t.Errorf("fresh restore = %q, want a new --session-id", argv)
	}
	if strings.Contains(fresh, "||") {
		t.Errorf("pi restore has a fallback chain: %q", fresh)
	}
}

func TestPiDecodeHook(t *testing.T) {
	h := For(Pi)
	tests := []struct {
		name string
		hook string
		in   string
		want map[string]any
	}{
		{
			name: "session start links the session file",
			hook: "hook-session-start",
			in:   `{"session_id":"sid","session_file":"/s/2026_sid.jsonl","cwd":"/w","reason":"startup"}`,
			want: map[string]any{"session_id": "sid", "cwd": "/w", "transcript_path": "/s/2026_sid.jsonl"},
		},
		{
			name: "bash tool call maps to Bash",
			hook: "hook-validate-bash",
			in:   `{"session_id":"sid","tool_name":"bash","input":{"command":"git push origin main"}}`,
			want: map[string]any{"session_id": "sid", "tool_name": "Bash", "tool_input": map[string]any{"command": "git push origin main"}},
		},
		{
			name: "read path becomes file_path",
			hook: "hook-pre-tool",
			in:   `{"tool_name":"read","input":{"path":"/w/a.go"}}`,
			want: map[string]any{"tool_name": "Read", "tool_input": map[string]any{"path": "/w/a.go", "file_path": "/w/a.go"}},
		},
		{
			name: "bash result is stdout",
			hook: "hook-post-tool",
			in:   `{"tool_name":"bash","input":{"command":"ls"},"content":"a\nb"}`,
			want: map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}, "tool_response": map[string]any{"stdout": "a\nb"}},
		},
		{
			name: "other tool result is output",
			hook: "hook-post-tool",
			in:   `{"tool_name":"edit","input":{"path":"x"},"content":"ok"}`,
			want: map[string]any{"tool_name": "Edit", "tool_input": map[string]any{"path": "x", "file_path": "x"}, "tool_response": map[string]any{"output": "ok"}},
		},
		{
			name: "failed tool carries the error",
			hook: "hook-post-tool-failure",
			in:   `{"tool_name":"bash","input":{"command":"false"},"content":"exit 1","is_error":true}`,
			want: map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "false"}, "error": "exit 1"},
		},
		{
			name: "turn end",
			hook: "hook-check-done",
			in:   `{"session_id":"sid","last_assistant_message":"done","stop_hook_active":true}`,
			want: map[string]any{"session_id": "sid", "last_assistant_message": "done", "stop_hook_active": true},
		},
		{
			name: "quit is a terminal session end",
			hook: "hook-session-end",
			in:   `{"session_id":"sid","reason":"quit"}`,
			want: map[string]any{"session_id": "sid", "reason": "prompt_input_exit"},
		},
		{
			name: "new session is not terminal",
			hook: "hook-session-end",
			in:   `{"reason":"new"}`,
			want: map[string]any{"reason": "clear"},
		},
		{
			name: "unknown reason",
			hook: "hook-session-end",
			in:   `{"reason":"whatever"}`,
			want: map[string]any{"reason": "other"},
		},
		{
			name: "empty payload",
			hook: "hook-user-prompt",
			in:   ``,
			want: map[string]any{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev, err := h.DecodeHook(tt.hook, []byte(tt.in))
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(ev.Raw, &got); err != nil {
				t.Fatalf("canonical payload %s: %v", ev.Raw, err)
			}
			wantJSON, _ := json.Marshal(tt.want)
			gotJSON, _ := json.Marshal(got)
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("canonical = %s\nwant        %s", gotJSON, wantJSON)
			}
		})
	}
}

func TestPiDecodeHookRejectsGarbage(t *testing.T) {
	if _, err := For(Pi).DecodeHook("hook-pre-tool", []byte("{not json")); err == nil {
		t.Error("want error for malformed payload")
	}
}

func TestPiEncodeDecision(t *testing.T) {
	h := For(Pi)
	tests := []struct {
		name string
		d    Decision
		want string
	}{
		{"deny", Decision{Kind: Deny, HookEvent: "PreToolUse", Reason: "nope"}, `{"block":true,"reason":"nope"}` + "\n"},
		{"allow", Decision{Kind: Allow, Reason: "ok"}, `{"block":false}` + "\n"},
		{"context", Decision{Kind: Context, HookEvent: "PostToolUse", Text: "mail"}, `{"context":"mail"}` + "\n"},
		{"no session context", Decision{Kind: SessionContext, Text: "hi"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(h.EncodeDecision(tt.d)); got != tt.want {
				t.Errorf("EncodeDecision = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPiPrepareWorkspaceWritesExtension(t *testing.T) {
	dir := t.TempDir()
	// A stale file, and a symlink planted at the destination, are replaced
	// rather than written through.
	target := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, PiExtensionFile)); err != nil {
		t.Fatal(err)
	}
	prep, err := For(Pi).PrepareWorkspace(Workspace{Dir: dir, WriteSettings: func() (string, error) {
		t.Fatal("pi must not write Claude settings")
		return "", nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if prep.ExtensionPath != filepath.Join(dir, PiExtensionFile) || prep.SettingsPath != "" {
		t.Fatalf("Prepared = %+v", prep)
	}
	got, err := os.ReadFile(prep.ExtensionPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, PiExtensionSource()) {
		t.Error("written extension differs from the embedded one")
	}
	if fi, _ := os.Lstat(prep.ExtensionPath); fi.Mode()&os.ModeSymlink != 0 {
		t.Error("extension path is still a symlink")
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Error("write followed the planted symlink")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".agentctl-pi-*")); len(left) != 0 {
		t.Errorf("temp files left behind: %v", left)
	}
	if _, err := For(Pi).PrepareWorkspace(Workspace{}); err == nil {
		t.Error("want error with no directory")
	}
}

// TestPiExtensionContract checks the embedded bridge against the contract
// the Go side relies on: every hook it calls exists, it passes --harness pi,
// blocks on exit 2 or {"block":true}, and uses only node builtins.
func TestPiExtensionContract(t *testing.T) {
	src := string(PiExtensionSource())
	for _, s := range []string{
		`"--harness", "pi"`,
		`"hook-session-start"`, `"hook-user-prompt"`, `"hook-pre-tool"`, `"hook-validate-bash"`,
		`"hook-post-tool"`, `"hook-post-tool-failure"`, `"hook-check-done"`, `"hook-session-end"`,
		`pi.on("session_start"`, `pi.on("agent_start"`, `pi.on("tool_call"`, `pi.on("tool_result"`,
		`pi.on("agent_end"`, `pi.on("agent_settled"`, `pi.on("session_shutdown"`,
		`res.code === 2`, `out.block === true`, `process.env.AGENTCTL_BIN`,
	} {
		if !strings.Contains(src, s) {
			t.Errorf("bridge extension lacks %s", s)
		}
	}
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "import ") && !strings.Contains(line, `"node:`) {
			t.Errorf("bridge imports a non-builtin module: %s", line)
		}
	}
}

func TestPiSessionLogPath(t *testing.T) {
	h := For(Pi)
	if got := h.SessionLogPath("sid", "/w", "/s/--w--/2026_sid.jsonl"); got != "/s/--w--/2026_sid.jsonl" {
		t.Errorf("SessionLogPath = %q", got)
	}
	if got := h.SessionLogPath("sid", "/w", "relative.jsonl"); got != "" {
		t.Errorf("relative path accepted: %q", got)
	}
	if got := h.LaunchLogPath("", "/w", "sid"); got != "" {
		t.Errorf("LaunchLogPath = %q, want unknown", got)
	}
	if got := h.ClassifyPane("Do you want to proceed?\n❯ 1. Yes"); got != panestate.Unknown {
		t.Errorf("ClassifyPane = %v, want unknown", got)
	}
}

func TestPiSessionsDir(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", "")
	t.Setenv("PI_CODING_AGENT_DIR", "")
	if got := PiSessionsDir(); got != "/home/u/.pi/agent/sessions" {
		t.Errorf("default = %q", got)
	}
	t.Setenv("PI_CODING_AGENT_DIR", "/pi")
	if got := PiSessionsDir(); got != "/pi/sessions" {
		t.Errorf("agent dir = %q", got)
	}
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", "/sess")
	if got := PiSessionsDir(); got != "/sess" {
		t.Errorf("session dir = %q", got)
	}
}

// TestPiParseTranscript feeds the captured pi 1.0.0 session through the
// same accumulator the monitor uses for the chrome bar's tokens and ctx%.
func TestPiParseTranscript(t *testing.T) {
	f, err := os.Open("../transcript/testdata/pi-session.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	entries, err := For(Pi).ParseTranscript(f)
	if err != nil {
		t.Fatal(err)
	}
	var u logstream.UsageAccumulator
	for _, e := range entries {
		u.OnEntry(e)
	}
	if u.TurnCount != 5 || u.OutputTokens != 347 || u.LastTurnInput != 39+2228 || u.LastTurnCacheRead != 2228 || u.ModelName != "gemma4:latest" {
		t.Errorf("usage = %+v", u)
	}
}

func TestPiBackendProvider(t *testing.T) {
	tests := []struct {
		name      string
		b         Backend
		wantKey   string
		wantURL   string
		wantModel []string
		wantCtx   int
		wantErr   bool
	}{
		{
			name:      "nous fetches the key by command",
			b:         Backend{Name: "nous", Kind: "nous", BaseURL: "https://inference-api.nousresearch.com", Models: []string{"qwen/qwen3-coder-next", "qwen/qwen3-coder-next", "b/cheap", ""}, ContextWindow: 262144, KeyCommand: "'/bin/agentctl' backend-token 'nous'"},
			wantKey:   "!'/bin/agentctl' backend-token 'nous'",
			wantURL:   "https://inference-api.nousresearch.com/v1",
			wantModel: []string{"qwen/qwen3-coder-next", "b/cheap"},
			wantCtx:   262144,
		},
		{
			name:      "ollama literal token is escaped for pi's syntax",
			b:         Backend{Name: "local", Kind: "ollama", BaseURL: "http://fenrir:11434/", Models: []string{"qwen3:8b"}, AuthToken: "!$tok"},
			wantKey:   "$!$$tok",
			wantURL:   "http://fenrir:11434/v1",
			wantModel: []string{"qwen3:8b"},
			wantCtx:   piDefaultContextWindow,
		},
		{name: "no model", b: Backend{Name: "x", BaseURL: "http://h", AuthToken: "t"}, wantErr: true},
		{name: "no credential", b: Backend{Name: "x", BaseURL: "http://h", Models: []string{"m"}}, wantErr: true},
		{name: "no base url", b: Backend{Name: "x", Models: []string{"m"}, AuthToken: "t"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, model, env, err := piBackendProvider(tt.b)
			if tt.wantErr {
				if err == nil {
					t.Fatal("want error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if provider != "agentctl-"+tt.b.Name || model != tt.wantModel[0] {
				t.Errorf("provider, model = %q, %q", provider, model)
			}
			var p piProvider
			if err := json.Unmarshal([]byte(env[EnvPiProvider]), &p); err != nil {
				t.Fatalf("%s: %v", EnvPiProvider, err)
			}
			if p.Name != provider || p.Config.APIKey != tt.wantKey || p.Config.BaseURL != tt.wantURL || p.Config.API != "openai-completions" {
				t.Errorf("provider = %+v", p)
			}
			var ids []string
			for _, m := range p.Config.Models {
				ids = append(ids, m.ID)
				if m.ContextWindow != tt.wantCtx || m.MaxTokens <= 0 || m.MaxTokens > m.ContextWindow {
					t.Errorf("model %s limits = %d/%d", m.ID, m.ContextWindow, m.MaxTokens)
				}
			}
			if !slices.Equal(ids, tt.wantModel) {
				t.Errorf("models = %q, want %q", ids, tt.wantModel)
			}
		})
	}
}

func TestPiPrepareWorkspaceWithBackend(t *testing.T) {
	b := &Backend{Name: "nous", BaseURL: "https://x", Models: []string{"a/b"}, KeyCommand: "agentctl backend-token nous"}
	prep, err := For(Pi).PrepareWorkspace(Workspace{Dir: t.TempDir(), Backend: b})
	if err != nil {
		t.Fatal(err)
	}
	if prep.Provider != "agentctl-nous" || prep.Model != "a/b" || prep.Env[EnvPiProvider] == "" {
		t.Errorf("Prepared = %+v", prep)
	}
	got, _ := For(Pi).Command(Invocation{SessionID: "s", Provider: prep.Provider, Model: prep.Model})
	if want := []string{"pi", "--session-id", "s", "--provider", "agentctl-nous", "--model", "a/b"}; !slices.Equal(fields(t, got), want) {
		t.Errorf("Command = %q, want %q", fields(t, got), want)
	}
	if _, err := For(Pi).PrepareWorkspace(Workspace{Dir: t.TempDir(), Backend: &Backend{Name: "bad"}}); err == nil {
		t.Error("want error for an unusable backend")
	}
}

func TestPiExtensionRegistersProvider(t *testing.T) {
	src := string(PiExtensionSource())
	if !strings.Contains(src, "process.env.AGENTCTL_PI_PROVIDER") || !strings.Contains(src, "pi.registerProvider(p.name, p.config)") {
		t.Error("bridge does not register the backend provider")
	}
}
