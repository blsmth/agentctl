package mailbox

import (
	"fmt"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/logging"
)

// DefaultTTL is how long a pending message may sit undelivered before
// DrainFor discards it instead of delivering it. Delivery only recently
// started working (see docs/agent-messaging-design.md §3); without an
// expiry, the first tool call or prompt after this change ships flips every
// stale message that piled up while delivery was silently broken straight
// into whatever window now owns that ID, potentially days-old and
// completely irrelevant to the agent currently running there.
const DefaultTTL = 1 * time.Hour

// maxDeliverBodyLen caps the coalesced body DrainFor returns. Anthropic's
// docs describe a 10,000-character cap on hook output strings
// (additionalContext included) but that has not been independently
// verified here, so this stays conservative rather than riding the
// documented limit exactly.
const maxDeliverBodyLen = 8000

// truncatedMarker is appended when a coalesced body exceeds maxDeliverBodyLen.
const truncatedMarker = "\n\n[truncated]"

// DrainFor is the single entry point for in-band mailbox delivery (see
// docs/agent-messaging-design.md §3). It claims and coalesces windowID's
// deliverable pending messages into one body, ready to be handed to a
// caller that will inject it as hook additionalContext.
//
// Callers pass sessionID from the window's live windowstate.SessionID.
// ttl is normally DefaultTTL; callers may pass a different value in tests.
//
// Returns ("", 0) when there is nothing to deliver. That path costs exactly
// one os.ReadDir (via Pending) on a directory that, for the overwhelming
// majority of hook invocations, does not exist -- this function runs on
// every single tool call and prompt submission, so it must stay cheap when
// idle.
//
// Every message returned by Pending is resolved to exactly one of three
// outcomes before this function returns:
//   - discarded for a session mismatch (moved to read/, never delivered)
//   - discarded for TTL expiry (moved to read/, never delivered)
//   - claimed for delivery (moved to read/ BEFORE the body is returned --
//     see the doc comment below on why claim-before-return is load-bearing)
func DrainFor(windowID, sessionID string, ttl time.Duration) (body string, n int) {
	pending, err := Pending(windowID)
	if err != nil {
		logging.Warn("mailbox: DrainFor(%s): Pending: %v", windowID, err)
		return "", 0
	}
	if len(pending) == 0 {
		return "", 0
	}

	now := time.Now()
	var deliverable []Message
	for _, msg := range pending {
		// Session check: tmux recycles "@N" window IDs after a window
		// closes, so a message enqueued for a since-recycled window must
		// never reach whoever owns that ID now. An empty ToSession (e.g.
		// `agentctl-originated message enqueued outside a tracked window) or an empty live
		// sessionID (windowstate not yet written) is never treated as a
		// mismatch -- there's nothing to compare, so we don't manufacture a
		// false positive.
		if msg.ToSession != "" && sessionID != "" && msg.ToSession != sessionID {
			logging.Log("mailbox: discard msg=%s window=%s: session mismatch (msg_session=%s window_session=%s); window ID likely recycled onto a different session", msg.ID, windowID, msg.ToSession, sessionID)
			if err := MarkRead(windowID, msg.ID); err != nil {
				logging.Warn("mailbox: MarkRead(%s, %s) after session-mismatch discard: %v", windowID, msg.ID, err)
			}
			continue
		}

		// TTL expiry: see DefaultTTL's doc comment for why this must not be
		// silent -- Warn, not Debug/Log, so discards of stale mail are
		// visible in normal logs rather than requiring debug mode.
		if ttl > 0 && now.Sub(msg.Created) > ttl {
			logging.Warn("mailbox: discard msg=%s window=%s: TTL expired (created=%s age=%s ttl=%s)", msg.ID, windowID, msg.Created.Format(time.RFC3339), now.Sub(msg.Created).Round(time.Second), ttl)
			if err := MarkRead(windowID, msg.ID); err != nil {
				logging.Warn("mailbox: MarkRead(%s, %s) after TTL-expiry discard: %v", windowID, msg.ID, err)
			}
			continue
		}

		deliverable = append(deliverable, msg)
	}
	if len(deliverable) == 0 {
		return "", 0
	}

	// Claim before returning: mark every delivered message read BEFORE the
	// caller ever writes it to stdout. The rename to read/ is the atomic
	// "claim". PostToolUse fires on every subsequent tool call, so without
	// claim-first the same message would be re-delivered on every tool call
	// until the agent happens to stop -- there is no other point at which
	// "this was delivered" gets recorded.
	for _, msg := range deliverable {
		if err := MarkRead(windowID, msg.ID); err != nil {
			logging.Warn("mailbox: MarkRead(%s, %s) after claiming for delivery: %v", windowID, msg.ID, err)
		}
	}

	body = truncateBody(formatCoalescedMessages(deliverable))
	return body, len(deliverable)
}

// truncateBody caps s at maxDeliverBodyLen, appending an explicit marker so
// truncation is never silent.
//
// The cut point is found by walking runes (not bytes): message bodies
// routinely contain multi-byte UTF-8 (em-dashes, etc.), and a byte-index
// slice (s[:cut]) can land mid-rune. That used to produce invalid UTF-8
// silently -- json.Marshal does not error on invalid UTF-8, it just
// replaces the broken bytes with U+FFFD ("�") -- so the delivered
// message ended in mojibake with no error anywhere in the chain.
func truncateBody(s string) string {
	if len(s) <= maxDeliverBodyLen {
		return s
	}
	limit := maxDeliverBodyLen - len(truncatedMarker)
	if limit < 0 {
		limit = 0
	}
	cut := 0
	for i := range s {
		if i > limit {
			break
		}
		cut = i
	}
	return s[:cut] + truncatedMarker
}

// formatCoalescedMessages combines msgs into a single injectable body
// (docs/agent-messaging-design.md §3: "Coalesce ... deliver all pending
// messages for one window as a single injection, not one per message").
// The header names the agentctl-originated notice and its sender so the receiving
// agent can tell them apart. msgs must be non-empty.
func formatCoalescedMessages(msgs []Message) string {
	var b strings.Builder
	if len(msgs) == 1 {
		fmt.Fprintf(&b, "[agentctl notice from %s]\n\n", displayFrom(msgs[0].From))
		b.WriteString(msgs[0].Body)
		return b.String()
	}

	fmt.Fprintf(&b, "[agentctl: %d notices]\n", len(msgs))
	for i, msg := range msgs {
		fmt.Fprintf(&b, "\n--- notice %d/%d from %s ---\n", i+1, len(msgs), displayFrom(msg.From))
		b.WriteString(msg.Body)
	}
	return b.String()
}

// displayFrom returns from, or a placeholder for a message enqueued without
// a sender.
func displayFrom(from string) string {
	if from == "" {
		return "unknown"
	}
	return from
}
