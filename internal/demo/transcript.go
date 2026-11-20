package demo

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Transcript is a canned conversation. It is written to the fake account's
// projects/ dir as Claude Code JSONL (what the hub, HUD and cost read) and
// drawn in the fake pane as Claude Code's TUI would show it.
type Transcript struct {
	Title   string  `yaml:"title"` // ai-title record
	Recap   string  `yaml:"recap"` // system/away_summary record
	PR      int     `yaml:"pr"`    // pr-link record number
	Entries []Entry `yaml:"entries"`
}

// Entry is one transcript step. Exactly one of User, Say or Tool is set.
type Entry struct {
	User string `yaml:"user"`
	Say  string `yaml:"say"`

	Tool    string         `yaml:"tool"`
	Input   map[string]any `yaml:"input"`
	Result  string         `yaml:"result"`
	Diff    string         `yaml:"diff"`    // unified-style lines (+, -, space) drawn under an edit
	Error   bool           `yaml:"error"`   // tool_result is_error
	Running bool           `yaml:"running"` // no result yet: the pane shows "Running…"
}

// TranscriptRef is a window's transcript: a fixture name (a scalar) or an
// inline transcript (a mapping).
type TranscriptRef struct {
	Name   string
	Inline *Transcript
}

// UnmarshalYAML accepts a fixture name or a strict inline transcript.
func (r *TranscriptRef) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		r.Name = n.Value
		return nil
	}
	var t Transcript
	if err := strictNode(n, &t); err != nil {
		return err
	}
	r.Inline = &t
	return nil
}

// IsZero reports whether no transcript is set.
func (r TranscriptRef) IsZero() bool { return r.Name == "" && r.Inline == nil }

func (t *Transcript) validate() error {
	for i, e := range t.Entries {
		n := 0
		for _, b := range []bool{e.User != "", e.Say != "", e.Tool != ""} {
			if b {
				n++
			}
		}
		if n != 1 {
			return fmt.Errorf("entries[%d]: exactly one of user, say or tool is required", i)
		}
		if e.Tool == "" && (e.Input != nil || e.Result != "" || e.Diff != "" || e.Error || e.Running) {
			return fmt.Errorf("entries[%d]: input/result/diff/error/running need tool", i)
		}
		if e.Running && i != len(t.Entries)-1 {
			return fmt.Errorf("entries[%d]: only the last entry can be running", i)
		}
	}
	return nil
}

// LoadFixture reads demo/transcripts/<name>.yaml from dir.
func LoadFixture(dir, name string) (*Transcript, error) {
	path := filepath.Join(dir, name+".yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("transcript fixture %q: %w", name, err)
	}
	var t Transcript
	if err := strictDecode(data, &t); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := t.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &t, nil
}

// Resolve returns the window's transcript (an empty one when none is set).
func (r TranscriptRef) Resolve(fixtures string) (*Transcript, error) {
	switch {
	case r.Inline != nil:
		return r.Inline, nil
	case r.Name != "":
		return LoadFixture(fixtures, r.Name)
	}
	return &Transcript{}, nil
}

// LastTool is the detail of the transcript's last tool call ("" when none),
// in the form agentctl's hooks record it.
func (t *Transcript) LastTool() string {
	for i := len(t.Entries) - 1; i >= 0; i-- {
		if e := t.Entries[i]; e.Tool != "" {
			return toolDetail(e)
		}
	}
	return ""
}

// LastText is the transcript's last assistant text.
func (t *Transcript) LastText() string {
	for i := len(t.Entries) - 1; i >= 0; i-- {
		if s := t.Entries[i].Say; s != "" {
			return s
		}
	}
	return ""
}

// toolDetail is the one-line argument shown next to a tool name.
func toolDetail(e Entry) string {
	for _, k := range []string{"command", "file_path", "pattern", "url", "prompt", "description"} {
		if v, ok := e.Input[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// SessionID is a stable UUID-shaped session ID derived from seed.
func SessionID(seed string) string {
	h := sha256.Sum256([]byte("agentctl-demo:" + seed))
	x := hex.EncodeToString(h[:16])
	return x[0:8] + "-" + x[8:12] + "-4" + x[13:16] + "-a" + x[17:20] + "-" + x[20:32]
}

// TranscriptMeta is what the JSONL records carry besides the entries.
type TranscriptMeta struct {
	SessionID string
	Cwd       string
	Branch    string
	Model     string
	Project   string // for the pr-link URL
	End       time.Time
}

// JSONL renders t as Claude Code transcript lines, the last one at m.End.
func (t *Transcript) JSONL(m TranscriptMeta) ([]byte, error) {
	var lines []any
	step := 20 * time.Second
	ts := m.End.Add(-time.Duration(len(t.Entries)+1) * step)
	base := func(typ string) map[string]any {
		ts = ts.Add(step)
		return map[string]any{
			"type": typ, "sessionId": m.SessionID, "cwd": m.Cwd, "gitBranch": m.Branch,
			"version": "2.4.1", "uuid": SessionID(fmt.Sprintf("%s-%d", m.SessionID, len(lines))),
			"timestamp": ts.UTC().Format(time.RFC3339Nano), "userType": "external",
		}
	}
	if t.Title != "" {
		lines = append(lines, map[string]any{"type": "ai-title", "aiTitle": t.Title, "sessionId": m.SessionID})
	}
	ctx := 18000
	for i, e := range t.Entries {
		switch {
		case e.User != "":
			r := base("user")
			r["message"] = map[string]any{"role": "user", "content": e.User}
			lines = append(lines, r)
		case e.Say != "":
			ctx += 900 + 37*len(e.Say)
			r := base("assistant")
			r["message"] = assistantMsg(m, i, ctx, []any{map[string]any{"type": "text", "text": e.Say}})
			lines = append(lines, r)
		default:
			id := fmt.Sprintf("toolu_demo%02d%s", i, m.SessionID[:6])
			ctx += 1400 + 11*len(e.Result)
			r := base("assistant")
			r["message"] = assistantMsg(m, i, ctx, []any{map[string]any{
				"type": "tool_use", "id": id, "name": e.Tool, "input": nonNil(e.Input),
			}})
			lines = append(lines, r)
			if e.Running {
				continue
			}
			u := base("user")
			u["message"] = map[string]any{"role": "user", "content": []any{map[string]any{
				"type": "tool_result", "tool_use_id": id, "content": e.Result, "is_error": e.Error,
			}}}
			lines = append(lines, u)
		}
	}
	if t.Recap != "" {
		r := base("system")
		r["subtype"] = "away_summary"
		r["content"] = t.Recap + " (disable recaps in /config)"
		lines = append(lines, r)
	}
	if t.PR > 0 {
		lines = append(lines, map[string]any{
			"type": "pr-link", "sessionId": m.SessionID, "prNumber": t.PR,
			"prUrl": PRURL(m.Project, t.PR), "prRepository": "acme/" + m.Project,
		})
	}
	var b strings.Builder
	for _, l := range lines {
		data, err := json.Marshal(l)
		if err != nil {
			return nil, fmt.Errorf("encoding transcript line: %w", err)
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	return []byte(b.String()), nil
}

// PRURL is the fake GitHub URL of a project's PR.
func PRURL(project string, n int) string {
	return fmt.Sprintf("https://github.com/acme/%s/pull/%d", project, n)
}

func assistantMsg(m TranscriptMeta, i, ctx int, content []any) map[string]any {
	return map[string]any{
		"id":      fmt.Sprintf("msg_demo%03d%s", i, m.SessionID[:8]),
		"type":    "message",
		"role":    "assistant",
		"model":   m.Model,
		"content": content,
		"usage": map[string]any{
			"input_tokens":                6,
			"cache_creation_input_tokens": 1200,
			"cache_read_input_tokens":     ctx,
			"output_tokens":               180 + 7*i,
		},
	}
}

func nonNil(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}
