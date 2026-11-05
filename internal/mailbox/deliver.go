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
	body, ids := DrainForIDs(windowID, sessionID, ttl)
	return body, len(ids)
}

// DrainForIDs is DrainFor plus the IDs of every message claimed for
// delivery, in delivery order. `agentctl wake` needs those IDs: if the
// tmux injection it performs after draining fails, the mail is already
// claimed (see the claim-before-return note below) and the only way to
// recover it by hand is a log line naming exactly which messages were lost.
// DrainFor itself has no such recovery path to worry about -- its caller is
// a hook writing straight to its own stdout, which cannot "fail to deliver"
// the way a separate tmux send-keys call can.
func DrainForIDs(windowID, sessionID string, ttl time.Duration) (body string, ids []string) {
	pending, err := Pending(windowID)
	if err != nil {
		logging.Warn("mailbox: DrainFor(%s): Pending: %v", windowID, err)
		return "", nil
	}
	if len(pending) == 0 {
		return "", nil
	}

	now := time.Now()
	var deliverable []Message
	for _, msg := range pending {
		// Session check: tmux recycles "@N" window IDs after a window
		// closes, so a message enqueued for a since-recycled window must
		// never reach whoever owns that ID now. An empty ToSession (e.g.
		// `agentctl send` run outside a tracked window) or an empty live
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
		return "", nil
	}

	// Claim before returning: mark every delivered message read BEFORE the
	// caller ever writes it to stdout. The rename to read/ is the atomic
	// "claim". PostToolUse fires on every subsequent tool call, so without
	// claim-first the same message would be re-delivered on every tool call
	// until the agent happens to stop -- there is no other point at which
	// "this was delivered" gets recorded.
	ids = make([]string, 0, len(deliverable))
	for _, msg := range deliverable {
		if err := MarkRead(windowID, msg.ID); err != nil {
			logging.Warn("mailbox: MarkRead(%s, %s) after claiming for delivery: %v", windowID, msg.ID, err)
		}
		ids = append(ids, msg.ID)
	}

	body = truncateBody(formatCoalescedMessages(deliverable))
	return body, ids
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
// Each message is clearly delimited and shows its sender so the receiving
// agent can tell them apart. msgs must be non-empty.
func formatCoalescedMessages(msgs []Message) string {
	var b strings.Builder
	if len(msgs) == 1 {
		msg := msgs[0]
		fmt.Fprintf(&b, "[agentctl message from %s]", displayFrom(msg.From))
		if msg.Subject != "" {
			fmt.Fprintf(&b, " %s", msg.Subject)
		}
		b.WriteString("\n\n")
		b.WriteString(msg.Body)
		return b.String()
	}

	fmt.Fprintf(&b, "[agentctl: %d messages received]\n", len(msgs))
	for i, msg := range msgs {
		fmt.Fprintf(&b, "\n--- message %d/%d from %s", i+1, len(msgs), displayFrom(msg.From))
		if msg.Subject != "" {
			fmt.Fprintf(&b, ": %s", msg.Subject)
		}
		b.WriteString(" ---\n")
		b.WriteString(msg.Body)
	}
	return b.String()
}

// displayFrom returns from, or a placeholder for a message enqueued without
// a sender (e.g. `agentctl send` run outside a tmux window).
func displayFrom(from string) string {
	if from == "" {
		return "unknown"
	}
	return from
}
