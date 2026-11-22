package serve

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/windowstate"
)

func backfillEnv(t *testing.T, ws windowstate.WindowState) (*Server, string, *int, *string) {
	t.Helper()
	windowstate.Init()
	if err := windowstate.Write("@77", ws); err != nil {
		t.Fatal(err)
	}
	scans, text := 0, new(string)
	oldF, oldFind := firstHumanTextF, findSessionLog
	firstHumanTextF = func(string) string { scans++; return *text }
	findSessionLog = func(id string) string {
		if id == "" {
			return ""
		}
		return "/logs/" + id + ".jsonl"
	}
	t.Cleanup(func() { firstHumanTextF, findSessionLog = oldF, oldFind })
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	return &Server{now: func() time.Time { return now }}, filepath.Dir(windowstate.Dir()), &scans, text
}

func metaOf(t *testing.T, dir string) fleet.Meta {
	t.Helper()
	return fleet.LoadMeta(fleet.Group{RuntimeDir: dir})["@77"]
}

func TestPromptForDerivesOnceAndPersists(t *testing.T) {
	s, dir, scans, text := backfillEnv(t, windowstate.WindowState{SessionID: "sid", FirstPrompt: "short"})
	*text = "the full prompt"
	if got := s.promptFor(dir, metaOf(t, dir)); got != "the full prompt" {
		t.Fatalf("prompt = %q", got)
	}
	ws, _ := windowstate.ReadIn(dir, "@77")
	if ws.Prompt != "the full prompt" {
		t.Fatalf("persisted prompt = %q", ws.Prompt)
	}
	if got := s.promptFor(dir, metaOf(t, dir)); got != "the full prompt" || *scans != 1 {
		t.Fatalf("second poll: %q, scans=%d", got, *scans)
	}
}

func TestPromptForCapsDerivedPrompt(t *testing.T) {
	s, dir, _, text := backfillEnv(t, windowstate.WindowState{SessionID: "sid"})
	big := make([]byte, windowstate.MaxPromptBytes*2)
	for i := range big {
		big[i] = 'a'
	}
	*text = string(big)
	if got := s.promptFor(dir, metaOf(t, dir)); len(got) != windowstate.MaxPromptBytes {
		t.Fatalf("len = %d", len(got))
	}
}

func TestPromptForNegativeCache(t *testing.T) {
	s, dir, scans, text := backfillEnv(t, windowstate.WindowState{SessionID: "sid", FirstPrompt: "short"})
	for i := 0; i < 3; i++ {
		if got := s.promptFor(dir, metaOf(t, dir)); got != "short" {
			t.Fatalf("prompt = %q, want fallback", got)
		}
	}
	if *scans != 1 {
		t.Fatalf("scans = %d, want 1", *scans)
	}
	// /clear: a new session ID rescans immediately.
	if err := windowstate.Update("@77", func(w *windowstate.WindowState) { w.SessionID = "sid2" }); err != nil {
		t.Fatal(err)
	}
	*text = "after clear"
	if got := s.promptFor(dir, metaOf(t, dir)); got != "after clear" || *scans != 2 {
		t.Fatalf("got %q scans=%d", got, *scans)
	}
	// Retry after the window passes.
	later := s.now().Add(promptRescanAfter + time.Minute)
	if err := windowstate.Update("@77", func(w *windowstate.WindowState) { w.Prompt = "" }); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return later }
	if got := s.promptFor(dir, metaOf(t, dir)); got != "after clear" {
		t.Fatalf("got %q", got)
	}
}

func TestPromptForNoLogOrRecord(t *testing.T) {
	s, dir, scans, text := backfillEnv(t, windowstate.WindowState{})
	*text = "x"
	if got := s.promptFor(dir, metaOf(t, dir)); got != "" || *scans != 0 {
		t.Fatalf("no session: %q scans=%d", got, *scans)
	}
	// A window without a record is never created.
	m := fleet.Meta{WindowID: "@99", SessionID: "sid"}
	if got := s.promptFor(dir, m); got != "x" {
		t.Fatalf("got %q", got)
	}
	if ws, _ := windowstate.ReadIn(dir, "@99"); ws.Prompt != "" || ws.SessionID != "" {
		t.Fatalf("record created: %+v", ws)
	}
}

func TestPromptForStoredWins(t *testing.T) {
	s, dir, scans, _ := backfillEnv(t, windowstate.WindowState{SessionID: "sid", Prompt: "stored"})
	if got := s.promptFor(dir, metaOf(t, dir)); got != "stored" || *scans != 0 {
		t.Fatalf("got %q scans=%d", got, *scans)
	}
}
