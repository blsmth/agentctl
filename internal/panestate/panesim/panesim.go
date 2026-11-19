// Package panesim simulates how Claude Code redraws a multi-select
// AskUserQuestion tab when a key is pressed, for fakes and tests that must
// answer the server's re-parse after every key. It is test support: no
// production code imports it.
package panesim

import (
	"regexp"
	"strings"

	"github.com/blsmth/agentctl/internal/panestate"
)

var optionRow = regexp.MustCompile(`^(❯\s*)?\d+\.\s`)

// Press returns pane after key on a multi-select tab: a digit toggles that
// option's checkbox ("[ ]" <-> "[✔]") without moving the cursor, Down/Up move
// the ❯ cursor one navigable row (numbered options and the "Next"/"Submit"
// row). submitted reports Enter with the cursor on the Next/Submit row;
// pane is then unchanged and the caller shows whatever comes next. Any other
// key leaves the pane as it was. ANSI in pane is kept.
func Press(pane, key string) (next string, submitted bool) {
	lines := strings.Split(pane, "\n")
	plain := make([]string, len(lines))
	for i, l := range lines {
		plain[i] = strings.TrimSpace(panestate.StripANSI(l))
	}
	switch key {
	case "Down", "Up":
		return move(lines, plain, key == "Down"), false
	case "Enter":
		c := cursorRow(plain)
		return pane, c >= 0 && isConfirm(plain[c])
	}
	for i, p := range plain {
		if !strings.HasPrefix(strings.TrimLeft(p, "❯ "), key+". [") {
			continue
		}
		if strings.Contains(lines[i], "[ ]") {
			lines[i] = strings.Replace(lines[i], "[ ]", "[✔]", 1)
		} else {
			lines[i] = strings.Replace(lines[i], "[✔]", "[ ]", 1)
		}
	}
	return strings.Join(lines, "\n"), false
}

func isConfirm(p string) bool {
	p = strings.TrimSpace(strings.TrimPrefix(p, "❯"))
	return p == "Next" || p == "Submit"
}

func cursorRow(plain []string) int {
	for i, p := range plain {
		if strings.HasPrefix(p, "❯") && (optionRow.MatchString(p) || isConfirm(p)) {
			return i
		}
	}
	return -1
}

// move shifts the cursor to the next navigable row up or down.
func move(lines, plain []string, down bool) string {
	var rows []int
	for i, p := range plain {
		if optionRow.MatchString(p) || isConfirm(p) {
			rows = append(rows, i)
		}
	}
	cur := cursorRow(plain)
	for j, r := range rows {
		if r != cur {
			continue
		}
		k := j - 1
		if down {
			k = j + 1
		}
		if k < 0 || k >= len(rows) {
			break
		}
		lines[cur] = strings.Replace(lines[cur], "❯", " ", 1)
		lines[rows[k]] = strings.Replace(lines[rows[k]], " ", "❯", 1)
		break
	}
	return strings.Join(lines, "\n")
}
