package statusline

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/render"
	"github.com/blsmth/agentctl/internal/state"
)

func mobilePayload(modelID string) *Payload {
	p := &Payload{
		Model:         &Model{ID: strPtr(modelID), DisplayName: strPtr("Opus 4.6")},
		ContextWindow: &ContextWindow{UsedPercentagePushed: f64Ptr(34)},
		Cost:          &Cost{TotalCostUSD: f64Ptr(13.43), TotalDurationMS: f64Ptr(3660000)},
		RateLimits: &RateLimits{
			FiveHour: &RateLimitWindow{UsedPercentage: f64Ptr(11), ResetsAt: f64Ptr(1)},
			SevenDay: &RateLimitWindow{UsedPercentage: f64Ptr(5), ResetsAt: f64Ptr(1)},
		},
		account: "work",
	}
	return p
}

func plain(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = stripANSI(l)
	}
	return out
}

func TestRenderMobile(t *testing.T) {
	bat := stripANSI(render.Battery(34))
	l1 := "opus 4.6 · " + bat + " 34% ctx"
	batW := lipgloss.Width(bat)

	tests := []struct {
		name  string
		mod   func(p *Payload)
		model string
		cols  int
		snap  *state.AgentState
		want  []string
	}{
		{name: "everything", cols: 100, want: []string{l1 + " · 1h 1m", "5h 11% · 7d 5% · $13.43 · work"}},
		{name: "no rate limits", cols: 100, mod: func(p *Payload) { p.RateLimits = nil },
			want: []string{l1 + " · 1h 1m", "$13.43 · work"}},
		{name: "non-claude hides cost", model: "openai/gpt-6-astra", cols: 100,
			want: []string{l1 + " · 1h 1m", "5h 11% · 7d 5% · work"}},
		{name: "no account", cols: 100, mod: func(p *Payload) { p.account = "" },
			want: []string{l1 + " · 1h 1m", "5h 11% · 7d 5% · $13.43"}},
		{name: "zero cost omitted", cols: 100, mod: func(p *Payload) { p.Cost.TotalCostUSD = f64Ptr(0) },
			want: []string{l1 + " · 1h 1m", "5h 11% · 7d 5% · work"}},
		{name: "zero ctx uses label style", cols: 100, mod: func(p *Payload) { p.ContextWindow.UsedPercentagePushed = f64Ptr(0) },
			want: []string{"opus 4.6 · " + stripANSI(render.Battery(0)) + " 0% ctx · 1h 1m", "5h 11% · 7d 5% · $13.43 · work"}},
		{name: "duration falls back to snapshot", cols: 100,
			mod:  func(p *Payload) { p.Cost.TotalDurationMS = nil },
			snap: &state.AgentState{StartTime: time.Now().Add(-2*time.Hour - 5*time.Minute).Unix()},
			want: []string{l1 + " · 2h 5m", "5h 11% · 7d 5% · $13.43 · work"}},
		{name: "no duration anywhere", cols: 100, mod: func(p *Payload) { p.Cost.TotalDurationMS = nil },
			want: []string{l1, "5h 11% · 7d 5% · $13.43 · work"}},
		{name: "drop session length first", cols: 12 + batW + 8, want: []string{l1, "5h 11% · 7d 5% · $13.43 · work"}},
		{name: "line 2 drops account", cols: 24, want: []string{"opus 4.6", "5h 11% · 7d 5% · $13.43"}},
		{name: "line 2 then 7d", cols: 17, want: []string{"opus 4.6", "5h 11% · $13.43"}},
		{name: "line 2 then cost", cols: 8, want: []string{"opus 4.6", "5h 11%"}},
		{name: "only line 2 data", cols: 100, mod: func(p *Payload) { p.Model = nil; p.ContextWindow = nil; p.Cost.TotalDurationMS = nil },
			want: []string{"5h 11% · 7d 5% · $13.43 · work"}},
		{name: "nothing", cols: 100, mod: func(p *Payload) { *p = Payload{} }, want: []string{noDataLine}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := tt.model
			if model == "" {
				model = "claude-opus-4-6"
			}
			p := mobilePayload(model)
			if tt.mod != nil {
				tt.mod(p)
			}
			got := plain(renderMobile(p, tt.cols, tt.snap))
			if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRenderMobile_StaleSnapshotIgnored(t *testing.T) {
	p := mobilePayload("claude-opus-4-6")
	p.Cost.TotalDurationMS = nil
	p.SessionID = strPtr("f2cbcc84-0000-0000-0000-000000000001")
	snap := &state.AgentState{
		LogPath:   "/p/b/271e25c0.jsonl",
		StartTime: time.Now().Add(-3 * time.Hour).Unix(),
	}
	got := plain(renderMobile(p, 100, consistentAgentState(snap, p)))
	if strings.Contains(got[0], "3h") {
		t.Errorf("stale snapshot duration rendered: %q", got[0])
	}
}

func TestRenderMobileAligned_Right(t *testing.T) {
	p := mobilePayload("claude-opus-4-6")
	lines := renderMobileAligned(p, 60, nil, "right")
	for _, l := range lines {
		if w := lipgloss.Width(l); w != 60-rightAlignReserve {
			t.Errorf("width = %d, want %d: %q", w, 60-rightAlignReserve, l)
		}
		if !strings.HasPrefix(l, string(padRune)) {
			t.Errorf("line not padded with padRune: %q", l)
		}
	}
	left := renderMobileAligned(p, 60, nil, "left")
	if strings.HasPrefix(left[0], string(padRune)) {
		t.Errorf("left align must not pad")
	}
}
