package race

import (
	"fmt"
	"sort"
	"time"
)

// AgentPos is where the agent is at a point on the timeline.
type AgentPos struct {
	Index int     // current step; len(Steps) once done
	Frac  float64 // progress through the current step, 0..1
	Done  bool
}

// AgentAt places the agent at transcript time t.
func (r *Race) AgentAt(t time.Duration) AgentPos {
	if len(r.Steps) == 0 || t >= r.AgentTotal {
		return AgentPos{Index: len(r.Steps), Frac: 1, Done: true}
	}
	if t < 0 {
		t = 0
	}
	i := sort.Search(len(r.Steps), func(i int) bool {
		s := r.Steps[i]
		return s.AgentStart+s.AgentDur > t
	})
	if i >= len(r.Steps) {
		return AgentPos{Index: len(r.Steps), Frac: 1, Done: true}
	}
	s := r.Steps[i]
	return AgentPos{Index: i, Frac: frac(t-s.AgentStart, s.AgentDur)}
}

// Phase is what the human is doing within a step.
type Phase int

// Human phases, in the order a step goes through them.
const (
	PhaseTyping Phase = iota
	PhaseReading
	PhaseThinking
	PhaseDone
)

func (p Phase) String() string {
	switch p {
	case PhaseTyping:
		return "typing"
	case PhaseReading:
		return "reading"
	case PhaseThinking:
		return "thinking"
	}
	return "done"
}

// HumanPos is where the human is at a point on the timeline.
type HumanPos struct {
	Index    int // current step; len(Steps) once done
	Phase    Phase
	Typed    int     // runes of the current step typed so far
	ReadFrac float64 // share of the current step's result read, 0..1
	Done     bool
}

// HumanAt places the human at transcript time t.
func (r *Race) HumanAt(t time.Duration) HumanPos {
	done := HumanPos{Index: len(r.Steps), Phase: PhaseDone, ReadFrac: 1, Done: true}
	if len(r.Steps) == 0 || t >= r.HumanTotal {
		return done
	}
	if t < 0 {
		t = 0
	}
	i := sort.Search(len(r.Steps), func(i int) bool {
		s := r.Steps[i]
		return s.HumanStart+s.HumanDur() > t
	})
	if i >= len(r.Steps) {
		return done
	}
	s := r.Steps[i]
	in := t - s.HumanStart
	switch {
	case in < s.TypeDur:
		typed := int(frac(in, s.TypeDur) * float64(s.TypedRunes))
		return HumanPos{Index: i, Phase: PhaseTyping, Typed: typed}
	case in < s.TypeDur+s.ReadDur:
		return HumanPos{Index: i, Phase: PhaseReading, Typed: s.TypedRunes, ReadFrac: frac(in-s.TypeDur, s.ReadDur)}
	default:
		return HumanPos{Index: i, Phase: PhaseThinking, Typed: s.TypedRunes, ReadFrac: 1}
	}
}

// HumanWorkFor is how long the human would need for the work the agent has
// finished by transcript time t (whole steps plus the share of the current
// one). Divided by t it is the live speedup.
func (r *Race) HumanWorkFor(t time.Duration) time.Duration {
	p := r.AgentAt(t)
	if p.Done {
		return r.HumanTotal
	}
	s := r.Steps[p.Index]
	return s.HumanStart + time.Duration(p.Frac*float64(s.HumanDur()))
}

func frac(part, whole time.Duration) float64 {
	if whole <= 0 {
		return 1
	}
	f := float64(part) / float64(whole)
	switch {
	case f < 0:
		return 0
	case f > 1:
		return 1
	}
	return f
}

// Factor is the replay rate in transcript seconds per wall second: speed when
// set, else whatever fits the agent's whole timeline into duration. Never
// below 1e-3.
func Factor(agentTotal, duration time.Duration, speed float64) float64 {
	f := speed
	if f <= 0 {
		f = 1
		if duration > 0 && agentTotal > 0 {
			f = float64(agentTotal) / float64(duration)
		}
	}
	if f < 1e-3 {
		f = 1e-3
	}
	return f
}

// Playback maps wall time onto transcript time. It holds no clock: every
// call takes the current wall time, so a replay is deterministic under a
// fake clock.
type Playback struct {
	factor float64
	pos    time.Duration // transcript time
	last   time.Time
	paused bool
}

// NewPlayback starts a replay at transcript time 0 at wall time now.
func NewPlayback(factor float64, now time.Time) *Playback {
	return &Playback{factor: factor, last: now}
}

// Advance moves transcript time forward by the wall time since the last
// call, scaled by the factor. A paused playback only records now.
func (p *Playback) Advance(now time.Time) time.Duration {
	if !p.paused && now.After(p.last) {
		p.pos += time.Duration(float64(now.Sub(p.last)) * p.factor)
	}
	p.last = now
	return p.pos
}

// Pos is the current transcript time.
func (p *Playback) Pos() time.Duration { return p.pos }

// Factor is the current replay rate.
func (p *Playback) Factor() float64 { return p.factor }

// Paused reports whether the playback is paused.
func (p *Playback) Paused() bool { return p.paused }

// TogglePause pauses or resumes at wall time now.
func (p *Playback) TogglePause(now time.Time) {
	p.Advance(now)
	p.paused = !p.paused
}

// SetFactor changes the rate from wall time now on, clamped to
// [1e-3, 1e6].
func (p *Playback) SetFactor(f float64, now time.Time) {
	p.Advance(now)
	switch {
	case f < 1e-3:
		f = 1e-3
	case f > 1e6:
		f = 1e6
	}
	p.factor = f
}

// Seek jumps to transcript time t at wall time now.
func (p *Playback) Seek(t time.Duration, now time.Time) {
	p.pos = t
	p.last = now
}

// Friendly formats a human-sized duration: seconds, minutes, hours, then
// 8-hour workdays and 40-hour weeks.
func Friendly(d time.Duration) string {
	const (
		workday = 8 * time.Hour
		week    = 5 * workday
	)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Round(time.Second)/time.Second))
	case d < time.Hour:
		d = d.Round(time.Second)
		return fmt.Sprintf("%dm %02ds", int(d/time.Minute), int(d%time.Minute/time.Second))
	case d < workday:
		return HM(d)
	case d < week:
		return plural(float64(d)/float64(workday), "workday")
	}
	return plural(float64(d)/float64(week), "week")
}

func plural(n float64, unit string) string {
	s := fmt.Sprintf("%.1f %s", n, unit)
	if s != "1.0 "+unit {
		s += "s"
	}
	return s
}

// HM formats d as hours and minutes ("25h 41m").
func HM(d time.Duration) string {
	d = d.Round(time.Minute)
	return fmt.Sprintf("%dh %02dm", int(d/time.Hour), int(d%time.Hour/time.Minute))
}

// Clock formats d as H:MM:SS.
func Clock(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Truncate(time.Second)
	return fmt.Sprintf("%d:%02d:%02d", int(d/time.Hour), int(d%time.Hour/time.Minute), int(d%time.Minute/time.Second))
}

// Multiplier formats a speedup ("37x", "2.5x").
func Multiplier(x float64) string {
	switch {
	case x <= 0:
		return "0x"
	case x < 10:
		return fmt.Sprintf("%.1fx", x)
	}
	return fmt.Sprintf("%.0fx", x)
}
