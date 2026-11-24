package serve

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/closedlog"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/cron"
	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/serve/fleetwatch"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/sweeprun"
)

func handleRow(id, sessionID, title string) fleet.Row {
	return fleet.Row{Session: "agentctl-p", Project: "p",
		AgentState: state.AgentState{Name: "go-engineer", AgentName: "go-engineer", WindowID: id, SessionID: sessionID, ShortTitle: title}}
}

func TestHandleRoute(t *testing.T) {
	live := handleRow("@7", "a3f9c0de-0000", "esc fix in hub")
	other := handleRow("@8", "a3f9ffff-1111", "other")
	group := fleet.Group{Session: "agentctl-p", Project: "p", Rows: []fleet.Row{live, other}}
	closed := closedlog.Record{ClosedAt: time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC), WindowID: "@3", SessionID: "bbbb1111-2222", Agent: "go-engineer", ShortTitle: "old work"}
	ix := fleet.NewIndex([]fleet.Group{group}, []closedlog.Record{closed})
	src := &fakeFleet{}
	src.set(fleetwatch.FromFleet([]fleet.Group{group})...)

	ts := newTestServerWith(t, config.ServeConfig{}, func(o *Options) {
		o.Fleet = src
		o.Panes = &fakePanes{}
		o.Agents = fakeAgents{}
		o.Awake = &fakeHolder{}
		o.HandleIndex = func() *fleet.Index { return ix }
	})
	_, tok := ts.pairDevice(t, "phone")
	get := func(h string) (int, map[string]any) {
		w := do(ts.Handler(), "GET", "/v1/handles/"+h, withCookie(tok))
		var m map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &m)
		return w.Code, m
	}

	if code, m := get("a3f9c"); code != http.StatusOK || m["live"] != true || m["window_id"] != "@7" || m["session"] != "agentctl-p" || m["label"] != "esc fix in hub" {
		t.Errorf("live: %d %v", code, m)
	}
	stale := handleRow("@9", "cccc1111-3333", "stale")
	ix2 := fleet.NewIndex([]fleet.Group{{Session: "agentctl-p", Rows: []fleet.Row{stale}}}, nil)
	ts.handles = func() *fleet.Index { return ix2 }
	if code, m := get("cccc"); code != http.StatusOK || m["live"] != false || m["closed"] != false {
		t.Errorf("stale: %d %v", code, m)
	}
	ts.handles = func() *fleet.Index { return ix }
	if code, m := get("bbbb"); code != http.StatusOK || m["live"] != false || m["closed_at"] != "2026-11-20T09:00:00Z" || m["label"] != "old work" {
		t.Errorf("closed: %d %v", code, m)
	}
	if code, m := get("a3f9"); code != http.StatusConflict || m["code"] != "ambiguous" || len(m["candidates"].([]any)) != 2 || !strings.Contains(m["error"].(string), "esc fix in hub") {
		t.Errorf("ambiguous: %d %v", code, m)
	}
	for _, bad := range []string{"zzzz", "a3f9%2Fx", strings.Repeat("a", 5000), "a3f9_c"} {
		if code, m := get(bad); code != http.StatusNotFound || m["error"] != "unknown session" {
			t.Errorf("invalid %.10q: %d %v", bad, code, m)
		}
	}
	if code, m := get("bbbb"); code != http.StatusOK || m["closed"] != true {
		t.Errorf("closed flag: %d %v", code, m)
	}
	if code, m := get("dead"); code != http.StatusNotFound || m["error"] != "unknown session" {
		t.Errorf("unknown: %d %v", code, m)
	}
	if w := do(ts.Handler(), "GET", "/v1/handles/bbbb"); w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden && w.Code != http.StatusNotFound {
		t.Errorf("unauthenticated status %d", w.Code)
	}
}

func TestRunsWithHandles(t *testing.T) {
	closed := closedlog.Record{WindowID: "@3", SessionID: "bbbb1111-2222", Agent: "go-engineer", ScheduledBy: "nightly"}
	ix := fleet.NewIndex(nil, []closedlog.Record{closed})
	s := &Server{handles: func() *fleet.Index { return ix }}
	runs := s.runsWithHandles([]cron.Entry{{Job: "nightly", SessionID: "bbbb1111-2222"}, {Job: "nightly"}}, "nightly")
	if runs[0].Handle != "bbbb" || runs[0].Label != "" || runs[1].Handle != "" {
		t.Errorf("runs %+v", runs)
	}
}

func TestNameSweepRows(t *testing.T) {
	rep := sweeprun.Report{Closed: []sweeprun.Row{{WindowID: "@1", Name: "✓go-engineer"}, {WindowID: "@2", Name: "x"}}}
	nameSweepRows(&rep, map[string]string{"@1": "esc fix (a3f9)"})
	if rep.Closed[0].Name != "esc fix (a3f9)" || rep.Closed[1].Name != "x" {
		t.Errorf("rows %+v", rep.Closed)
	}
}
