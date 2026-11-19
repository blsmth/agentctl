package statusline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func mobileRepo(t *testing.T, branch string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	head := "ref: refs/heads/" + branch + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte(head), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func mobilePayload(dir, modelID string) *Payload {
	p := &Payload{
		Model:         &Model{ID: strPtr(modelID), DisplayName: strPtr("Opus 4.6")},
		ContextWindow: &ContextWindow{UsedPercentagePushed: f64Ptr(34)},
		Cost:          &Cost{TotalCostUSD: f64Ptr(13.43)},
		RateLimits: &RateLimits{
			FiveHour: &RateLimitWindow{UsedPercentage: f64Ptr(11), ResetsAt: f64Ptr(1)},
			SevenDay: &RateLimitWindow{UsedPercentage: f64Ptr(5), ResetsAt: f64Ptr(1)},
		},
		account: "work",
	}
	if dir != "" {
		p.Cwd = strPtr(dir)
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
	repo := mobileRepo(t, "feat-x")

	tests := []struct {
		name  string
		mod   func(p *Payload)
		dir   string
		model string
		cols  int
		want  []string
	}{
		{name: "everything", dir: repo, cols: 80, want: []string{"opus 4.6 · ctx 34% · $13.43", "5h 11% · 7d 5% · work · ⎇ feat-x"}},
		{name: "no rate limits", dir: repo, cols: 80, mod: func(p *Payload) { p.RateLimits = nil },
			want: []string{"opus 4.6 · ctx 34% · $13.43", "work · ⎇ feat-x"}},
		{name: "non-claude hides cost", dir: repo, model: "openai/gpt-6-astra", cols: 80,
			want: []string{"opus 4.6 · ctx 34%", "5h 11% · 7d 5% · work · ⎇ feat-x"}},
		{name: "no repo no account", dir: t.TempDir(), cols: 80, mod: func(p *Payload) { p.account = "" },
			want: []string{"opus 4.6 · ctx 34% · $13.43", "5h 11% · 7d 5%"}},
		{name: "zero cost omitted", dir: repo, cols: 80, mod: func(p *Payload) { p.Cost.TotalCostUSD = f64Ptr(0) },
			want: []string{"opus 4.6 · ctx 34%", "5h 11% · 7d 5% · work · ⎇ feat-x"}},
		{name: "drop branch first", dir: repo, cols: 30, want: []string{"opus 4.6 · ctx 34% · $13.43", "5h 11% · 7d 5% · work"}},
		{name: "then account", dir: repo, cols: 20, want: []string{"opus 4.6 · ctx 34%", "5h 11% · 7d 5%"}},
		{name: "then 7d", dir: repo, cols: 12, want: []string{"opus 4.6", "5h 11%"}},
		{name: "only line 2 data", dir: "", cols: 80, mod: func(p *Payload) { p.Model = nil; p.ContextWindow = nil; p.Cost = nil },
			want: []string{"5h 11% · 7d 5% · work"}},
		{name: "nothing", dir: "", cols: 80, mod: func(p *Payload) { *p = Payload{} }, want: []string{noDataLine}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := tt.model
			if model == "" {
				model = "claude-opus-4-6"
			}
			p := mobilePayload(tt.dir, model)
			if tt.mod != nil {
				tt.mod(p)
			}
			got := plain(renderMobile(p, tt.cols))
			if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRenderMobileAligned_Right(t *testing.T) {
	p := mobilePayload("", "claude-opus-4-6")
	lines := renderMobileAligned(p, 60, "right")
	for _, l := range lines {
		if w := lipgloss.Width(l); w != 60-rightAlignReserve {
			t.Errorf("width = %d, want %d: %q", w, 60-rightAlignReserve, l)
		}
		if !strings.HasPrefix(l, string(padRune)) {
			t.Errorf("line not padded with padRune: %q", l)
		}
	}
	left := renderMobileAligned(p, 60, "left")
	if strings.HasPrefix(left[0], string(padRune)) {
		t.Errorf("left align must not pad")
	}
}
