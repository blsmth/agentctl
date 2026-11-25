package hooks

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/attention"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/logstream"
	"github.com/blsmth/agentctl/internal/notify"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/textutil"
	"github.com/blsmth/agentctl/internal/verdicts"
	"github.com/blsmth/agentctl/internal/windowstate"
	"github.com/blsmth/agentctl/internal/winname"
)

// attentionNotificationType is the alert type of a window that needs the
// human (the ! state). Gated by alert_config.blocked, like a blocked window.
const attentionNotificationType = "attention"

// turnEnd is what a Stop hook knows when it decides a window's attention.
type turnEnd struct {
	// Autonomous: a dispatched worker (dispatch, adhoc, review-pr, triage)
	// whose final text goes to its orchestrator, not the human, so it never
	// raises a question.
	Autonomous   bool
	Text         string // the turn's final assistant text
	ToolFailures int    // consecutive failed tool calls at the turn end
	LastError    string // the last tool failure ("Bash: exit 1")
	Verdict      *windowstate.PendingVerdict
	// Missed is an approval popup that timed out unanswered. It raises an
	// attention even for an autonomous window: the human missed it.
	Missed *windowstate.MissedApproval
}

// turnEndAttention decides the attention a turn end raises, or nil. A pending
// review verdict wins (it names a PR), then a missed approval, then a
// question to the human, then a failure streak of
// windowstate.AttentionToolFailures or more.
//
// A window with working children still raises a question: the question is
// the window's own text addressed to the human, and an orchestrator that is
// only waiting on a child ends its turn with a statement ("I'll wait for the
// reviewer"), not a question.
func turnEndAttention(in turnEnd, now time.Time) *windowstate.Attention {
	if in.Verdict != nil {
		return verdictAttention(*in.Verdict, now)
	}
	if in.Missed != nil {
		return missedApprovalAttention(*in.Missed, now)
	}
	if !in.Autonomous {
		if d, ok := attention.Question(in.Text); ok {
			return &windowstate.Attention{Reason: windowstate.AttentionQuestion, Detail: d, At: now}
		}
	}
	if in.ToolFailures >= windowstate.AttentionToolFailures {
		d := fmt.Sprintf("%d tool failures in a row", in.ToolFailures)
		if in.LastError != "" {
			d += ": " + textutil.OneLine(in.LastError)
		}
		return &windowstate.Attention{Reason: windowstate.AttentionErrors, Detail: textutil.Truncate(d, attention.MaxDetailRunes, "…"), At: now}
	}
	return nil
}

// verdictAttention is the "message" attention for a pending verdict.
func verdictAttention(v windowstate.PendingVerdict, now time.Time) *windowstate.Attention {
	return &windowstate.Attention{
		Reason: windowstate.AttentionMessage,
		Detail: fmt.Sprintf("PR #%d review: %s", v.PR, v.Verdict),
		At:     now,
	}
}

// missedApprovalAttention is the "approval" attention for a missed approval:
// "<kind>: <detail>", shown after the "missed approval" label.
func missedApprovalAttention(m windowstate.MissedApproval, now time.Time) *windowstate.Attention {
	d := m.Kind
	if m.Detail != "" {
		if d != "" {
			d += ": "
		}
		d += m.Detail
	}
	return &windowstate.Attention{
		Reason: windowstate.AttentionApproval,
		Detail: textutil.Truncate(d, attention.MaxDetailRunes, "…"),
		At:     now,
	}
}

// peerPromptPrefixes start a UserPromptSubmit prompt that no human typed: a
// message from another Claude session, or a background task's notice.
var peerPromptPrefixes = []string{
	"Another Claude session sent a message",
	"<cross-session-message",
	"<task-notification",
}

// isHumanPrompt reports whether prompt was typed by the human, as opposed to
// a SendMessage delivery or a task notification. Only a human prompt answers
// a pending verdict or a missed approval.
func isHumanPrompt(prompt string) bool {
	p := strings.TrimSpace(prompt)
	for _, pre := range peerPromptPrefixes {
		if strings.HasPrefix(p, pre) {
			return false
		}
	}
	return true
}

// transcriptTailBytes is how much of a transcript lastAssistantText reads.
const transcriptTailBytes = 256 << 10

// lastAssistantText returns the text of the last assistant entry of the
// transcript at path that has any, read through logstream (which normalizes
// other harnesses' records), stopping at the last human prompt. It is the
// fallback when a Stop payload carries no last_assistant_message; "" when
// unknown.
func lastAssistantText(path string) string {
	if path == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close() //nolint:errcheck
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	off := max(st.Size()-transcriptTailBytes, 0)
	data, err := io.ReadAll(io.NewSectionReader(f, off, st.Size()-off))
	if err != nil {
		return ""
	}
	lines := bytes.Split(data, []byte("\n"))
	if off > 0 && len(lines) > 0 {
		lines = lines[1:] // a partial first line
	}
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 {
			continue
		}
		e := logstream.ParseLine(line)
		switch e.Type {
		case "assistant":
			var parts []string
			for _, b := range e.Content {
				if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
					parts = append(parts, b.Text)
				}
			}
			if len(parts) > 0 {
				return strings.Join(parts, "\n\n")
			}
		case "user":
			if isPromptEntry(e) {
				return ""
			}
		}
	}
	return ""
}

// isPromptEntry reports a user entry that is a prompt (string content), not
// a tool result.
func isPromptEntry(e logstream.Entry) bool {
	msg, _ := e.Raw["message"].(map[string]any)
	_, isString := msg["content"].(string)
	return isString
}

// alertAttention sends the desktop notification for a newly raised attention.
func alertAttention(windowID, agentName string, ws windowstate.WindowState, a *windowstate.Attention) {
	if a == nil {
		return
	}
	title, titled := alertTitle(ws, agentName, windowID)
	sendAlert(agentName, attentionNotificationType, notify.Options{
		Title:    title,
		Subtitle: alertSubtitle(attentionEvent(a.Reason), titled, agentName, paths.DirName(), alertHandle(ws)),
		Message:  alertMessage(a.Detail),
		Env: map[string]string{
			"AGENTCTL_AGENT_NAME":        agentName,
			"AGENTCTL_NOTIFICATION_TYPE": attentionNotificationType,
			"AGENTCTL_ATTENTION_REASON":  a.Reason,
			"AGENTCTL_WINDOW_ID":         windowID,
		},
	})
}

// attentionEvent is the notification subtitle's event for a reason.
func attentionEvent(reason string) string {
	switch reason {
	case windowstate.AttentionQuestion:
		return "Asks you something"
	case windowstate.AttentionErrors:
		return "Looks stuck"
	case windowstate.AttentionMessage:
		return "Review needs you"
	case windowstate.AttentionApproval:
		return "Missed an approval"
	}
	return "Needs you"
}

// sameAttention reports whether a and b are the same raised attention, so a
// repeated write does not notify twice.
func sameAttention(a, b *windowstate.Attention) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Reason == b.Reason && a.Detail == b.Detail
}

// updateVerdictTarget is a seam for the orchestrator write in
// notePendingVerdict.
var updateVerdictTarget = winname.Update

// notePendingVerdict records a BLOCKED or NEEDS-BRENDAN verdict on the
// window that dispatched the reviewer (its orchestrator, matched by window
// and session), and lifts it again on a later MERGE verdict for the same PR.
// An orchestrator already idle gets its attention at once; otherwise its next
// turn end raises it.
func notePendingVerdict(all map[string]windowstate.WindowState, reviewer string, pr int, verdict string, now time.Time) {
	r := all[reviewer]
	parent, session := r.SpawnedBy, r.SpawnedBySession
	if parent == "" || session == "" || parent == reviewer {
		return
	}
	p, ok := all[parent]
	if !ok || p.SessionID != session {
		return
	}
	needs := verdict == verdicts.Blocked || verdict == verdicts.NeedsBrendan
	if !needs && (p.PendingVerdict == nil || p.PendingVerdict.PR != pr) {
		return
	}
	var raised *windowstate.Attention
	var snap windowstate.WindowState
	err := updateVerdictTarget(parent, func(w *windowstate.WindowState) {
		if w.SessionID != session {
			return
		}
		if !needs {
			if w.PendingVerdict != nil && w.PendingVerdict.PR == pr {
				w.PendingVerdict = nil
				if w.Attention != nil && w.Attention.Reason == windowstate.AttentionMessage {
					w.Attention = nil
				}
			}
			return
		}
		v := windowstate.PendingVerdict{PR: pr, Verdict: verdict, At: now}
		if w.State == "idle" || w.State == "" {
			a := verdictAttention(v, now)
			if !sameAttention(w.Attention, a) {
				raised = a
				v.Alerted = true // the turn-end raise of this verdict stays quiet
			}
			w.Attention = a
		}
		w.PendingVerdict = &v
		snap = *w
	})
	if err != nil {
		logUpdateErr("hook-post-tool", err)
		return
	}
	if raised != nil {
		alertAttention(parent, snap.AgentName, snap, raised)
	}
	logging.Log("hook-post-tool: verdict %s for PR #%d noted on orchestrator window=%s", verdict, pr, parent)
}
