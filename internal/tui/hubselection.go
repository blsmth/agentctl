package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/logging"
)

// hubSelectionFile is the name, in the agentctl state dir, of the file the
// hub list publishes its selection to. The follow-hub HUD (`agentctl hud
// --follow-hub`) reads it each tick and retargets.
const hubSelectionFile = "hub-selection.json"

// HubSelection is the hub's selected agent window: which tmux session and
// window it is, and the runtime dir ("<project>/.agentctl") its windowstate
// records and agentctl logs live in. The zero value means "no agent selected"
// (nothing, a group header, or a shell row).
type HubSelection struct {
	Session    string `json:"session,omitempty"`
	WindowID   string `json:"window_id,omitempty"`
	RuntimeDir string `json:"runtime_dir,omitempty"`
	// Name and Title are display hints for the follower's header: the row's
	// instance name and short title as the hub list shows them.
	Name  string `json:"name,omitempty"`
	Title string `json:"title,omitempty"`
	// Status is the selected row's detail for the hub's status pane (see
	// hubStatusFor); nil with the zero selection.
	Status *hubStatus `json:"status,omitempty"`
}

// hubStatusVersion marks a status block written for the facts-footer layout.
// A block without it (written by an older hub) still carries Chips, and the
// pane lays it out the old way.
const hubStatusVersion = 2

// hubStatus is what the hub's status pane shows for the selected row, built
// by the hub list from the same helpers that draw the row (rowGlyph,
// rowRecap, detailPartsFor, rowChips) so both always agree. Layout to the
// pane width happens in the status pane. Ages and durations are published as
// timestamps, not rendered text: the pane ticks every 250ms and computes them.
// Every field after Chips is additive and omitempty.
type hubStatus struct {
	Glyph   string `json:"glyph,omitempty"`
	Role    string `json:"role,omitempty"`
	Title   string `json:"title,omitempty"`
	Project string `json:"project,omitempty"`
	// Note is the fallback sentence of a row with no recap ("no prompt yet
	// · 4m"), drawn muted under the title.
	Note   string         `json:"note,omitempty"`
	Detail rowDetailParts `json:"detail"`
	// Chips is the old pill line, still published for one release so an older
	// pane keeps rendering across a hot reload; the new pane ignores it.
	Chips []string `json:"chips,omitempty"`

	// Version is hubStatusVersion for a block the facts footer can lay out.
	Version int `json:"v,omitempty"`
	// State is the state word ("blocked", "working", "done", "your turn",
	// "idle") and StateSince when the row entered its state.
	State      string    `json:"state,omitempty"`
	StateSince time.Time `json:"state_since,omitzero"`
	// PendingTool/PendingToolDetail are set only for a blocked row.
	PendingTool       string `json:"pending_tool,omitempty"`
	PendingToolDetail string `json:"pending_tool_detail,omitempty"`

	// Footer facts, left zone.
	Ticket      int       `json:"ticket,omitempty"`
	Agent       string    `json:"agent,omitempty"`
	Branch      string    `json:"branch,omitempty"`
	PRNumber    int       `json:"pr_number,omitempty"`
	PRURL       string    `json:"pr_url,omitempty"`
	ParentTitle string    `json:"parent_title,omitempty"`
	SchedBy     string    `json:"sched_by,omitempty"`
	SchedAt     time.Time `json:"sched_at,omitzero"`
	Kids        int       `json:"kids,omitempty"`
	KidsBlocked int       `json:"kids_blocked,omitempty"`
	KidsWorking int       `json:"kids_working,omitempty"`
	Extra       []string  `json:"extra,omitempty"` // non-default backend, "acct <name>"

	// Footer facts, right zone.
	Model     string  `json:"model,omitempty"`
	CtxPct    int     `json:"ctx_pct,omitempty"`
	CtxWarn   int     `json:"ctx_warn,omitempty"`
	CtxCrit   int     `json:"ctx_crit,omitempty"`
	CostUSD   float64 `json:"cost_usd,omitempty"`
	StartTime int64   `json:"start_time,omitempty"` // unix seconds; monitor snapshot only
}

// hubStatusFor builds the status pane content for row r; groups resolve the
// parent's title and the direct children. The activity line carries the recap
// only: the last command is visible in the viewer and the activity pane, so
// the activity line and an activity recap are left out.
func hubStatusFor(r fleet.Row, d fleetDisplay, groups []fleet.Group) *hubStatus {
	glyph, _ := rowGlyph(r)
	recap := rowRecap(r)
	warn, crit := d.thresholds()
	st := &hubStatus{
		Glyph:      glyph,
		Role:       r.ShortRole,
		Title:      recap.Short(),
		Project:    r.Project,
		Detail:     statusDetailParts(r),
		Chips:      rowChips(r, d),
		Version:    hubStatusVersion,
		State:      statusStateWord(r),
		StateSince: r.StateSince,
		Ticket:     r.Ticket,
		Agent:      fleet.BaseName(r),
		PRNumber:   r.PRNumber,
		PRURL:      r.PRURL,
		SchedBy:    r.ScheduledBy,
		SchedAt:    r.ScheduledAt,
		Model:      shortModel(r.ModelName),
		CtxPct:     r.ContextPct,
		CtxWarn:    warn,
		CtxCrit:    crit,
		CostUSD:    r.CostUSD,
		StartTime:  r.StartTime,
	}
	if r.State == "blocked" {
		st.PendingTool, st.PendingToolDetail = r.PendingTool, r.PendingToolDetail
	}
	if r.Dispatched || r.Detached {
		st.Branch = r.Branch
	}
	if b := strings.TrimSpace(r.Backend); !defaultBackends[strings.ToLower(b)] {
		st.Extra = append(st.Extra, b)
	}
	if a := strings.TrimSpace(r.Account); a != "" && a != d.defaultAccount {
		st.Extra = append(st.Extra, "acct "+a)
	}
	if rowNeedsNameFallback(r, recap) {
		st.Title = fleet.BaseName(r)
	}
	if recap.Source == fleet.RecapNone && recap.Text != st.Title {
		st.Note = recap.Text
	}
	var kids []treeRow
	for _, g := range groups {
		for _, o := range g.Rows {
			if o.Session != r.Session || o.Shell {
				continue
			}
			if r.WindowID != "" && o.Parent == r.WindowID {
				kids = append(kids, treeRow{Row: o})
			}
			if r.Parent != "" && o.WindowID == r.Parent {
				st.ParentTitle = firstNonEmpty(rowRecap(o).Short(), o.Name)
			}
		}
	}
	c := tallySubtree(kids)
	st.Kids, st.KidsBlocked, st.KidsWorking = c.total, c.blocked, c.working
	return st
}

// statusStateWord is the state as the pane's title line words it, from the
// same fields as the glyph: blocked and working win, then done, then a bare
// window at a turn end ("your turn"), else idle.
func statusStateWord(r fleet.Row) string {
	switch {
	case r.State == "blocked":
		return "blocked"
	case r.State == "working":
		return "working"
	case r.Done:
		return "done"
	case r.TurnDone:
		return "your turn"
	}
	return "idle"
}

// statusDetailParts is detailPartsFor without the activity: a recap that is
// itself the activity is dropped, and no activity line or marker is set.
// (Older status panes lay these parts out with textLines, so the shape stays.)
func statusDetailParts(r fleet.Row) rowDetailParts {
	p := detailPartsFor(r, false)
	if p.RecapIsActivity {
		return rowDetailParts{}
	}
	return rowDetailParts{Recap: p.Recap}
}

// valid reports whether s names a followable agent window.
func (s HubSelection) valid() bool {
	return s.Session != "" && s.WindowID != "" && s.RuntimeDir != ""
}

// sameTarget reports whether s and o name the same window in the same
// project; display hints are ignored.
func (s HubSelection) sameTarget(o HubSelection) bool {
	return s.Session == o.Session && s.WindowID == o.WindowID && s.RuntimeDir == o.RuntimeDir
}

// hubSelectionPath is the selection file's path; a seam for tests.
var hubSelectionPath = func() (string, error) {
	dir, err := config.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, hubSelectionFile), nil
}

// readHubSelection reads the selection file at path. A missing file is the
// zero selection with no error; an unreadable or corrupt one is an error
// (the follower shows its placeholder either way).
func readHubSelection(path string) (HubSelection, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return HubSelection{}, nil
		}
		return HubSelection{}, fmt.Errorf("reading hub selection: %w", err)
	}
	var s HubSelection
	if err := json.Unmarshal(data, &s); err != nil {
		return HubSelection{}, fmt.Errorf("parsing hub selection %s: %w", path, err)
	}
	return s, nil
}

// writeHubSelection writes s to path (0600, temp file + rename).
func writeHubSelection(path string, s HubSelection) error {
	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("encoding hub selection: %w", err)
	}
	return writeFileAtomic(path, append(data, '\n'))
}

// writeFileAtomic writes data to path with mode 0600 through a temp file in
// the same dir and a rename, so a reader never sees a torn file.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod %s: %w", tmp.Name(), err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("renaming to %s: %w", path, err)
	}
	return nil
}

// hubSelection returns the hub's current selection as published to the
// follow-hub HUD: the selected agent row, or the zero value when nothing, a
// header or a shell row is selected, or the row has no known runtime dir.
func (m FleetModel) hubSelection() HubSelection {
	if m.selIsHeader || m.selSession == "" || m.selWindowID == "" {
		return HubSelection{}
	}
	for _, g := range m.groups {
		if g.Session != m.selSession {
			continue
		}
		for _, r := range g.Rows {
			if r.WindowID != m.selWindowID {
				continue
			}
			sel := selectionForRow(g, r)
			if sel.valid() {
				sel.Status = hubStatusFor(r, m.display, m.groups)
			}
			return sel
		}
	}
	return HubSelection{}
}

// selectionForRow builds the published selection for row r of group g.
func selectionForRow(g fleet.Group, r fleet.Row) HubSelection {
	if r.Shell || g.RuntimeDir == "" {
		return HubSelection{}
	}
	return HubSelection{
		Session:    g.Session,
		WindowID:   r.WindowID,
		RuntimeDir: g.RuntimeDir,
		Name:       r.Name,
		Title:      r.Recap.Title,
	}
}

// publishSelection writes the current selection to the selection file when
// it differs from the last one written. It runs only while a follower is
// open (the activity pane, or the status pane above the live viewer), and
// does nothing until the list has loaded, so a hot-reloaded hub does not
// blank a follower before its pending focus is re-applied. Opening either
// pane forces a fresh write (selPublished is reset). A failed write is
// logged once per distinct selection and retried on the next change.
func (m *FleetModel) publishSelection() {
	if !m.loaded || !m.selectionFollowed() {
		return
	}
	sel := m.hubSelection()
	if m.selPublished && reflect.DeepEqual(sel, m.lastSelection) {
		return
	}
	m.lastSelection, m.selPublished = sel, true
	path, err := hubSelectionPath()
	if err == nil {
		err = writeHubSelection(path, sel)
	}
	if err != nil {
		logging.Log("hub: publishing selection: %v", err)
	}
}

// selectionFollowed reports whether a pane is following the selection file:
// the activity pane, or the status pane above the live viewer.
func (m FleetModel) selectionFollowed() bool {
	return m.activity.open() || m.statusPaneOpen()
}
