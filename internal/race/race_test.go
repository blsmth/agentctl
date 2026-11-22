package race

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/logstream"
	"github.com/blsmth/agentctl/internal/testenv"
)

func TestMain(m *testing.M) {
	testenv.Main(m)
}

var t0 = time.Date(2026, 11, 20, 15, 0, 0, 0, time.UTC)

// line builds one transcript line at t0+sec.
func line(t *testing.T, typ string, sec float64, content any, extra ...string) string {
	t.Helper()
	m := map[string]any{
		"type":      typ,
		"timestamp": t0.Add(time.Duration(sec * float64(time.Second))).Format(time.RFC3339Nano),
		"message":   map[string]any{"role": typ, "content": content},
	}
	for i := 0; i+1 < len(extra); i += 2 {
		m[extra[i]] = extra[i+1] == "true"
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func toolUse(id, name string, input map[string]any) []any {
	return []any{map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}}
}

func toolResult(id string, content any) []any {
	return []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": content}}
}

func parse(t *testing.T, lines ...string) []logstream.Entry {
	t.Helper()
	es, err := logstream.ReadAll(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	return es
}

func TestTypedText(t *testing.T) {
	tests := []struct {
		tool  string
		input map[string]any
		want  string
	}{
		{"Bash", map[string]any{"command": "go test ./...", "description": "run"}, "go test ./..."},
		{"Edit", map[string]any{"file_path": "/a.go", "old_string": "x", "new_string": "yy"}, "yy"},
		{"MultiEdit", map[string]any{"edits": []any{map[string]any{"new_string": "a"}, map[string]any{"new_string": "b"}}}, "a\nb"},
		{"Write", map[string]any{"file_path": "/a.go", "content": "package a"}, "package a"},
		{"Task", map[string]any{"description": "d", "prompt": "find callers"}, "find callers"},
		{"Agent", map[string]any{"prompt": "p"}, "p"},
		{"Read", map[string]any{"file_path": "/x/y.go", "limit": 20.0}, "/x/y.go"},
		{"Grep", map[string]any{"pattern": "foo", "path": "internal", "-n": true}, "foo internal"},
		{"Glob", map[string]any{"pattern": "**/*.go"}, "**/*.go"},
		{"WebFetch", map[string]any{"url": "https://x.dev", "prompt": "summarize"}, "https://x.dev"},
		{"TodoWrite", map[string]any{"todos": []any{map[string]any{"content": "one"}, map[string]any{"content": "two"}}}, "one\ntwo"},
		{"Mystery", map[string]any{"q": "z"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			if got := TypedText(tt.tool, tt.input); got != tt.want {
				t.Errorf("TypedText = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildCostModel(t *testing.T) {
	// 5 chars/s at 60 WPM; 250 WPM reading is 1250 chars/min.
	text := strings.Repeat("é", 300) // multi-byte: 300 runes, 600 bytes
	result := strings.Repeat("r", 1250)
	es := parse(t,
		line(t, "user", 0, "please do it"),
		line(t, "assistant", 10, []any{map[string]any{"type": "text", "text": text}}),
		line(t, "assistant", 12, toolUse("t1", "Bash", map[string]any{"command": "echo hi"})),
		line(t, "user", 15, toolResult("t1", result)),
	)
	tests := []struct {
		name      string
		p         Params
		wantHuman time.Duration
	}{
		{"defaults", DefaultParams(), 60*time.Second + 1400*time.Millisecond + time.Minute},
		{"faster typist", Params{WPM: 120, ReadWPM: 250, MaxGap: time.Minute}, 30*time.Second + 700*time.Millisecond + time.Minute},
		{"think per step", Params{WPM: 60, ReadWPM: 250, Think: 5 * time.Second, MaxGap: time.Minute}, 60*time.Second + 1400*time.Millisecond + time.Minute + 10*time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Build(es, tt.p)
			if len(r.Steps) != 2 {
				t.Fatalf("steps = %d, want 2", len(r.Steps))
			}
			if r.Steps[0].TypedRunes != 300 {
				t.Errorf("text runes = %d, want 300 (runes, not bytes)", r.Steps[0].TypedRunes)
			}
			if r.TypedRunes != 307 || r.ReadRunes != 1250 {
				t.Errorf("typed/read = %d/%d, want 307/1250", r.TypedRunes, r.ReadRunes)
			}
			if r.HumanTotal != tt.wantHuman {
				t.Errorf("HumanTotal = %v, want %v", r.HumanTotal, tt.wantHuman)
			}
			// 10s to the text, 2s to the call, 3s running it.
			if r.AgentTotal != 15*time.Second {
				t.Errorf("AgentTotal = %v, want 15s", r.AgentTotal)
			}
			if r.Steps[1].AgentStart != 10*time.Second || r.Steps[1].AgentDur != 5*time.Second {
				t.Errorf("bash step start/dur = %v/%v, want 10s/5s", r.Steps[1].AgentStart, r.Steps[1].AgentDur)
			}
		})
	}
}

func TestBuildGapCapAndHumanWait(t *testing.T) {
	es := parse(t,
		line(t, "user", 0, "go"),
		line(t, "assistant", 5, toolUse("a", "Bash", map[string]any{"command": "make"})),
		// Approval wait: 10 minutes between call and result, capped.
		line(t, "user", 605, toolResult("a", "ok")),
		line(t, "assistant", 607, []any{map[string]any{"type": "text", "text": "done"}}),
		// The person was away for an hour: not charged at all.
		line(t, "user", 4207, []any{map[string]any{"type": "text", "text": "again"}}),
		// Thinking only: carried to the next step.
		line(t, "assistant", 4210, []any{map[string]any{"type": "thinking", "thinking": "hm"}}),
		line(t, "assistant", 4214, toolUse("b", "Read", map[string]any{"file_path": "/f"})),
		line(t, "user", 4215, toolResult("b", []any{map[string]any{"type": "text", "text": "abc"}})),
	)
	tests := []struct {
		maxGap time.Duration
		want   time.Duration
	}{
		{60 * time.Second, 5*time.Second + 60*time.Second + 2*time.Second + 3*time.Second + 4*time.Second + time.Second},
		{30 * time.Minute, 5*time.Second + 600*time.Second + 2*time.Second + 3*time.Second + 4*time.Second + time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.maxGap.String(), func(t *testing.T) {
			p := DefaultParams()
			p.MaxGap = tt.maxGap
			r := Build(es, p)
			if r.AgentTotal != tt.want {
				t.Errorf("AgentTotal = %v, want %v", r.AgentTotal, tt.want)
			}
			if got := r.Steps[2].AgentDur; got != 8*time.Second {
				t.Errorf("Read step dur = %v, want 8s (3s thinking + 4s + 1s)", got)
			}
			if r.Steps[2].ReadRunes != 3 {
				t.Errorf("block content read runes = %d, want 3", r.Steps[2].ReadRunes)
			}
		})
	}
}

func TestBuildSkipsSidechainAndEmpty(t *testing.T) {
	es := parse(t,
		line(t, "assistant", 1, toolUse("s", "Bash", map[string]any{"command": "side"}), "isSidechain", "true"),
		line(t, "assistant", 2, []any{map[string]any{"type": "text", "text": "   "}}),
		`{"type":"summary","summary":"x"}`,
		`{not json`,
	)
	r := Build(es, DefaultParams())
	if len(r.Steps) != 0 || r.AgentTotal != 0 || r.HumanTotal != 0 {
		t.Fatalf("got %d steps, agent %v, human %v; want an empty race", len(r.Steps), r.AgentTotal, r.HumanTotal)
	}
	if r.Speedup() != 0 {
		t.Errorf("Speedup = %v, want 0", r.Speedup())
	}
	if r.AgentAt(0).Done != true || r.HumanAt(0).Done != true {
		t.Error("an empty race should be done at once")
	}
}

func loadFixture(t *testing.T) *Race {
	t.Helper()
	f, err := os.Open("testdata/demo.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	es, err := logstream.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return Build(es, DefaultParams())
}

func TestFixtureSteps(t *testing.T) {
	r := loadFixture(t)
	if len(r.Steps) != 36 {
		t.Errorf("steps = %d, want 36", len(r.Steps))
	}
	want := map[string]int{"Bash": 8, "Edit": 10, "Glob": 1, "Grep": 3, "Read": 5, "Task": 1, "TodoWrite": 1, "Write": 2}
	for k, v := range want {
		if r.ToolCalls[k] != v {
			t.Errorf("%s calls = %d, want %d", k, r.ToolCalls[k], v)
		}
	}
	if len(r.ToolCalls) != len(want) {
		t.Errorf("tools = %v, want only %v (sidechain Grep calls must be skipped)", r.ToolCalls, want)
	}
	if r.TextBlocks != 5 {
		t.Errorf("text blocks = %d, want 5", r.TextBlocks)
	}
	var task *Step
	for i := range r.Steps {
		if r.Steps[i].Tool == "Task" {
			task = &r.Steps[i]
		}
	}
	if task == nil || task.Label != "Explore: Map Sender callers and concurrency" || task.ReadRunes < 500 {
		t.Fatalf("task step = %+v, want the Explore summary and its report read", task)
	}
	var failed bool
	for _, s := range r.Steps {
		failed = failed || s.IsError
	}
	if !failed {
		t.Error("the failing go test step should be marked as an error")
	}
	// The 7-minute idle before the second prompt is not charged, and the
	// 3-minute approval wait on the commit is capped at 60s.
	if r.AgentTotal > 6*time.Minute || r.AgentTotal < 4*time.Minute {
		t.Errorf("AgentTotal = %v, want about 5m", r.AgentTotal)
	}
	if r.Speedup() < 5 {
		t.Errorf("Speedup = %.1f, want a clear win for the agent", r.Speedup())
	}
}

func TestAgentAndHumanAt(t *testing.T) {
	r := &Race{Steps: []Step{
		{AgentStart: 0, AgentDur: 10 * time.Second, TypedRunes: 50, TypeDur: 10 * time.Second, ReadDur: 10 * time.Second, ThinkDur: 5 * time.Second},
		{AgentStart: 10 * time.Second, AgentDur: 10 * time.Second, HumanStart: 25 * time.Second, TypedRunes: 10, TypeDur: 2 * time.Second},
	}, AgentTotal: 20 * time.Second, HumanTotal: 27 * time.Second}

	agent := []struct {
		at   time.Duration
		want AgentPos
	}{
		{0, AgentPos{Index: 0, Frac: 0}},
		{5 * time.Second, AgentPos{Index: 0, Frac: 0.5}},
		{10 * time.Second, AgentPos{Index: 1, Frac: 0}},
		{20 * time.Second, AgentPos{Index: 2, Frac: 1, Done: true}},
	}
	for _, tt := range agent {
		if got := r.AgentAt(tt.at); got != tt.want {
			t.Errorf("AgentAt(%v) = %+v, want %+v", tt.at, got, tt.want)
		}
	}

	human := []struct {
		at   time.Duration
		want HumanPos
	}{
		{4 * time.Second, HumanPos{Index: 0, Phase: PhaseTyping, Typed: 20}},
		{15 * time.Second, HumanPos{Index: 0, Phase: PhaseReading, Typed: 50, ReadFrac: 0.5}},
		{22 * time.Second, HumanPos{Index: 0, Phase: PhaseThinking, Typed: 50, ReadFrac: 1}},
		{26 * time.Second, HumanPos{Index: 1, Phase: PhaseTyping, Typed: 5}},
		{27 * time.Second, HumanPos{Index: 2, Phase: PhaseDone, ReadFrac: 1, Done: true}},
	}
	for _, tt := range human {
		if got := r.HumanAt(tt.at); got != tt.want {
			t.Errorf("HumanAt(%v) = %+v, want %+v", tt.at, got, tt.want)
		}
	}

	if got := r.HumanWorkFor(5 * time.Second); got != 12500*time.Millisecond {
		t.Errorf("HumanWorkFor(5s) = %v, want 12.5s", got)
	}
	if got := r.HumanWorkFor(time.Minute); got != r.HumanTotal {
		t.Errorf("HumanWorkFor(after the end) = %v, want %v", got, r.HumanTotal)
	}
}

func TestPlaybackFakeClock(t *testing.T) {
	wall := t0
	p := NewPlayback(Factor(10*time.Minute, 30*time.Second, 0), wall) // 20x
	step := func(d time.Duration) time.Duration { wall = wall.Add(d); return p.Advance(wall) }

	if got := step(time.Second); got != 20*time.Second {
		t.Fatalf("after 1s wall = %v, want 20s", got)
	}
	p.TogglePause(wall)
	if got := step(5 * time.Second); got != 20*time.Second {
		t.Fatalf("paused playback moved to %v", got)
	}
	p.TogglePause(wall)
	p.SetFactor(p.Factor()*2, wall)
	if got := step(time.Second); got != 60*time.Second {
		t.Fatalf("after doubling = %v, want 60s", got)
	}
	p.Seek(0, wall)
	if got := step(500 * time.Millisecond); got != 20*time.Second {
		t.Fatalf("after seek = %v, want 20s", got)
	}
	// A clock that goes backwards never rewinds the replay.
	wall = wall.Add(-time.Hour)
	if got := p.Advance(wall); got != 20*time.Second {
		t.Fatalf("backwards clock moved playback to %v", got)
	}
}

func TestFactor(t *testing.T) {
	tests := []struct {
		name     string
		total    time.Duration
		duration time.Duration
		speed    float64
		want     float64
	}{
		{"fit duration", 15 * time.Minute, 45 * time.Second, 0, 20},
		{"speed wins", 15 * time.Minute, 45 * time.Second, 3, 3},
		{"empty timeline", 0, 45 * time.Second, 0, 1},
		{"floor", time.Millisecond, time.Hour, 0, 1e-3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Factor(tt.total, tt.duration, tt.speed); got != tt.want {
				t.Errorf("Factor = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFormatting(t *testing.T) {
	friendly := []struct {
		d    time.Duration
		want string
	}{
		{42 * time.Second, "42s"},
		{14*time.Minute + 3*time.Second, "14m 03s"},
		{3*time.Hour + 12*time.Minute, "3h 12m"},
		{8 * time.Hour, "1.0 workday"},
		{25*time.Hour + 36*time.Minute, "3.2 workdays"},
		{56 * time.Hour, "1.4 weeks"},
	}
	for _, tt := range friendly {
		if got := Friendly(tt.d); got != tt.want {
			t.Errorf("Friendly(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
	if got := Clock(time.Hour + 2*time.Minute + 3*time.Second + 900*time.Millisecond); got != "1:02:03" {
		t.Errorf("Clock = %q", got)
	}
	if got := HM(25*time.Hour + 41*time.Minute); got != "25h 41m" {
		t.Errorf("HM = %q", got)
	}
	for x, want := range map[float64]string{37.4: "37x", 2.54: "2.5x", 0: "0x"} {
		if got := Multiplier(x); got != want {
			t.Errorf("Multiplier(%v) = %q, want %q", x, got, want)
		}
	}
}

func TestSummarize(t *testing.T) {
	r := loadFixture(t)
	s := r.Summarize("demo.jsonl")
	if s.Steps != 36 || s.ToolCalls != 31 || s.CharsTyped != r.TypedRunes || s.CharsRead != r.ReadRunes {
		t.Errorf("summary = %+v", s)
	}
	if s.Assumptions.WPM != 60 || s.Assumptions.MaxGapSeconds != 60 {
		t.Errorf("assumptions = %+v", s.Assumptions)
	}
	if !strings.Contains(s.AssumptionTxt, "lower bound: never pauses, never typos") {
		t.Errorf("assumptions text = %q, want the lower-bound note", s.AssumptionTxt)
	}
	r.Params.Think = 2 * time.Second
	if strings.Contains(r.Assumptions(), "lower bound") || !strings.Contains(r.Assumptions(), "thinks 2s per step") {
		t.Errorf("with think: %q", r.Assumptions())
	}
}
