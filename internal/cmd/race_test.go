package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/race"
	"github.com/blsmth/agentctl/internal/windowstate"
)

const raceFixturePath = "../race/testdata/demo.jsonl"

// stubRace replaces the race seams for one test.
func stubRace(t *testing.T, pane, window string, records map[string]windowstate.WindowState, logs map[string]string) {
	t.Helper()
	oldPane, oldWin, oldRead, oldFind, oldTUI, oldRemote := raceTmuxPane, raceWindowForPane, raceReadWindow, raceFindSession, raceRunTUI, raceRemoteWindow
	t.Cleanup(func() {
		raceTmuxPane, raceWindowForPane, raceReadWindow, raceFindSession, raceRunTUI, raceRemoteWindow = oldPane, oldWin, oldRead, oldFind, oldTUI, oldRemote
	})
	raceTmuxPane = func() string { return pane }
	raceWindowForPane = func(string) string { return window }
	raceReadWindow = func(id string) (windowstate.WindowState, error) { return records[id], nil }
	raceRemoteWindow = func(string) windowstate.WindowState { return windowstate.WindowState{} }
	raceFindSession = func(id string) string { return logs[id] }
	raceRunTUI = func(tea.Model) error { t.Fatal("TUI started in a test"); return nil }
}

func TestResolveRaceLog(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "abc.jsonl")
	if err := os.WriteFile(logPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	records := map[string]windowstate.WindowState{
		"@7": {LogPath: logPath},
		"@8": {SessionID: "sess-8"},
		"@9": {LogPath: filepath.Join(dir, "gone.jsonl"), SessionID: "sess-9"},
		"@5": {SessionID: "sess-missing"},
	}
	logs := map[string]string{"sess-8": "/logs/8.jsonl", "sess-9": "/logs/9.jsonl", "uuid-1": "/logs/u1.jsonl"}

	tests := []struct {
		name    string
		target  string
		pane    string
		window  string
		want    string
		wantErr string
	}{
		{"path", logPath, "", "", logPath, ""},
		{"missing path", filepath.Join(dir, "nope.jsonl"), "", "", "", "no transcript at"},
		{"window log path", "@7", "", "", logPath, ""},
		{"window session id", "@8", "", "", "/logs/8.jsonl", ""},
		{"stale log path falls back to session", "@9", "", "", "/logs/9.jsonl", ""},
		{"window without transcript", "@5", "", "", "", "no transcript found for session sess-missing"},
		{"untracked window", "@4", "", "", "", "no recorded session"},
		{"session id", "uuid-1", "", "", "/logs/u1.jsonl", ""},
		{"unknown session", "uuid-2", "", "", "", `no transcript found for session "uuid-2"`},
		{"current window", "", "%3", "@8", "/logs/8.jsonl", ""},
		{"outside tmux", "", "", "", "", "not inside tmux"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubRace(t, tt.pane, tt.window, records, logs)
			got, err := resolveRaceLog(tt.target)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("resolveRaceLog(%q) = %q, %v; want %q", tt.target, got, err, tt.want)
			}
		})
	}
}

func raceOpts(target string) RaceOptions {
	return RaceOptions{Target: target, Params: race.DefaultParams(), Duration: 45 * time.Second}
}

func TestRunRaceJSON(t *testing.T) {
	stubRace(t, "", "", nil, nil)
	opts := raceOpts(raceFixturePath)
	opts.JSON = true
	var out bytes.Buffer
	if err := RunRace(&out, opts); err != nil {
		t.Fatal(err)
	}
	var s race.Summary
	if err := json.Unmarshal(out.Bytes(), &s); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
	if s.Steps != 36 || s.ToolCalls != 31 || s.ToolCallsBy["Edit"] != 10 {
		t.Errorf("summary = %+v", s)
	}
	if s.AgentSeconds <= 0 || s.HumanSeconds <= s.AgentSeconds || s.Speedup <= 1 {
		t.Errorf("times: agent %v, human %v, speedup %v", s.AgentSeconds, s.HumanSeconds, s.Speedup)
	}
	for _, key := range []string{`"chars_typed"`, `"chars_read"`, `"agent_active_seconds"`, `"human_seconds"`, `"speedup"`, `"tool_calls_by_name"`} {
		if !strings.Contains(out.String(), key) {
			t.Errorf("JSON lacks %s", key)
		}
	}
}

func TestRunRaceEmptyTranscript(t *testing.T) {
	stubRace(t, "", "", nil, nil)
	path := filepath.Join(t.TempDir(), "empty.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"user","message":{"role":"user","content":"hi"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunRace(&out, raceOpts(path)); err != nil {
		t.Fatalf("empty transcript should not fail: %v", err)
	}
	if !strings.Contains(out.String(), "Nothing to race") {
		t.Errorf("output = %q, want a friendly message", out.String())
	}
}

func TestRunRaceStartsTUI(t *testing.T) {
	stubRace(t, "", "", nil, nil)
	started := false
	raceRunTUI = func(m tea.Model) error {
		_, started = m.(interface{ View() tea.View })
		return nil
	}
	if err := RunRace(&bytes.Buffer{}, raceOpts(raceFixturePath)); err != nil {
		t.Fatal(err)
	}
	if !started {
		t.Fatal("TUI not started")
	}
}

func TestRunRaceValidates(t *testing.T) {
	stubRace(t, "", "", nil, nil)
	tests := []struct {
		name string
		mod  func(*RaceOptions)
		want string
	}{
		{"wpm", func(o *RaceOptions) { o.Params.WPM = 0 }, "--wpm"},
		{"read-wpm", func(o *RaceOptions) { o.Params.ReadWPM = -1 }, "--read-wpm"},
		{"think", func(o *RaceOptions) { o.Params.Think = -time.Second }, "--think"},
		{"max-gap", func(o *RaceOptions) { o.Params.MaxGap = 0 }, "--max-gap"},
		{"speed", func(o *RaceOptions) { o.Speed = -2 }, "--speed"},
		{"duration", func(o *RaceOptions) { o.Duration = 0 }, "--duration"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := raceOpts(raceFixturePath)
			tt.mod(&o)
			if err := RunRace(&bytes.Buffer{}, o); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want one naming %s", err, tt.want)
			}
		})
	}
}

func TestRaceTitle(t *testing.T) {
	tests := map[string]string{
		"/home/u/.claude/projects/-Users-u-code-x/7c1e2b9a-4d3f-4b8e-9a51-2f6d0c3e8b17.jsonl": "7c1e2b9a",
		"internal/race/testdata/demo.jsonl":                                                   "testdata/demo",
		"demo.jsonl":                                                                          "demo",
	}
	for in, want := range tests {
		if got := raceTitle(in); got != want {
			t.Errorf("raceTitle(%q) = %q, want %q", in, got, want)
		}
	}
}
