package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/servectl"
)

// The hub's phone indicator: a glyph+label after the title, fed by a probe
// on the poll tick (one local socket connect and a stat of the phone-off
// switch, plus the pidfile liveness check only when the socket fails; never
// launchctl, never per render). It shows phone access (on/off), and the
// serve process being down or stuck distinctly. Clicking it or pressing S
// toggles phone access (servectl.Controller.Toggle: the process keeps
// serving artifact links) and flashes the outcome in the footer. The hub
// runs outside Claude Code, so the Bash validator's serve-tamper rule, which
// keeps agents from doing the same, does not apply to it.

// fleetServeKey toggles phone access from the hub list.
const fleetServeKey = "S"

// fleetServeGap separates the indicator from the title.
const fleetServeGap = "  "

// Seams so tests never probe or toggle the real server.
var (
	fleetServeProbe = func() servectl.State {
		dir, err := servectl.Dir()
		if err != nil {
			return servectl.StateOff
		}
		return servectl.Probe(dir)
	}
	fleetServeToggle = func() (string, error) {
		c, err := servectl.New()
		if err != nil {
			return "", err
		}
		return c.Toggle()
	}
)

// fleetServeProbedMsg carries a probe result.
type fleetServeProbedMsg struct{ state servectl.State }

// fleetServeToggledMsg carries the outcome of a toggle.
type fleetServeToggledMsg struct {
	msg string
	err error
}

// serveProbeCmd starts a probe unless one is in flight.
func (m *FleetModel) serveProbeCmd() tea.Cmd {
	if m.serveProbing {
		return nil
	}
	m.serveProbing = true
	return func() tea.Msg { return fleetServeProbedMsg{state: fleetServeProbe()} }
}

// applyServeProbe records a probe result.
func (m *FleetModel) applyServeProbe(msg fleetServeProbedMsg) {
	m.serveProbing = false
	m.serveState, m.serveKnown = msg.state, true
}

// toggleServe starts a toggle (one at a time) with a progress notice.
func (m *FleetModel) toggleServe() tea.Cmd {
	if m.serveBusy {
		return nil
	}
	m.serveBusy = true
	m.jumpErr = ""
	switch {
	case !m.serveKnown, m.serveState == servectl.StateOff:
		m.setProgress("starting agentctl serve…")
	case m.serveState == servectl.StatePhoneOff:
		m.setProgress("turning phone access on…")
	default:
		m.setProgress("turning phone access off…")
	}
	return func() tea.Msg {
		msg, err := fleetServeToggle()
		return fleetServeToggledMsg{msg: msg, err: err}
	}
}

// applyServeToggled flashes a toggle's outcome and re-probes at once.
func (m *FleetModel) applyServeToggled(msg fleetServeToggledMsg) tea.Cmd {
	m.serveBusy = false
	if msg.err != nil {
		m.clearInfo()
		m.jumpErr = "serve: " + msg.err.Error()
	} else {
		m.setSuccess(msg.msg)
	}
	return m.serveProbeCmd()
}

// serveBadge renders the indicator: green "on" when serve answers with
// phone access on, dim "off" when phone access is off (artifacts still
// served), dim struck-through "down" when no serve process runs, yellow
// "stuck" when its process is alive but the control socket is dead. Empty
// until the first probe lands, so a running server never flashes off.
func (m FleetModel) serveBadge() string {
	if !m.serveKnown {
		return ""
	}
	switch m.serveState {
	case servectl.StateRunning:
		return fleetSuccessStyle.Render("📱 on")
	case servectl.StatePhoneOff:
		return fleetDimStyle.Render("📱 off")
	case servectl.StateUnresponsive:
		return fleetBlockedStyle.Bold(true).Render("📱 stuck")
	}
	return fleetDimStyle.Strikethrough(true).Render("📱 down")
}

// serveBadgeSpan is the indicator's [start, end) columns on the title row;
// ok is false when it is not shown.
func (m FleetModel) serveBadgeSpan() (span [2]int, ok bool) {
	span = m.titleLayout(m.contentWidth()).badge
	return span, span[1] > 0
}

// onServeBadge reports whether (x, y) hits the indicator (title row only).
func (m FleetModel) onServeBadge(x, y int) bool {
	if y != 0 {
		return false
	}
	span, ok := m.serveBadgeSpan()
	return ok && x >= span[0] && x < span[1]
}
