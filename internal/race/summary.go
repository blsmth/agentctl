package race

import (
	"fmt"
	"math"
	"time"
)

// Summary is the race's totals, the payload of `agentctl race --json`.
type Summary struct {
	Source        string         `json:"source,omitempty"`
	Steps         int            `json:"steps"`
	ToolCalls     int            `json:"tool_calls"`
	ToolCallsBy   map[string]int `json:"tool_calls_by_name"`
	TextBlocks    int            `json:"text_blocks"`
	CharsTyped    int            `json:"chars_typed"`
	CharsRead     int            `json:"chars_read"`
	AgentSeconds  float64        `json:"agent_active_seconds"`
	AgentActive   string         `json:"agent_active"`
	HumanSeconds  float64        `json:"human_seconds"`
	Human         string         `json:"human"`
	Speedup       float64        `json:"speedup"`
	Assumptions   Assumptions    `json:"assumptions"`
	AssumptionTxt string         `json:"assumptions_text"`
}

// Assumptions are the parameters the human time rests on.
type Assumptions struct {
	WPM           float64 `json:"wpm"`
	ReadWPM       float64 `json:"read_wpm"`
	ThinkSeconds  float64 `json:"think_seconds_per_step"`
	MaxGapSeconds float64 `json:"max_gap_seconds"`
}

// Summarize returns the race's totals.
func (r *Race) Summarize(source string) Summary {
	calls := 0
	by := map[string]int{}
	for k, v := range r.ToolCalls {
		by[k] = v
		calls += v
	}
	return Summary{
		Source:       source,
		Steps:        len(r.Steps),
		ToolCalls:    calls,
		ToolCallsBy:  by,
		TextBlocks:   r.TextBlocks,
		CharsTyped:   r.TypedRunes,
		CharsRead:    r.ReadRunes,
		AgentSeconds: round1(r.AgentTotal.Seconds()),
		AgentActive:  Friendly(r.AgentTotal),
		HumanSeconds: round1(r.HumanTotal.Seconds()),
		Human:        Friendly(r.HumanTotal),
		Speedup:      round1(r.Speedup()),
		Assumptions: Assumptions{
			WPM:           r.Params.WPM,
			ReadWPM:       r.Params.ReadWPM,
			ThinkSeconds:  r.Params.Think.Seconds(),
			MaxGapSeconds: r.Params.MaxGap.Seconds(),
		},
		AssumptionTxt: r.Assumptions(),
	}
}

// Assumptions is the one-line statement of what the numbers assume.
func (r *Race) Assumptions() string {
	return r.HumanAssumptions() + " · " + r.AgentAssumptions()
}

// HumanAssumptions is what the human's time assumes.
func (r *Race) HumanAssumptions() string {
	p := r.Params
	s := fmt.Sprintf("human: %g WPM typing, %g WPM reading", p.WPM, p.ReadWPM)
	if p.Think > 0 {
		return s + fmt.Sprintf(", thinks %s per step", shortDur(p.Think))
	}
	return s + " · nonstop: never pauses, never typos, reads every result in full"
}

// AgentAssumptions is how the agent's time is measured.
func (r *Race) AgentAssumptions() string {
	return fmt.Sprintf("agent: real timestamps, gaps capped at %s, waiting on you not counted", shortDur(r.Params.MaxGap))
}

// shortDur formats a flag-sized duration: "60s", "5m", "1.5s".
func shortDur(d time.Duration) string {
	switch {
	case d >= time.Minute && d%time.Minute == 0 && d > 2*time.Minute:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d%time.Second == 0:
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
	return d.String()
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }
