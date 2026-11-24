package panesim

import (
	"strings"

	"github.com/blsmth/agentctl/internal/panestate"
)

// PressTrust returns pane after key on Claude Code's folder-trust dialog, as
// a scratch run of Claude Code 2.1.328 showed it: Down and Up move the ❯
// between its two rows and wrap (Up on the top row lands on the bottom one),
// Enter answers the row under the cursor and Escape answers "exit". answered
// is panestate.TrustKeyTrust or TrustKeyExit once the dialog is answered (the
// pane is then unchanged and the caller shows what comes next), else "".
// The rows are redrawn without ANSI.
func PressTrust(pane, key string) (next, answered string) {
	lines := strings.Split(pane, "\n")
	var rows []int
	cursor := -1
	for i, l := range lines {
		plain := strings.TrimSpace(panestate.StripANSI(l))
		if strings.HasSuffix(plain, "No, exit") || strings.HasSuffix(plain, "Yes, I trust this folder") {
			if strings.HasPrefix(plain, "❯") {
				cursor = len(rows)
			}
			rows = append(rows, i)
		}
	}
	if len(rows) != 2 || cursor < 0 {
		return pane, ""
	}
	switch key {
	case "Down", "Up":
		next := (cursor + 1) % 2 // two rows: either arrow lands on the other
		for i, row := range rows {
			label := strings.TrimLeft(strings.TrimSpace(panestate.StripANSI(lines[row])), "❯ ")
			lines[row] = "   " + label
			if i == next {
				lines[row] = " ❯ " + label
			}
		}
		return strings.Join(lines, "\n"), ""
	case "Enter":
		if strings.Contains(lines[rows[cursor]], "Yes, I trust") {
			return pane, panestate.TrustKeyTrust
		}
		return pane, panestate.TrustKeyExit
	case "Escape":
		return pane, panestate.TrustKeyExit
	}
	return pane, ""
}
