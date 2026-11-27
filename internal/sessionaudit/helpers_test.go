package sessionaudit

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/logstream"
	"github.com/blsmth/agentctl/internal/testenv"
)

// TestMain isolates the tests from the live home dir, Claude config dirs,
// agentctl runtime dir and tmux server.
func TestMain(m *testing.M) {
	testenv.Main(m)
}

const (
	testCwd  = "/repo"
	testHome = "/home/u"
)

var t0 = time.Date(2026, 11, 20, 15, 0, 0, 0, time.UTC)

// call is one tool call in a synthetic transcript.
type call struct {
	tool   string
	input  map[string]any
	result string
	isErr  bool
	gap    time.Duration // before the call; default 2s
	run    time.Duration // call to result (the tool running); default 1s
	id     string        // tool_use id; default toolu_<n>
}

func bash(cmd string) call {
	return call{tool: "Bash", input: map[string]any{"command": cmd}, result: "ok"}
}

func bashErr(cmd string) call {
	c := bash(cmd)
	c.result, c.isErr = "exit status 1", true
	return c
}

func read(path string) call {
	return call{tool: "Read", input: map[string]any{"file_path": path}, result: "line1\nline2\nline3\nline4"}
}

func edit(path string) call {
	return call{tool: "Edit", input: map[string]any{"file_path": path, "old_string": "a", "new_string": "b"}, result: "ok"}
}

func write(path string) call {
	return call{tool: "Write", input: map[string]any{"file_path": path, "content": "x"}, result: "ok"}
}

// lines renders a transcript: one prompt, then each call with its result.
func lines(t *testing.T, calls ...call) []map[string]any {
	t.Helper()
	return linesPrompt(t, "do the thing", calls...)
}

// linesPrompt is lines with the given human prompt.
func linesPrompt(t *testing.T, prompt string, calls ...call) []map[string]any {
	t.Helper()
	at := t0
	ts := func() string { return at.Format(time.RFC3339Nano) }
	out := []map[string]any{{
		"type": "user", "sessionId": "sess-1", "cwd": testCwd, "agentSetting": "go-engineer",
		"permissionMode": "default", "timestamp": ts(),
		"message": map[string]any{"role": "user", "content": prompt},
	}}
	for i, c := range calls {
		gap := c.gap
		if gap == 0 {
			gap = 2 * time.Second
		}
		at = at.Add(gap)
		id := c.id
		if id == "" {
			id = fmt.Sprintf("toolu_%d", i+1)
		}
		out = append(out, map[string]any{
			"type": "assistant", "sessionId": "sess-1", "cwd": testCwd, "timestamp": ts(),
			"message": map[string]any{
				"id": fmt.Sprintf("msg_%d", i+1), "role": "assistant", "model": "claude-opus-4-5",
				"content": []any{map[string]any{"type": "tool_use", "id": id, "name": c.tool, "input": c.input}},
				"usage":   map[string]any{"input_tokens": 10, "output_tokens": 20, "cache_read_input_tokens": 100},
			},
		})
		run := c.run
		if run == 0 {
			run = time.Second
		}
		at = at.Add(run)
		res := map[string]any{"type": "tool_result", "tool_use_id": id, "content": c.result}
		if c.isErr {
			res["is_error"] = true
		}
		out = append(out, map[string]any{
			"type": "user", "sessionId": "sess-1", "cwd": testCwd, "timestamp": ts(),
			"message": map[string]any{"role": "user", "content": []any{res}},
		})
	}
	return out
}

func toEntries(t *testing.T, recs []map[string]any) []logstream.Entry {
	t.Helper()
	var out []logstream.Entry
	for _, r := range recs {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, logstream.ParseLine(b))
	}
	return out
}

func testParams() Params {
	p := DefaultParams()
	p.Home = testHome
	return p
}

func analyzeCalls(t *testing.T, calls ...call) *Report {
	t.Helper()
	return Analyze(toEntries(t, lines(t, calls...)), nil, testParams())
}

// ruleIDs lists the rules that fired, in report order.
func ruleIDs(r *Report) []string {
	out := []string{}
	for _, f := range r.Findings {
		out = append(out, f.Rule)
	}
	return out
}
