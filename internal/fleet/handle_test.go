package fleet

import (
	"errors"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/closedlog"
	"github.com/blsmth/agentctl/internal/state"
)

func TestHandle(t *testing.T) {
	tests := []struct {
		name  string
		id    string
		known []string
		want  string
	}{
		{"no session id has no handle", "", []string{"abcd1234"}, ""},
		{"min length", "a3f9c1d2-0000", []string{"a3f9c1d2-0000", "b1111111"}, "a3f9"},
		{"lengthens on a shared prefix", "a3f9c1d2", []string{"a3f9c1d2", "a3f9c2e0"}, "a3f9c1"},
		{"lengthens past the longest clash", "a3f9c1d2", []string{"a3f9c1d9", "a3f9c2e0"}, "a3f9c1d2"},
		{"other id is a prefix of ours", "a3f9c1", []string{"a3f9"}, "a3f9c"},
		{"known need not contain it", "a3f9c1d2", nil, "a3f9"},
		{"short id", "ab", nil, "ab"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Handle(tt.id, tt.known); got != tt.want {
				t.Errorf("Handle = %q, want %q", got, tt.want)
			}
		})
	}
}

func testGroups() []Group {
	row := func(win, sid, title string) Row {
		return Row{AgentState: state.AgentState{WindowID: win, SessionID: sid, ShortTitle: title}}
	}
	return []Group{{Session: "agentctl-p", Project: "p", Rows: []Row{
		row("@1", "a3f9c1d2-1111", "first"),
		row("@2", "a3f9c2e0-2222", "second"),
		row("@3", "", "launching"),
		{AgentState: state.AgentState{WindowID: "@4", SessionID: "zzzz"}, Shell: true},
	}}}
}

func TestIndexResolve(t *testing.T) {
	closed := []closedlog.Record{
		{SessionID: "7e21aaaa-3333", WindowID: "@9", Agent: "go-engineer", ShortTitle: "old work"},
		{SessionID: "a3f9c1d2-1111", WindowID: "@1", ShortTitle: "stale record of a live session"},
	}
	ix := NewIndex(testGroups(), closed)

	t.Run("handle lengthens for a clash", func(t *testing.T) {
		s, err := ix.Resolve("a3f9c1")
		if err != nil || s.SessionID != "a3f9c1d2-1111" || s.Handle != "a3f9c1" || s.Label != "first" || !s.Live {
			t.Errorf("got %+v, %v", s, err)
		}
	})
	t.Run("full id", func(t *testing.T) {
		s, err := ix.Resolve("a3f9c2e0-2222")
		if err != nil || s.Label != "second" {
			t.Errorf("got %+v, %v", s, err)
		}
	})
	t.Run("closed session resolves with its record", func(t *testing.T) {
		s, err := ix.Resolve("7E21")
		if err != nil || s.Live || s.Closed == nil || s.Closed.Agent != "go-engineer" || s.Label != "old work" || s.Handle != "7e21" {
			t.Errorf("got %+v, %v", s, err)
		}
	})
	t.Run("live wins over a stale closed record", func(t *testing.T) {
		s, _ := ix.Resolve("a3f9c1d2-1111")
		if !s.Live || s.Closed != nil {
			t.Errorf("got %+v", s)
		}
	})
	t.Run("ambiguous lists candidates with labels", func(t *testing.T) {
		_, err := ix.Resolve("a3f9")
		var amb *AmbiguousError
		if !errors.As(err, &amb) || len(amb.Candidates) != 2 {
			t.Fatalf("err = %v", err)
		}
		for _, want := range []string{`a3f9c1 "first"`, `a3f9c2 "second"`} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%q missing %q", err, want)
			}
		}
	})
	t.Run("unknown and too short", func(t *testing.T) {
		for _, ref := range []string{"ffff", "a3f", ""} {
			if _, err := ix.Resolve(ref); !errors.Is(err, ErrUnknownSession) {
				t.Errorf("Resolve(%q) err = %v", ref, err)
			}
		}
	})
	t.Run("rows with no session id and shells are not indexed", func(t *testing.T) {
		if got := ix.HandleFor(""); got != "" {
			t.Errorf("HandleFor(\"\") = %q", got)
		}
		if _, err := ix.Resolve("zzzz"); !errors.Is(err, ErrUnknownSession) {
			t.Errorf("shell indexed: %v", err)
		}
	})
}
