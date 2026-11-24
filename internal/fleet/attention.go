package fleet

import (
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// AttentionGlyph is the needs-attention glyph, as in tmux window names.
const AttentionGlyph = "!"

// NeedsAttention reports whether a shows the ! glyph: it has an attention
// flag and is not working or blocked (precedence ⏸ > ● > ! > ✓ > ○).
func NeedsAttention(a state.AgentState) bool {
	return a.Attention != nil && a.State != "working" && a.State != "blocked"
}

// AttentionLabel is the short human label of an attention reason, for row
// details, `status` and the phone: "asks you", "stuck", "review verdict".
func AttentionLabel(reason string) string {
	switch reason {
	case windowstate.AttentionQuestion:
		return "asks you"
	case windowstate.AttentionErrors:
		return "stuck"
	case windowstate.AttentionMessage:
		return "review verdict"
	}
	return "needs you"
}

// AttentionLine is "<label>: <detail>" (or the label alone) for a row that
// needs attention, "" otherwise.
func AttentionLine(a state.AgentState) string {
	if !NeedsAttention(a) {
		return ""
	}
	l := AttentionLabel(a.Attention.Reason)
	if a.Attention.Detail != "" {
		l += ": " + a.Attention.Detail
	}
	return l
}
