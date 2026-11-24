package hooks

import (
	"fmt"
	"strings"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/textutil"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// maxAlertMessageRunes is the budget of a notification message line.
const maxAlertMessageRunes = 120

// alertTitle is the notification title for a window: its row title (the short
// title, manual or generated) when set, else the display name. titled reports
// which, so the subtitle can name the agent only when the title does not.
//
// The title must not carry durations or tool names: a notify_cmd using
// `--group {{title}}` relies on it staying the same across repeat firings.
// The short title changes at most once per window, then freezes.
func alertTitle(ws windowstate.WindowState, agentName, windowID string) (title string, titled bool) {
	if t := textutil.OneLine(ws.ShortTitle.Title); t != "" {
		return t, true
	}
	name := fleet.DisplayName(ws.Instance, agentName, windowID)
	if name == windowID {
		name = "session" // never show a raw window ID to a person
	}
	return name, false
}

// alertSubtitle joins the event with the agent (only when the title is the
// row title, which does not name it) and the project: "Needs permission ·
// go-engineer · agentctl · a3f9" (the last part is the session handle).
func alertSubtitle(event string, titled bool, agentName, project, handle string) string {
	parts := []string{event}
	if titled && agentName != "" {
		parts = append(parts, agentName)
	}
	if project != "" {
		parts = append(parts, project)
	}
	if handle != "" {
		parts = append(parts, handle)
	}
	return strings.Join(parts, " · ")
}

// alertMessage flattens s to one line and cuts it at a word boundary.
func alertMessage(s string) string {
	return fleet.TruncateWords(textutil.OneLine(s), maxAlertMessageRunes)
}

// upperFirst upper-cases the first rune of s.
func upperFirst(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

// prReady names the PR recorded for the window ("PR #273 ready"), or "".
func prReady(ws windowstate.WindowState) string {
	switch {
	case ws.PRNumber > 0:
		return fmt.Sprintf("PR #%d ready", ws.PRNumber)
	case ws.PRURL != "":
		return "PR ready"
	}
	return ""
}

// approvalMessage says what is being approved: the action, plus its detail
// (the command, the job and schedule, the dispatch prompt's first line)
// unless the action already says it.
func approvalMessage(n popupNotice) string {
	action, detail := textutil.OneLine(n.Action), textutil.OneLine(n.Detail)
	switch {
	case action == "":
		return alertMessage(detail)
	case detail == "" || strings.Contains(action, detail):
		return alertMessage(upperFirst(action))
	}
	if head, rest, ok := strings.Cut(detail, ": "); ok && strings.HasSuffix(action, head) {
		return alertMessage(upperFirst(action) + ": " + rest)
	}
	return alertMessage(upperFirst(action) + ": " + detail)
}
