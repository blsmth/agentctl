package panestate

import "testing"

const (
	idle = `✻ Sautéed for 21s
──────────────────────────────────────────────────────────── lead ──
❯
────────────────────────────────────────────────────────────────────────
  ⏸ manual mode on · ← for agents`

	busy = `· Twisting… (10m 5s · ↓ 35.9k tokens)
──────────────────────────────────────────────────────────── go-engineer ──
❯
────────────────────────────────────────────────────────────────────────
  ⏸ manual mode on · esc to interrupt · ← 1 agent`

	dialog = ` Do you want to proceed?
 ❯ 1. Yes
   2. No
 Esc to cancel · Tab to amend · ctrl+e to explain`

	// Dialog with a stale busy footer: dialog must win.
	dialogBusyFooter = dialog + "\n  ⏸ manual mode on · esc to interrupt"

	// Dialog that phrases its question differently.
	dialogOther = ` Allow this thing?
 ❯ 1. Yes
   2. No`

	staged = `──────────────────────────────────────────────────────────── lead ──
❯ please rebase
❯
────────────────────────────────────────────────────────────────────────`

	boxed = "╭──────────╮\n│ ❯        │\n╰──────────╯\n"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		text string
		want Verdict
	}{
		{"idle", idle, Idle},
		{"busy", busy, Busy},
		{"dialog", dialog, AwaitingInput},
		{"dialog beats busy footer", dialogBusyFooter, AwaitingInput},
		{"numbered dialog fallback", dialogOther, AwaitingInput},
		{"staged content", staged, Idle},
		{"boxed prompt", boxed, Idle},
		{"empty", "", Unknown},
		{"whitespace", "  \n\t\n", Unknown},
		{"no prompt no marker", "some random output\n", Unknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.text); got != tt.want {
				t.Errorf("Classify = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPromptCount(t *testing.T) {
	if got := PromptCount(staged); got != 2 {
		t.Errorf("staged = %d, want 2", got)
	}
	if got := PromptCount(idle); got != 1 {
		t.Errorf("idle = %d, want 1", got)
	}
}

func TestVerdictString(t *testing.T) {
	for v, want := range map[Verdict]string{Unknown: "unknown", AwaitingInput: "awaiting-input", Busy: "busy", Idle: "idle"} {
		if v.String() != want {
			t.Errorf("%d = %q, want %q", v, v.String(), want)
		}
	}
}
