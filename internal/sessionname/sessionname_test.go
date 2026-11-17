package sessionname

import (
	"testing"

	"github.com/blsmth/agentctl/internal/paths"
)

func TestRecordLookup(t *testing.T) {
	paths.Init(t.TempDir())

	if got := Lookup("sess-1"); got != "" {
		t.Fatalf("Lookup before Record = %q, want empty", got)
	}
	if err := Record("sess-1", "agent-go-engineer-issue-42"); err != nil {
		t.Fatal(err)
	}
	if got := Lookup("sess-1"); got != "agent-go-engineer-issue-42" {
		t.Errorf("Lookup = %q", got)
	}
	// Unsafe IDs and empty names are ignored.
	if err := Record("../evil", "x"); err != nil || Lookup("../evil") != "" {
		t.Error("path-traversal ID must be ignored")
	}
	if err := Record("sess-2", ""); err != nil || Lookup("sess-2") != "" {
		t.Error("empty name must not be recorded")
	}
}
