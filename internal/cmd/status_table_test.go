package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/cliout"
	"github.com/blsmth/agentctl/internal/fleet"
)

func TestPrintStatusTableRowsReachPlainOutput(t *testing.T) {
	var rows []statusRow
	for _, id := range []string{"a1b2", "c3d4", "e5f6"} {
		var r fleet.Row
		r.WindowID, r.Name, r.State, r.Done = "@"+id, "go-engineer", "idle", true
		rows = append(rows, statusRow{row: r, group: fleet.Group{Project: "proj"}, handle: id, label: "fix " + id, parent: "a1b2"})
	}
	var buf bytes.Buffer
	printStatusTable(cliout.StripWriter(&buf), rows, StatusOptions{})
	out := buf.String()
	if strings.Contains(out, "@") {
		t.Errorf("window IDs leaked into the table:\n%s", out)
	}
	if strings.ContainsRune(out, 0x1b) {
		t.Fatalf("escapes leaked: %q", out)
	}
	for _, want := range []string{"SESSION", "ID", "NAME", "──", "a1b2", "c3d4", "e5f6", "fix c3d4", "go-engineer", "done"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}
