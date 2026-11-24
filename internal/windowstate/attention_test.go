package windowstate

import (
	"testing"
	"time"
)

func TestAttentionRoundTripAndClear(t *testing.T) {
	SetDirForTest(t.TempDir())
	t.Cleanup(func() { SetDirForTest("") })
	at := time.Unix(1700000000, 0).UTC()
	if err := Update("@1", func(w *WindowState) {
		w.Attention = &Attention{Reason: AttentionErrors, Detail: "3 tool failures in a row", At: at}
		w.ToolFailures = 3
		w.PendingVerdict = &PendingVerdict{PR: 4, Verdict: "BLOCKED", At: at}
		w.TurnDoneHeld = true
	}); err != nil {
		t.Fatal(err)
	}
	ws, err := Read("@1")
	if err != nil {
		t.Fatal(err)
	}
	if ws.Attention == nil || ws.Attention.Reason != AttentionErrors || !ws.Attention.At.Equal(at) ||
		ws.ToolFailures != 3 || ws.PendingVerdict == nil || ws.PendingVerdict.PR != 4 || !ws.TurnDoneHeld {
		t.Fatalf("round trip lost fields: %+v", ws)
	}
	ClearAttention(&ws)
	if ws.Attention != nil || ws.ToolFailures != 0 {
		t.Errorf("ClearAttention left %+v / %d", ws.Attention, ws.ToolFailures)
	}
	if ws.PendingVerdict == nil {
		t.Error("ClearAttention must leave the pending verdict: only a human prompt answers it")
	}
}
