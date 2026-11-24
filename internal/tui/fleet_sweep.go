package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/sweeprun"
	"github.com/blsmth/agentctl/internal/theme"
)

// fleetSweepLabel is the group header's sweep button.
const fleetSweepLabel = "sweep"

// fleetSweepKey sweeps the selected group.
const fleetSweepKey = "w"

// fleetSweep runs `agentctl sweep` for one session as a subprocess; a seam so
// tests never spawn agentctl. Never cmd.RunSweep in process: it assigns the
// global tmux.SessionName.
var fleetSweep sweeprun.Func = sweeprun.Run

// fleetSweptMsg reports a finished sweep subprocess: a preview (dryRun) or
// an apply.
type fleetSweptMsg struct {
	session string
	dryRun  bool
	rep     sweeprun.Report
	err     error
}

// sweepTarget is the sweep target for g.
func sweepTarget(g fleet.Group) sweeprun.Target {
	return sweeprun.Target{Session: g.Session, RuntimeDir: g.RuntimeDir, ProjectDir: g.ProjectDir}
}

// groupCanSweep reports whether the hub may offer sweep for g: only an
// agentctl project session with a known runtime dir (never the hub's own
// session) is a target.
func groupCanSweep(g fleet.Group) bool {
	return sweepTarget(g).Validate() == nil
}

// fleetSweepCmd runs the sweep off the UI goroutine.
func fleetSweepCmd(t sweeprun.Target, dryRun bool) tea.Cmd {
	return func() tea.Msg {
		rep, err := fleetSweep(context.Background(), t, dryRun)
		return fleetSweptMsg{session: t.Session, dryRun: dryRun, rep: rep, err: err}
	}
}

// fleetSweepNudge is the sweepable count at which the button stops being dim.
const fleetSweepNudge = 5

// sweepButtonStyle styles g's sweep button: highlighted while its preview is
// running, armed or applying, accented under the pointer, dim otherwise.
func (m FleetModel) sweepButtonStyle(g fleet.Group, hover fleetHoverRegion) lipgloss.Style {
	switch {
	case m.sweepBusy == g.Session || m.sweepArmedFor(g):
		return fleetBlockedStyle
	case hover == fleetHoverSweep:
		return fleetNewHoverStyle
	case groupSweepable(g) >= fleetSweepNudge:
		// A pile of finished sessions is worth noticing.
		return lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Current().Text))
	}
	return fleetDimStyle
}

// onSweepButton reports whether column x hits group g's sweep button (one
// column of slack on the left; the right edge belongs to "+ new").
func (m FleetModel) onSweepButton(g fleet.Group, x int) bool {
	span := m.headerSweepSpan(g, m.listColWidth())
	return span[1] > 0 && x >= span[0]-1 && x < span[1]
}

func (m FleetModel) sweepArmed() bool { return m.sweepSession != "" }

// sweepArmedFor reports whether g's sweep confirmation is showing.
func (m FleetModel) sweepArmedFor(g fleet.Group) bool {
	return m.sweepArmed() && g.Session == m.sweepSession
}

func (m *FleetModel) cancelSweep() {
	m.sweepSession, m.sweepLine, m.sweepPlan = "", 0, sweeprun.Report{}
}

// startSweep previews a sweep of the group owning line idx. A sweep already
// running (preview or apply, any group) makes it a no-op, so double clicks
// never start a second subprocess.
func (m *FleetModel) startSweep(idx int) tea.Cmd {
	if m.sweepBusy != "" {
		return nil
	}
	g := m.groupOfLine(idx)
	if g == nil {
		return nil
	}
	m.cancelSweep()
	m.cancelClose()
	m.cancelNew()
	if !groupCanSweep(*g) {
		m.jumpErr = "cannot sweep " + m.projectLabel(g.Session) + ": not an agentctl session"
		return nil
	}
	m.jumpErr = ""
	m.sweepBusy = g.Session
	m.setProgress("checking what to sweep in " + m.projectLabel(g.Session) + "…")
	return fleetSweepCmd(sweepTarget(*g), true)
}

// confirmSweep applies the armed sweep. Sweep re-classifies every window at
// apply time, so the result is reported from the apply, not the preview.
func (m *FleetModel) confirmSweep() tea.Cmd {
	session := m.sweepSession
	m.cancelSweep()
	if m.sweepBusy != "" {
		return nil
	}
	g := m.groupForSession(session)
	if g == nil || !groupCanSweep(*g) {
		m.clearInfo()
		m.jumpErr = "cannot sweep " + session + ": the session is gone"
		return nil
	}
	m.jumpErr = ""
	m.sweepBusy = session
	m.setProgress("sweeping " + m.projectLabel(session) + "…")
	return fleetSweepCmd(sweepTarget(*g), false)
}

// applySwept handles a finished sweep subprocess.
func (m *FleetModel) applySwept(msg fleetSweptMsg) tea.Cmd {
	if m.sweepBusy == msg.session {
		m.sweepBusy = ""
	}
	label := m.projectLabel(msg.session)
	if msg.err != nil {
		m.cancelSweep()
		m.clearInfo()
		m.jumpErr = msg.err.Error()
		if msg.dryRun {
			return nil
		}
		return fleetLoadCmd()
	}
	if msg.dryRun {
		return m.armSweep(msg, label)
	}
	n := len(msg.rep.Closed)
	if failed := msg.rep.Failed(); len(failed) > 0 {
		m.clearInfo()
		m.jumpErr = fmt.Sprintf("swept %d in %s; %d failed: %s: %s", n-len(failed), label, len(failed),
			failed[0].Name, strings.Join(failed[0].Errors, "; "))
		return fleetLoadCmd()
	}
	m.setSuccess(fmt.Sprintf("swept %d in %s", n, label))
	return fleetLoadCmd()
}

// armSweep shows a preview's plan as an inline y/n confirmation, or "nothing
// to sweep". The confirm is armed only while the cursor is still in the group
// the sweep was started from, on the line it is on now.
func (m *FleetModel) armSweep(msg fleetSweptMsg, label string) tea.Cmd {
	if len(msg.rep.Closed) == 0 {
		text := "sweep " + label + ": nothing to sweep"
		if plan := msg.rep.Plan(); plan != "nothing to sweep" {
			text += " (" + plan + ")"
		}
		m.setSuccess(text)
		return nil
	}
	cur := m.splitPane.Cursor()
	if o := m.groupOfLine(cur); o == nil || o.Session != msg.session {
		m.clearInfo() // the user moved to another group while the preview ran
		return nil
	}
	m.clearInfo()
	m.sweepSession, m.sweepLine, m.sweepPlan = msg.session, cur, msg.rep
	return nil
}

// validateSweep cancels the armed confirm when its group is gone or no longer
// owns the armed line (after a reload).
func (m *FleetModel) validateSweep() {
	if !m.sweepArmed() {
		return
	}
	if o := m.groupOfLine(m.sweepLine); o == nil || o.Session != m.sweepSession {
		m.cancelSweep()
	}
}

// fleetSweepAsk ends the armed confirm's prompt; it survives truncation.
const fleetSweepAsk = "  y/n"

// sweepPrompt is the footer text of the armed confirm, at most w columns:
// the plan truncates, the trailing "y/n" never does.
func (m FleetModel) sweepPrompt(w int) string {
	text := "sweep " + m.projectLabel(m.sweepSession) + ": " + m.sweepPlan.Plan()
	return truncateToWidth(text, max(w-lipgloss.Width(fleetSweepAsk), 1)) + fleetSweepAsk
}

// fleetSweepHints replace the footer hints while a sweep confirm is armed.
var fleetSweepHints = []fleetHint{
	{"y", "sweep"},
	{"n", "cancel"},
}
