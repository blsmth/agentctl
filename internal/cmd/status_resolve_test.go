package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/closedlog"
	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/state"
)

func resolveFixture() ([]statusRow, *fleet.Index) {
	mk := func(win, sid, title string) fleet.Row {
		return fleet.Row{AgentState: state.AgentState{WindowID: win, SessionID: sid, Name: "go-engineer", ShortTitle: title}}
	}
	g := fleet.Group{Session: "agentctl-proj", Project: "proj", Rows: []fleet.Row{
		mk("@1234", "a3f9c1d2-1", "first"),
		mk("@7", "a3f9c2e0-2", "second"),
		mk("@8", "", "launching"),
	}}
	var rows []statusRow
	for _, r := range g.Rows {
		rows = append(rows, statusRow{row: r, group: g})
	}
	closed := []closedlog.Record{{SessionID: "1234beef-3", WindowID: "@50", Agent: "go-engineer", ShortTitle: "old", ProjectDir: "/x/proj"}}
	return rows, fleet.NewIndex([]fleet.Group{g}, closed)
}

func TestResolveTarget(t *testing.T) {
	rows, ix := resolveFixture()

	t.Run("handle resolves a live window", func(t *testing.T) {
		got, err := resolveTarget(rows, ix, "a3f9c1")
		if err != nil || got.closed != nil || got.row.row.WindowID != "@1234" {
			t.Errorf("got %+v, %v", got, err)
		}
	})
	t.Run("full session id", func(t *testing.T) {
		got, err := resolveTarget(rows, ix, "a3f9c2e0-2")
		if err != nil || got.row.row.WindowID != "@7" {
			t.Errorf("got %+v, %v", got, err)
		}
	})
	t.Run("@N still names the window even when digits match a handle", func(t *testing.T) {
		got, err := resolveTarget(rows, ix, "@1234")
		if err != nil || got.row.row.WindowID != "@1234" {
			t.Errorf("got %+v, %v", got, err)
		}
	})
	t.Run("a numeric handle wins over the same window number", func(t *testing.T) {
		got, err := resolveTarget(rows, ix, "1234")
		if err != nil || got.closed == nil || got.closed.Label != "old" {
			t.Errorf("got %+v, %v", got, err)
		}
	})
	t.Run("short bare number is a window", func(t *testing.T) {
		got, err := resolveTarget(rows, ix, "7")
		if err != nil || got.row.row.WindowID != "@7" {
			t.Errorf("got %+v, %v", got, err)
		}
	})
	t.Run("agent name still works", func(t *testing.T) {
		if _, err := resolveTarget(rows[2:], ix, "go-engineer"); err != nil {
			t.Error(err)
		}
	})
	t.Run("ambiguous prefix lists candidates with labels", func(t *testing.T) {
		_, err := resolveTarget(rows, ix, "a3f9")
		var amb *fleet.AmbiguousError
		if !errors.As(err, &amb) || !strings.Contains(err.Error(), `"first"`) || !strings.Contains(err.Error(), `"second"`) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("unknown hex reference says so", func(t *testing.T) {
		_, err := resolveTarget(rows, ix, "dead")
		if err == nil || !strings.Contains(err.Error(), "unknown session") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestRowsGetHandlesAndLabels(t *testing.T) {
	old := closedLoad
	closedLoad = func(time.Time) ([]closedlog.Record, error) {
		return []closedlog.Record{{SessionID: "a3f9ffff-9", ShortTitle: "closed twin"}}, nil
	}
	t.Cleanup(func() { closedLoad = old })
	rows, _ := resolveFixture()
	g := rows[0].group
	got := statusRowsFromGroups([]fleet.Group{g}, StatusOptions{})
	if len(got) != 3 {
		t.Fatalf("rows = %d", len(got))
	}
	// The closed session shares "a3f9", so the live handles lengthen past it.
	if got[0].handle != "a3f9c1" || got[0].label != "first" {
		t.Errorf("row 0 handle/label = %q/%q", got[0].handle, got[0].label)
	}
	if got[2].handle != "" {
		t.Errorf("launching window has handle %q", got[2].handle)
	}
	if j := got[0].jsonRow(); j.Handle != "a3f9c1" || j.Label != "first" || j.WindowID != "@1234" {
		t.Errorf("json = %+v", j.Window)
	}
}

func TestPeekClosedSession(t *testing.T) {
	stubClosed(t, []closedlog.Record{{ClosedAt: time.Now(), SessionID: "9c0d1111-1", ShortTitle: "old work", ProjectDir: "/x/p"}}, time.Now())
	err := RunPeek("9c0d", 10, false)
	if err == nil || !strings.Contains(err.Error(), "closed") || !strings.Contains(err.Error(), "agentctl resume 9c0d") {
		t.Errorf("err = %v", err)
	}
}

func TestStatusDetailClosedSession(t *testing.T) {
	now := time.Now()
	stubClosed(t, []closedlog.Record{{ClosedAt: now.Add(-time.Hour), SessionID: "9c0d1111-1", ShortTitle: "old work", ProjectDir: "/x/p", Agent: "go-engineer", Done: true}}, now)
	var out bytes.Buffer
	ix := loadSessionIndex(nil)
	s, err := ix.Resolve("9c0d")
	if err != nil {
		t.Fatal(err)
	}
	if err := printClosedDetail(&out, s, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"9c0d", "old work", "(closed)", "agentctl resume 9c0d", "1h ago"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	if err := printClosedDetail(&out, s, true); err != nil {
		t.Fatal(err)
	}
	var j map[string]any
	if err := json.Unmarshal(out.Bytes(), &j); err != nil || j["handle"] != "9c0d" || j["label"] != "old work" {
		t.Errorf("json = %v, %v", j, err)
	}
}

func TestRunResume(t *testing.T) {
	now := time.Now()
	stubClosed(t, []closedlog.Record{
		{ClosedAt: now, SessionID: "9c0d1111-1", ShortTitle: "old work", ProjectDir: "/x/p", Agent: "go-engineer"},
		{ClosedAt: now, SessionID: "9c0e2222-2", ShortTitle: "other", Agent: "go-engineer"},
		{ClosedAt: now, SessionID: "9c0e3333-3", ShortTitle: "third", ProjectDir: "/x/p"},
	}, now)
	var spawned []closedlog.Record
	old := resumeSpawn
	resumeSpawn = func(r closedlog.Record) (string, error) { spawned = append(spawned, r); return "@9\n", nil }
	t.Cleanup(func() { resumeSpawn = old })

	t.Run("resumes a closed session through hub-resume", func(t *testing.T) {
		spawned = nil
		var out bytes.Buffer
		if err := RunResume(&out, "9c0d"); err != nil {
			t.Fatal(err)
		}
		if len(spawned) != 1 || spawned[0].SessionID != "9c0d1111-1" {
			t.Errorf("spawned = %+v", spawned)
		}
		if !strings.Contains(out.String(), "old work") || strings.Contains(out.String(), "@") {
			t.Errorf("out = %q", out.String())
		}
	})
	t.Run("ambiguous and unknown spawn nothing", func(t *testing.T) {
		spawned = nil
		var amb *fleet.AmbiguousError
		if err := RunResume(&bytes.Buffer{}, "9c0e"); !errors.As(err, &amb) {
			t.Errorf("err = %v", err)
		}
		if err := RunResume(&bytes.Buffer{}, "ffff"); !errors.Is(err, fleet.ErrUnknownSession) {
			t.Errorf("err = %v", err)
		}
		if len(spawned) != 0 {
			t.Errorf("spawned = %+v", spawned)
		}
	})
	t.Run("record without a project dir", func(t *testing.T) {
		if err := RunResume(&bytes.Buffer{}, "9c0e2"); err == nil || !strings.Contains(err.Error(), "project directory") {
			t.Errorf("err = %v", err)
		}
	})
}
