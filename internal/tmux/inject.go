package tmux

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/panestate"
)

// pasteSettleDelay is how long InjectPromptToWindow waits between sending
// literal content via `tmux send-keys -l` and sending the follow-up Enter
// that submits it.
//
// `send-keys -l` writes the entire payload to the pty in one shot. For
// multi-line content, Claude Code's input handling detects that burst of
// text -- several newlines arriving with no human-typing gaps between them
// -- and buffers it into a bracketed-paste attachment ("[Pasted text #1
// +N lines]") rather than treating each line as a submitted prompt. An
// Enter that arrives while that buffering window is still open gets
// absorbed into the paste instead of submitting it, which is exactly the
// silent-staging bug this file exists to fix. Waiting here gives the paste
// time to settle before Enter is sent, so Enter is recognized as the
// deliberate submit keystroke it is.
//
// A package-level var, not a const, so tests can shrink it and avoid
// sleeping in real wall-clock time through the retry loop below.
var pasteSettleDelay = 150 * time.Millisecond

// injectVerifyDelay is how long InjectPromptToWindow waits after sending an
// Enter before capturing the pane to check whether it submitted. Claude
// Code's render loop is not instantaneous; capturing immediately after
// send-keys can observe a stale frame.
//
// A package-level var for the same test-speed reason as pasteSettleDelay.
var injectVerifyDelay = 200 * time.Millisecond

// injectMaxRetries bounds how many additional Enters InjectPromptToWindow
// will send if the pane capture still shows the content staged
// (unsubmitted) in the input box. Bounded so a genuinely broken pane (dead
// process, wedged UI) fails loudly after a short, fixed window instead of
// retrying forever and never returning.
const injectMaxRetries = 3

// promptGlyph is the prompt glyph Claude Code renders at the start of its
// input box. Staged, unsubmitted content -- literal text or a bracketed-paste
// placeholder like "[Pasted text #1 +6 lines]" -- renders on the same line.
// tmux may trim the trailing space of an empty box, so no space is included.
const promptGlyph = "❯"

// promptBoxBorder is the set of box-drawing and whitespace characters that
// can trail real content on the prompt-marker line (the input box's right
// border, and the padding tmux pads capture-pane output with). Trimmed off
// before deciding whether anything meaningful follows the marker.
const promptBoxBorder = "│╮╯╭╰─ \t"

// Seams for testing -- these shell out to real tmux in production; tests
// substitute fakes so the retry/verification loop can be exercised without
// a live tmux session. Mirrors the clientDimensions pattern in
// internal/hooks/popup_clamp.go.
var (
	injectSendLiteral = tmuxSendKeysLiteral
	injectSendEnter   = tmuxSendKeysEnter
	injectCapturePane = tmuxCaptureMainPane
)

// tmuxSendKeysLiteral sends content to paneID literally (no tmux key-name
// lookup, no shell interpretation of its bytes).
func tmuxSendKeysLiteral(paneID, content string) error {
	return exec.Command("tmux", "send-keys", "-t", paneID, "-l", content).Run()
}

// tmuxSendKeysEnter sends a single Enter keypress to paneID.
func tmuxSendKeysEnter(paneID string) error {
	return exec.Command("tmux", "send-keys", "-t", paneID, "Enter").Run()
}

// tmuxCaptureMainPane captures paneID's FULL current on-screen contents (no
// scrollback, no line cap -- a bounded tail could cut a dialog off and cause
// a false negative, which here means an unreviewed approval), so a paste marker or prompt text from an
// already-submitted, already-scrolled-off earlier turn cannot be mistaken
// for still-staged content.
func tmuxCaptureMainPane(paneID string) (string, error) {
	out, err := exec.Command("tmux", "capture-pane", "-p", "-t", paneID).Output()
	if err != nil {
		return "", fmt.Errorf("capture-pane %s: %w", paneID, err)
	}
	return string(out), nil
}

// InjectPromptToAgent sends a text message to a running agent's main pane,
// as if the operator had typed it and pressed Enter. The agent name is matched
// against running windows (case-insensitive, stripping instance suffixes).
//
// Returns an error if the agent is not found or the send fails, including
// when InjectPromptToWindow cannot confirm the content was actually
// submitted (see its doc comment).
func InjectPromptToAgent(agentName, content string) error {
	agents := RunningAgents()
	var windowID string
	for id, name := range agents {
		if strings.EqualFold(BaseAgentName(name), BaseAgentName(agentName)) {
			windowID = id
			break
		}
	}
	if windowID == "" {
		return fmt.Errorf("agent %q is not running in the agentctl session", agentName)
	}

	if err := InjectPromptToWindow(windowID, content); err != nil {
		return fmt.Errorf("agent %q: %w", agentName, err)
	}
	return nil
}

// InjectPromptToWindow sends content into windowID's main pane exactly as if
// the operator had typed it, then presses Enter, and VERIFIES the content
// actually submitted before returning success.
//
// Verification exists because of a real, observed failure mode: for
// multi-line content, Claude Code stages the pasted text as a
// "[Pasted text #1 +N lines]" attachment (see pasteSettleDelay's doc
// comment), and an Enter sent immediately afterward can be absorbed into
// that paste instead of submitting it. Both send-keys calls still succeed
// in that case -- there is no tmux-level error -- so a caller that trusts
// "no error" as "delivered" is wrong. This function instead captures the
// pane after sending Enter and checks whether the input box still shows
// staged content; if so, it retries the Enter a bounded number of times,
// and only returns success once a capture confirms the box emptied out.
// If it is still staged after the retry budget, it returns an error so
// callers can treat the content as undelivered (and, for mailbox delivery,
// log the full body for manual recovery -- see internal/cmd/wake.go).
//
// Unlike InjectPromptToAgent, this addresses an exact tmux window ID rather
// than resolving an ambiguous agent name to whichever running window happens
// to match first. Callers that already hold a concrete window ID -- notably
// mailbox delivery, where the address is a (windowID, sessionID) pair by
// design (see docs/agent-messaging-design.md §1) -- must use this, not
// InjectPromptToAgent's arbitrary-first-match name lookup.
func InjectPromptToWindow(windowID, content string) error {
	mainPane := findMainPane(windowID)
	if mainPane == "" {
		return fmt.Errorf("could not find main pane for window %s", windowID)
	}

	if err := sendAndVerifySubmission(mainPane, content); err != nil {
		return fmt.Errorf("window %s: %w", windowID, err)
	}
	return nil
}

// ErrDialogBlocking is returned (wrapped) when a permission/confirmation
// dialog is on screen in the target pane. Injecting then would type into the
// dialog and the Enter would hit its focused option -- approving a tool call
// no human reviewed. Callers should defer delivery, not retry blindly.
var ErrDialogBlocking = errors.New("permission dialog is showing in the target pane")

// ErrCannotVerify is returned (wrapped) when the pane's state cannot be
// established well enough to know that sending keys is safe, or that a send
// landed. Delivery is treated as FAILED, never as success.
var ErrCannotVerify = errors.New("could not establish pane state")

// checkInjectable is the fail-CLOSED gate in front of every keystroke sent to
// a pane. It returns nil only when the full visible pane is positively
// classified as an agent at a prompt (idle, or busy -- Claude Code queues
// input typed mid-turn). A dialog, an unclassifiable pane, an empty capture,
// and a capture error all refuse.
//
// The error directions are asymmetric: refusing a healthy pane defers a
// delivery (recoverable); sending into a dialog approves an unreviewed tool
// call. Hence the allowlist shape -- uncertainty falls through to refusal
// rather than to a send.
func checkInjectable(paneID string) error {
	captured, err := injectCapturePane(paneID)
	if err != nil {
		return fmt.Errorf("%w: pane %s: %v", ErrCannotVerify, paneID, err)
	}
	switch panestate.Classify(captured) {
	case panestate.Idle, panestate.Busy:
		return nil
	case panestate.AwaitingInput:
		return fmt.Errorf("%w (pane %s)", ErrDialogBlocking, paneID)
	default:
		return fmt.Errorf("%w: pane %s is empty or unrecognised", ErrCannotVerify, paneID)
	}
}

// CheckWindowInjectable reports whether it is currently safe to inject into
// windowID's main pane, without sending anything. Callers that irreversibly
// claim state before injecting (mailbox drain) use it to defer instead.
func CheckWindowInjectable(windowID string) error {
	mainPane := findMainPane(windowID)
	if mainPane == "" {
		return fmt.Errorf("could not find main pane for window %s", windowID)
	}
	return checkInjectable(mainPane)
}

// guardedLiteral and guardedEnter are the ONLY call sites of the raw send
// seams: each re-checks the pane immediately before sending, so no path
// reaches send-keys without passing checkInjectable. A dialog can appear
// between any two steps (e.g. a tool call finishing while content settles).
func guardedLiteral(paneID, content string) error {
	if err := checkInjectable(paneID); err != nil {
		return err
	}
	return injectSendLiteral(paneID, content)
}

func guardedEnter(paneID string) error {
	if err := checkInjectable(paneID); err != nil {
		return err
	}
	return injectSendEnter(paneID)
}

// submission is the tri-state outcome of verifying a send.
type submission int

const (
	// submissionConfirmed: the input box emptied; the content went through.
	submissionConfirmed submission = iota
	// submissionStaged: content is still sitting unsubmitted in the input box.
	submissionStaged
	// submissionUnverifiable: the pane shows a dialog, or is empty or
	// unrecognised. The absence of a staged "❯ " line here does NOT mean
	// success -- a dialog swallows input and hides the input box entirely.
	submissionUnverifiable
)

// verifySubmission classifies a post-Enter capture. Only a positively
// recognised prompt/busy pane with an empty input box counts as confirmed.
func verifySubmission(captured string) submission {
	switch panestate.Classify(captured) {
	case panestate.Idle, panestate.Busy:
		if promptStillStaged(captured) {
			return submissionStaged
		}
		return submissionConfirmed
	default:
		return submissionUnverifiable
	}
}

// sendAndVerifySubmission does the actual literal-send, settle-delay,
// Enter, and verify-or-retry work for InjectPromptToWindow, addressed at an
// already-resolved paneID. Split out from InjectPromptToWindow so tests can
// exercise the loop through the injectSendLiteral / injectSendEnter /
// injectCapturePane seams without needing a real tmux window.
//
// Every send is gated by checkInjectable (fail closed), and an unverifiable
// outcome is a delivery failure, not a success.
func sendAndVerifySubmission(paneID, content string) error {
	if err := guardedLiteral(paneID, content); err != nil {
		return fmt.Errorf("send content: %w", err)
	}

	time.Sleep(pasteSettleDelay)

	for attempt := 0; ; attempt++ {
		if err := guardedEnter(paneID); err != nil {
			return fmt.Errorf("send enter: %w", err)
		}

		time.Sleep(injectVerifyDelay)

		captured, err := injectCapturePane(paneID)
		if err != nil {
			return fmt.Errorf("%w: pane %s after Enter: %v", ErrCannotVerify, paneID, err)
		}
		switch verifySubmission(captured) {
		case submissionConfirmed:
			return nil
		case submissionUnverifiable:
			if panestate.DialogShowing(captured) {
				return fmt.Errorf("%w after Enter: message may have been swallowed", ErrDialogBlocking)
			}
			return fmt.Errorf("%w: pane %s after Enter is empty or unrecognised", ErrCannotVerify, paneID)
		}
		if attempt >= injectMaxRetries {
			return fmt.Errorf(
				"content still staged in input box after %d Enter attempt(s) -- submission could not be confirmed",
				attempt+1)
		}
		logging.Warn("sendAndVerifySubmission: pane %s still shows staged content after Enter (attempt %d/%d), retrying",
			paneID, attempt+1, injectMaxRetries)
	}
}

// promptStillStaged is a pure function over a tmux pane capture that
// decides whether Claude Code's input box still holds unsubmitted content.
//
// The transcript-echo trap: Claude Code renders every submitted user message
// in the transcript with the same "❯" glyph as the input box
// ("❯ <message>"). Scanning every marker line therefore reports a message as
// "staged" the instant it is successfully submitted, because it now appears
// in the transcript. So only the real input box is inspected: the LAST
// marker line that is immediately preceded by a horizontal-rule line (a run
// of "─", possibly carrying a label like "──── name ─"). If no
// rule-anchored marker line exists (older side-bordered "│ ❯ ... │" boxes),
// fall back to the last marker line in the capture -- never an earlier one.
//
// The marker is "❯" with an optional trailing space, since tmux may trim the
// trailing space of an empty box. Staged content -- literal text, or a
// bracketed-paste placeholder like "[Pasted text #1 +6 lines]" -- always
// renders on the marker line (multi-line text shows its first line there).
// Text elsewhere in the capture that merely mentions such a placeholder must
// not count as staged. An empty box shows only border/whitespace after the
// marker.
func promptStillStaged(captured string) bool {
	lines := strings.Split(captured, "\n")
	anchored, last := -1, -1
	for i, line := range lines {
		if !strings.Contains(line, promptGlyph) {
			continue
		}
		last = i
		if i > 0 && isRuleLine(lines[i-1]) {
			anchored = i
		}
	}
	idx := anchored
	if idx == -1 {
		idx = last
	}
	if idx == -1 {
		return false
	}
	line := lines[idx]
	after := line[strings.Index(line, promptGlyph)+len(promptGlyph):]
	// Real captures render the box's padding as U+00A0 after the glyph, so
	// treat any Unicode space (and U+2800 padding) as blank, not just ASCII.
	return strings.TrimFunc(after, func(r rune) bool {
		return unicode.IsSpace(r) || r == '\u2800' || strings.ContainsRune(promptBoxBorder, r)
	}) != ""
}

// isRuleLine reports whether line is a horizontal rule: non-space content
// that is mostly "─", optionally with a short label.
func isRuleLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	total := utf8.RuneCountInString(trimmed)
	if total == 0 {
		return false
	}
	rules := strings.Count(trimmed, "─")
	return rules >= 3 && rules*2 >= total
}
