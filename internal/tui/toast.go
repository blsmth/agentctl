package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/theme"
)

// ToastLevel controls the visual style of a toast notification.
type ToastLevel int

const (
	// ToastInfo is for informational messages; renders with the accent/blue background.
	ToastInfo ToastLevel = iota
	// ToastWarn is for warning messages; renders with the yellow background.
	ToastWarn
	// ToastError is for error messages; renders with the red background.
	ToastError
)

const (
	// toastAnimFrames is the number of animation ticks for slide-in and slide-out.
	// ≥ 3 ensures the acceptance criterion of "≥ 3 visible frames on entry and exit".
	toastAnimFrames = 5

	// toastAnimInterval is the tick rate for slide animation (~25 FPS).
	toastAnimInterval = 40 * time.Millisecond

	// toastDefaultHold is the default time a toast stays fully visible.
	toastDefaultHold = 3 * time.Second

	// toastMaxIndent is the maximum left-indent used during slide animation.
	// At indent == toastMaxIndent the toast content is fully hidden to the right;
	// at indent == 0 it is fully revealed at the left edge.
	toastMaxIndent = 20
)

// toastState is the internal per-frame animation state for the active toast.
type toastState int

const (
	toastStateHidden     toastState = iota // no toast visible
	toastStateSlidingIn                    // enter animation; frame runs 0→toastAnimFrames
	toastStateVisible                      // fully visible; hold timer running
	toastStateSlidingOut                   // exit animation; frame runs 0→toastAnimFrames
)

// toastEntry is one item stored in the pending queue.
type toastEntry struct {
	msg      string
	level    ToastLevel
	duration time.Duration
}

// ToastMsg is the public contract between callers and ChromeBarModel.
// Any model in the codebase fires a toast with one line:
//
//	return NewToast("dispatch complete")
//
// ChromeBarModel intercepts ToastMsg and routes it to the embedded ToastModel.
type ToastMsg struct {
	msg      string
	level    ToastLevel
	duration time.Duration
}

// toastAnimMsg fires on each animation-frame tick during slide-in / slide-out.
type toastAnimMsg struct{}

// toastHoldDoneMsg fires when the visible-hold timer expires.
type toastHoldDoneMsg struct{}

// ToastOpt is a functional option for NewToast.
type ToastOpt func(*ToastMsg)

// WithToastLevel sets the notification level (info / warn / error).
func WithToastLevel(l ToastLevel) ToastOpt {
	return func(m *ToastMsg) { m.level = l }
}

// WithToastDuration overrides the default 3-second visible hold duration.
func WithToastDuration(d time.Duration) ToastOpt {
	return func(m *ToastMsg) { m.duration = d }
}

// NewToast returns a tea.Cmd that injects a ToastMsg into the Bubble Tea event
// loop. Any model in the codebase can trigger an in-TUI toast with one line:
//
//	return NewToast("task complete", WithToastLevel(ToastInfo))
func NewToast(msg string, opts ...ToastOpt) tea.Cmd {
	t := &ToastMsg{
		msg:      msg,
		level:    ToastInfo,
		duration: toastDefaultHold,
	}
	for _, opt := range opts {
		opt(t)
	}
	return func() tea.Msg { return *t }
}

// ToastModel manages the queue and animation state for in-TUI toast notifications.
// Embed it inside ChromeBarModel (or any Bubble Tea model) and delegate
// ToastMsg / toastAnimMsg / toastHoldDoneMsg to its Update method.
type ToastModel struct {
	queue   []toastEntry
	current *toastEntry
	state   toastState
	frame   int // current animation frame (0..toastAnimFrames)
}

// newToastModel creates a ready-to-use ToastModel (state: hidden, queue empty).
func newToastModel() ToastModel {
	return ToastModel{}
}

// Update handles the three toast-specific messages. It is the caller's
// responsibility to forward these messages from the parent model's Update.
func (m ToastModel) Update(msg tea.Msg) (ToastModel, tea.Cmd) {
	switch msg := msg.(type) {
	case ToastMsg:
		entry := toastEntry(msg)
		if m.state == toastStateHidden {
			// Start the slide-in animation immediately.
			m.current = &entry
			m.state = toastStateSlidingIn
			m.frame = 0
			return m, toastAnimTick()
		}
		// A toast is already showing — queue this one.
		m.queue = append(m.queue, entry)
		return m, nil

	case toastAnimMsg:
		switch m.state {
		case toastStateSlidingIn:
			m.frame++
			if m.frame >= toastAnimFrames {
				// Slide-in complete; enter the visible/hold phase.
				m.state = toastStateVisible
				m.frame = 0
				hold := toastDefaultHold
				if m.current != nil {
					hold = m.current.duration
				}
				return m, toastHoldTick(hold)
			}
			return m, toastAnimTick()

		case toastStateSlidingOut:
			m.frame++
			if m.frame >= toastAnimFrames {
				// Slide-out complete; advance to the next queued toast or hide.
				return m.advance()
			}
			return m, toastAnimTick()
		}
		return m, nil

	case toastHoldDoneMsg:
		if m.state == toastStateVisible {
			m.state = toastStateSlidingOut
			m.frame = 0
			return m, toastAnimTick()
		}
		return m, nil
	}
	return m, nil
}

// advance pops the next queued toast (starting its slide-in) or resets to hidden.
func (m ToastModel) advance() (ToastModel, tea.Cmd) {
	if len(m.queue) > 0 {
		next := m.queue[0]
		m.queue = m.queue[1:]
		m.current = &next
		m.state = toastStateSlidingIn
		m.frame = 0
		return m, toastAnimTick()
	}
	m.current = nil
	m.state = toastStateHidden
	m.frame = 0
	return m, nil
}

// Active reports whether a toast is currently animating or visible.
// Use this to decide whether to render the toast line.
func (m ToastModel) Active() bool {
	return m.state != toastStateHidden
}

// View renders the toast notification into a single line of exactly width columns.
// Returns "" when no toast is active — callers should check Active() first or
// simply ignore the empty string.
//
// Animation: the content slides in from the right (large left-indent → 0) and
// slides out to the right (0 → large left-indent). Each direction has
// toastAnimFrames (5) visible frames, satisfying the "≥ 3 frames" requirement.
func (m ToastModel) View(width int) string {
	if m.current == nil || m.state == toastStateHidden {
		return ""
	}

	t := theme.Current()

	// Choose level-appropriate style roles (no raw hex — all from theme).
	var fgColor, bgColor string
	var icon string
	switch m.current.level {
	case ToastWarn:
		fgColor = t.Base
		bgColor = t.Yellow
		icon = "⚠"
	case ToastError:
		fgColor = t.Bright
		bgColor = t.Red
		icon = "✖"
	default: // ToastInfo
		fgColor = t.Bright
		bgColor = t.Accent
		icon = "ℹ"
	}

	contentStyle := toastInfoStyle.
		Foreground(lipgloss.Color(fgColor)).
		Background(lipgloss.Color(bgColor))

	fillStyle := lipgloss.NewStyle().
		Background(lipgloss.Color(t.Base))

	// Compute the left indent that produces the slide effect.
	// Slide-in:  indent decreases toastMaxIndent → 0 as frame advances.
	// Slide-out: indent increases 0 → toastMaxIndent as frame advances.
	indent := 0
	switch m.state {
	case toastStateSlidingIn:
		indent = toastMaxIndent * (toastAnimFrames - m.frame) / toastAnimFrames
	case toastStateSlidingOut:
		indent = toastMaxIndent * m.frame / toastAnimFrames
	}

	content := fmt.Sprintf("  %s  %s  ", icon, m.current.msg)
	contentW := lipgloss.Width(content)

	prefix := fillStyle.Render(strings.Repeat(" ", indent))
	rendered := contentStyle.Render(content)

	usedW := indent + contentW
	rightFill := width - usedW
	if rightFill < 0 {
		rightFill = 0
	}
	suffix := fillStyle.Render(strings.Repeat(" ", rightFill))

	return prefix + rendered + suffix
}

// toastAnimTick schedules the next animation frame tick.
func toastAnimTick() tea.Cmd {
	return tea.Tick(toastAnimInterval, func(_ time.Time) tea.Msg {
		return toastAnimMsg{}
	})
}

// toastHoldTick schedules the hold-expiry message after the given duration.
func toastHoldTick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(_ time.Time) tea.Msg {
		return toastHoldDoneMsg{}
	})
}
