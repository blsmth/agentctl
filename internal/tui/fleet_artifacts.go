package tui

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/artifacts"
	"github.com/blsmth/agentctl/internal/fleet"
)

// The hub's artifact chip: a dim "◆N" before the title of a row whose window
// has published N artifacts (internal/artifacts, matched on the artifact's
// window_id + session). `o` on such a row opens the newest one in the browser
// through `agentctl artifact open`. The listing is read on the poll tick, off
// the UI goroutine, and only when the store directory's mtime changed; render
// reads the cached counts and never touches disk.

// fleetArtifactKey opens the selected row's newest artifact. On a row with no
// artifacts the key keeps its older meaning (toggle the sort).
const fleetArtifactKey = "o"

// fleetArtifactGlyph leads the chip.
const fleetArtifactGlyph = "◆"

// Seams so tests never read the real store or spawn agentctl/a browser.
var (
	// fleetArtifactStamp returns the store directory's mtime (zero, false
	// when the store does not exist yet).
	fleetArtifactStamp = func() (time.Time, bool) {
		dir, err := artifacts.New().Dir()
		if err != nil {
			return time.Time{}, false
		}
		fi, err := os.Stat(dir)
		if err != nil {
			return time.Time{}, false
		}
		return fi.ModTime(), true
	}
	fleetArtifactList = func() ([]artifacts.Meta, error) {
		return artifacts.New().List(artifacts.Filter{})
	}
	// fleetArtifactOpen runs `agentctl artifact open <id>`.
	fleetArtifactOpen = func(id string) error {
		_, err := runAgentctlHelper("", nil, "artifact", "open", id)
		return err
	}
)

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
		return fleetArtifactsMsg{stamp: stamp, byWindow: indexArtifacts(metas)}
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
