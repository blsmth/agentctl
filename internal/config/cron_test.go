package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestCronConfigDefaults(t *testing.T) {
	tests := []struct {
		name string
		cfg  CronConfig
		want struct {
			enabled bool
			minInt  time.Duration
			maxConc int
			catchup time.Duration
			usage   CronUsageThresholds
		}
	}{
		{
			name: "zero value",
			want: struct {
				enabled bool
				minInt  time.Duration
				maxConc int
				catchup time.Duration
				usage   CronUsageThresholds
			}{true, 15 * time.Minute, 2, 6 * time.Hour, CronUsageThresholds{FiveHour: 80, SevenDay: 90}},
		},
		{
			name: "explicit values",
			cfg: CronConfig{
				Enabled: boolPtr(false), MinInterval: "1h", MaxConcurrent: 5, CatchupWindow: "30m",
				SkipIfUsageAbove: CronUsageThresholds{FiveHour: 50},
			},
			want: struct {
				enabled bool
				minInt  time.Duration
				maxConc int
				catchup time.Duration
				usage   CronUsageThresholds
			}{false, time.Hour, 5, 30 * time.Minute, CronUsageThresholds{FiveHour: 50, SevenDay: 90}},
		},
		{
			name: "invalid durations fall back",
			cfg:  CronConfig{MinInterval: "soon", CatchupWindow: "-1h", MaxConcurrent: -3},
			want: struct {
				enabled bool
				minInt  time.Duration
				maxConc int
				catchup time.Duration
				usage   CronUsageThresholds
			}{true, 15 * time.Minute, 2, 6 * time.Hour, CronUsageThresholds{FiveHour: 80, SevenDay: 90}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := tt.cfg
			if got := c.EnabledOrDefault(); got != tt.want.enabled {
				t.Errorf("EnabledOrDefault = %v", got)
			}
			if got := c.EffectiveMinInterval(); got != tt.want.minInt {
				t.Errorf("EffectiveMinInterval = %v", got)
			}
			if got := c.EffectiveMaxConcurrent(); got != tt.want.maxConc {
				t.Errorf("EffectiveMaxConcurrent = %v", got)
			}
			if got := c.EffectiveCatchupWindow(); got != tt.want.catchup {
				t.Errorf("EffectiveCatchupWindow = %v", got)
			}
			if got := c.EffectiveUsageThresholds(); got != tt.want.usage {
				t.Errorf("EffectiveUsageThresholds = %v", got)
			}
		})
	}
}

func boolPtr(b bool) *bool { return &b }

// A project-local config must never be able to configure scheduling: the
// cron block is stripped and a stray "schedules" key has nowhere to land.
func TestProjectLayerCronIgnored(t *testing.T) {
	const global = `{"cron": {"enabled": false, "max_concurrent": 1}}`
	const project = `{
		"cron": {"enabled": true, "max_concurrent": 50, "min_interval": "1m"},
		"schedules": {"jobs": [{"name": "evil", "schedule": "* * * * *"}]}
	}`
	dir := t.TempDir()
	gp, pp := filepath.Join(dir, "g.json"), filepath.Join(dir, "p.json")
	if err := os.WriteFile(gp, []byte(global), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pp, []byte(project), 0o644); err != nil {
		t.Fatal(err)
	}
	setTestPaths(t, pp, gp)
	cfg := Load()
	want := CronConfig{Enabled: boolPtr(false), MaxConcurrent: 1}
	if !reflect.DeepEqual(cfg.Cron, want) {
		t.Errorf("Cron = %+v, want global %+v", cfg.Cron, want)
	}

	// Saving through the project layer must not write cron there either.
	cfg.Cron.MaxConcurrent = 9
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	layer, err := readLayer(pp)
	if err != nil {
		t.Fatal(err)
	}
	if layer.Cron.MaxConcurrent == 9 {
		t.Error("cron saved into the project-local layer")
	}
}
