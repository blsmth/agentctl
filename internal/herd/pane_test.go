package herd

import "testing"

// Real captured pane fragments. These are verbatim shapes from live agentctl
// windows — the classifier is only worth anything if it holds against what
// tmux actually emits, not against idealized fixtures.
const (
	// An idle agent: finished report, one empty prompt, mode footer.
	paneIdle = `  2. #997's dedupe is still unverified.
✻ Sautéed for 21s
                                                new task? /clear to save 198.5k tokens
──────────────────────────────────────────────────────────── lead ──
❯
────────────────────────────────────────────────────────────────────────
  ⏸ manual mode on · ← for agents`

	// A working agent: spinner line plus "esc to interrupt" in the footer.
	paneBusy = `⏺ Verifying the claim before recording it.
· Twisting… (10m 5s · ↓ 35.9k tokens)
  ⎿  Tip: Use /btw to ask a quick side question
──────────────────────────────────────────────────────────── go-engineer ──
❯
────────────────────────────────────────────────────────────────────────
  ⏸ manual mode on · esc to interrupt · ← 1 agent`

	// A blocked agent: permission dialog awaiting a human.
	paneDialog = `   Dispatch go-engineer to improve dispatch user messages
 Newline followed by # inside a quoted argument can hide arguments
 Do you want to proceed?
 ❯ 1. Yes
   2. No
 Esc to cancel · Tab to amend · ctrl+e to explain`

	// An edit-confirmation dialog, which phrases its question differently.
	paneEditDialog = ` Do you want to make this edit to 2026-08-19.md?
 ❯ 1. Yes
   2. Yes, allow all edits in Daily/ during this session (shift+tab)
   3. No
 Esc to cancel · Tab to amend`

	// Input staged into the box but never submitted — the wake/send-keys
	// failure mode. Two prompt lines instead of one.
	paneStaged = `✻ Done for now
──────────────────────────────────────────────────────────── lead ──
❯ please rebase onto origin/main and force-push
❯
────────────────────────────────────────────────────────────────────────
  ⏸ manual mode on · ← for agents`
)

func TestClassifyPane(t *testing.T) {
	tests := []struct {
		name string
		text string
		want PaneVerdict
	}{
		{"idle pane with one prompt", paneIdle, PaneIdle},
		{"working pane", paneBusy, PaneBusy},
		{"permission dialog", paneDialog, PaneAwaitingInput},
		{"edit confirmation dialog", paneEditDialog, PaneAwaitingInput},
		{"staged input still counts as idle", paneStaged, PaneIdle},
		{"empty pane", "", PaneUnknown},
		{"whitespace only", "   \n\n  ", PaneUnknown},
		{"unrecognized content", "some random output\nwith no markers", PaneUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyPane(tt.text); got != tt.want {
				t.Errorf("ClassifyPane() = %v, want %v", got, tt.want)
			}
		})
	}
}

// The "⏸" glyph appears in the persistent "⏸ manual mode on" footer of every
// pane, idle and working alike. Treating it as a blocked signal would classify
// nearly every window as awaiting input — a false positive on the one verdict
// that makes a human go look.
func TestClassifyPane_PauseGlyphIsNotBlocked(t *testing.T) {
	for name, text := range map[string]string{"idle": paneIdle, "busy": paneBusy} {
		t.Run(name, func(t *testing.T) {
			if !contains(text, "⏸") {
				t.Fatalf("fixture %q lost its ⏸ footer; this test no longer proves anything", name)
			}
			if got := ClassifyPane(text); got == PaneAwaitingInput {
				t.Errorf("pane with a ⏸ mode footer classified as %v", got)
			}
		})
	}
}

// A dialog can appear while a footer left over from the triggering turn is
// still on screen. The dialog must win, or a blocked agent reads as busy and
// nobody goes to unblock it.
func TestClassifyPane_DialogBeatsBusyFooter(t *testing.T) {
	mixed := paneDialog + "\n  ⏸ manual mode on · esc to interrupt · ← 1 agent"
	if got := ClassifyPane(mixed); got != PaneAwaitingInput {
		t.Errorf("ClassifyPane() = %v, want %v", got, PaneAwaitingInput)
	}
}

func TestPromptCount(t *testing.T) {
	tests := []struct {
		name string
		text string
		want int
	}{
		{"idle has exactly one", paneIdle, 1},
		{"staged input has two", paneStaged, 2},
		{"dialog options are not prompts", paneEditDialog, 1}, // "❯ 1. Yes"
		{"empty", "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PromptCount(tt.text); got != tt.want {
				t.Errorf("PromptCount() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestPaneVerdictString(t *testing.T) {
	tests := map[PaneVerdict]string{
		PaneUnknown:       "unknown",
		PaneAwaitingInput: "awaiting-input",
		PaneBusy:          "busy",
		PaneIdle:          "idle",
	}
	for v, want := range tests {
		if got := v.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", v, got, want)
		}
	}
}

func TestPaneVerdictAgreesWith(t *testing.T) {
	tests := []struct {
		verdict  PaneVerdict
		recorded string
		want     bool
	}{
		{PaneAwaitingInput, "blocked", true},
		{PaneAwaitingInput, "working", false},
		{PaneAwaitingInput, "idle", false},
		{PaneBusy, "working", true},
		{PaneBusy, "idle", false},
		{PaneIdle, "idle", true},
		{PaneIdle, "", true},
		// The case that mattered in practice: windowstate kept asserting
		// "working"/"blocked" for a window that had been parked for days,
		// while its pane showed a finished report and an empty prompt.
		{PaneIdle, "working", false},
		{PaneIdle, "blocked", false},
		// Unknown must never manufacture a disagreement — an unreadable pane
		// is not evidence that the recorded state is wrong.
		{PaneUnknown, "working", true},
		{PaneUnknown, "blocked", true},
		{PaneUnknown, "idle", true},
	}
	for _, tt := range tests {
		if got := tt.verdict.AgreesWith(tt.recorded); got != tt.want {
			t.Errorf("%v.AgreesWith(%q) = %v, want %v", tt.verdict, tt.recorded, got, tt.want)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
