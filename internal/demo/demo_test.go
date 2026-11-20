package demo

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/transcript"
	"github.com/blsmth/agentctl/internal/windowstate"
)

func sampleTranscript() *Transcript {
	return &Transcript{
		Title: "Orders rate limiter",
		Recap: "Added the limiter.",
		PR:    64,
		Entries: []Entry{
			{User: "Add a limiter"},
			{Say: "Looking at the router."},
			{Tool: "Bash", Input: map[string]any{"command": "go test ./..."}, Result: "ok"},
			{Tool: "Edit", Input: map[string]any{"file_path": "router.go"}, Diff: "-old\n+new\n+newer"},
			{Tool: "Bash", Input: map[string]any{"command": "go vet ./..."}, Running: true},
		},
	}
}

func TestTranscriptJSONL(t *testing.T) {
	sid := SessionID("acme-api/orch")
	data, err := sampleTranscript().JSONL(TranscriptMeta{SessionID: sid, Cwd: "/w", Branch: "main", Model: "claude-opus-5-5", Project: "acme-api", End: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]int{}
	toolUses, toolResults := 0, 0
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		var rec map[string]any
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("line is not JSON: %s", sc.Text())
		}
		typ, _ := rec["type"].(string)
		types[typ]++
		msg, _ := rec["message"].(map[string]any)
		content, _ := msg["content"].([]any)
		for _, c := range content {
			switch c.(map[string]any)["type"] {
			case "tool_use":
				toolUses++
			case "tool_result":
				toolResults++
			}
		}
	}
	want := map[string]int{"ai-title": 1, "user": 3, "assistant": 4, "system": 1, "pr-link": 1}
	for k, n := range want {
		if types[k] != n {
			t.Errorf("%s records = %d, want %d (all: %v)", k, types[k], n, types)
		}
	}
	if toolUses != 3 || toolResults != 2 {
		t.Errorf("tool_use=%d tool_result=%d, want 3 and 2 (the running call has no result)", toolUses, toolResults)
	}
	if !transcript.LogMatchesSession(sid+".jsonl", sid) {
		t.Error("session ID does not match its log name")
	}
}

func TestSessionIDStableAndUUIDShaped(t *testing.T) {
	a, b := SessionID("x"), SessionID("x")
	if a != b || len(a) != 36 || strings.Count(a, "-") != 4 {
		t.Errorf("SessionID = %q, %q", a, b)
	}
	if SessionID("y") == a {
		t.Error("different seeds gave the same ID")
	}
}

func TestRender(t *testing.T) {
	base := PaneSpec{Transcript: *sampleTranscript(), WorkDir: "/w"}
	tests := []struct {
		name    string
		window  Window
		status  string
		want    []string
		notWant []string
	}{
		{
			name:    "working",
			window:  Window{State: "working", Mode: "dispatched", Spinner: "Testing… (41s)"},
			status:  "STATUSLINE",
			want:    []string{"Welcome to", "Bash", "Update", "Running…", "✻ Testing… (41s)", "STATUSLINE", "auto mode on"},
			notWant: []string{"Do you want to proceed?"},
		},
		{
			name:    "blocked",
			window:  Window{State: "blocked", Mode: "bare", PendingTool: "Bash", PendingDetail: "psql -f m.sql"},
			status:  "STATUSLINE",
			want:    []string{"Bash command", "psql -f m.sql", "Do you want to proceed?", "❯ 1. Yes"},
			notWant: []string{"STATUSLINE", "? for shortcuts"},
		},
		{
			name:   "idle bare",
			window: Window{State: "idle", Mode: "bare"},
			want:   []string{"? for shortcuts", "Updated router.go with 2 additions and 1 removals"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := base
			spec.Window = tt.window
			for _, width := range []int{60, 160} {
				out := Render(spec, width, tt.status, nil)
				plain := ansi.Strip(out)
				for _, w := range tt.want {
					if !strings.Contains(plain, w) {
						t.Errorf("width %d: missing %q", width, w)
					}
				}
				for _, w := range tt.notWant {
					if strings.Contains(plain, w) {
						t.Errorf("width %d: unexpected %q", width, w)
					}
				}
				for _, line := range strings.Split(out, "\r\n") {
					if n := ansi.StringWidth(line); n > width {
						t.Errorf("width %d: line is %d wide: %q", width, n, ansi.Strip(line))
					}
				}
			}
		})
	}
}

func TestTape(t *testing.T) {
	s, err := ParseScene([]byte(minimalScene + "  - attach: worker\n  - screenshot: after\n  - key: Down\n    repeat: 3\n"))
	if err != nil {
		t.Fatal(err)
	}
	s.Name = "hub"
	m := &Manifest{Windows: map[string]WindowPlace{
		"orch":   {Session: "agentctl-acme-api", ID: "@7"},
		"worker": {Session: "agentctl-acme-api", ID: "@8"},
	}}
	tape, outs, err := Tape(s, m, "/out/hub", "/tmp/scratch.gif")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`Output "/tmp/scratch.gif"`,
		"tmux -L agentctl-demo attach -t '=agentctl-acme-api:@7'",
		"env -u TMUX",
		"switch-client -t '=agentctl-acme-api:@8'",
		"Ctrl+B\nType \"g\"",
		`Screenshot "/out/hub.png"`,
		`Screenshot "/out/hub-after.png"`,
		"Down 3",
	} {
		if !strings.Contains(tape, want) {
			t.Errorf("tape missing %q:\n%s", want, tape)
		}
	}
	if len(outs.Shots) != 2 || outs.GIF != "" {
		t.Errorf("outputs = %+v", outs)
	}

	delete(m.Windows, "worker")
	if _, _, err := Tape(s, m, "/out/hub", "/tmp/x.gif"); err == nil || !strings.Contains(err.Error(), "not in the manifest") {
		t.Errorf("missing manifest window: err = %v", err)
	}

	s.Output = "gif"
	tape, outs, _ = Tape(s, &Manifest{Windows: map[string]WindowPlace{"orch": {}, "worker": {}}}, "/out/hub", "/tmp/x.gif")
	if outs.GIF != "/out/hub.gif" || !strings.Contains(tape, `Output "/out/hub.gif"`) {
		t.Errorf("gif output = %q", outs.GIF)
	}
}

func TestTerminalPixels(t *testing.T) {
	w, h := Terminal{Cols: 160, Rows: 45, FontSize: 14, Padding: 20}.Pixels()
	// The cell size measured from vhs: the result must hold the grid but
	// less than one extra cell.
	cw, ch := cellWidthPerPx*14, cellHeightPerPx*14
	if cols := float64(w-40) / cw; cols < 160 || cols >= 161 {
		t.Errorf("width %d holds %.2f columns", w, cols)
	}
	if rows := float64(h-40) / ch; rows < 45 || rows >= 46 {
		t.Errorf("height %d holds %.2f rows", h, rows)
	}
}

func TestApplyWindow(t *testing.T) {
	l := Layout{Root: "/r/agentctl-demo"}
	since := time.Now().Add(-4 * time.Minute)
	tr := sampleTranscript()
	parent := Parent{WindowID: "@7", SessionID: "sess-7"}
	tests := []struct {
		name  string
		w     Window
		check func(t *testing.T, ws windowstate.WindowState)
	}{
		{"done dispatched", Window{Key: "w", Project: "p", Agent: "go-engineer", Instance: "go-engineer", Mode: "dispatched", State: "done", Ticket: 61, TicketTitle: "CSV", PR: 64, Branch: "agent/go-engineer/issue-61"},
			func(t *testing.T, ws windowstate.WindowState) {
				if ws.State != "idle" || !ws.Done || ws.Seen || ws.Mode != "dispatch" || ws.Ticket != "#61" || ws.PRNumber != 64 || ws.SpawnedBySession != "sess-7" {
					t.Errorf("got %+v", ws)
				}
				if ws.WorktreeDir == "" || ws.Branch != "agent/go-engineer/issue-61" {
					t.Errorf("worktree/branch = %q %q", ws.WorktreeDir, ws.Branch)
				}
			}},
		{"done bare", Window{Key: "b", Project: "p", Agent: "docs", Instance: "docs", Mode: "bare", State: "done"},
			func(t *testing.T, ws windowstate.WindowState) {
				if ws.Done || !ws.TurnDone || ws.Seen || ws.Branch != "" || ws.WorktreeDir != "" {
					t.Errorf("got done=%v turnDone=%v seen=%v branch=%q", ws.Done, ws.TurnDone, ws.Seen, ws.Branch)
				}
			}},
		{"blocked", Window{Key: "x", Project: "p", Agent: "go-engineer", Mode: "bare", State: "blocked", PendingTool: "Bash", PendingDetail: "psql"},
			func(t *testing.T, ws windowstate.WindowState) {
				if ws.State != "blocked" || ws.PendingTool != "Bash" || ws.PendingToolDetail != "psql" || !ws.Seen {
					t.Errorf("got %+v", ws)
				}
			}},
		{"review", Window{Key: "r", Project: "p", Agent: "code-reviewer", Mode: "review-pr", ReviewPR: 138, State: "working", Title: "Review retries", CtxPct: 15},
			func(t *testing.T, ws windowstate.WindowState) {
				if ws.Mode != "review-pr" || ws.ShortTitle.Kind != "review" || ws.Task != "Review PR #138" || ws.CtxPct != 15 || ws.CtxPctSource != "statusline" {
					t.Errorf("got mode=%q kind=%q task=%q ctx=%d", ws.Mode, ws.ShortTitle.Kind, ws.Task, ws.CtxPct)
				}
				if ws.LastToolDetail != "go vet ./..." {
					t.Errorf("LastToolDetail = %q", ws.LastToolDetail)
				}
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ws windowstate.WindowState
			applyWindow(&ws, l, tt.w, tr, parent, since)
			if ws.SessionID != WindowSessionID(tt.w) || !strings.HasSuffix(ws.LogPath, ws.SessionID+".jsonl") {
				t.Errorf("session %q log %q", ws.SessionID, ws.LogPath)
			}
			tt.check(t, ws)
		})
	}
}

func TestNewLayout(t *testing.T) {
	for _, root := range []string{"", "relative/agentctl-demo", "/tmp", "/Users/me"} {
		if _, err := NewLayout(root); err == nil {
			t.Errorf("NewLayout(%q) succeeded", root)
		}
	}
	l, err := NewLayout("/tmp/agentctl-demo/")
	if err != nil || l.Root != "/tmp/agentctl-demo" {
		t.Fatalf("NewLayout = %+v, %v", l, err)
	}
	env := strings.Join(l.Env(""), "\n")
	for _, want := range []string{"HOME=/tmp/agentctl-demo/home", "CLAUDE_CONFIG_DIR=/tmp/agentctl-demo/home/.claude", "PATH=/tmp/agentctl-demo/bin:"} {
		if !strings.Contains(env, want) {
			t.Errorf("env missing %q", want)
		}
	}
	if strings.Contains(env, "TMUX=") {
		t.Error("env without a socket carries TMUX")
	}
	if !strings.Contains(strings.Join(l.Env("/s/agentctl-demo,1,0"), "\n"), "TMUX=/s/agentctl-demo,1,0") {
		t.Error("env with a socket lacks TMUX")
	}
}

func TestTmuxAlwaysUsesDemoSocket(t *testing.T) {
	var got []string
	var gotEnv []string
	tm := Tmux{Layout: Layout{Root: "/r/agentctl-demo"}, run: func(env []string, args ...string) ([]byte, error) {
		got, gotEnv = args, env
		return []byte("x\n"), nil
	}}
	if _, err := tm.Run("list-sessions"); err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 || got[0] != "-L" || got[1] != Label {
		t.Errorf("args = %v", got)
	}
	for _, kv := range gotEnv {
		if strings.HasPrefix(kv, "TMUX=") {
			t.Errorf("tmux env carries %s", kv)
		}
	}
}

func TestWriteWorld(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	l, err := NewLayout(filepath.Join(t.TempDir(), "agentctl-demo"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := ParseScene([]byte(minimalScene))
	if err != nil {
		t.Fatal(err)
	}
	s.Windows[1].Dirty = []string{"internal/x.go"}
	s.Windows[1].Transcript = TranscriptRef{Inline: sampleTranscript()}
	if err := WriteWorld(l, s, t.TempDir(), time.Now()); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(l.ConfigHome(), "agentctl", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.MonitorEnabled() {
		t.Error("monitor enabled")
	}
	if cmd, set := cfg.EffectiveNotifyCmd(); !set || cmd != "" {
		t.Errorf("notify_cmd = %q set=%v, want explicitly empty", cmd, set)
	}
	if cfg.Metrics.Enabled || cfg.Titles.Enabled == nil || *cfg.Titles.Enabled || cfg.DispatchApproval != "all" {
		t.Errorf("config = %s", data)
	}

	for _, p := range []string{
		filepath.Join(l.Bin(), "claude"),
		filepath.Join(l.Bin(), "gh"),
		filepath.Join(l.ClaudeDir(), "agents", "go-engineer.md"),
		filepath.Join(l.ClaudeDir(), "agents", "orchestrator.md"),
		filepath.Join(l.Repo("acme-api"), ".git"),
		l.PaneSpec("acme-api", "worker"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("missing %s", p)
		}
	}
	worker, _ := s.Window("worker")
	if _, err := os.Stat(filepath.Join(WorkDir(l, worker), "internal", "x.go")); err != nil {
		t.Errorf("worktree dirty file: %v", err)
	}
	logData, err := os.ReadFile(LogPath(l, worker))
	if err != nil || !bytes.Contains(logData, []byte(`"pr-link"`)) {
		t.Errorf("transcript at %s: %v", LogPath(l, worker), err)
	}
	if !strings.HasPrefix(LogPath(l, worker), l.ClaudeDir()+string(filepath.Separator)+"projects") {
		t.Errorf("transcript outside the fake account: %s", LogPath(l, worker))
	}
	out, err := exec.Command("git", "-C", WorkDir(l, worker), "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil || strings.TrimSpace(string(out)) != worker.Branch {
		t.Errorf("worktree branch = %q, %v", out, err)
	}
	if agent, _ := os.ReadFile(filepath.Join(l.ClaudeDir(), "agents", "orchestrator.md")); !bytes.Contains(agent, []byte("role: orchestrator")) {
		t.Errorf("orchestrator agent lacks its role:\n%s", agent)
	}
}
