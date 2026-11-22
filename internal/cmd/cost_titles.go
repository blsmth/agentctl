package cmd

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/claudehome"
	"github.com/blsmth/agentctl/internal/closedlog"
	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/registry"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// costTitleWidth is the TITLE column width of the cost report, in cells.
const costTitleWidth = 30

// costTitles maps session IDs to a display title. Seam for tests.
var costTitles = loadCostTitles

// titleIndex collects titles per session ID. A real short title always beats
// a first-prompt fallback; among equals the first source added wins.
type titleIndex struct {
	short  map[string]string
	prompt map[string]string
}

func newTitleIndex() *titleIndex {
	return &titleIndex{short: map[string]string{}, prompt: map[string]string{}}
}

func (x *titleIndex) add(sessionID, short, prompt string) {
	if sessionID == "" {
		return
	}
	if short = strings.TrimSpace(short); short != "" {
		if _, ok := x.short[sessionID]; !ok {
			x.short[sessionID] = short
		}
	}
	if p := firstTitleLine(prompt); p != "" {
		if _, ok := x.prompt[sessionID]; !ok {
			x.prompt[sessionID] = p
		}
	}
}

func (x *titleIndex) get(sessionID string) string {
	t, ok := x.short[sessionID]
	if !ok {
		t = x.prompt[sessionID]
	}
	return fleet.TruncateWords(t, costTitleWidth)
}

func firstTitleLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return strings.Join(strings.Fields(s), " ")
}

// loadCostTitles reads titles from the windowstate and restore.json of every
// project runtime dir it can find (this project, live sessions in the
// registry, and the runtime dirs recorded by the closed-window ledger), plus
// the ledger itself. Read-only: no network, no LLM, no writes.
func loadCostTitles(since time.Duration) func(sessionID string) string {
	idx := newTitleIndex()
	dirs := map[string]bool{}
	addDir := func(d string) {
		if d != "" {
			dirs[d] = true
		}
	}
	addDir(paths.RuntimeDir())
	if entries, err := registry.List(); err == nil {
		for _, e := range entries {
			addDir(e.RuntimeDir)
		}
	}
	// The ledger is bounded by its own 90-day retention; read everything in
	// range plus a day of slack.
	recs, _ := closedlog.Load(costNow().Add(-since - 24*time.Hour))
	for _, r := range recs {
		addDir(r.RuntimeDir)
	}
	for _, r := range recs {
		idx.add(r.SessionID, r.ShortTitle, firstNonEmpty(r.FirstPrompt, r.TicketTitle))
	}
	for d := range dirs {
		for _, ws := range windowstate.AllFrom(windowstate.WindowsDirFor(d)) {
			sid := ws.SessionID
			if sid == "" && ws.LogPath != "" && ws.Harness == "" {
				sid = strings.TrimSuffix(filepath.Base(ws.LogPath), ".jsonl")
			}
			idx.add(sid, ws.ShortTitle.Title, firstNonEmpty(ws.FirstPrompt, ws.TicketTitle))
		}
		if st, err := state.LoadFrom(filepath.Join(d, state.RestoreFileName)); err == nil && st != nil {
			for _, a := range st.Agents {
				idx.add(a.SessionID, "", firstNonEmpty(a.FirstPrompt, a.TicketTitle))
			}
		}
	}
	return idx.get
}

// accountOfLog names the account whose config dir holds the transcript, ""
// when it is under none (an unnamed default dir, or a pi session).
func accountOfLog(logFile string, accts []claudehome.Account) string {
	for _, a := range accts {
		if a.Dir != "" && strings.HasPrefix(logFile, filepath.Clean(a.Dir)+string(filepath.Separator)) {
			return a.Name
		}
	}
	return ""
}
