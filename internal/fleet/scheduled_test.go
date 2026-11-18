package fleet

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/windowstate"
)

var firedAt = time.Date(2026, 11, 18, 7, 0, 0, 0, time.UTC)

func TestBuildAgentsScheduled(t *testing.T) {
	wins := []tmux.WindowInfo{{ID: "@1", Name: "triage"}, {ID: "@2", Name: "lead"}}
	records := map[string]windowstate.WindowState{
		"@1": {State: "working", Instance: "triage", Mode: "adhoc", ScheduledBy: "nightly-triage", ScheduledAt: firedAt},
		"@2": {State: "idle", Instance: "lead"},
	}
	got := BuildAgents(wins, records, nil, firedAt)
	if len(got) != 2 {
		t.Fatalf("got %d agents, want 2", len(got))
	}
	if got[0].ScheduledBy != "nightly-triage" || !got[0].ScheduledAt.Equal(firedAt) {
		t.Errorf("cron agent = %q %v, want nightly-triage %v", got[0].ScheduledBy, got[0].ScheduledAt, firedAt)
	}
	if got[1].ScheduledBy != "" || !got[1].ScheduledAt.IsZero() {
		t.Errorf("non-cron agent has schedule: %q %v", got[1].ScheduledBy, got[1].ScheduledAt)
	}
}

func TestLoadMetaScheduled(t *testing.T) {
	dir := t.TempDir()
	winDir := windowstate.WindowsDirFor(dir)
	if err := os.MkdirAll(winDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"state":"working","mode":"adhoc","scheduled_by":"nightly-triage","scheduled_at":"2026-11-18T07:00:00Z"}`
	if err := os.WriteFile(filepath.Join(winDir, "@4.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	m := LoadMeta(Group{RuntimeDir: dir})["@4"]
	if m.ScheduledBy != "nightly-triage" || !m.ScheduledAt.Equal(firedAt) {
		t.Errorf("meta = %q %v", m.ScheduledBy, m.ScheduledAt)
	}
}

func TestToWindowScheduled(t *testing.T) {
	row := Row{}
	row.Name, row.WindowID, row.State = "triage", "@1", "working"
	tests := []struct {
		name     string
		meta     Meta
		wantBy   string
		wantAt   string
		wantJSON []string
		denyJSON []string
	}{
		{
			name:     "cron",
			meta:     Meta{Mode: "adhoc", ScheduledBy: "nightly-triage", ScheduledAt: firedAt},
			wantBy:   "nightly-triage",
			wantAt:   "2026-11-18T07:00:00Z",
			wantJSON: []string{`"scheduled_by":"nightly-triage"`, `"scheduled_at":"2026-11-18T07:00:00Z"`},
		},
		{
			name:     "not cron",
			meta:     Meta{Mode: "adhoc"},
			denyJSON: []string{"scheduled_by", "scheduled_at"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := ToWindow(row, tt.meta)
			if w.ScheduledBy != tt.wantBy || w.ScheduledAt != tt.wantAt {
				t.Errorf("got %q %q, want %q %q", w.ScheduledBy, w.ScheduledAt, tt.wantBy, tt.wantAt)
			}
			raw, err := json.Marshal(w)
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range tt.wantJSON {
				if !strings.Contains(string(raw), s) {
					t.Errorf("JSON missing %s: %s", s, raw)
				}
			}
			for _, s := range tt.denyJSON {
				if strings.Contains(string(raw), s) {
					t.Errorf("JSON has %s: %s", s, raw)
				}
			}
		})
	}
}
