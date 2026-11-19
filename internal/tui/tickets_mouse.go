package tui

import (
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// Mouse handling for the tickets browser.
//
// Rules (see the zone registry in zones.go for hit-testing):
//   - A click runs exactly the code path of its key: most zones are named
//     "key:<k>@<where>" and synthesize that key press through handleKey.
//   - A click fires on release, and only when the release lands in the zone
//     the press started in; dragging off cancels it.
//   - An open modal owns all mouse input: its own zones are the only live
//     ones, a click outside it cancels it (never a dirty form), and the wheel
//     does nothing.
//   - Mouse is additive: every zone has a key, and nothing depends on a
//     mouse event ever arriving.

// tkPress is a press in progress (or, as lastClick, the last activation).
type tkPress struct {
	zone   string
	at     time.Time
	active bool
}

// zoneParts splits "kind:arg@where" into kind and arg.
func zoneParts(id string) (kind, arg string) {
	base, _, _ := strings.Cut(id, "@")
	kind, arg, _ = strings.Cut(base, ":")
	return kind, arg
}

// tkKey builds the key press a click synthesizes for key name k.
func tkKey(k string) tea.KeyPressMsg {
	switch k {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	}
	if mod, key, ok := strings.Cut(k, "+"); ok && mod == "ctrl" && len(key) == 1 {
		return tea.KeyPressMsg{Code: rune(key[0]), Mod: tea.ModCtrl}
	}
	r := []rune(k)
	if len(r) == 1 {
		return tea.KeyPressMsg{Code: r[0], Text: k}
	}
	return tea.KeyPressMsg{}
}

// currentZones returns the zones of the last rendered frame, rendering one
// when nothing has been drawn yet.
func (m TicketsModel) currentZones() zoneList {
	if m.zones != nil && len(*m.zones) > 0 {
		return *m.zones
	}
	return m.renderZones()
}

// clickZoneAt is the zone a click at (x, y) targets.
func (m TicketsModel) clickZoneAt(x, y int) string {
	return m.currentZones().at(x, y)
}

// isRegionZone reports ids that mark an area rather than a target.
func isRegionZone(id string) bool {
	return strings.HasPrefix(id, "pane:") || id == "modal:box"
}

// hoverZoneAt is the innermost target under (x, y), ignoring area regions.
func (m TicketsModel) hoverZoneAt(z zoneList, x, y int) string {
	best, bestW := "", -1
	for _, r := range z {
		if r.y != y || x < r.x0 || x >= r.x1 || isRegionZone(r.id) {
			continue
		}
		if w := r.x1 - r.x0; bestW < 0 || w < bestW {
			best, bestW = r.id, w
		}
	}
	return best
}

func (m TicketsModel) handleMousePress(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	ev := msg.Mouse()
	if ev.Button != tea.MouseLeft {
		return m, nil
	}
	m.press = tkPress{zone: m.clickZoneAt(ev.X, ev.Y), at: tkNow(), active: true}
	return m, nil
}

func (m TicketsModel) handleMouseRelease(msg tea.MouseReleaseMsg) (tea.Model, tea.Cmd) {
	if !m.press.active {
		return m, nil
	}
	ev := msg.Mouse()
	pressed := m.press
	m.press = tkPress{}

	var next tea.Model = m
	var cmd tea.Cmd
	if zone := m.clickZoneAt(ev.X, ev.Y); zone == pressed.zone {
		next, cmd = m.click(zone)
	}

	// A refresh that arrived during the press is applied now.
	tm, ok := next.(TicketsModel)
	if !ok || tm.deferredFetch == nil {
		return next, cmd
	}
	fetched := *tm.deferredFetch
	tm.deferredFetch = nil
	applied, applyCmd := tm.applyFetched(fetched)
	return applied, tea.Batch(cmd, applyCmd)
}

// click activates zone (pressed and released in it). Two clicks on the same
// zone within tkDoubleClick make the second a double-click.
func (m TicketsModel) click(zone string) (tea.Model, tea.Cmd) {
	now := tkNow()
	double := zone != "" && m.lastClick.zone == zone && now.Sub(m.lastClick.at) <= tkDoubleClick
	if double {
		m.lastClick = tkPress{}
	} else {
		m.lastClick = tkPress{zone: zone, at: now}
	}
	if m.modalOpen() {
		return m.clickModal(zone)
	}
	if m.searchMode && !strings.HasSuffix(zone, "@search") {
		m.commitSearch() // clicking elsewhere keeps the typed query
	}
	return m.activate(zone, double)
}

// activate runs a click on a zone of the list or detail view.
func (m TicketsModel) activate(zone string, double bool) (tea.Model, tea.Cmd) {
	kind, arg := zoneParts(zone)
	switch kind {
	case "key":
		return m.handleKey(tkKey(arg))

	case "row":
		n, _ := strconv.Atoi(arg)
		if double && m.selectedNumber() == n {
			return m.handleKey(tkKey("enter"))
		}
		return m, m.selectIssue(n)

	case "label":
		m.setLabelFilter(arg)
		if m.viewMode == tkViewDetail {
			m.backToList()
		}
		return m, m.previewCmdForCursor()

	case "chip":
		switch arg {
		case "label-clear":
			m.setLabelFilter("")
		case "search-clear":
			m.searchQuery = ""
			m.searchInput.SetValue("")
			m.applyFilters()
		}
		return m, m.previewCmdForCursor()

	case "sort":
		m.toggleSortColumn(arg)
		return m, m.previewCmdForCursor()

	case "pr", "assign":
		key := "p"
		if kind == "assign" {
			key = "a"
		}
		n, _ := strconv.Atoi(arg)
		var sel tea.Cmd
		if m.viewMode == tkViewList {
			sel = m.selectIssue(n)
		}
		next, cmd := m.handleKey(tkKey(key))
		return next, tea.Batch(sel, cmd)

	case "url":
		return m.handleKey(tkKey("o"))
	}
	return m, nil
}

// clickModal handles a click while a modal is open. zone is "" for a click
// outside the modal box.
func (m TicketsModel) clickModal(zone string) (tea.Model, tea.Cmd) {
	if zone == "" {
		return m.dismissModalByClick()
	}
	kind, arg := zoneParts(zone)
	switch kind {
	case "mkey":
		return m.handleKey(tkKey(arg))
	case "mrow":
		i, err := strconv.Atoi(arg)
		if err != nil {
			return m, nil
		}
		return m.clickModalRow(zone, i)
	}
	return m, nil // inside the box but not on a control
}

// clickModalRow picks row i of the agent or prompt picker: the same as
// moving the cursor there and pressing enter.
func (m TicketsModel) clickModalRow(zone string, i int) (tea.Model, tea.Cmd) {
	switch {
	case strings.HasSuffix(zone, "@prompt") && m.promptModal.Visible:
		if psm, ok := m.promptModal.Content.(PromptSelectModel); ok && i >= 0 && i < len(tlPromptOptions) {
			psm.cursor = i
			m.promptModal.Content = psm
			return m.handleKey(tkKey("enter"))
		}
	case strings.HasSuffix(zone, "@agent") && m.agentModal.Visible:
		if asm, ok := m.agentModal.Content.(AgentSelectModel); ok && i >= 0 && i < len(asm.agents) {
			asm.cursor = i
			m.agentModal.Content = asm
			return m.handleKey(tkKey("enter"))
		}
	}
	return m, nil
}

// dismissModalByClick handles a click outside the open modal: confirmations
// and pickers cancel; a form with typed content is left alone.
func (m TicketsModel) dismissModalByClick() (tea.Model, tea.Cmd) {
	switch {
	case m.confirmModal.Visible:
		return m.handleKey(tkKey("esc"))
	case m.promptModal.Visible:
		m.promptModal.Visible = false // close the whole dispatch flow
		return m, nil
	case m.agentModal.Visible:
		return m.handleKey(tkKey("esc"))
	case m.createModal.Visible:
		if ctm, ok := m.createModal.Content.(CreateTicketModel); ok && ctm.dirty() {
			return m, nil // never discard a typed form
		}
		return m.handleKey(tkKey("esc"))
	case m.helpModal.Visible:
		m.helpModal.Visible = false
		return m, nil
	}
	return m, nil
}

func (m TicketsModel) handleMouseMotion(msg tea.MouseMotionMsg) (tea.Model, tea.Cmd) {
	ev := msg.Mouse()
	z := m.currentZones()
	hover := m.hoverZoneAt(z, ev.X, ev.Y)
	row := ""
	if !m.modalOpen() {
		row = z.atPrefix(ev.X, ev.Y, "row:")
	}
	if hover == m.hover && row == m.hoverRow {
		return m, nil // same target: nothing to redraw
	}
	m.hover, m.hoverRow = hover, row
	return m, nil
}

// tkWheelStep is how many lines one wheel notch scrolls a text pane.
const tkWheelStep = 3

func (m TicketsModel) handleMouseWheel(msg tea.MouseWheelMsg) (tea.Model, tea.Cmd) {
	if m.modalOpen() {
		return m, nil // the wheel never scrolls the view behind a modal
	}
	ev := msg.Mouse()
	var dir int
	switch ev.Button {
	case tea.MouseWheelUp:
		dir = -1
	case tea.MouseWheelDown:
		dir = 1
	default:
		return m, nil
	}
	switch m.currentZones().atPrefix(ev.X, ev.Y, "pane:") {
	case "pane:list":
		key := "j"
		if dir < 0 {
			key = "k"
		}
		return m.handleKey(tkKey(key))
	case "pane:preview":
		m.scrollPreview(dir * tkWheelStep)
	case "pane:body":
		m.scrollDetail(dir * tkWheelStep)
	}
	return m, nil
}
