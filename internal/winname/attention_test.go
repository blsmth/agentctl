package winname

import (
	"testing"

	"github.com/blsmth/agentctl/internal/windowstate"
)

func attn() *windowstate.Attention {
	return &windowstate.Attention{Reason: windowstate.AttentionQuestion, Detail: "Merge it?"}
}

func TestComputeAttention(t *testing.T) {
	tests := []struct {
		name string
		ws   windowstate.WindowState
		want string
	}{
		{"attention", windowstate.WindowState{Instance: "a:1", State: "idle", Attention: attn()}, "! a:1"},
		{"attention survives Seen", windowstate.WindowState{Instance: "a:1", State: "idle", Attention: attn(), Seen: true}, "! a:1"},
		{"attention beats done", windowstate.WindowState{Instance: "a:1", State: "idle", Done: true, Attention: attn()}, "! a:1"},
		{"attention beats turn done", windowstate.WindowState{Instance: "a:1", State: "idle", TurnDone: true, Attention: attn()}, "! a:1"},
		{"working beats attention", windowstate.WindowState{Instance: "a:1", State: "working", Attention: attn()}, "● a:1"},
		{"blocked beats attention", windowstate.WindowState{Instance: "a:1", State: "blocked", Attention: attn()}, "⏸ a:1"},
		{"fallback attention glyph", windowstate.WindowState{RenderedName: "! b", State: "working"}, "● b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Compute(tt.ws); got != tt.want {
				t.Errorf("Compute() = %q, want %q", got, tt.want)
			}
		})
	}
	if got := stripGlyph("! x"); got != "x" {
		t.Errorf("stripGlyph(! x) = %q", got)
	}
}

func TestApplyClearsAttentionWhenWorking(t *testing.T) {
	f := install(t)
	ws := windowstate.WindowState{Instance: "a:1", State: "idle", Attention: attn()}
	Apply("@1", &ws, func(w *windowstate.WindowState) { w.LastText = "x" })
	if ws.Attention == nil {
		t.Fatal("a write that stays idle cleared attention")
	}
	Apply("@1", &ws, func(w *windowstate.WindowState) { w.State = "working" })
	if ws.Attention != nil {
		t.Error("going working kept attention")
	}
	if ws.RenderedName != "● a:1" {
		t.Errorf("name = %q", ws.RenderedName)
	}
	_ = f
}

func TestReleaseTurnDone(t *testing.T) {
	f := install(t)
	write := func(id string, ws windowstate.WindowState) {
		t.Helper()
		if err := windowstate.Write(id, ws); err != nil {
			t.Fatal(err)
		}
	}
	parent := windowstate.WindowState{Instance: "p:1", RenderedName: "p:1", State: "idle", SessionID: "S", TurnDoneHeld: true}
	write("@1", parent)
	write("@2", windowstate.WindowState{Instance: "c:1", State: "working", SpawnedBy: "@1", SpawnedBySession: "S"})
	write("@3", windowstate.WindowState{Instance: "c:2", State: "idle", SpawnedBy: "@1", SpawnedBySession: "S"})

	ReleaseTurnDone("@1", "S")
	if got, _ := windowstate.Read("@1"); got.TurnDone || !got.TurnDoneHeld {
		t.Fatalf("released while a child works: %+v", got)
	}

	write("@2", windowstate.WindowState{Instance: "c:1", State: "idle", SpawnedBy: "@1", SpawnedBySession: "S"})
	ReleaseTurnDone("@1", "other-session")
	if got, _ := windowstate.Read("@1"); got.TurnDone {
		t.Fatal("released for another session")
	}
	ReleaseTurnDone("@1", "S")
	got, _ := windowstate.Read("@1")
	if !got.TurnDone || got.TurnDoneHeld {
		t.Fatalf("not released once children settled: %+v", got)
	}
	if got.RenderedName != "✓ p:1" {
		t.Errorf("name = %q, want ✓ p:1", got.RenderedName)
	}
	if len(f.renames) == 0 {
		t.Error("no rename")
	}

	// A parent that started a new turn is not touched.
	write("@4", windowstate.WindowState{Instance: "q:1", State: "working", SessionID: "T", TurnDoneHeld: true})
	ReleaseTurnDone("@4", "T")
	if got, _ := windowstate.Read("@4"); got.TurnDone {
		t.Error("TurnDone set on a working parent")
	}
}
