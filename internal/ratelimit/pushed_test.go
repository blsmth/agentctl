package ratelimit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveLoadPushed_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	original := &PushedRateLimits{
		UpdatedAt: time.Now().Unix(),
		Source:    "statusline",
		FiveHour:  &PushedWindow{UsedPercentage: 23.5, ResetsAt: 1738425600},
		SevenDay:  &PushedWindow{UsedPercentage: 41.2, ResetsAt: 1738857600},
	}

	if err := SavePushed(original); err != nil {
		t.Fatalf("SavePushed: %v", err)
	}

	loaded, err := LoadPushed()
	if err != nil {
		t.Fatalf("LoadPushed: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected non-nil PushedRateLimits")
	}
	if loaded.FiveHour == nil || loaded.FiveHour.UsedPercentage != 23.5 {
		t.Errorf("FiveHour = %+v, want UsedPercentage 23.5", loaded.FiveHour)
	}
	if loaded.SevenDay == nil || loaded.SevenDay.ResetsAt != 1738857600 {
		t.Errorf("SevenDay = %+v, want ResetsAt 1738857600", loaded.SevenDay)
	}
	// spend_limit was never set -- must round-trip as absent, not zero.
	if loaded.SpendLimit != nil {
		t.Errorf("SpendLimit = %+v, want nil (absent, not zero)", loaded.SpendLimit)
	}
}

func TestLoadPushed_MissingFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	loaded, err := LoadPushed()
	if err != nil {
		t.Fatalf("expected no error for missing file, got: %v", err)
	}
	if loaded != nil {
		t.Errorf("expected nil for missing file, got %+v", loaded)
	}
}

func TestSavePushed_WritesAtomically(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	if err := SavePushed(&PushedRateLimits{UpdatedAt: 1, Source: "statusline"}); err != nil {
		t.Fatalf("SavePushed: %v", err)
	}

	path := filepath.Join(dir, ".local", "state", "agentctl", "ratelimits.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file at %s: %v", path, err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("expected tmp file to be renamed away, got err=%v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading written file: %v", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
}

func TestPushedRateLimits_Fresh(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)

	tests := []struct {
		name string
		pr   *PushedRateLimits
		want bool
	}{
		{"nil receiver", nil, false},
		{
			"no five_hour window",
			&PushedRateLimits{UpdatedAt: now.Unix(), SevenDay: &PushedWindow{}},
			false,
		},
		{
			"fresh with five_hour",
			&PushedRateLimits{UpdatedAt: now.Add(-1 * time.Minute).Unix(), FiveHour: &PushedWindow{}},
			true,
		},
		{
			"exactly at freshness boundary",
			&PushedRateLimits{UpdatedAt: now.Add(-pushedFreshness).Unix(), FiveHour: &PushedWindow{}},
			true,
		},
		{
			"stale beyond freshness window",
			&PushedRateLimits{UpdatedAt: now.Add(-pushedFreshness - time.Minute).Unix(), FiveHour: &PushedWindow{}},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.pr.Fresh(now); got != tt.want {
				t.Errorf("Fresh() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestSavePushed_DoesNotFollowPlantedTempSymlink: the old fixed
// "ratelimits.json.tmp" name could be pre-created as a symlink.
func TestSavePushed_DoesNotFollowPlantedTempSymlink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	path, err := pushedRateLimitsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, path+".tmp"); err != nil {
		t.Fatal(err)
	}
	if err := SavePushed(&PushedRateLimits{UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(victim); string(got) != "original" {
		t.Errorf("victim modified: %q", got)
	}
}
