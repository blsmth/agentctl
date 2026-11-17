package cmd

import (
	"math"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/sessions"
	"github.com/blsmth/agentctl/internal/tokens"
)

func mkRow(agent, project, model string, day int, b sessions.Billing, usd float64, known bool) costRow {
	return costRow{
		Start:   time.Date(2026, 11, day, 12, 0, 0, 0, time.Local),
		Project: project, Agent: agent, Model: model, Turns: 2,
		Cost: sessions.SessionCost{
			USD: usd, Known: known, Billing: b,
			Usage:   tokens.Usage{CacheRead: 1000, Output: 10},
			ByModel: []sessions.ModelCost{{Model: model, Turns: 2, USD: usd, Known: known, Usage: tokens.Usage{CacheRead: 1000, Output: 10}}},
		},
	}
}

func TestParseSince(t *testing.T) {
	tests := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"", 7 * 24 * time.Hour, false},
		{"7d", 7 * 24 * time.Hour, false},
		{"2w", 14 * 24 * time.Hour, false},
		{"36h", 36 * time.Hour, false},
		{"90m", 90 * time.Minute, false},
		{"abc", 0, true},
		{"-3d", 0, true},
	}
	for _, tt := range tests {
		got, err := parseSince(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("parseSince(%q) = (%v, %v), want (%v, err=%v)", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestSumCosts_UnknownExcludedAndBillingSplit(t *testing.T) {
	rows := []costRow{
		mkRow("a", "p", "claude-opus-5-5", 1, sessions.BillingSub, 10, true),
		mkRow("a", "p", "openai/gpt-6-astra", 2, sessions.BillingMetered, 7.22, true),
		mkRow("a", "p", "qwen3", 3, sessions.BillingFree, 0, true),
		mkRow("a", "p", "mystery", 4, sessions.BillingUnknown, 0, false),
	}
	got := sumCosts(rows)
	if got.Sessions != 4 || got.Excluded != 1 {
		t.Errorf("sessions=%d excluded=%d, want 4 and 1", got.Sessions, got.Excluded)
	}
	if math.Abs(got.Metered-7.22) > 1e-9 || math.Abs(got.Sub-10) > 1e-9 || math.Abs(got.Total()-17.22) > 1e-9 {
		t.Errorf("metered=%v sub=%v total=%v", got.Metered, got.Sub, got.Total())
	}
}

func TestGroupCosts(t *testing.T) {
	rows := []costRow{
		mkRow("lead", "agentctl", "claude-opus-5-5", 1, sessions.BillingSub, 10, true),
		mkRow("lead", "other", "claude-opus-5-5", 1, sessions.BillingSub, 5, true),
		mkRow("review", "agentctl", "openai/gpt-6-astra", 2, sessions.BillingMetered, 7, true),
		mkRow("review", "agentctl", "mystery", 2, sessions.BillingUnknown, 0, false),
	}
	byAgent, err := groupCosts(rows, "agent")
	if err != nil {
		t.Fatal(err)
	}
	if len(byAgent) != 2 || byAgent[0].Key != "lead" || byAgent[0].Total() != 15 || byAgent[1].Excluded != 1 {
		t.Errorf("by agent = %+v", byAgent)
	}
	byDay, _ := groupCosts(rows, "day")
	if len(byDay) != 2 || byDay[0].Key != "2026-11-01" {
		t.Errorf("by day should be chronological: %+v", byDay)
	}
	byModel, _ := groupCosts(rows, "model")
	if len(byModel) != 3 {
		t.Errorf("by model = %+v", byModel)
	}
	byProject, _ := groupCosts(rows, "project")
	if len(byProject) != 2 || byProject[0].Key != "agentctl" {
		t.Errorf("by project = %+v", byProject)
	}
	if _, err := groupCosts(rows, "bogus"); err == nil {
		t.Error("invalid --by should error")
	}
}
