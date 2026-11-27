package tui

import (
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/theme"
	"github.com/blsmth/agentctl/internal/verdicts"
)

const (
	r2PRURL     = "https://github.com/acme/app/pull/418"
	r2TicketURL = "https://github.com/acme/app/issues/42"
)

func r2Selection() HubSelection {
	st := layoutStatus()
	st.Ticket, st.TicketURL = 42, r2TicketURL
	st.PRNumber, st.PRURL = 418, r2PRURL
	st.Verdict = verdicts.Merge
	return HubSelection{Session: "s", WindowID: "@1", RuntimeDir: "/p/.agentctl", Status: st}
}

// stubOpener replaces the browser opener and returns what it was asked to open.
func stubOpener(t *testing.T) *[]string {
	t.Helper()
	var got []string
	old := openURL
	openURL = func(u string) error { got = append(got, u); return nil }
	t.Cleanup(func() { openURL = old })
	return &got
}

// colOf is the display column of the first occurrence of sub on line y.
func colOf(t *testing.T, out string, y int, sub string) int {
	t.Helper()
	line := strings.Split(ansi.Strip(out), "\n")[y]
	i := strings.Index(line, sub)
	if i < 0 {
		t.Fatalf("%q not on line %d: %q", sub, y, line)
	}
	return lipgloss.Width(line[:i])
}

func TestHubStatusClickOpensLinks(t *testing.T) {
	InitStyles()
	for _, w := range []int{100, 70, 52} {
		t.Run(strings.Join([]string{"w", string(rune('0' + w/10%10)), string(rune('0' + w%10))}, ""), func(t *testing.T) {
			opened := stubOpener(t)
			focused := 0
			m := HubStatusModel{sel: r2Selection(), width: w, height: 6, focusViewer: func() error { focused++; return nil }}
			out, spans := renderHubStatusLayout(m.sel, false, w, 6, statusNow)
			plain := strings.Split(ansi.Strip(out), "\n")
			y := -1
			for i, l := range plain {
				if strings.Contains(l, "PR #418") {
					y = i
				}
			}
			if y < 0 {
				t.Fatalf("no PR fact at w=%d:\n%s", w, ansi.Strip(out))
			}
			click := func(x, y int, b tea.MouseButton) {
				_, cmd := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: b})
				if cmd != nil {
					cmd()
				}
			}
			px := colOf(t, out, y, "PR #418")
			click(px, y, tea.MouseLeft)
			click(px+6, y, tea.MouseLeft)
			if len(*opened) != 2 || (*opened)[0] != r2PRURL || (*opened)[1] != r2PRURL {
				t.Fatalf("opened = %v, want the PR URL twice", *opened)
			}
			tx := colOf(t, out, y, "#42")
			click(tx, y, tea.MouseLeft)
			if got := (*opened)[len(*opened)-1]; got != r2TicketURL {
				t.Fatalf("ticket click opened %q", got)
			}
			if focused != 0 {
				t.Fatalf("link clicks moved focus (%d)", focused)
			}
			n := len(*opened)
			click(px-1, y, tea.MouseLeft) // the separator before the PR fact
			click(px, y, tea.MouseRight)  // not a left click
			click(px, 0, tea.MouseLeft)   // the title line
			if len(*opened) != n || focused != 3 {
				t.Fatalf("elsewhere: opened %v, focused %d; want focus x3 and no open", *opened, focused)
			}
			for _, sp := range spans {
				if sp.x1 > w || sp.x0 < hubStatusIndent {
					t.Errorf("span %+v outside the pane (w=%d)", sp, w)
				}
			}
		})
	}
}

func TestHubStatusSpansFollowTruncation(t *testing.T) {
	InitStyles()
	st := r2Selection().Status
	// Squeeze until the line is end-truncated: spans must never run past it.
	for w := 60; w >= 8; w-- {
		line, spans := hubStatusFooterSpans(st, w, statusNow)
		for _, sp := range spans {
			if sp.x1 > w || sp.x0 >= sp.x1 {
				t.Fatalf("w=%d: span %+v over line %q", w, sp, ansi.Strip(line))
			}
			if lipgloss.Width(line) < sp.x1 {
				t.Fatalf("w=%d: span %+v past the %d-wide line", w, sp, lipgloss.Width(line))
			}
		}
	}
}

func TestHubStatusVerdictLabels(t *testing.T) {
	InitStyles()
	tests := []struct {
		verdict, want string
		style         lipgloss.Style
	}{
		{verdicts.Merge, "PR #418 · approved", fleetDoneStyle},
		{verdicts.MergeWithFollowup, "PR #418 · approved", fleetDoneStyle},
		{verdicts.Blocked, "PR #418 · changes", fleetAttentionStyle},
		{verdicts.NeedsBrendan, "PR #418 · needs you", fleetBlockedStyle},
		{"", "PR #418", fleetDimStyle},
		{"WHAT", "PR #418", fleetDimStyle},
	}
	for _, tt := range tests {
		t.Run(tt.verdict, func(t *testing.T) {
			st := &hubStatus{PRNumber: 418, PRURL: r2PRURL, Verdict: tt.verdict}
			seg := prSeg(st, footPlan{})
			if got := ansi.Strip(strings.ReplaceAll(strings.ReplaceAll(seg.text, ansi.SetHyperlink(r2PRURL), ""), ansi.ResetHyperlink(), "")); got != tt.want {
				t.Errorf("text = %q, want %q", got, tt.want)
			}
			if seg.w != lipgloss.Width(tt.want) || seg.link != r2PRURL {
				t.Errorf("w=%d link=%q", seg.w, seg.link)
			}
			if label, style := hubStatusVerdict(tt.verdict); label != "" && style.Render("x") != tt.style.Render("x") {
				t.Errorf("style for %s", tt.verdict)
			}
		})
	}
	// Dropped before anything else is lost when the line is narrow.
	st := &hubStatus{Version: hubStatusVersion, PRNumber: 418, PRURL: r2PRURL, Verdict: verdicts.Merge}
	if got := ansi.Strip(hubStatusFooter(st, 10, statusNow)); got != "PR #418" {
		t.Errorf("narrow footer = %q", got)
	}
}

func TestHubStatusPollLooksUpVerdict(t *testing.T) {
	InitStyles()
	var gotPR int
	var gotRepo string
	m := HubStatusModel{
		read: func() (HubSelection, error) { return r2Selection(), nil },
		lookupVerdict: func(pr int, repo string) string {
			gotPR, gotRepo = pr, repo
			return verdicts.Blocked
		},
	}
	m.poll()
	if gotPR != 418 || gotRepo != "acme/app" || m.sel.Status.Verdict != verdicts.Blocked {
		t.Fatalf("lookup got %d %q, verdict %q", gotPR, gotRepo, m.sel.Status.Verdict)
	}
}

func TestTicketURLFor(t *testing.T) {
	old := ticketRepoSlug
	t.Cleanup(func() { ticketRepoSlug = old })
	calls := 0
	ticketRepoSlug = func(dir string) string { calls++; return "acme/other" }
	if got := ticketURLFor(&hubStatus{Ticket: 5, PRURL: r2PRURL}, "/x/.agentctl"); got != "https://github.com/acme/app/issues/5" {
		t.Errorf("from PR url = %q", got)
	}
	if calls != 0 {
		t.Errorf("git consulted despite a PR URL")
	}
	for range 3 {
		if got := ticketURLFor(&hubStatus{Ticket: 5}, "/r2-remote/.agentctl"); got != "https://github.com/acme/other/issues/5" {
			t.Errorf("from remote = %q", got)
		}
	}
	if calls != 1 {
		t.Errorf("remote resolved %d times, want 1 (cached)", calls)
	}
	if got := ticketURLFor(&hubStatus{}, "/x/.agentctl"); got != "" {
		t.Errorf("no ticket = %q", got)
	}
}

func TestHubStatusTintFullWidth(t *testing.T) {
	InitStyles()
	dark := theme.Theme{Name: "dark", Base: "#1e1e2e", Surface: "#313244"}
	light := theme.Theme{Name: "light", Base: "#eff1f5", Surface: "#ccd0da"}
	for _, th := range []theme.Theme{dark, light} {
		bg := hubStatusBackgroundFor(th)
		if bg == "" {
			t.Fatalf("%s: no background sequence", th.Name)
		}
		for _, w := range []int{40, 70, 100} {
			t.Run(th.Name+"/"+strconv.Itoa(w), func(t *testing.T) {
				lines, spans := hubStatusLayout(r2Selection().Status, w, 6, statusNow)
				out, _ := renderHubStatusLines(lines, spans, w, 6, bg)
				rows := strings.Split(out, "\n")
				if len(rows) != 6 {
					t.Fatalf("%d lines, want 6", len(rows))
				}
				for i, l := range rows {
					if got := lipgloss.Width(l); got != w {
						t.Errorf("line %d is %d wide, want %d", i, got, w)
					}
					if !strings.HasPrefix(l, bg) {
						t.Errorf("line %d not tinted: %q", i, l)
					}
					// Every reset in the line is followed by the background again.
					body := strings.TrimSuffix(l, "\x1b[m")
					if strings.Count(body, "\x1b[m") != strings.Count(body, "\x1b[m"+bg) {
						t.Errorf("line %d loses the background after a reset: %q", i, l)
					}
				}
				// The reverse-video pill is still there, and still reverse.
				if !strings.Contains(rows[0], "\x1b[1;7;") && !strings.Contains(rows[0], ";7;") && !strings.Contains(rows[0], "[7;") {
					t.Errorf("title line lost its reverse-video pill: %q", rows[0])
				}
			})
		}
	}
}

func TestHubStatusNoSurfaceNoTint(t *testing.T) {
	InitStyles()
	for _, th := range []theme.Theme{{Name: "none", Base: "#000000"}, {Name: "same", Base: "#112233", Surface: "#112233"}} {
		if bg := hubStatusBackgroundFor(th); bg != "" {
			t.Fatalf("%s: background %q with no suitable surface colour", th.Name, bg)
		}
	}
	lines, spans := hubStatusLayout(r2Selection().Status, 50, 6, statusNow)
	out, _ := renderHubStatusLines(lines, spans, 50, 6, "")
	for i, l := range strings.Split(out, "\n") {
		if lipgloss.Width(l) != 50 {
			t.Errorf("line %d is %d wide", i, lipgloss.Width(l))
		}
	}
}
