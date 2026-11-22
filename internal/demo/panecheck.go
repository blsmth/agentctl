package demo

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// paneMarker is one structural feature of a Claude Code pane. Only the
// feature's presence is ever recorded, never the pane's text.
type paneMarker struct {
	Name  string
	Found func(plain string) bool
}

var paneMarkers = []paneMarker{
	{"banner glyphs (▐▛███▜▌)", func(s string) bool {
		return strings.Contains(s, "▐▛███▜▌") && strings.Contains(s, "▝▜█████▛▘")
	}},
	{"prompt (\"❯ \")", func(s string) bool { return hasLinePrefix(s, "❯ ") }},
	{"assistant bullet (\"⏺ \")", func(s string) bool { return hasLinePrefix(s, "⏺ ") }},
	{"result marker (⎿)", func(s string) bool { return strings.Contains(s, "⎿") }},
	{"input rules (────)", func(s string) bool { return strings.Contains(s, strings.Repeat("─", 20)) }},
	{"mode line (⏵⏵)", func(s string) bool { return strings.Contains(s, "⏵⏵") }},
	{"turn footer (✻)", func(s string) bool { return hasLinePrefix(s, "✻ ") }},
}

func hasLinePrefix(s, prefix string) bool {
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimLeft(l, " "), prefix) {
			return true
		}
	}
	return false
}

// PaneMarkers returns the names of the structural markers present in pane
// text (ANSI is stripped first).
func PaneMarkers(text string) []string {
	plain := ansi.Strip(strings.ReplaceAll(text, "\r\n", "\n"))
	var out []string
	for _, m := range paneMarkers {
		if m.Found(plain) {
			out = append(out, m.Name)
		}
	}
	return out
}

// MarkerDiff compares the markers of a real pane with the fake's: onlyReal
// are present in the real pane only, onlyFake in the fake only.
func MarkerDiff(real, fake []string) (onlyReal, onlyFake []string) {
	in := func(list []string, s string) bool {
		for _, x := range list {
			if x == s {
				return true
			}
		}
		return false
	}
	for _, m := range real {
		if !in(fake, m) {
			onlyReal = append(onlyReal, m)
		}
	}
	for _, m := range fake {
		if !in(real, m) {
			onlyFake = append(onlyFake, m)
		}
	}
	return onlyReal, onlyFake
}

// referencePane is a freshly drawn fake pane that exercises every marker: a
// finished turn with a tool call, an auto-mode input area and a footer.
func referencePane() string {
	spec := PaneSpec{
		Window: Window{State: "idle", Mode: "dispatched", Model: "claude-opus-5-5"},
		Transcript: Transcript{Entries: []Entry{
			{User: "Fix the thing."},
			{Tool: "Bash", Input: map[string]any{"command": "go test ./..."}, Result: "ok"},
			{Say: "Done."},
		}},
		WorkDir: "/work/acme-api",
	}
	return Render(spec, 120, "", nil)
}

// CheckPane captures the real Claude Code pane at target (default tmux
// server) and returns how its structural markers differ from the fake pane's.
// The real pane's text is analysed in memory and discarded.
func CheckPane(target string) (onlyReal, onlyFake []string, err error) {
	out, err := exec.Command("tmux", "capture-pane", "-p", "-t", target).Output()
	if err != nil {
		return nil, nil, fmt.Errorf("capturing pane %q: %w", target, err)
	}
	onlyReal, onlyFake = MarkerDiff(PaneMarkers(string(out)), PaneMarkers(referencePane()))
	return onlyReal, onlyFake, nil
}
