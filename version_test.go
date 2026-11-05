package main

import (
	"runtime/debug"
	"testing"
)

func TestFormatVersion(t *testing.T) {
	buildInfo := &debug.BuildInfo{
		Main: debug.Module{Version: "v1.2.3"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "0123456789abcdef"},
			{Key: "vcs.time", Value: "2026-10-01T00:00:00Z"},
			{Key: "vcs.modified", Value: "false"},
		},
	}
	dirty := &debug.BuildInfo{
		Main: debug.Module{Version: "(devel)"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "0123456789abcdef"},
			{Key: "vcs.modified", Value: "true"},
		},
	}
	tests := []struct {
		name    string
		v, c, d string
		info    *debug.BuildInfo
		want    string
	}{
		{"ldflags set ignores build info", "1.0.0", "abc1234", "2026-01-01", buildInfo, "agentctl 1.0.0 (abc1234, 2026-01-01)"},
		{"no ldflags, no build info", "dev", "none", "unknown", nil, "agentctl dev (none, unknown)"},
		{"no ldflags, build info", "dev", "none", "unknown", buildInfo, "agentctl v1.2.3 (0123456, 2026-10-01T00:00:00Z)"},
		{"devel module version stays dev; dirty", "dev", "none", "unknown", dirty, "agentctl dev (0123456-dirty, unknown)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatVersion(tt.v, tt.c, tt.d, tt.info); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestVersionString_UsesPackageVars(t *testing.T) {
	oldV, oldC, oldD := version, commit, date
	t.Cleanup(func() { version, commit, date = oldV, oldC, oldD })
	version, commit, date = "9.9.9", "deadbee", "2026-02-02"
	if got, want := versionString(), "agentctl 9.9.9 (deadbee, 2026-02-02)"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
