package transcript

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"
)

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestNormalizeLeavesClaudeRecordsAlone(t *testing.T) {
	for _, line := range []string{
		`{"type":"assistant","message":{"id":"m","usage":{"input_tokens":1}}}`,
		`{"type":"user","message":{"content":"hi"}}`,
		`{"type":"system","subtype":"away_summary","content":"x"}`,
		`{"type":"summary","summary":"s"}`,
		`{"type":"custom","customType":"agentctl"}`, // no parentId: not pi
	} {
		in := decode(t, line)
		out := Normalize(in)
		a, _ := json.Marshal(in)
		b, _ := json.Marshal(out)
		if string(a) != string(b) {
			t.Errorf("Normalize changed a Claude record:\n in  %s\n out %s", a, b)
		}
	}
}

func TestNormalizePiRecords(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "assistant with usage, thinking and a tool call",
			in:   `{"type":"message","id":"a1","parentId":"u1","timestamp":"2026-11-17T20:09:23.855Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hm"},{"type":"text","text":"ok"},{"type":"toolCall","id":"c1","name":"read","arguments":{"path":"/w/a.go"}}],"model":"claude-sonnet-4-5","usage":{"input":29,"output":59,"cacheRead":2045,"cacheWrite":7,"totalTokens":2140},"stopReason":"toolUse"}}`,
			want: `{"message":{"content":[{"thinking":"hm","type":"thinking"},{"text":"ok","type":"text"},{"id":"c1","input":{"file_path":"/w/a.go","path":"/w/a.go"},"name":"Read","type":"tool_use"}],"id":"a1","model":"claude-sonnet-4-5","role":"assistant","stop_reason":"toolUse","usage":{"cache_creation_input_tokens":7,"cache_read_input_tokens":2045,"input_tokens":29,"output_tokens":59}},"parentUuid":"u1","timestamp":"2026-11-17T20:09:23.855Z","type":"assistant","uuid":"a1"}`,
		},
		{
			name: "user string content becomes a text block",
			in:   `{"type":"message","id":"u1","parentId":null,"timestamp":"t","message":{"role":"user","content":"hello"}}`,
			want: `{"message":{"content":[{"text":"hello","type":"text"}],"role":"user"},"parentUuid":null,"timestamp":"t","type":"user","uuid":"u1"}`,
		},
		{
			name: "tool result",
			in:   `{"type":"message","id":"r1","parentId":"a1","timestamp":"t","message":{"role":"toolResult","toolCallId":"c1","toolName":"bash","content":[{"type":"text","text":"no"}],"isError":true}}`,
			want: `{"message":{"content":[{"content":"no","is_error":true,"tool_use_id":"c1","type":"tool_result"}],"role":"user"},"parentUuid":"a1","timestamp":"t","type":"user","uuid":"r1"}`,
		},
		{
			name: "agent tag",
			in:   `{"type":"custom","customType":"agentctl","data":{"agent":"pi-tester"},"id":"x","parentId":"y","timestamp":"t"}`,
			want: `{"agentSetting":"pi-tester","timestamp":"t","type":"agent-setting"}`,
		},
		{
			name: "usage entry counts toward totals",
			in:   `{"type":"usage","id":"w","parentId":"v","timestamp":"t","kind":"cache_warm","provider":"anthropic","model":"claude-sonnet-4-5","usage":{"input":0,"output":0,"cacheRead":50000,"cacheWrite":0}}`,
			want: `{"message":{"content":[],"id":"w","model":"claude-sonnet-4-5","role":"assistant","usage":{"cache_creation_input_tokens":0,"cache_read_input_tokens":50000,"input_tokens":0,"output_tokens":0}},"timestamp":"t","type":"assistant","uuid":"w"}`,
		},
		{
			name: "system message has no Claude equivalent",
			in:   `{"type":"message","id":"s","parentId":null,"timestamp":"t","message":{"role":"system","content":""}}`,
			want: `{"id":"s","message":{"content":"","role":"system"},"parentId":null,"timestamp":"t","type":"pi:message:system"}`,
		},
		{
			name: "model change is kept but not matched",
			in:   `{"type":"model_change","id":"m","parentId":null,"timestamp":"t","provider":"ollama","modelId":"gemma4"}`,
			want: `{"id":"m","modelId":"gemma4","parentId":null,"provider":"ollama","timestamp":"t","type":"pi:model_change"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := json.Marshal(Normalize(decode(t, tt.in)))
			if string(got) != tt.want {
				t.Errorf("Normalize =\n %s\nwant\n %s", got, tt.want)
			}
		})
	}
}

// TestNormalizeRealSession runs the normalizer over a session file captured
// from pi 1.0.0 (agentctl dispatch on the pi harness, ollama gemma4; home
// dir rewritten): every line must normalize, the agent tag must surface, and
// assistant usage must come through.
func TestNormalizeRealSession(t *testing.T) {
	f, err := os.Open("testdata/pi-session.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	var agent string
	var assistants, toolCalls, toolResults int
	var output float64
	for sc.Scan() {
		var rec map[string]any
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("fixture line: %v", err)
		}
		if !IsPi(rec) {
			t.Fatalf("fixture record not recognized as pi: %s", sc.Bytes())
		}
		out := Normalize(rec)
		switch out["type"] {
		case "agent-setting":
			agent, _ = out["agentSetting"].(string)
		case "assistant":
			assistants++
			msg := out["message"].(map[string]any)
			if u, ok := msg["usage"].(map[string]any); ok {
				output += u["output_tokens"].(float64)
			}
			for _, b := range msg["content"].([]any) {
				if b.(map[string]any)["type"] == "tool_use" {
					toolCalls++
				}
			}
		case "user":
			for _, b := range out["message"].(map[string]any)["content"].([]any) {
				if b.(map[string]any)["type"] == "tool_result" {
					toolResults++
				}
			}
		}
	}
	if agent != "pi-tester" {
		t.Errorf("agent = %q, want pi-tester", agent)
	}
	if assistants == 0 || output == 0 || toolCalls == 0 || toolResults == 0 {
		t.Errorf("assistants=%d output=%v toolCalls=%d toolResults=%d; want all > 0", assistants, output, toolCalls, toolResults)
	}
}

func TestToolNameAndInput(t *testing.T) {
	if ToolName("bash") != "Bash" || ToolName("find") != "Glob" || ToolName("my_tool") != "my_tool" {
		t.Error("ToolName mapping")
	}
	in := map[string]any{"path": "/a"}
	out := ToolInput("edit", in)
	if out["file_path"] != "/a" {
		t.Errorf("ToolInput = %v", out)
	}
	if _, mutated := in["file_path"]; mutated {
		t.Error("ToolInput mutated its argument")
	}
	if got := ToolInput("bash", nil); len(got) != 0 {
		t.Errorf("nil args = %v", got)
	}
}

func TestSessionFileNames(t *testing.T) {
	const pi = "2026-11-17T20-08-01-304Z_241cdf3b-204d-43dd-aeaf-a29e54628c45.jsonl"
	const id = "241cdf3b-204d-43dd-aeaf-a29e54628c45"
	if got := PiSessionID(pi); got != id {
		t.Errorf("PiSessionID = %q", got)
	}
	if got := PiSessionID(id + ".jsonl"); got != "" {
		t.Errorf("PiSessionID(claude) = %q", got)
	}
	for _, tt := range []struct {
		base, sid string
		want      bool
	}{
		{pi, id, true},
		{id + ".jsonl", id, true},
		{"x_" + id + ".jsonl", "other", false},
		{id + ".jsonl", "", false},
	} {
		if got := LogMatchesSession(tt.base, tt.sid); got != tt.want {
			t.Errorf("LogMatchesSession(%q, %q) = %v", tt.base, tt.sid, got)
		}
	}
	if got := SessionIDFromLog(pi); got != id {
		t.Errorf("SessionIDFromLog(pi) = %q", got)
	}
	if got := SessionIDFromLog(id + ".jsonl"); got != id {
		t.Errorf("SessionIDFromLog(claude) = %q", got)
	}
}
