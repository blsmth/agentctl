package serve

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/serve/fleetwatch"
)

const (
	// flapWindow suppresses a repeat push of the same kind for a window: a
	// hook state that flaps blocked/working/blocked must not buzz the phone
	// each time.
	flapWindow = 30 * time.Second
	// prGrace is how long a done push for a dispatched window waits for
	// dispatch completion to record the PR, so "PR #N ready" is not
	// preempted by a plain "finished".
	prGrace = 20 * time.Second
	// notifyBuffer is the outgoing queue's capacity. The bus is never
	// blocked: when the queue is full the new message is dropped and the
	// queued (older) ones are kept.
	notifyBuffer = 32
)

// episode is the push state of one window.
type episode struct {
	status string // last seen: blocked, working, done, idle
	win    fleet.Window

	blockedAt   time.Time // start of the current blocked episode
	reminded    bool      // reminder sent (or deliberately skipped) this episode
	lastBlocked time.Time // last blocked push, for flap suppression
	lastDone    time.Time

	doneDeadline time.Time // non-zero: done push waiting for the PR until then
}

// Notifier turns fleet bus events into push messages. The decision logic
// (handle, tick) is synchronous and clock-driven so tests run it with a fake
// clock; Run wires it to a bus and a delivery queue.
type Notifier struct {
	cfg     config.ServePushConfig
	now     func() time.Time
	deliver func(Message)

	mu       sync.Mutex
	episodes map[string]*episode
}

// NewNotifier returns a Notifier that hands messages to deliver.
func NewNotifier(cfg config.ServePushConfig, now func() time.Time, deliver func(Message)) *Notifier {
	if now == nil {
		now = time.Now
	}
	return &Notifier{cfg: cfg, now: now, deliver: deliver, episodes: make(map[string]*episode)}
}

func winKey(w fleet.Window) string { return w.Session + "\x00" + w.WindowID }

// Seed records the windows present at startup without notifying about them: a
// restart must not re-announce every blocked agent.
func (n *Notifier) Seed(groups []fleetwatch.Group) {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := n.now()
	for _, g := range groups {
		for _, w := range g.Windows {
			ep := &episode{status: fleetwatch.Status(w), win: w}
			if ep.status == "blocked" {
				ep.blockedAt, ep.reminded = now, true
			}
			n.episodes[winKey(w)] = ep
		}
	}
}

// Handle processes one bus event.
func (n *Notifier) Handle(ev fleetwatch.Event) {
	n.mu.Lock()
	defer n.mu.Unlock()
	switch ev.Kind {
	case fleetwatch.KindTransition:
		n.observeLocked(ev.Window)
	case fleetwatch.KindWindow:
		// A rendered field changed. Besides state, the PR URL arriving after
		// the done transition matters.
		n.observeLocked(ev.Window)
	case fleetwatch.KindFleet:
		// Events were dropped; compare against what we know.
		for _, g := range ev.Fleet {
			for _, w := range g.Windows {
				n.observeLocked(w)
			}
		}
	case fleetwatch.KindGone:
		delete(n.episodes, ev.Session+"\x00"+ev.WindowID)
	}
}

// observeLocked compares w with the window's tracked state and fires triggers.
func (n *Notifier) observeLocked(w fleet.Window) {
	now := n.now()
	k := winKey(w)
	to := fleetwatch.Status(w)
	ep, known := n.episodes[k]
	if !known {
		// First sight of a window that appeared after startup: a window
		// that starts out blocked or done is worth a push; idle/working is not.
		ep = &episode{status: "idle"}
		n.episodes[k] = ep
	}
	from := ep.status
	ep.status, ep.win = to, w

	if from != to {
		switch to {
		case "blocked":
			ep.blockedAt, ep.reminded = now, false
			n.fireBlockedLocked(ep, w, now)
		case "done":
			n.startDoneLocked(ep, w, now)
		}
		if from == "blocked" {
			ep.blockedAt, ep.reminded = time.Time{}, false // re-arm
		}
		if from == "done" {
			ep.doneDeadline = time.Time{}
		}
		return
	}
	// Same status: a done window whose PR just got recorded.
	if to == "done" && !ep.doneDeadline.IsZero() && w.PRURL != "" {
		n.sendDoneLocked(ep, w, now)
	}
}

// Tick fires time-based triggers: blocked reminders and done pushes whose PR
// grace period ran out. Call it about once a second.
func (n *Notifier) Tick() {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := n.now()
	remind := time.Duration(n.cfg.EffectiveBlockedReminderMinutes()) * time.Minute
	for _, ep := range n.episodes {
		if !ep.doneDeadline.IsZero() && !now.Before(ep.doneDeadline) && ep.status == "done" {
			n.sendDoneLocked(ep, ep.win, now)
		}
		if remind > 0 && ep.status == "blocked" && !ep.reminded && !ep.blockedAt.IsZero() && now.Sub(ep.blockedAt) >= remind {
			ep.reminded = true // once per episode, even if suppressed
			n.fireReminderLocked(ep.win, now.Sub(ep.blockedAt))
		}
	}
}

func (n *Notifier) fireBlockedLocked(ep *episode, w fleet.Window, now time.Time) {
	if !n.cfg.BlockedEnabled() || !n.allowedLocked(w, now) {
		return
	}
	if !ep.lastBlocked.IsZero() && now.Sub(ep.lastBlocked) < flapWindow {
		return
	}
	ep.lastBlocked = now
	body := windowLabel(w)
	if w.PendingTool != "" {
		body += " · " + w.PendingTool
	}
	n.deliver(Message{
		Title:  fmt.Sprintf("agentctl: %s blocked", w.Agent),
		Body:   body,
		URL:    windowURL(w.Session, w.WindowID),
		Tag:    w.Session + ":" + w.WindowID,
		Urgent: true,
		Kind:   "blocked",
	})
}

func (n *Notifier) fireReminderLocked(w fleet.Window, blockedFor time.Duration) {
	if !n.cfg.BlockedEnabled() || !n.allowedLocked(w, n.now()) {
		return
	}
	n.deliver(Message{
		Title:  fmt.Sprintf("agentctl: %s still blocked", w.Agent),
		Body:   fmt.Sprintf("%s · waiting %d min", windowLabel(w), int(blockedFor.Minutes())),
		URL:    windowURL(w.Session, w.WindowID),
		Tag:    w.Session + ":" + w.WindowID,
		Urgent: true,
		Kind:   "reminder",
	})
}

// startDoneLocked fires the done push, or waits briefly for the PR when
// dispatch completion (which records it) is expected to run for this window.
func (n *Notifier) startDoneLocked(ep *episode, w fleet.Window, now time.Time) {
	if w.PRURL == "" && (w.Mode == "dispatch" || w.Mode == "adhoc") {
		ep.doneDeadline = now.Add(prGrace)
		return
	}
	n.sendDoneLocked(ep, w, now)
}

func (n *Notifier) sendDoneLocked(ep *episode, w fleet.Window, now time.Time) {
	ep.doneDeadline = time.Time{}
	if !n.cfg.DoneEnabled() || !n.allowedLocked(w, now) {
		return
	}
	if !ep.lastDone.IsZero() && now.Sub(ep.lastDone) < flapWindow {
		return
	}
	ep.lastDone = now
	what := "finished"
	switch {
	case w.PRNumber > 0:
		what = fmt.Sprintf("PR #%d ready", w.PRNumber)
	case w.PRURL != "":
		what = "PR ready"
	}
	n.deliver(Message{
		Title: fmt.Sprintf("agentctl: %s done", w.Agent),
		Body:  windowLabel(w) + " · " + what,
		URL:   windowURL(w.Session, w.WindowID),
		Tag:   w.Session + ":" + w.WindowID,
		Kind:  "done",
	})
}

// allowedLocked applies muted projects and quiet hours.
func (n *Notifier) allowedLocked(w fleet.Window, now time.Time) bool {
	if slices.Contains(n.cfg.MutedProjects, w.Project) {
		return false
	}
	return !InQuietHours(n.cfg.QuietHours, now)
}

func windowLabel(w fleet.Window) string {
	return fmt.Sprintf("%s · %s", w.Project, w.WindowID)
}

// InQuietHours reports whether t falls in spec, a local-time "HH:MM-HH:MM"
// range that may cross midnight (22:00-07:00). The start is inclusive, the end
// exclusive. An empty or malformed spec is never quiet. t's own location is
// used, so callers pass time.Now() for local time.
func InQuietHours(spec string, t time.Time) bool {
	start, end, ok := parseQuietHours(spec)
	if !ok {
		return false
	}
	m := t.Hour()*60 + t.Minute()
	switch {
	case start == end:
		return false
	case start < end:
		return m >= start && m < end
	default:
		return m >= start || m < end
	}
}

func parseQuietHours(spec string) (start, end int, ok bool) {
	from, to, found := strings.Cut(strings.TrimSpace(spec), "-")
	if !found {
		return 0, 0, false
	}
	s, err1 := time.Parse("15:04", strings.TrimSpace(from))
	e, err2 := time.Parse("15:04", strings.TrimSpace(to))
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return s.Hour()*60 + s.Minute(), e.Hour()*60 + e.Minute(), true
}

// runNotifier drives n from bus until ctx is done: bus events as they come
// and a one-second tick for reminders and PR grace deadlines.
func runNotifier(ctx context.Context, bus *fleetwatch.Bus, n *Notifier) {
	groups, sub := bus.Subscribe()
	defer sub.Close()
	n.Seed(groups)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-sub.C:
			n.Handle(ev)
		case <-tick.C:
			n.Tick()
		}
	}
}

// pushDispatcher delivers messages off the bus goroutine: Notifier.deliver
// only enqueues; one worker does the network I/O.
type pushDispatcher struct {
	sender *Sender
	ntfy   *Ntfy
	q      chan Message
}

func newPushDispatcher(sender *Sender, ntfy *Ntfy) *pushDispatcher {
	return &pushDispatcher{sender: sender, ntfy: ntfy, q: make(chan Message, notifyBuffer)}
}

// enqueue never blocks; a full queue drops the message.
func (d *pushDispatcher) enqueue(m Message) {
	select {
	case d.q <- m:
	default:
		logging.Warn("serve: push queue full, dropping %s notification for %s", m.Kind, m.Tag)
	}
}

func (d *pushDispatcher) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-d.q:
			d.sender.Broadcast(ctx, m)
			if d.ntfy != nil {
				if err := d.ntfy.Send(ctx, m); err != nil {
					logging.Warn("serve: %v", err)
				}
			}
		}
	}
}
