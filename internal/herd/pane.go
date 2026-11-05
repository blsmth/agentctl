package herd

import "github.com/blsmth/agentctl/internal/panestate"

// PaneVerdict is what a pane's visible contents say the agent is doing, as
// opposed to what windowstate claims. When the two disagree, the pane wins.
//
// The classification predicate itself lives in internal/panestate so
// internal/tmux can share it (herd imports tmux, so the reverse import would
// be a cycle). This type is a thin herd-facing wrapper over it.
type PaneVerdict int

const (
	// PaneUnknown means the pane could not be read or matched no rule.
	PaneUnknown = PaneVerdict(panestate.Unknown)
	// PaneAwaitingInput means a permission/confirmation dialog is on screen.
	PaneAwaitingInput = PaneVerdict(panestate.AwaitingInput)
	// PaneBusy means the agent is mid-turn.
	PaneBusy = PaneVerdict(panestate.Busy)
	// PaneIdle means the agent finished its turn and is sitting at a prompt.
	PaneIdle = PaneVerdict(panestate.Idle)
)

// String returns a lowercase, stable name suitable for JSON and table output.
func (v PaneVerdict) String() string { return panestate.Verdict(v).String() }

// ClassifyPane derives a verdict from captured pane text. See
// panestate.Classify.
func ClassifyPane(text string) PaneVerdict {
	return PaneVerdict(panestate.Classify(text))
}

// PromptCount returns the number of "❯" prompt lines in the pane. See
// panestate.PromptCount.
func PromptCount(text string) int { return panestate.PromptCount(text) }

// AgreesWith reports whether a pane verdict is consistent with a windowstate
// state string ("working", "blocked", "idle"). Used to flag the disagreements
// worth a human's attention rather than printing both and leaving the reader
// to diff them.
func (v PaneVerdict) AgreesWith(recorded string) bool {
	switch v {
	case PaneAwaitingInput:
		return recorded == "blocked"
	case PaneBusy:
		return recorded == "working"
	case PaneIdle:
		return recorded == "idle" || recorded == ""
	}
	return true // Unknown never contradicts anything.
}
