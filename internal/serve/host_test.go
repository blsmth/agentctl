package serve

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/config"
)

type fakeHost struct{ calls int }

func (f *fakeHost) Collect(context.Context) HostStats {
	f.calls++
	cpu := 42.5
	return HostStats{
		System: HostSystem{Hostname: "mac", OS: "darwin", Arch: "arm64", Cores: 8, CPUPercent: &cpu,
			Load: &LoadAvg{L1: 9, L5: 5, L15: 2}, MemoryPressure: "warn"},
		Processes: ProcessStats{
			Groups: []ProcessGroup{{Kind: "agentctl chrome-bar", Count: 62, CPUPercent: 400}},
			Top:    []ProcessInfo{{Name: "agentctl", PID: 7, CPUPercent: 12}},
		},
	}
}

func newHostEnv(t *testing.T, winErr error) (*testServer, string, *fakeHost, *int) {
	t.Helper()
	fh := &fakeHost{}
	wins := 0
	ts := newTestServerWith(t, config.ServeConfig{}, func(o *Options) {
		o.Host = fh
		o.TmuxWindows = func() (int, error) { wins++; return 5, winErr }
	})
	_, tok := ts.pairDevice(t, "phone")
	return ts, tok, fh, &wins
}

func TestHostRouteShape(t *testing.T) {
	ts, tok, _, _ := newHostEnv(t, nil)
	w := do(ts.Handler(), "GET", "/v1/host", withCookie(tok))
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var got struct {
		ServerTime string `json:"server_time"`
		Host       struct {
			Hostname       string   `json:"hostname"`
			Cores          int      `json:"cores"`
			CPUPercent     *float64 `json:"cpu_percent"`
			MemoryPressure string   `json:"memory_pressure"`
			Memory         any      `json:"memory"`
		} `json:"host"`
		Agentctl struct {
			Version     string `json:"version"`
			TmuxWindows *int   `json:"tmux_windows"`
		} `json:"agentctl"`
		Processes struct {
			Groups []ProcessGroup `json:"groups"`
			Top    []ProcessInfo  `json:"top"`
		} `json:"processes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if _, err := time.Parse(time.RFC3339Nano, got.ServerTime); err != nil {
		t.Errorf("server_time: %v", err)
	}
	if got.Host.Hostname != "mac" || got.Host.Cores != 8 || got.Host.CPUPercent == nil || got.Host.MemoryPressure != "warn" {
		t.Errorf("host = %+v", got.Host)
	}
	if got.Host.Memory != nil {
		t.Errorf("missing memory must be omitted, got %v", got.Host.Memory)
	}
	if got.Agentctl.Version != "test" || got.Agentctl.TmuxWindows == nil || *got.Agentctl.TmuxWindows != 5 {
		t.Errorf("agentctl = %+v", got.Agentctl)
	}
	if len(got.Processes.Groups) != 1 || got.Processes.Groups[0].Count != 62 || len(got.Processes.Top) != 1 {
		t.Errorf("processes = %+v", got.Processes)
	}
}

func TestHostRouteRequiresDevice(t *testing.T) {
	ts, _, fh, _ := newHostEnv(t, nil)
	if w := do(ts.Handler(), "GET", "/v1/host"); w.Code != 401 {
		t.Errorf("status %d", w.Code)
	}
	if fh.calls != 0 {
		t.Error("collected for an unauthenticated request")
	}
}

func TestHostWindowCountCachedAndOptional(t *testing.T) {
	ts, tok, _, wins := newHostEnv(t, nil)
	for range 3 {
		do(ts.Handler(), "GET", "/v1/host", withCookie(tok))
	}
	if *wins != 1 {
		t.Errorf("tmux counted %d times within the TTL", *wins)
	}
	ts.clock.Advance(hostCacheTTL + time.Second)
	do(ts.Handler(), "GET", "/v1/host", withCookie(tok))
	if *wins != 2 {
		t.Errorf("tmux counted %d times after the TTL", *wins)
	}

	ts2, tok2, _, _ := newHostEnv(t, errors.New("no tmux"))
	w := do(ts2.Handler(), "GET", "/v1/host", withCookie(tok2))
	var m map[string]map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	if _, ok := m["agentctl"]["tmux_windows"]; ok {
		t.Error("tmux_windows must be omitted when tmux fails")
	}
}

func TestLiveHostCachesWithinTTL(t *testing.T) {
	clock := newClock()
	l := newLiveHost(clock.Now)
	first := l.Collect(context.Background())
	if first.System.Cores == 0 || first.System.OS == "" {
		t.Errorf("live stats missing basics: %+v", first.System)
	}
	l.mu.Lock()
	at := l.at
	l.mu.Unlock()
	l.Collect(context.Background())
	l.mu.Lock()
	same := l.at.Equal(at)
	l.mu.Unlock()
	if !same {
		t.Error("second call inside the TTL recollected")
	}
	clock.Advance(hostCacheTTL + time.Second)
	l.Collect(context.Background())
	l.mu.Lock()
	moved := l.at.After(at)
	l.mu.Unlock()
	if !moved {
		t.Error("call after the TTL did not recollect")
	}
}

func TestClassifyProcess(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"claude", nil, "claude"},
		{"agentctl", []string{"agentctl", "chrome-bar"}, "agentctl chrome-bar"},
		{"agentctl", []string{"/x/agentctl", "--debug", "serve", "run"}, "agentctl serve"},
		{"agentctl", []string{"agentctl", "hub-view"}, "agentctl hub-view"},
		{"agentctl", []string{"agentctl", "reconcile"}, "agentctl other"},
		{"agentctl", nil, "agentctl other"},
		{"2.1.45", []string{"/Users/x/.local/share/claude/versions/2.1.45"}, "claude"},
		{"2.1.45", []string{"/usr/bin/other"}, ""},
		{"tmux", nil, ""},
		{"Safari", nil, ""},
	}
	for _, tt := range tests {
		if got := classifyProcess(tt.name, tt.args); got != tt.want {
			t.Errorf("%s %v = %q, want %q", tt.name, tt.args, got, tt.want)
		}
	}
}
