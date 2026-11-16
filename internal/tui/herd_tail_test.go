package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/herd"
	"github.com/blsmth/agentctl/internal/state"
)

func numberedPreview(n int) string {
	ls := make([]string, n)
	for i := range ls {
		ls[i] = fmt.Sprintf("line%02d", i+1)
	}
	ls[n-1] = "status"
	return strings.Join(ls, "\n")
}

func TestRenderAgentDetail_BottomAnchored(t *testing.T) {
	InitStyles()
	r := herd.Row{AgentState: state.AgentState{Name: "a", State: "working"}}
	out := renderAgentDetail(r, numberedPreview(100), "", 80, 30, 0)
	if got := strings.Count(out, "\n") + 1; got > 30 {
		t.Errorf("rendered %d lines, want <= 30", got)
	}
	if !strings.Contains(out, "status") || strings.Contains(out, "line01") {
		t.Errorf("want tail without head; got:\n%s", out)
	}
	if !strings.Contains(out, "State:") {
		t.Errorf("header missing")
	}
	up := renderAgentDetail(r, numberedPreview(100), "", 80, 30, 5)
	if strings.Contains(up, "status") || !strings.Contains(up, "line95") {
		t.Errorf("scroll up should reveal older lines; got:\n%s", up)
	}
}

func TestRenderAgentDetail_ShortPreviewUnchanged(t *testing.T) {
	InitStyles()
	r := herd.Row{AgentState: state.AgentState{Name: "a"}}
	out := renderAgentDetail(r, "one\ntwo\nthree", "", 80, 50, 0)
	if !strings.HasSuffix(out, "one\ntwo\nthree") {
		t.Errorf("short preview altered:\n%s", out)
	}
}

func TestPreviewLines_StripsBrailleBlank(t *testing.T) {
	pad := strings.Repeat("⠀", 200)
	for name, in := range map[string]string{
		"plain": pad + "model: x",
		"sgr":   "\x1b[2m" + pad + "\x1b[0m\x1b[36mmodel: x\x1b[0m",
	} {
		t.Run(name, func(t *testing.T) {
			got := strings.Join(previewLines(in), "\n")
			if strings.ContainsRune(got, '⠀') {
				t.Fatal("U+2800 not stripped")
			}
			if s := ansi.Strip(truncateToWidth(got, 20)); s != "model: x" {
				t.Errorf("got %q", s)
			}
		})
	}
	// Other braille glyphs (spinners) survive.
	if got := previewLines("⠋ thinking"); got[0] != "⠋ thinking" {
		t.Errorf("spinner altered: %q", got[0])
	}
}

func TestHerdModel_SelectionChangeResetsPreviewUp(t *testing.T) {
	m := &HerdModel{}
	m.lines = []herdLine{
		{kind: herdLineRow, row: herd.Row{Session: "s", AgentState: state.AgentState{WindowID: "@1"}}},
		{kind: herdLineRow, row: herd.Row{Session: "s", AgentState: state.AgentState{WindowID: "@2"}}},
	}
	m.splitPane.SetListContent("a\nb", 2)
	m.syncSelectionFromCursor()
	m.previewUp = 7
	m.syncSelectionFromCursor()
	if m.previewUp != 7 {
		t.Fatalf("same selection must keep scroll, got %d", m.previewUp)
	}
	m.splitPane.SetCursor(1)
	m.syncSelectionFromCursor()
	if m.previewUp != 0 {
		t.Errorf("previewUp = %d, want 0 after selection change", m.previewUp)
	}
}
