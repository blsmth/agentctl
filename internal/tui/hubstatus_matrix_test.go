package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/fleet"
)

// matrixStatuses are the cases the pane is checked against at every width and
// height.
func matrixStatuses() map[string]*hubStatus {
	working := layoutStatus()
	blocked := layoutStatus()
	blocked.State, blocked.Glyph = "blocked", "⏸"
	blocked.PendingTool, blocked.PendingToolDetail = "Bash", "rm -rf node_modules \x1b[31m&&\x1b[0m\npnpm install --frozen-lockfile"
	yourTurn := &hubStatus{Version: hubStatusVersion, Glyph: "✓", Title: "Writing Backend Rollback Runbook", Project: "apppack",
		State: "your turn", StateSince: statusNow.Add(-14 * time.Hour), Agent: "ll-lead", Extra: []string{"acct ll"}}
	cron := &hubStatus{Version: hubStatusVersion, Glyph: "○", Title: "nightly", State: "idle",
		StateSince: statusNow.Add(-3 * time.Hour), SchedBy: "nightly", SchedAt: statusNow.Add(-3 * time.Hour)}
	kids := layoutStatus()
	kids.Kids, kids.KidsBlocked, kids.KidsWorking = 2, 1, 1
	return map[string]*hubStatus{"working": working, "blocked with tool": blocked, "your turn no recap": yourTurn, "cron": cron, "sub-agents": kids}
}

func TestHubStatusMatrix(t *testing.T) {
	InitStyles()
	// Statusline fields never appear in the pane.
	banned := []string{"opus", "sonnet", "ctx", "context", "cost", "$", "branch", "agent/", "usage"}
	for name, st := range matrixStatuses() {
		for _, w := range []int{40, 70, 100} {
			for h := 1; h <= 6; h++ {
				t.Run(fmt.Sprintf("%s/w%d/h%d", name, w, h), func(t *testing.T) {
					lines := renderPlain(st, w, h)
					if len(lines) != h {
						t.Fatalf("%d lines, want %d", len(lines), h)
					}
					for i, l := range lines {
						if lipgloss.Width(l) > w {
							t.Errorf("line %d is %d wide, over %d: %q", i, lipgloss.Width(l), w, l)
						}
					}
					all := strings.Join(lines, "\n")
					if !strings.Contains(lines[0], "▌") || !strings.Contains(lines[0], st.Glyph) {
						t.Errorf("title row lacks gutter or glyph: %q", lines[0])
					}
					for _, b := range banned {
						if strings.Contains(strings.ToLower(all), b) {
							t.Errorf("statusline field %q in:\n%s", b, all)
						}
					}
					if strings.Contains(all, "nothing to summarise") || strings.Contains(all, "\x1b") {
						t.Errorf("filler or raw escape in:\n%s", all)
					}
					if st.PendingTool != "" && h >= 2 && !strings.Contains(lines[1], "needs you ▸ Bash") {
						t.Errorf("needs-you line lost at h=%d:\n%s", h, all)
					}
					if name == "your turn no recap" && h >= 2 && !strings.Contains(lines[1], "Waiting on you since yesterday") {
						t.Errorf("waiting line missing:\n%s", all)
					}
				})
			}
		}
	}
}

func TestHubStatusNeedsSurvivesTightRows(t *testing.T) {
	InitStyles()
	st := matrixStatuses()["blocked with tool"]
	for h := 2; h <= 3; h++ {
		lines := renderPlain(st, 70, h)
		if !strings.Contains(lines[1], "needs you ▸ Bash  rm -rf node_modules && pnpm") {
			t.Errorf("h=%d needs line = %q", h, lines[1])
		}
	}
	// Attention without a pending tool is the same line.
	att := layoutStatus()
	att.Attention = "ci failing: build"
	if got := renderPlain(att, 70, 2)[1]; !strings.Contains(got, "needs you ▸ ci failing: build") {
		t.Errorf("attention line = %q", got)
	}
}

func TestHubStatusContentPacksUp(t *testing.T) {
	InitStyles()
	st := layoutStatus() // one-line recap, facts
	lines := renderPlain(st, 100, 6)
	if !strings.Contains(lines[2], "PR #7") || strings.TrimSpace(lines[3]) != "" {
		t.Errorf("facts not directly under the recap:\n%s", strings.Join(lines, "\n"))
	}
}

func TestHubStatusWaitingSince(t *testing.T) {
	InitStyles()
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	for name, tt := range map[string]struct {
		since time.Time
		want  string
	}{
		"today":     {time.Date(2026, 11, 23, 9, 4, 0, 0, time.UTC), "Waiting on you since 09:04"},
		"yesterday": {time.Date(2026, 11, 22, 4, 10, 0, 0, time.UTC), "Waiting on you since yesterday 04:10"},
		"older":     {time.Date(2026, 11, 10, 4, 10, 0, 0, time.UTC), "Waiting on you since Nov 10"},
		"last year": {time.Date(2025, 11, 10, 4, 10, 0, 0, time.UTC), "Waiting on you since Nov 10, 2025"},
	} {
		t.Run(name, func(t *testing.T) {
			st := &hubStatus{Version: hubStatusVersion, Glyph: "○", Title: "T", State: "idle", StateSince: tt.since}
			text, _ := hubStatusBody(st, false, now)
			if text != tt.want {
				t.Errorf("got %q, want %q", text, tt.want)
			}
		})
	}
	// Working with no recap: the note, else nothing.
	w := &hubStatus{Version: hubStatusVersion, State: "working", StateSince: now}
	if text, _ := hubStatusBody(w, false, now); text != "" {
		t.Errorf("working no recap = %q", text)
	}
	w.Note = "no prompt yet"
	if text, _ := hubStatusBody(w, false, now); text != "no prompt yet" {
		t.Errorf("working with note = %q", text)
	}
}

func TestHubStatusAgeEscalates(t *testing.T) {
	InitStyles()
	waiting := &hubStatus{State: "your turn", StateSince: statusNow.Add(-59 * time.Minute)}
	if got := hubStatusAgeStyle(waiting, statusNow).Render("x"); got != fleetDimStyle.Render("x") {
		t.Errorf("under 1h should stay dim")
	}
	waiting.StateSince = statusNow.Add(-time.Hour)
	if got := hubStatusAgeStyle(waiting, statusNow).Render("x"); got != fleetAttentionStyle.Render("x") {
		t.Errorf("1h should escalate")
	}
	waiting.State = "working"
	if got := hubStatusAgeStyle(waiting, statusNow).Render("x"); got != fleetDimStyle.Render("x") {
		t.Errorf("a long working age is normal")
	}
}

func TestHubStatusKidsDots(t *testing.T) {
	InitStyles()
	plain := func(st *hubStatus) string { return ansi.Strip(kidsSeg(st).text) }
	tests := []struct {
		name string
		st   hubStatus
		want string
	}{
		{"worst first", hubStatus{Kids: 4, KidsWorking: 2, KidsBlocked: 1}, "⏸ ● ● ○"},
		{"all states", hubStatus{Kids: 5, KidsWorking: 1, KidsBlocked: 1, KidsAttention: 1, KidsDone: 1}, "⏸ " + fleet.AttentionGlyph + " ● ✓ ○"},
		{"exactly eight", hubStatus{Kids: 8, KidsWorking: 8}, "● ● ● ● ● ● ● ●"},
		{"overflow", hubStatus{Kids: 11, KidsBlocked: 2, KidsWorking: 9}, "⏸ ⏸ ● ● ● ● ● ● +3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := tt.st
			seg := kidsSeg(&st)
			if got := plain(&st); got != tt.want {
				t.Errorf("dots = %q, want %q", got, tt.want)
			}
			if seg.w != lipgloss.Width(tt.want) {
				t.Errorf("width = %d, want %d", seg.w, lipgloss.Width(tt.want))
			}
		})
	}
	st := &hubStatus{Kids: 2, KidsBlocked: 1, KidsWorking: 1}
	want := fleetBlockedStyle.Render("⏸") + " " + fleetWorkingStyle.Render("●")
	if got := kidsSeg(st).text; got != want {
		t.Errorf("coloured %q, want %q", got, want)
	}
}

func TestHubStatusLegacyHasNoFiller(t *testing.T) {
	InitStyles()
	st := &hubStatus{Glyph: "●", Title: "Old"}
	if all := strings.Join(renderPlain(st, 60, 4), "\n"); strings.Contains(all, "nothing to summarise") {
		t.Errorf("legacy filler:\n%s", all)
	}
}
