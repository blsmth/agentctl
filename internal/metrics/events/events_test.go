package events

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/windowstate"
)

func enable(t *testing.T, on bool, rt string) {
	t.Helper()
	oe, or, on_ := Enabled, RuntimeDir, Now
	Enabled = func() bool { return on }
	RuntimeDir = func() string { return rt }
	t.Cleanup(func() { Enabled, RuntimeDir, Now = oe, or, on_ })
}

func readAll(t *testing.T, rt string) []byte {
	t.Helper()
	var all []byte
	for _, p := range Files(rt) {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, b...)
	}
	return all
}

func TestAppendConcurrentWritersWholeLines(t *testing.T) {
	rt := t.TempDir()
	const writers, per = 16, 50
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				e := Event{Project: "/p", WindowID: "@1", Agent: "a", From: "idle", To: "working", Tool: strings.Repeat("x", 3000)}
				if err := Append(rt, e); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()

	data := readAll(t, rt)
	lines := bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n"))
	if len(lines) != writers*per {
		t.Fatalf("got %d lines, want %d", len(lines), writers*per)
	}
	for i, l := range lines {
		if _, ok := Decode(l); !ok {
			t.Fatalf("line %d is not a whole event: %q", i, l)
		}
	}
}

func TestEncodeBounded(t *testing.T) {
	huge := strings.Repeat("é", 100000)
	for name, e := range map[string]Event{
		"huge tool":     {WindowID: "@1", To: "working", Tool: huge},
		"huge all":      {Project: huge, WindowID: huge, Instance: huge, Agent: huge, From: huge, To: huge, Tool: huge},
		"quotes escape": {WindowID: "@1", To: "x", Tool: strings.Repeat(`"`, 5000)},
	} {
		t.Run(name, func(t *testing.T) {
			b, err := encode(e)
			if err != nil {
				t.Fatal(err)
			}
			if len(b) > MaxLine {
				t.Fatalf("line is %d bytes, max %d", len(b), MaxLine)
			}
			if b[len(b)-1] != '\n' {
				t.Fatal("no trailing newline")
			}
			if _, ok := Decode(b); !ok {
				t.Fatal("does not decode")
			}
		})
	}
}

func TestAppendFileModeAndSymlink(t *testing.T) {
	rt := t.TempDir()
	if err := Append(rt, Event{WindowID: "@1", To: "working"}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(Files(rt)[0])
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}

	rt2 := t.TempDir()
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(rt2, Dir)); err != nil {
		t.Fatal(err)
	}
	if err := Append(rt2, Event{WindowID: "@1", To: "working"}); err == nil {
		t.Error("append through a symlinked metrics dir should fail")
	}
}

func TestEmitDisabledWritesNothing(t *testing.T) {
	rt := t.TempDir()
	enable(t, false, rt)
	Emit(rt, Event{WindowID: "@1", To: "working"})
	RecordStateChange("@1", "idle", windowstate.WindowState{State: "working", Instance: "a:1"})
	RecordGone(rt, "@1", windowstate.WindowState{State: "working", Instance: "a:1"})
	if _, err := os.Stat(DirFor(rt)); !os.IsNotExist(err) {
		t.Errorf("metrics dir exists while disabled: %v", err)
	}
}

func TestRecordStateChange(t *testing.T) {
	tests := []struct {
		name     string
		prev     string
		ws       windowstate.WindowState
		wantFrom string
		wantTo   string
		wantTool string
		want     bool
	}{
		{"idle to working", "idle", windowstate.WindowState{State: "working", Instance: "go-engineer:2"}, "idle", "working", "", true},
		{"empty prev is idle", "", windowstate.WindowState{State: "working", Instance: "a:1"}, "idle", "working", "", true},
		{"no change", "working", windowstate.WindowState{State: "working", Instance: "a:1", PendingTool: "Bash"}, "", "", "", false},
		{"empty to idle is no change", "idle", windowstate.WindowState{State: "", Instance: "a:1"}, "", "", "", false},
		{"to blocked carries tool", "working", windowstate.WindowState{State: "blocked", Instance: "a:1", PendingTool: "Bash"}, "working", "blocked", "Bash", true},
		{"resume from blocked has no tool", "blocked", windowstate.WindowState{State: "working", Instance: "a:1", PendingTool: "Bash"}, "blocked", "working", "", true},
		{"to idle has no tool", "working", windowstate.WindowState{State: "idle", Instance: "a:1", PendingTool: "Bash"}, "working", "idle", "", true},
		{"shell window ignored", "idle", windowstate.WindowState{State: "working", Instance: "shell"}, "", "", "", false},
		{"improve window ignored", "idle", windowstate.WindowState{State: "working", Instance: "improve~x"}, "", "", "", false},
		{"no identity ignored", "idle", windowstate.WindowState{State: "working"}, "", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := filepath.Join(t.TempDir(), "proj", ".agentctl")
			enable(t, true, rt)
			RecordStateChange("@7", tt.prev, tt.ws)
			data := readAll(t, rt)
			if !tt.want {
				if len(data) != 0 {
					t.Fatalf("unexpected event: %s", data)
				}
				return
			}
			e, ok := Decode(bytes.TrimSpace(data))
			if !ok {
				t.Fatalf("no decodable event in %q", data)
			}
			if e.From != tt.wantFrom || e.To != tt.wantTo || e.Tool != tt.wantTool || e.WindowID != "@7" {
				t.Errorf("event = %+v", e)
			}
			if e.Project != filepath.Dir(rt) {
				t.Errorf("project = %q, want %q", e.Project, filepath.Dir(rt))
			}
		})
	}
}

func TestRecordGone(t *testing.T) {
	rt := filepath.Join(t.TempDir(), ".agentctl")
	enable(t, true, rt)
	RecordGone(rt, "@3", windowstate.WindowState{State: "working", Instance: "go-engineer:1", AgentName: "go-engineer"})
	e, ok := Decode(bytes.TrimSpace(readAll(t, rt)))
	if !ok || e.From != "working" || e.To != "gone" || e.Agent != "go-engineer" {
		t.Fatalf("event = %+v ok=%v", e, ok)
	}
}

func TestTailer(t *testing.T) {
	rt := t.TempDir()
	day1 := time.Date(2026, 11, 16, 23, 59, 0, 0, time.UTC)
	day2 := day1.Add(2 * time.Minute)
	ev := func(ts time.Time, to string) Event {
		return Event{TS: ts, WindowID: "@1", Agent: "a", From: "idle", To: to}
	}
	// History that must not be replayed.
	if err := Append(rt, ev(day1.Add(-time.Hour), "working")); err != nil {
		t.Fatal(err)
	}
	tl := NewTailer(rt)
	if got := tl.Poll(); len(got) != 0 {
		t.Fatalf("history replayed: %v", got)
	}

	// Append to the existing file, then roll over to a new day file.
	if err := Append(rt, ev(day1, "blocked")); err != nil {
		t.Fatal(err)
	}
	if err := Append(rt, ev(day2, "idle")); err != nil {
		t.Fatal(err)
	}
	got := tl.Poll()
	if len(got) != 2 || got[0].To != "blocked" || got[1].To != "idle" {
		t.Fatalf("poll = %+v", got)
	}
	if got := tl.Poll(); len(got) != 0 {
		t.Fatalf("second poll = %+v, want none", got)
	}

	// A partial line is held back until it is complete.
	p := filepath.Join(DirFor(rt), FileName(day2))
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	line, _ := encode(ev(day2, "working"))
	f.Write(line[:10]) //nolint:errcheck
	if got := tl.Poll(); len(got) != 0 {
		t.Fatalf("partial line delivered: %+v", got)
	}
	f.Write(line[10:]) //nolint:errcheck
	f.Close()          //nolint:errcheck
	if got := tl.Poll(); len(got) != 1 || got[0].To != "working" {
		t.Fatalf("completed line = %+v", got)
	}

	// Deleted then recreated (rotation): the new file is read from the start.
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if got := tl.Poll(); len(got) != 0 {
		t.Fatalf("after delete = %+v", got)
	}
	if err := Append(rt, ev(day2, "blocked")); err != nil {
		t.Fatal(err)
	}
	if got := tl.Poll(); len(got) != 1 || got[0].To != "blocked" {
		t.Fatalf("after recreate = %+v", got)
	}

	// Truncated in place: read from the start again.
	if err := os.Truncate(p, 0); err != nil {
		t.Fatal(err)
	}
	if err := Append(rt, ev(day2, "idle")); err != nil {
		t.Fatal(err)
	}
	if got := tl.Poll(); len(got) != 1 || got[0].To != "idle" {
		t.Fatalf("after truncate = %+v", got)
	}
}

func TestTailerSinceFiltersOldEvents(t *testing.T) {
	rt := t.TempDir()
	start := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	for _, ts := range []time.Time{start.Add(-time.Minute), start.Add(time.Second)} {
		if err := Append(rt, Event{TS: ts, WindowID: "@1", Agent: "a", From: "idle", To: "working"}); err != nil {
			t.Fatal(err)
		}
	}
	got := NewTailerSince(rt, start).Poll()
	if len(got) != 1 || !got[0].TS.Equal(start.Add(time.Second)) {
		t.Fatalf("got %+v", got)
	}
}

func TestPrune(t *testing.T) {
	rt := t.TempDir()
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	for _, d := range []int{0, 1, 5, 9} {
		if err := Append(rt, Event{TS: now.AddDate(0, 0, -d), WindowID: "@1", To: "idle"}); err != nil {
			t.Fatal(err)
		}
	}
	Prune(rt, now, 3*24*time.Hour)
	if n := len(Files(rt)); n != 2 {
		t.Errorf("files after prune = %d, want 2", n)
	}
}
