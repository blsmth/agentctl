package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/artifacts"
	"github.com/blsmth/agentctl/internal/fleet"
)

// The hub's artifact chip: a dim "◆N" before the title of a row whose window
// has published N artifacts (internal/artifacts, matched on the artifact's
// window_id + session). `A` on such a row opens the newest one in the browser
// through `agentctl artifact open`. The listing is read on the poll tick, off
// the UI goroutine, and only when the store directory's mtime changed; render
// reads the cached counts and never touches disk.

// fleetArtifactKey opens the selected row's newest artifact; a no-op on a row
// without artifacts.
const fleetArtifactKey = "A"

// fleetArtifactGlyph leads the chip.
const fleetArtifactGlyph = "◆"

// Seams so tests never read the real store or spawn agentctl/a browser.
var (
	// fleetArtifactStamp returns the newest of the store directory's mtime
	// and every artifact's meta.json mtime (a new revision rewrites only
	// meta.json and a rev subdir, which leaves the store dir's mtime alone);
	// zero, false when the store does not exist yet.
	fleetArtifactStamp = artifactStoreStamp
	fleetArtifactList  = func() ([]artifacts.Meta, error) {
		return artifacts.New().List(artifacts.Filter{})
	}
	// fleetArtifactOpen runs `agentctl artifact open <id>`.
	fleetArtifactOpen = func(id string) error {
		_, err := runAgentctlHelper("", nil, "artifact", "open", id)
		return err
	}
)

// artifactStoreStamp is the default fleetArtifactStamp.
func artifactStoreStamp() (time.Time, bool) {
	dir, err := artifacts.New().Dir()
	if err != nil {
		return time.Time{}, false
	}
	return storeStampOf(dir)
}

// storeStampOf is artifactStoreStamp for the store at dir.
func storeStampOf(dir string) (time.Time, bool) {
	fi, err := os.Stat(dir)
	if err != nil {
		return time.Time{}, false
	}
	newest := fi.ModTime()
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if !e.IsDir() || !artifacts.ValidID(e.Name()) {
			continue
		}
		if mi, err := os.Stat(filepath.Join(dir, e.Name(), "meta.json")); err == nil && mi.ModTime().After(newest) {
			newest = mi.ModTime()
		}
	}
	return newest, true
}

// fleetArtifactInfo is what a row's chip needs.
type fleetArtifactInfo struct {
	n      int
	newest string // id of the most recently updated artifact
}

// fleetArtifactsMsg carries a listing; unchanged means the directory mtime
// matched and nothing was re-read.
type fleetArtifactsMsg struct {
	stamp     time.Time
	unchanged bool
	byWindow  map[string]fleetArtifactInfo
	list      []artifacts.Meta // the whole listing, for the Artifacts tab
	err       error
}

func artifactKey(session, windowID string) string { return session + "|" + windowID }

// artifactsLoadCmd refreshes the per-window artifact counts, unless a read is
// already in flight.
func (m *FleetModel) artifactsLoadCmd() tea.Cmd {
	if m.artifactsLoading {
		return nil
	}
	m.artifactsLoading = true
	last := m.artifactsStamp
	return func() tea.Msg {
		stamp, ok := fleetArtifactStamp()
		if !ok {
			return fleetArtifactsMsg{byWindow: map[string]fleetArtifactInfo{}}
		}
		if !last.IsZero() && stamp.Equal(last) {
			return fleetArtifactsMsg{stamp: stamp, unchanged: true}
		}
		metas, err := fleetArtifactList()
		if err != nil {
			return fleetArtifactsMsg{err: err}
		}
		return fleetArtifactsMsg{stamp: stamp, byWindow: indexArtifacts(metas), list: metas}
	}
}

// indexArtifacts counts artifacts per window; metas are newest first, so the
// first one seen for a window is its newest.
func indexArtifacts(metas []artifacts.Meta) map[string]fleetArtifactInfo {
	out := map[string]fleetArtifactInfo{}
	for _, a := range metas {
		if a.WindowID == "" || a.Session == "" {
			continue
		}
		k := artifactKey(a.Session, a.WindowID)
		in := out[k]
		if in.n == 0 {
			in.newest = a.ID
		}
		in.n++
		out[k] = in
	}
	return out
}

// applyArtifacts stores a listing. A failed read keeps the old chips.
func (m *FleetModel) applyArtifacts(msg fleetArtifactsMsg) {
	m.artifactsLoading = false
	if msg.err != nil || msg.unchanged {
		return
	}
	m.artifactsStamp = msg.stamp
	m.artifacts = msg.byWindow
	if !slices.EqualFunc(m.artList, msg.list, artMetaSame) {
		m.artList = msg.list
		m.relayout()
	}
}

// artMetaSame reports whether two listing entries draw the same row.
func artMetaSame(a, b artifacts.Meta) bool {
	return a.ID == b.ID && a.Title == b.Title && a.Pinned == b.Pinned && a.Kind == b.Kind &&
		a.Updated.Equal(b.Updated) && len(a.Revs) == len(b.Revs) && len(a.Comments) == len(b.Comments) &&
		a.ProjectDir == b.ProjectDir
}

// rowArtifacts is r's artifact info (n == 0 when it has none).
func (m FleetModel) rowArtifacts(r fleet.Row) fleetArtifactInfo {
	return m.artifacts[artifactKey(r.Session, r.WindowID)]
}

// artifactChip is the dim "◆N " tag drawn before a row's title, or "".
func (m FleetModel) artifactChip(r fleet.Row) string {
	if n := m.rowArtifacts(r).n; n > 0 {
		return fleetArtifactGlyph + strconv.Itoa(n)
	}
	return ""
}

// selectedArtifactRow reports whether the cursor is on a row with artifacts.
func (m *FleetModel) selectedArtifactRow() bool {
	r := m.selectedRow()
	return r != nil && m.rowArtifacts(*r).n > 0
}

// fleetArtifactOpenedMsg is the outcome of opening an artifact.
type fleetArtifactOpenedMsg struct {
	id  string
	err error
}

// openSelectedArtifact opens the selected row's newest artifact. The newest is
// re-resolved at press time, so an update since the last poll is honoured.
func (m *FleetModel) openSelectedArtifact() tea.Cmd {
	r := m.selectedRow()
	if r == nil {
		return nil
	}
	info := m.rowArtifacts(*r)
	if info.n == 0 {
		return nil
	}
	key := artifactKey(r.Session, r.WindowID)
	m.setProgress("opening artifact…")
	return func() tea.Msg {
		id := info.newest
		if metas, err := fleetArtifactList(); err == nil {
			if in, ok := indexArtifacts(metas)[key]; ok {
				id = in.newest
			}
		}
		return fleetArtifactOpenedMsg{id: id, err: fleetArtifactOpen(id)}
	}
}

func (m *FleetModel) applyArtifactOpened(msg fleetArtifactOpenedMsg) {
	m.clearInfo()
	if msg.err != nil {
		m.jumpErr = "artifact: " + msg.err.Error()
		return
	}
	m.jumpErr = ""
	m.setSuccess(fmt.Sprintf("opened artifact %s", strings.TrimSpace(msg.id)))
}
