package cmd

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/windowstate"
)

func attentionRow(id, st string) fleet.Row {
	r := row(id, "brandon:1", st, false)
	r.Attention = &windowstate.Attention{Reason: windowstate.AttentionQuestion, Detail: "Merge it?"}
	return r
}

func TestStatusAttention(t *testing.T) {
	idle, working := attentionRow("@1", "idle"), attentionRow("@2", "working")
	if !stateMatches(idle, "attention") || stateMatches(working, "attention") {
		t.Error("--state attention should match the idle ! row only")
	}
	if stateMatches(row("@3", "a", "idle", true), "attention") {
		t.Error("a done row without attention matched --state attention")
	}
	if c := stateCell(idle); c.text != "attention" {
		t.Errorf("stateCell(idle+attention) = %q", c.text)
	}
	if c := stateCell(working); c.text != "working" {
		t.Errorf("stateCell(working+attention) = %q, want working (● wins)", c.text)
	}
	done := idle
	done.Done = true
	if c := stateCell(done); c.text != "attention" {
		t.Errorf("stateCell(done+attention) = %q, want attention (! over ✓)", c.text)
	}
	if c := stateCell(attentionRow("@4", "blocked")); c.text != "blocked" {
		t.Errorf("stateCell(blocked+attention) = %q", c.text)
	}

	j := statusRow{row: idle}.jsonRow()
	if j.Attention != windowstate.AttentionQuestion || j.AttentionDetail != "Merge it?" {
		t.Errorf("json attention = %q %q", j.Attention, j.AttentionDetail)
	}
	if j := (statusRow{row: working}).jsonRow(); j.Attention != "" {
		t.Errorf("working row json attention = %q, want omitted", j.Attention)
	}
}

func TestPrintAttentionSummaryCountsAttention(t *testing.T) {
	rows := []statusRow{
		{row: attentionRow("@1", "idle")},
		{row: row("@2", "a", "blocked", false)},
		{row: row("@3", "b", "idle", true)},
	}
	got := captureStderr(t, func() { printAttentionSummary(rows) })
	want := "Needs attention: 1 blocked, 1 need you (!), 1 done"
	if !strings.Contains(got, want) {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = old }()
	fn()
	w.Close() //nolint:errcheck
	b, _ := io.ReadAll(r)
	return string(b)
}
