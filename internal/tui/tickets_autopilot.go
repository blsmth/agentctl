package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/blsmth/agentctl/internal/agentlabel"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/selfpath"
	"github.com/blsmth/agentctl/internal/theme"
	"github.com/charmbracelet/x/ansi"
)

// Autopilot polls GitHub every auto_poll_interval for open issues carrying
// autoPollReadyLabel and dispatches each one (skipping autoPollSkipLabel) to
// the agent its labels name. auto_poll is effectively global-only (a project
// config can never switch it on), so the browser always shows what
// config.Load resolves after a toggle rather than what it asked for.
const (
	autoPollReadyLabel = "ready"
	autoPollSkipLabel  = "in-progress"
)

// ── Messages ─────────────────────────────────────────────────────────────────

// autoPollTickMsg fires on each poll interval. gen identifies the tick loop
// that scheduled it: turning autopilot off (or on again) bumps the model's
// generation, so a tick from an older loop is dropped instead of polling or
// rescheduling, and toggling quickly can never leave two loops running.
type autoPollTickMsg struct{ gen int }

// autopilotClockMsg ticks the countdown once a second while autopilot is on.
type autopilotClockMsg struct{ gen int }

// autoPollCandidate is an issue the next poll would dispatch.
type autoPollCandidate struct {
	number int
	title  string
	agent  string
}

// autoPollListedMsg carries the ready issues found by a poll.
type autoPollListedMsg struct {
	gen   int
	ready []autoPollCandidate
	err   error
}

// autoPollDispatchedMsg reports one dispatch of a poll cycle; rest is the
// queue still to dispatch.
type autoPollDispatchedMsg struct {
	gen  int
	cand autoPollCandidate
	err  error
	rest []autoPollCandidate
}

// autopilotPreviewMsg carries the ready issues for the turn-on confirmation.
type autopilotPreviewMsg struct {
	ready []autoPollCandidate
	err   error
}

// ── Runner seams ─────────────────────────────────────────────────────────────

// autoPollRunner abstracts the external effects of an autopoll cycle so tests
// can inject them.
type autoPollRunner struct {
	// list returns the JSON of ready issues for repoSlug.
	list func(repoSlug string) ([]byte, error)
	// dispatch launches an agent on an issue.
	dispatch func(issue int, agent string) error
}

// execAutoPollRunner is the production runner. It dispatches via the running
// binary (os.Executable) rather than whatever "agentctl" happens to be first on
// PATH, which may be a different version or absent.
var execAutoPollRunner = autoPollRunner{
	list: func(repoSlug string) ([]byte, error) {
		return exec.Command("gh", "issue", "list",
			"--repo", repoSlug,
			"--label", autoPollReadyLabel,
			"--state", "open",
			"--json", "number,title,labels",
		).Output()
	},
	dispatch: func(issue int, agent string) error {
		exe, err := selfpath.Spawnable()
		if err != nil {
			return fmt.Errorf("locating agentctl executable: %w", err)
		}
		out, err := exec.Command(exe, "dispatch",
			"--issue", strconv.Itoa(issue),
			"--agent", agent,
		).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	},
}

// tkAutoPollRunner is the runner the browser uses; TestMain replaces it so
// no test ever lists or dispatches for real.
var tkAutoPollRunner = execAutoPollRunner

// tkLoadAutoPoll resolves the effective autopilot setting.
var tkLoadAutoPoll = func() (on bool, interval time.Duration) {
	cfg := config.Load()
	return cfg.AutoPoll, cfg.EffectiveAutoPollInterval()
}

// tkWriteAutoPoll saves auto_poll=want to the global config (the only layer
// that honors it) when the resolved value differs.
var tkWriteAutoPoll = func(want bool) error {
	cfg := config.Load()
	if cfg.AutoPoll == want {
		return nil
	}
	_, err := cfg.ToggleAutoPoll()
	return err
}

// autoPollReady lists the issues a poll would dispatch: open, labelled
// ready, not in progress, with the agent their labels infer.
func autoPollReady(repoSlug string, r autoPollRunner) ([]autoPollCandidate, error) {
	if repoSlug == "" {
		return nil, nil
	}
	out, err := r.list(repoSlug)
	if err != nil {
		return nil, err
	}
	var issues []struct {
		Number int       `json:"number"`
		Title  string    `json:"title"`
		Labels []ghLabel `json:"labels"`
	}
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, err
	}
	var ready []autoPollCandidate
	for _, iss := range issues {
		if c, ok := autoPollCandidateFor(iss.Number, iss.Title, iss.Labels); ok {
			ready = append(ready, c)
		}
	}
	return ready, nil
}

// autoPollCandidateFor applies the autopilot filter to one issue.
func autoPollCandidateFor(number int, title string, labels []ghLabel) (autoPollCandidate, bool) {
	// GitHub label names match case-insensitively (gh's --label does too).
	if !hasLabelFold(labels, autoPollReadyLabel) || hasLabelFold(labels, autoPollSkipLabel) {
		return autoPollCandidate{}, false
	}
	agent := inferAgentFromLabels(labels)
	if agent == "" {
		agent = agentlabel.Default
	}
	return autoPollCandidate{number: number, title: title, agent: agent}, true
}

// hasLabelFold reports whether labels include name, ignoring case.
func hasLabelFold(labels []ghLabel, name string) bool {
	for _, l := range labels {
		if strings.EqualFold(l.Name, name) {
			return true
		}
	}
	return false
}

// readyFromList is the autopilot filter over the loaded (open) issues.
func readyFromList(issues []ghIssue) []autoPollCandidate {
	var out []autoPollCandidate
	for _, iss := range issues {
		if iss.State != "" && !strings.EqualFold(iss.State, "open") {
			continue
		}
		if c, ok := autoPollCandidateFor(iss.Number, iss.Title, iss.Labels); ok {
			out = append(out, c)
		}
	}
	return out
}

// ── Commands ─────────────────────────────────────────────────────────────────

// autoPollTickCmd schedules the next autopilot poll tick of loop gen.
func autoPollTickCmd(interval time.Duration, gen int) tea.Cmd {
	return tea.Tick(interval, func(time.Time) tea.Msg { return autoPollTickMsg{gen: gen} })
}

// autopilotClockCmd schedules the next countdown tick of loop gen.
func autopilotClockCmd(gen int) tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return autopilotClockMsg{gen: gen} })
}

func autoPollListCmd(repoSlug string, gen int) tea.Cmd {
	r := tkAutoPollRunner
	return func() tea.Msg {
		ready, err := autoPollReady(repoSlug, r)
		return autoPollListedMsg{gen: gen, ready: ready, err: err}
	}
}

func autoPollDispatchCmd(gen int, c autoPollCandidate, rest []autoPollCandidate) tea.Cmd {
	r := tkAutoPollRunner
	return func() tea.Msg {
		err := r.dispatch(c.number, c.agent)
		if err != nil {
			logging.Warn("autopoll: dispatch #%d to %s failed: %v", c.number, c.agent, err)
		}
		return autoPollDispatchedMsg{gen: gen, cand: c, err: err, rest: rest}
	}
}

func autopilotPreviewCmd(repoSlug string) tea.Cmd {
	r := tkAutoPollRunner
	return func() tea.Msg {
		ready, err := autoPollReady(repoSlug, r)
		return autopilotPreviewMsg{ready: ready, err: err}
	}
}

// ── State ────────────────────────────────────────────────────────────────────

// tkPollPhase is what the autopilot is doing right now.
type tkPollPhase int

const (
	pollIdle tkPollPhase = iota
	pollListing
	pollDispatching
)

// tkAutopilot is the browser's autopilot state.
type tkAutopilot struct {
	on       bool
	interval time.Duration
	// ticker is true while a tick loop (generation gen) is scheduled.
	ticker bool
	gen    int

	phase    tkPollPhase
	issue    int // issue being dispatched (pollDispatching)
	nextPoll time.Time
	lastPoll time.Time

	// status is the slot after the timers: "✓ nothing ready",
	// "✓ dispatched #171", or an error (statusErr).
	status    string
	statusErr bool
	// cycle accumulates the current cycle's outcomes.
	done  []int
	fails []string
}

// startLoop begins a new tick loop unless one is already running, returning
// the poll and countdown ticks to schedule.
func (a *tkAutopilot) startLoop(now time.Time) tea.Cmd {
	if !a.on || a.ticker {
		return nil
	}
	a.gen++
	a.ticker = true
	a.nextPoll = now.Add(a.interval)
	return tea.Batch(autoPollTickCmd(a.interval, a.gen), autopilotClockCmd(a.gen))
}

// stopLoop invalidates the running loop: its pending ticks are dropped.
func (a *tkAutopilot) stopLoop() {
	a.gen++
	a.ticker = false
	a.phase = pollIdle
	a.issue = 0
}

// ── Model integration ───────────────────────────────────────────────────────

// toggleAutopilot is P and the pill: turning off is instant, turning on asks
// first.
func (m TicketsModel) toggleAutopilot() (tea.Model, tea.Cmd) {
	if m.autopilot.on {
		return m.setAutopilot(false)
	}
	// Show the interval the config would actually use.
	_, m.autopilot.interval = tkLoadAutoPoll()
	m.choiceModal = NewModal("Turn on autopilot?", m.autopilotConfirm(readyFromList(m.allIssues), true), ModalMedium)
	return m, autopilotPreviewCmd(m.repoSlug)
}

// autopilotConfirm builds the turn-on confirmation. checking marks the list
// as provisional (from the loaded issues) until the poll preview returns.
func (m TicketsModel) autopilotConfirm(ready []autoPollCandidate, checking bool) tkChoiceModel {
	t := theme.Current()
	// The modal box's border and padding come out of its content width.
	cw := modalContentWidth(ModalMedium) - 4
	text := lipgloss.NewStyle().Width(cw).Foreground(lipgloss.Color(t.Text))
	dim := lipgloss.NewStyle().Width(cw).Foreground(lipgloss.Color(t.Muted))

	lines := []string{
		text.Render(fmt.Sprintf("Picks open issues labelled %q (not %q); the agent comes from the issue's labels.",
			autoPollReadyLabel, autoPollSkipLabel)),
		fgStyle(t.Yellow).Bold(true).Render("Global setting") + fgStyle(t.Muted).Render(" · saved to "+
			truncateMiddle(displayConfigPath(), cw-lipgloss.Width("Global setting · saved to "))),
		"",
	}
	head := fmt.Sprintf("First poll in %s. Right now it would dispatch:", fmtInterval(m.autopilot.interval))
	if checking {
		head = fmt.Sprintf("First poll in %s. Right now it would dispatch (checking GitHub…):", fmtInterval(m.autopilot.interval))
	}
	lines = append(lines, text.Render(head))
	if len(ready) == 0 {
		lines = append(lines, dim.Render("  nothing is ready right now"))
	}
	const maxShown = 8
	for i, c := range ready {
		if i == maxShown {
			lines = append(lines, dim.Render(fmt.Sprintf("  … and %d more", len(ready)-maxShown)))
			break
		}
		num := fgStyle(t.Accent).Render(fmt.Sprintf("#%-5d", c.number))
		agent := fgStyle(t.Green).Render("→ " + c.agent)
		title := ansi.Truncate(c.title, cw-lipgloss.Width(agent)-12, "…")
		lines = append(lines, "  "+num+" "+fgStyle(t.Text).Render(title)+"  "+agent)
	}
	return tkChoiceModel{
		kind:  "autopilot",
		lines: lines,
		choices: []tkChoice{
			{label: "Cancel", key: "n"},
			{label: "Turn on", key: "y", primary: true},
		},
		cancel: 0,
		chosen: -1,
	}
}

// displayConfigPath is the global config path with the home dir as ~.
func displayConfigPath() string {
	p, err := config.GlobalFilePath()
	if err != nil {
		return "the global agentctl config"
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
			return "~/" + rel
		}
	}
	return p
}

// truncateMiddle shortens s to width by replacing its middle with "…", so
// both ends (a path's root and its file name) stay readable.
func truncateMiddle(s string, width int) string {
	r := []rune(s)
	if len(r) <= width || width < 5 {
		return s
	}
	tail := (width - 1) / 2
	head := width - 1 - tail
	return string(r[:head]) + "…" + string(r[len(r)-tail:])
}

// fmtInterval renders a poll interval compactly ("5m", "90s", "1h30m").
func fmtInterval(d time.Duration) string {
	s := d.String()
	s = strings.TrimSuffix(s, "0s")
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	if s == "" {
		return d.String()
	}
	return s
}

// setAutopilot writes auto_poll=want and then shows whatever the config
// actually resolves to, surfacing a write error or a value that did not
// stick.
func (m TicketsModel) setAutopilot(want bool) (tea.Model, tea.Cmd) {
	writeErr := tkWriteAutoPoll(want)
	on, interval := tkLoadAutoPoll()
	m.autopilot.on, m.autopilot.interval = on, interval

	var cmds []tea.Cmd
	if on {
		cmds = append(cmds, m.autopilot.startLoop(tkNow()))
	} else if m.autopilot.ticker {
		m.autopilot.stopLoop()
	}

	switch {
	case writeErr != nil:
		m.autopilot.status, m.autopilot.statusErr = "✗ "+writeErr.Error(), true
		cmds = append(cmds, m.setFlash("✗ autopilot: couldn't save the setting: "+writeErr.Error(), true))
	case on != want:
		state := "off"
		if on {
			state = "on"
		}
		msg := fmt.Sprintf("✗ autopilot is still %s: the config resolves auto_poll=%v", state, on)
		m.autopilot.status, m.autopilot.statusErr = msg, true
		cmds = append(cmds, m.setFlash(msg, true))
	case on:
		m.autopilot.status, m.autopilot.statusErr = "", false
		cmds = append(cmds, m.setFlash("Autopilot on — polling every "+fmtInterval(interval), false))
	default:
		m.autopilot.status, m.autopilot.statusErr = "", false
		cmds = append(cmds, m.setFlash("Autopilot off", false))
	}
	return m, tea.Batch(cmds...)
}

// updateAutopilot handles the autopilot's own messages.
func (m TicketsModel) updateAutopilot(msg tea.Msg) (tea.Model, tea.Cmd) {
	a := &m.autopilot
	switch msg := msg.(type) {
	case autoPollTickMsg:
		if msg.gen != a.gen || !a.on || !a.ticker {
			return m, nil // stale loop: drop it, never poll or reschedule
		}
		now := tkNow()
		a.nextPoll = now.Add(a.interval)
		next := autoPollTickCmd(a.interval, a.gen)
		if a.phase != pollIdle {
			return m, next // the previous cycle is still running
		}
		a.phase = pollListing
		a.done, a.fails = nil, nil
		return m, tea.Batch(autoPollListCmd(m.repoSlug, a.gen), next)

	case autopilotClockMsg:
		if msg.gen != a.gen || !a.on || !a.ticker {
			return m, nil
		}
		return m, autopilotClockCmd(a.gen)

	case autoPollListedMsg:
		if msg.gen != a.gen || !a.on {
			return m, nil // turned off (or restarted) since: never dispatch
		}
		a.lastPoll = tkNow()
		if msg.err != nil {
			a.phase = pollIdle
			a.status, a.statusErr = "✗ "+firstNonBlankLine(msg.err.Error()), true
			return m, nil
		}
		if len(msg.ready) == 0 {
			a.phase = pollIdle
			a.status, a.statusErr = "✓ nothing ready", false
			return m, nil
		}
		a.phase, a.issue = pollDispatching, msg.ready[0].number
		return m, autoPollDispatchCmd(a.gen, msg.ready[0], msg.ready[1:])

	case autoPollDispatchedMsg:
		if msg.gen != a.gen {
			return m, nil
		}
		if msg.err != nil {
			a.fails = append(a.fails, fmt.Sprintf("#%d: %s", msg.cand.number, firstNonBlankLine(msg.err.Error())))
		} else {
			a.done = append(a.done, msg.cand.number)
		}
		if a.on && len(msg.rest) > 0 {
			a.issue = msg.rest[0].number
			return m, autoPollDispatchCmd(a.gen, msg.rest[0], msg.rest[1:])
		}
		a.phase, a.issue = pollIdle, 0
		a.status, a.statusErr = cycleStatus(a.done, a.fails)
		// Dispatch relabels issues: refresh the list.
		m.loading = true
		return m, tkFetchTicketsCmd(m.dir, m.issueState)

	case autopilotPreviewMsg:
		if !m.choiceModal.Visible {
			return m, nil
		}
		if c, ok := m.choiceModal.Content.(tkChoiceModel); ok && c.kind == "autopilot" {
			ready := msg.ready
			if msg.err != nil {
				ready = readyFromList(m.allIssues)
			}
			fresh := m.autopilotConfirm(ready, false)
			fresh.focus = c.focus
			if msg.err != nil {
				fresh.lines = append(fresh.lines, fgStyle(theme.Current().Red).Render("  ✗ couldn't check GitHub: "+firstNonBlankLine(msg.err.Error())))
			}
			m.choiceModal.Content = fresh
		}
		return m, nil
	}
	return m, nil
}

// cycleStatus summarises a finished poll cycle for the pill.
func cycleStatus(done []int, fails []string) (string, bool) {
	var nums []string
	for _, n := range done {
		nums = append(nums, "#"+strconv.Itoa(n))
	}
	switch {
	case len(fails) > 0 && len(done) > 0:
		return fmt.Sprintf("✓ dispatched %s · ✗ %s", strings.Join(nums, ", "), fails[0]), true
	case len(fails) > 0:
		return "✗ " + fails[0], true
	case len(done) > 0:
		return "✓ dispatched " + strings.Join(nums, ", "), false
	}
	return "✓ nothing ready", false
}

// ── Pill ─────────────────────────────────────────────────────────────────────

// renderPill renders the autopilot pill and its status. compact drops the
// detail after the timers for narrow headers.
func (m TicketsModel) renderPill(compact bool) string {
	t := theme.Current()
	a := m.autopilot
	const id = "key:P@pill"
	sep := tkDimStyle.Render(" · ")

	if !a.on {
		st := fgStyle(t.Muted)
		if m.hover == id {
			st = st.Reverse(true)
		}
		pill := zoneMark(id, st.Render("[ Autopilot ○ off ]"))
		if a.status != "" && a.statusErr {
			return pill + "  " + fgStyle(t.Red).Render(a.status)
		}
		if n := len(readyFromList(m.allIssues)); n > 0 && !compact {
			noun := "issues"
			if n == 1 {
				noun = "issue"
			}
			hint := fmt.Sprintf("%d %s %s waiting", n, autoPollReadyLabel, noun)
			if m.narrow() {
				hint = fmt.Sprintf("%d %s", n, autoPollReadyLabel)
			}
			return pill + "  " + fgStyle(t.Yellow).Render(hint)
		}
		return pill
	}

	st := fgStyle(t.Green).Bold(true)
	if m.hover == id {
		st = st.Reverse(true)
	}
	parts := []string{zoneMark(id, st.Render("[ Autopilot ● ON ]"))}
	var detail []string
	switch a.phase {
	case pollListing:
		detail = append(detail, fgStyle(t.Sky).Render("polling…"))
	case pollDispatching:
		detail = append(detail, fgStyle(t.Sky).Render(fmt.Sprintf("dispatching #%d…", a.issue)))
	default:
		left := a.nextPoll.Sub(tkNow())
		if left < 0 {
			left = 0
		}
		detail = append(detail, tkDimStyle.Render("next ")+fgStyle(t.Text).Render(fmtCountdown(left)))
	}
	if !compact && !a.lastPoll.IsZero() && a.phase == pollIdle {
		last := tkDimStyle.Render("last ") + fgStyle(t.Text).Render(a.lastPoll.Format("15:04"))
		if a.status != "" {
			stSt := fgStyle(t.Green)
			if a.statusErr {
				stSt = fgStyle(t.Red)
			}
			last += " " + stSt.Render(a.status)
		}
		detail = append(detail, last)
	} else if !compact && a.status != "" && a.statusErr && a.phase == pollIdle {
		detail = append(detail, fgStyle(t.Red).Render(a.status))
	}
	return parts[0] + "  " + strings.Join(detail, sep)
}

// fmtCountdown renders d as "4m" or "23h 59m", with seconds only in the
// last minute ("42s").
func fmtCountdown(d time.Duration) string {
	s := int(d.Round(time.Second).Seconds())
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm", s/60)
	}
	return fmt.Sprintf("%dh %dm", s/3600, s%3600/60)
}
