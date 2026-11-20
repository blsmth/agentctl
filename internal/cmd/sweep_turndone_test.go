package cmd

import (
	"testing"

	"github.com/blsmth/agentctl/internal/cleanup"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// TurnDone is a display-only signal: sweep facts and classification of a bare
// window must be identical with and without it.
func TestSweepIgnoresTurnDone(t *testing.T) {
	base := windowstate.WindowState{State: "idle", Mode: "bare", TurnCount: 2}
	withTD := base
	withTD.TurnDone = true

	var a, b cleanup.SweepFacts
	a.Tracked, b.Tracked = true, true
	a.IncludeBare, b.IncludeBare = true, true
	sweepDeps{}.applyDynamic(&a, base)
	sweepDeps{}.applyDynamic(&b, withTD)
	if a != b {
		t.Fatalf("facts differ: %+v vs %+v", a, b)
	}
	if a.Done {
		t.Fatal("bare window must not be Done")
	}
	if va, vb := cleanup.ClassifyWindow(a), cleanup.ClassifyWindow(b); va != vb {
		t.Fatalf("verdicts differ: %+v vs %+v", va, vb)
	}
}
