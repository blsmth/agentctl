package tui

import (
	"errors"
	"fmt"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/shorttitle"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// The hub's rename: R, the row menu's "rename", or a double click on a row's
// title opens an inline editor in place of the title. Enter saves the text
// as the window's human title (windowstate short_title, Manual), which the
// title pipeline never replaces; an empty save clears it so automatic titles
// resume. Esc, or a click anywhere else, cancels.

// fleetRenameKey opens the rename editor on the selected row ("r" is refresh).
const fleetRenameKey = "R"

// fleetRenamePlaceholder shows in an empty editor.
const fleetRenamePlaceholder = "title (empty = auto)"

// fleetRenameHints replace the footer hints while the editor is open.
var fleetRenameHints = []fleetHint{
	{"⏎", "save"},
	{"esc", "cancel"},
}

// fleetTitleHoldTTL bounds how long a just-saved title overrides loaded rows
// that do not show it yet (a load that started before the write).
const fleetTitleHoldTTL = 10 * time.Second

// fleetSaveTitle writes title (already cleaned; "" clears) as the human title
// of windowID in the project at runtimeDir. A seam for tests.
var fleetSaveTitle = func(runtimeDir, windowID, title string) error {
	return windowstate.UpdateIn(runtimeDir, windowID, func(ws *windowstate.WindowState) {
		shorttitle.SetManual(&ws.ShortTitle, title, fleetNow())
	})
}

// fleetRenameState is the open editor, keyed by window (session + window ID)
// rather than line index, so a poll that reorders lines keeps it on its row.
// orig is the prefill, the title as it was shown.
type fleetRenameState struct {
	session, windowID string
	orig              string
	input             textinput.Model
}

// fleetTitleHold is a title the hub saved, shown until a load reflects it.
type fleetTitleHold struct {
	title string
	until time.Time
}

func (m FleetModel) renameOpen() bool { return m.rename != nil }

// renamingRow reports whether r is the row the editor is open on.
func (m FleetModel) renamingRow(r fleet.Row) bool {
	return m.rename != nil && r.Session == m.rename.session && r.WindowID == m.rename.windowID
}

// rowCanRename reports whether the hub may rename r: an agent row (not a
// shell) with a real window, in a group whose runtime dir is known.
func (m *FleetModel) rowCanRename(r fleet.Row) bool {
	if r.Shell || !rowCanClose(r) {
		return false
	}
	g := m.groupForSession(r.Session)
	return g != nil && g.RuntimeDir != ""
}

// openRename opens the editor on the row at line idx, prefilled with its
// title as shown (empty for an untitled row, which shows a recap sentence)
// and the cursor at the end. Every other inline state closes. The returned
// command starts the cursor blinking; blink messages are routed to the input
// only while the editor is open, so closing it ends the tick loop.
func (m *FleetModel) openRename(idx int) tea.Cmd {
	if idx < 0 || idx >= len(m.lines) || m.lines[idx].kind != fleetLineRow {
		return nil
	}
	r := m.lines[idx].row
	if !m.rowCanRename(r) {
		m.jumpErr = "cannot rename this window"
		return nil
	}
	m.cancelNewSession()
	m.cancelClose()
	m.cancelNew()
	m.cancelSweep()
	m.closeMenu()
	m.jumpErr = ""
	m.selectLine(idx)
	orig := renamePrefill(r)
	ti := textinput.New()
	ti.Prompt = ""
	ti.Placeholder = fleetRenamePlaceholder
	ti.CharLimit = shorttitle.MaxManualRunes
	s := ti.Styles()
	s.Cursor.Blink = true
	ti.SetStyles(s)
	ti.SetValue(orig)
	ti.CursorEnd()
	blink := ti.Focus()
	m.rename = &fleetRenameState{session: r.Session, windowID: r.WindowID, orig: orig, input: ti}
	return blink
}

// renamePrefill is the title text r shows: its title, or the auto short title
// standing in for a missing recap. A row showing a recap sentence is empty.
func renamePrefill(r fleet.Row) string {
	rc := rowRecap(r)
	if rc.Title == "" && rc.Source == fleet.RecapTitle {
		return rc.Short()
	}
	return rc.Title
}

// cancelRename closes the editor without saving.
func (m *FleetModel) cancelRename() { m.rename = nil }

// validateRename closes the editor when its window is no longer drawn (gone,
// folded or filtered away by a reload).
func (m *FleetModel) validateRename() {
	if m.rename == nil {
		return
	}
	if _, ok := m.renameLine(); !ok {
		m.cancelRename()
	}
}

// renameLine finds the editor's row in m.lines.
func (m FleetModel) renameLine() (int, bool) {
	for i, l := range m.lines {
		if l.kind == fleetLineRow && m.renamingRow(l.row) {
			return i, true
		}
	}
	return -1, false
}

// handleRenameKey owns every key while the editor is open, so typed text
// never triggers a hub binding.
func (m *FleetModel) handleRenameKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.cancelRename()
		return nil
	case "enter":
		m.submitRename()
		return nil
	case "ctrl+c":
		m.quitting = true
		return tea.Quit
	}
	var cmd tea.Cmd
	m.rename.input, cmd = m.rename.input.Update(msg)
	return cmd
}

// submitRename saves the editor's text and closes it. Unchanged text saves
// nothing (so opening and confirming never pins an automatic title). A
// failed write reports in the footer; the editor stays open unless the
// window or its record is gone.
func (m *FleetModel) submitRename() {
	st := m.rename
	title := shorttitle.CleanManual(st.input.Value())
	if title == shorttitle.CleanManual(st.orig) {
		m.cancelRename()
		return
	}
	idx, ok := m.renameLine()
	g := m.groupForSession(st.session)
	if !ok || g == nil {
		m.cancelRename()
		m.jumpErr = "rename: window is gone"
		return
	}
	if err := fleetSaveTitle(g.RuntimeDir, st.windowID, title); err != nil {
		if errors.Is(err, windowstate.ErrNoRecord) || errors.Is(err, windowstate.ErrGone) {
			m.cancelRename()
			m.jumpErr = "rename: this window has no agentctl record"
			return
		}
		m.jumpErr = fmt.Sprintf("rename: %v", err)
		return
	}
	name := m.lines[idx].row.Name
	m.cancelRename()
	m.holdTitle(st.session, st.windowID, title)
	m.searchReset()
	m.applyTitleHolds()
	m.rebuildLines()
	m.resolveSelection()
	if title == "" {
		m.setSuccess("cleared title of " + name)
	} else {
		m.setSuccess("renamed " + name)
	}
}

// holdTitle remembers a just-saved title so the row shows it at once and a
// stale in-flight load cannot put the old one back.
func (m *FleetModel) holdTitle(session, windowID, title string) {
	if m.titleHolds == nil {
		m.titleHolds = map[string]fleetTitleHold{}
	}
	m.titleHolds[session+"|"+windowID] = fleetTitleHold{title: title, until: fleetNow().Add(fleetTitleHoldTTL)}
}

// applyTitleHolds applies held titles to m.groups. A hold whose row already
// shows it (the load read the write) or that expired is dropped.
func (m *FleetModel) applyTitleHolds() {
	if len(m.titleHolds) == 0 {
		return
	}
	now := fleetNow()
	for key, h := range m.titleHolds {
		if !now.Before(h.until) {
			delete(m.titleHolds, key)
		}
	}
	for gi := range m.groups {
		rows := m.groups[gi].Rows
		for ri := range rows {
			key := rows[ri].Session + "|" + rows[ri].WindowID
			h, ok := m.titleHolds[key]
			if !ok {
				continue
			}
			if rows[ri].ShortTitle == h.title && rows[ri].ShortManual == (h.title != "") {
				delete(m.titleHolds, key)
				continue
			}
			fleet.Retitle(&rows[ri], h.title)
		}
	}
}

// renameView renders the editor in a w-column title slot. It is sized here,
// not when it opens, so a resize never overflows it.
func (m FleetModel) renameView(w int) string {
	in := m.rename.input
	in.SetWidth(max(w-1, 1)) // the cursor takes one more column
	in.SetCursor(in.Position())
	return in.View()
}

// onRowTitle reports whether column x hits the title text of the row at line
// idx, as treeRowLayout draws it.
func (m FleetModel) onRowTitle(idx, x int) bool {
	if idx < 0 || idx >= len(m.lines) || m.lines[idx].kind != fleetLineRow {
		return false
	}
	l := m.lines[idx]
	cw := m.listColWidth()
	selected := idx == m.splitPane.Cursor()
	span := m.rowLayout(l, selected, fleetHoverBody, cw).title
	end := min(span[1], cw-fleetSidebarRightGutter)
	return x >= span[0] && x < end
}

// rowCanRenameAt is rowCanRename for the row at line idx.
func (m FleetModel) rowCanRenameAt(idx int) bool {
	if idx < 0 || idx >= len(m.lines) || m.lines[idx].kind != fleetLineRow {
		return false
	}
	return m.rowCanRename(m.lines[idx].row)
}
