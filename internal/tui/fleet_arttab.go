package tui

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/artifacts"
	"github.com/blsmth/agentctl/internal/artifacturl"
	"github.com/blsmth/agentctl/internal/fleet"
)

// The Artifacts tab: a project group with artifacts gets an "Artifacts N"
// header tab (next to Sessions and Schedules, see fleet_schedgroup.go) that
// lists the project's artifacts, pinned first then in the store's order
// (newest first unless the project has a saved order). An artifact belongs
// to the group the same way a job does: the first group whose resolved
// project dir is the artifact's project_dir or contains it, longest first.
// Artifacts with no project are not listed, and neither are those of a
// project with no group (no live session).
//
// The listing is the one the ◆N chip already polls (fleet_artifacts.go): it
// is cached in artList and re-read only when the store's stamp changes, so
// rendering never touches disk. ⏎ or a click opens the artifact in the
// laptop browser; p pins or unpins it.

// fleetArtSelPrefix prefixes the selSchedule key of an artifact row.
const fleetArtSelPrefix = "\x00art:"

// fleetArtPinKey toggles the pin of the selected artifact.
const fleetArtPinKey = "p"

// fleetArtPinGlyph marks a pinned artifact; an unpinned row leaves a blank.
const fleetArtPinGlyph = "★"

// errArtServeDown means serve is not running, so a URL would be dead.
var errArtServeDown = errors.New("serve is not running")

// Seams so tests never probe serve, spawn `open` or write the real store.
var (
	// fleetArtifactServe is the PWA base URL and whether serve is running.
	fleetArtifactServe = artifacturl.Serve
	// fleetArtOpenURL opens a URL in the laptop browser.
	fleetArtOpenURL = artifacturl.OpenInBrowser
	// fleetArtifactPin sets an artifact's pinned flag.
	fleetArtifactPin = func(id string, pinned bool) (artifacts.Meta, error) {
		return artifacts.New().Pin(id, pinned)
	}
	// fleetArtifactRemove deletes an artifact from the store.
	fleetArtifactRemove = func(id string) (artifacts.Meta, error) {
		return artifacts.New().Remove(id)
	}
)

// artSel is the selSchedule key of artifact id.
func artSel(id string) string { return fleetArtSelPrefix + id }

// isArtSel reports whether a selSchedule key names an artifact row.
func isArtSel(sel string) bool { return strings.HasPrefix(sel, fleetArtSelPrefix) }

// placeArtifacts assigns the cached artifacts to the groups that host a
// block (keys, see schedGroupKeys), pinned first. Nothing is placed before
// the first listing.
func (m *FleetModel) placeArtifacts(keys map[int]string) map[int][]artifacts.Meta {
	if len(m.artList) == 0 {
		return nil
	}
	if m.schedDirs == nil {
		m.schedDirs = newFleetDirCache()
	}
	out := make(map[int][]artifacts.Meta)
	for _, a := range m.artList {
		if a.ProjectDir == "" {
			continue
		}
		dir := m.schedDirs.resolve(a.ProjectDir)
		best, bestLen := -1, -1
		for i := range m.groups {
			key, ok := keys[i]
			if ok && pathWithin(dir, key) && len(key) > bestLen {
				best, bestLen = i, len(key)
			}
		}
		if best >= 0 {
			out[best] = append(out[best], a)
		}
	}
	for i := range out {
		slices.SortStableFunc(out[i], func(a, b artifacts.Meta) int {
			switch {
			case a.Pinned && !b.Pinned:
				return -1
			case !a.Pinned && b.Pinned:
				return 1
			}
			return 0
		})
	}
	return out
}

// artGroupLines is group g's Artifacts tab body: one row per artifact.
func (m *FleetModel) artGroupLines(g fleet.Group, key string, arts []artifacts.Meta) []fleetLine {
	lines := make([]fleetLine, 0, len(arts))
	for _, a := range arts {
		lines = append(lines, fleetLine{kind: fleetLineArtifact, group: g, schedBlock: key, art: a})
	}
	return lines
}

// noteHiddenArts records the artifact rows a folded group or another tab
// hides, so the cursor falls back to the group header (unless visible).
func (m *FleetModel) noteHiddenArts(g fleet.Group, arts []artifacts.Meta, visible bool) {
	if visible {
		return
	}
	if m.schedHidden == nil {
		m.schedHidden = make(map[string]fleetSchedHider)
	}
	for _, a := range arts {
		m.schedHidden[artSel(a.ID)] = fleetSchedHider{session: g.Session}
	}
}

// selectedArtLine is the selected artifact line, or nil.
func (m FleetModel) selectedArtLine() *fleetLine {
	idx := m.splitPane.Cursor()
	if idx < 0 || idx >= len(m.lines) || m.lines[idx].kind != fleetLineArtifact {
		return nil
	}
	l := m.lines[idx]
	return &l
}

// artifactLineKey runs key on a selected artifact row; handled is false for
// keys that keep their usual meaning.
func (m *FleetModel) artifactLineKey(key string) (tea.Cmd, bool) {
	l := m.selectedArtLine()
	if l == nil {
		return nil, false
	}
	switch key {
	case "enter":
		return m.openArtifactTab(l.art), true
	case fleetArtPinKey:
		return m.pinArtifact(l.art), true
	case "x":
		m.startArtDelete(l.art.ID)
		return nil, true
	case "tab", "e", "g", "d", "space":
		return nil, true // nothing live to act on
	}
	return nil, false
}

// startArtDelete arms the inline delete confirmation for artifact id. Only
// one confirmation is armed at a time, so an armed window close is dropped.
func (m *FleetModel) startArtDelete(id string) {
	m.cancelClose()
	m.jumpErr = ""
	m.artDelID = id
}

func (m *FleetModel) cancelArtDelete() { m.artDelID = "" }

func (m FleetModel) artDeleteArmed() bool { return m.artDelID != "" }

// artDeleteArmedFor reports whether a's delete awaits confirmation.
func (m FleetModel) artDeleteArmedFor(a artifacts.Meta) bool {
	return m.artDeleteArmed() && a.ID == m.artDelID
}

// artDeleteKey handles a key while a delete is armed: y deletes, n/esc
// cancels, anything else cancels and then runs as usual (handled is false).
func (m *FleetModel) artDeleteKey(key string) (tea.Cmd, bool) {
	switch key {
	case "y", "Y":
		return m.confirmArtDelete(), true
	case "n", "N", "esc":
		m.cancelArtDelete()
		return nil, true
	}
	m.cancelArtDelete()
	return nil, false
}

// fleetArtDeletedMsg is the outcome of a delete.
type fleetArtDeletedMsg struct {
	id  string
	err error
}

// confirmArtDelete runs the armed delete.
func (m *FleetModel) confirmArtDelete() tea.Cmd {
	id := m.artDelID
	m.cancelArtDelete()
	if id == "" {
		return nil
	}
	m.setProgress("deleting artifact…")
	return func() tea.Msg {
		_, err := fleetArtifactRemove(id)
		return fleetArtDeletedMsg{id: id, err: err}
	}
}

func (m *FleetModel) applyArtDeleted(msg fleetArtDeletedMsg) {
	m.clearInfo()
	gone := errors.Is(msg.err, artifacts.ErrNotFound)
	if msg.err != nil && !gone {
		// Store errors carry the full id in paths; ids are capabilities.
		short := artifacts.ShortID(msg.id)
		m.setError("delete " + short + ": " + strings.ReplaceAll(msg.err.Error(), msg.id, short))
		return
	}
	m.jumpErr = ""
	m.moveCursorOffArt(msg.id)
	m.artList = slices.DeleteFunc(slices.Clone(m.artList), func(a artifacts.Meta) bool { return a.ID == msg.id })
	// The store stamp moved with the write; drop ours so the next poll re-reads.
	m.artifactsStamp = m.artifactsStamp.Add(-1)
	m.relayout()
	if gone {
		m.setSuccess("artifact " + artifacts.ShortID(msg.id) + " was already deleted")
		return
	}
	m.setSuccess("deleted artifact " + artifacts.ShortID(msg.id))
}

// moveCursorOffArt points the selection at the row after artifact id (the one
// before when it was last), or at its group header when the tab empties, so
// the relayout lands somewhere sensible.
func (m *FleetModel) moveCursorOffArt(id string) {
	at := -1
	for i, l := range m.lines {
		if l.kind == fleetLineArtifact && l.art.ID == id {
			at = i
			break
		}
	}
	if at < 0 || m.selSchedule != artSel(id) {
		return // the cursor is elsewhere and stays put
	}
	same := func(i int) bool {
		return i >= 0 && i < len(m.lines) && m.lines[i].kind == fleetLineArtifact && m.lines[i].group.Session == m.lines[at].group.Session
	}
	switch {
	case same(at + 1):
		m.selSchedule = artSel(m.lines[at+1].art.ID)
	case same(at - 1):
		m.selSchedule = artSel(m.lines[at-1].art.ID)
	default:
		m.selSchedule = ""
		m.selSession, m.selWindowID, m.selIsHeader = m.lines[at].group.Session, "", true
	}
}

// artifactHints are the footer hints on an artifact row.
func (m FleetModel) artifactHints(l fleetLine) []fleetHint {
	pin := "pin"
	if l.art.Pinned {
		pin = "unpin"
	}
	if m.artDeleteArmedFor(l.art) {
		return fleetSchedYesNoHints
	}
	return []fleetHint{{"j/k", "move"}, {"⏎", "open"}, {fleetArtPinKey, pin}, {"x", "delete"}, {fleetSchedulesKey, "next tab"}, {"?", "more"}}
}

// fleetArtOpenedMsg is the outcome of opening an artifact from the tab.
type fleetArtOpenedMsg struct {
	id  string
	url string
	err error
}

// openArtifactTab opens a in the laptop browser by its direct URL, or hints
// that serve is down instead of opening a dead URL.
func (m *FleetModel) openArtifactTab(a artifacts.Meta) tea.Cmd {
	m.setProgress("opening artifact…")
	id := a.ID
	return func() tea.Msg {
		base, running := fleetArtifactServe()
		if !running {
			return fleetArtOpenedMsg{id: id, err: errArtServeDown}
		}
		u := artifacturl.Direct(base, id, 0)
		return fleetArtOpenedMsg{id: id, url: u, err: fleetArtOpenURL(u)}
	}
}

func (m *FleetModel) applyArtOpened(msg fleetArtOpenedMsg) {
	m.clearInfo()
	switch {
	case errors.Is(msg.err, errArtServeDown):
		m.setError("serve is not running · start it with agentctl serve start, then open again")
	case msg.err != nil:
		m.setError("artifact: " + msg.err.Error() + " · " + msg.url)
	default:
		m.jumpErr = ""
		m.setSuccess("opened artifact " + artifacts.ShortID(msg.id))
	}
}

// fleetArtPinnedMsg is the outcome of a pin toggle.
type fleetArtPinnedMsg struct {
	meta artifacts.Meta
	err  error
}

// pinArtifact toggles a's pin.
func (m *FleetModel) pinArtifact(a artifacts.Meta) tea.Cmd {
	want := !a.Pinned
	id := a.ID
	return func() tea.Msg {
		meta, err := fleetArtifactPin(id, want)
		return fleetArtPinnedMsg{meta: meta, err: err}
	}
}

func (m *FleetModel) applyArtPinned(msg fleetArtPinnedMsg) {
	if msg.err != nil {
		m.setError("pin: " + msg.err.Error())
		return
	}
	for i := range m.artList {
		if m.artList[i].ID == msg.meta.ID {
			m.artList[i].Pinned = msg.meta.Pinned
		}
	}
	// The store stamp moved with the write; drop ours so the next poll
	// re-reads rather than trusting the optimistic copy.
	m.artifactsStamp = m.artifactsStamp.Add(-1)
	m.relayout()
	if msg.meta.Pinned {
		m.setSuccess("pinned")
	} else {
		m.setSuccess("unpinned")
	}
}

// setError shows msg as the footer error.
func (m *FleetModel) setError(msg string) { m.jumpErr = msg }

// renderArtRow draws one artifact row: pin marker, title, then the dim
// columns kind, revision count (when more than one), comment count and age.
// The title is cut by display width; the columns drop from the left (comment
// count first, age last) as the pane narrows.
func (m FleetModel) renderArtRow(l fleetLine, selected bool, hover fleetHoverRegion, cw int) string {
	a := l.art
	cursor := "  "
	if selected {
		cursor = cursorActiveStyle.Render("▸") + " "
	}
	lead := fleetProjectBar(l.group.Project) + cursor + "  "
	pin := " "
	if a.Pinned {
		pin = fleetBlockedStyle.Render(fleetArtPinGlyph)
	}
	title := a.Title
	if title == "" {
		title = artifacts.ShortID(a.ID)
	}
	var cols []string // dropped from the front as the pane narrows
	if n := len(a.Comments); n > 0 {
		cols = append(cols, fmt.Sprintf("✎%d", n))
	}
	if a.Kind != "" {
		cols = append(cols, a.Kind)
	}
	if n := len(a.Revs); n > 1 {
		cols = append(cols, fmt.Sprintf("r%d", a.Latest()))
	}
	cols = append(cols, fleet.CompactAge(fleetNow().Sub(a.Updated)))
	inner := cw - fleetSidebarRightGutter
	fixed := lipgloss.Width(lead) + 2 // pin and its space
	const minTitle = 10
	right := strings.Join(cols, " ")
	for len(cols) > 1 && inner-fixed-lipgloss.Width(right)-2 < minTitle {
		cols = cols[1:]
		right = strings.Join(cols, " ")
	}
	room := inner - fixed - lipgloss.Width(right) - 2
	if room < 1 { // even the age does not fit beside a title
		right, room = "", inner-fixed
	}
	nameSt := fleetNameDoneStyle
	if hover == fleetHoverArt && !selected {
		nameSt = nameSt.Underline(true)
	}
	left := lead + pin + " " + nameSt.Render(truncateToWidth(title, max(room, 1)))
	if m.artDeleteArmedFor(a) {
		q := "delete? y/n"
		if a.Pinned {
			q = "delete pinned? y/n"
		}
		right = fleetBlockedStyle.Render(q)
		rw := lipgloss.Width(right)
		left = lead + pin + " " + nameSt.Render(truncateToWidth(title, max(inner-fixed-rw-2, 1)))
		gap := max(inner-lipgloss.Width(left)-rw, 1)
		return fleetFitLine(left+strings.Repeat(" ", gap)+right, selected, cw)
	}
	if right == "" {
		return fleetFitLine(left, selected, cw)
	}
	gap := max(inner-lipgloss.Width(left)-lipgloss.Width(right), 1)
	return fleetFitLine(left+strings.Repeat(" ", gap)+fleetCountStyle.Render(right), selected, cw)
}
