package ratelimit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ── windowTokenScanner / scanWindowTokens tests ──────────────────────────────

// writeJSONLEntry writes a single JSONL entry to w with the given timestamp
// and input/output/cache token counts.
func writeJSONLEntry(t *testing.T, f *os.File, ts time.Time, inputTokens, outputTokens, cacheRead, cacheCreate int) {
	t.Helper()
	type usage struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	}
	type message struct {
		Usage usage `json:"usage"`
	}
	type entry struct {
		Timestamp string  `json:"timestamp"`
		Message   message `json:"message"`
	}
	e := entry{
		Timestamp: ts.Format(time.RFC3339Nano),
		Message: message{Usage: usage{
			InputTokens:              inputTokens,
			OutputTokens:             outputTokens,
			CacheReadInputTokens:     cacheRead,
			CacheCreationInputTokens: cacheCreate,
		}},
	}
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal entry: %v", err)
	}
	fmt.Fprintf(f, "%s\n", data)
}

// TestScanWindowTokens verifies that only entries within the window are counted.
func TestScanWindowTokens(t *testing.T) {
	// Set HOME to a temp dir so the scanner finds our test files.
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	projectDir := filepath.Join(dir, ".claude", "projects", "myproject")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	windowStart := time.Now().Add(-2 * time.Hour)

	f, err := os.Create(filepath.Join(projectDir, "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// In-window entry: 5000 input + 1000 output = 6000.
	writeJSONLEntry(t, f, time.Now().Add(-1*time.Hour), 5000, 1000, 0, 0)
	// Out-of-window entry (3h ago, before windowStart of 2h ago): should be excluded.
	writeJSONLEntry(t, f, time.Now().Add(-3*time.Hour), 10000, 2000, 0, 0)
	f.Close()

	got := scanWindowTokens(windowStart)
	if got != 6000 {
		t.Errorf("scanWindowTokens() = %d, want 6000", got)
	}
}

// TestScanWindowTokens_SkipsOldFiles verifies that files with ModTime older than
// windowStart are skipped entirely (no reads, no counts).
func TestScanWindowTokens_SkipsOldFiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	projectDir := filepath.Join(dir, ".claude", "projects", "oldproject")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(projectDir, "old.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writeJSONLEntry(t, f, time.Now().Add(-30*time.Minute), 9999, 9999, 0, 0)
	f.Close()

	// Set the file's ModTime to 6 hours ago, before our windowStart.
	oldTime := time.Now().Add(-6 * time.Hour)
	if err := os.Chtimes(path, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	windowStart := time.Now().Add(-5 * time.Hour)
	got := scanWindowTokens(windowStart)
	if got != 0 {
		t.Errorf("scanWindowTokens() = %d, want 0 (old file should be skipped)", got)
	}
}

// TestScanWindowTokens_SubagentFiles verifies that tokens in subagent JSONL
// files are included in the window total.
func TestScanWindowTokens_SubagentFiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	// Subagent path: ~/.claude/projects/{project}/{session}/subagents/*.jsonl
	subagentDir := filepath.Join(dir, ".claude", "projects", "proj1", "sess-abc", "subagents")
	if err := os.MkdirAll(subagentDir, 0o755); err != nil {
		t.Fatal(err)
	}

	windowStart := time.Now().Add(-2 * time.Hour)

	f, err := os.Create(filepath.Join(subagentDir, "sub1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	writeJSONLEntry(t, f, time.Now().Add(-30*time.Minute), 3000, 500, 0, 0)
	f.Close()

	got := scanWindowTokens(windowStart)
	if got != 3500 {
		t.Errorf("scanWindowTokens() = %d, want 3500 (subagent tokens)", got)
	}
}

// TestScanWindowTokens_ExcludesCache verifies that cache tokens are NOT counted.
func TestScanWindowTokens_ExcludesCache(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	projectDir := filepath.Join(dir, ".claude", "projects", "cachetest")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	windowStart := time.Now().Add(-2 * time.Hour)

	f, err := os.Create(filepath.Join(projectDir, "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// 1000 input + 200 output + 50000 cache read + 10000 cache create.
	// Only 1000 + 200 = 1200 should be counted.
	writeJSONLEntry(t, f, time.Now().Add(-1*time.Hour), 1000, 200, 50000, 10000)
	f.Close()

	got := scanWindowTokens(windowStart)
	if got != 1200 {
		t.Errorf("scanWindowTokens() = %d, want 1200 (cache excluded)", got)
	}
}

// TestScanWindowTokens_EmptyDir verifies that an empty projects dir returns 0.
func TestScanWindowTokens_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	if err := os.MkdirAll(filepath.Join(dir, ".claude", "projects"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := scanWindowTokens(time.Now().Add(-5 * time.Hour))
	if got != 0 {
		t.Errorf("scanWindowTokens() = %d, want 0 for empty dir", got)
	}
}

// TestWindowTokenScannerTick verifies that WindowTokenScanner.Tick() returns 0
// initially and then reflects a completed scan result.
func TestWindowTokenScannerTick(t *testing.T) {
	s := NewWindowTokenScanner()
	// Set lastScan to long ago to allow a scan.
	s.lastScan = time.Now().Add(-10 * time.Minute)

	windowStart := time.Now().Add(-5 * time.Hour)

	// First tick: should return 0 (no scan completed yet) and kick off a scan.
	// Set HOME to temp dir with empty projects dir so the scan returns 0 quickly.
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	os.MkdirAll(filepath.Join(dir, ".claude", "projects"), 0o755) //nolint:errcheck

	got := s.Tick(windowStart)
	if got != 0 {
		t.Errorf("first tick = %d, want 0 (scan not yet complete)", got)
	}

	// Wait for the background scan to complete.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case result := <-s.resultCh:
			s.mu.Lock()
			s.windowTokens = result
			s.scanning = false
			s.mu.Unlock()
			// Verify we can call Tick again safely.
			got2 := s.Tick(windowStart)
			_ = got2 // result may be 0 for empty dir; just verify no panic
			return
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	// If no result came within 3s, that's a failure.
	t.Error("WindowTokenScanner: background scan did not complete within 3s")
}

// ── WindowTokenScanner laziness ───────────────────────────────────

// TestWindowTokenScannerTick_SkipsScanWhenPushedFresh verifies that Tick
// starts zero scans when the injected freshness check reports fresh pushed
// data -- the whole point of retiring this scanner to a fallback role.
func TestWindowTokenScannerTick_SkipsScanWhenPushedFresh(t *testing.T) {
	s := NewWindowTokenScanner()
	s.lastScan = time.Now().Add(-10 * time.Minute) // would otherwise be eligible to scan
	s.pushedFresh = func() bool { return true }

	got := s.Tick(time.Now().Add(-5 * time.Hour))
	if got != 0 {
		t.Errorf("Tick() = %d, want 0 (no scan ever ran, cache starts empty)", got)
	}

	// No scan should have been kicked off at all -- resultCh must stay empty.
	select {
	case v := <-s.resultCh:
		t.Fatalf("a scan ran despite fresh pushed data (resultCh delivered %d)", v)
	case <-time.After(200 * time.Millisecond):
		// expected: nothing arrives.
	}
}

// TestWindowTokenScannerTick_ScansWhenPushedNotFresh verifies scan behavior
// is unchanged (a scan starts) when the injected freshness check reports no
// fresh pushed data -- the same assertion TestWindowTokenScannerTick already
// makes, but via the injected hook rather than the real filesystem, so this
// test doesn't depend on HOME/LoadPushed at all.
func TestWindowTokenScannerTick_ScansWhenPushedNotFresh(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	if err := os.MkdirAll(filepath.Join(dir, ".claude", "projects"), 0o755); err != nil {
		t.Fatal(err)
	}

	s := NewWindowTokenScanner()
	s.lastScan = time.Now().Add(-10 * time.Minute)
	s.pushedFresh = func() bool { return false }

	s.Tick(time.Now().Add(-5 * time.Hour))

	select {
	case result := <-s.resultCh:
		// Empty projects dir -> 0 tokens, but the point is a scan ran at all.
		if result != 0 {
			t.Errorf("scan result = %d, want 0 for empty projects dir", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("expected a scan to start when pushed data is not fresh")
	}
}

func TestRateLimitProberInitial(t *testing.T) {
	p := NewRateLimitProber()
	// Isolate from the real filesystem: a real ratelimits.json on the host
	// would otherwise make this test depend on host state.
	p.loadPushed = func() (*PushedRateLimits, error) { return nil, nil }
	if info := p.Tick(); info != nil {
		t.Errorf("expected nil on first tick, got %+v", info)
	}
}

// ── RateLimitProber gating ───────────────────────────────────────────

// TestRateLimitProberGating verifies that Tick returns info only when pushed
// statusLine data is fresh and carries a five_hour window, and nil otherwise
// (the display is simply absent -- there is no probe fallback).
func TestRateLimitProberGating(t *testing.T) {
	fixedNow := time.Unix(2_000_000_000, 0)

	tests := []struct {
		name     string
		pushed   *PushedRateLimits
		loadErr  error
		wantInfo bool
	}{
		{
			name: "fresh pushed data with five_hour",
			pushed: &PushedRateLimits{
				UpdatedAt: fixedNow.Add(-1 * time.Minute).Unix(),
				FiveHour:  &PushedWindow{UsedPercentage: 10, ResetsAt: fixedNow.Add(time.Hour).Unix()},
			},
			wantInfo: true,
		},
		{
			name: "stale pushed data",
			pushed: &PushedRateLimits{
				UpdatedAt: fixedNow.Add(-30 * time.Minute).Unix(),
				FiveHour:  &PushedWindow{UsedPercentage: 10, ResetsAt: fixedNow.Add(time.Hour).Unix()},
			},
		},
		{name: "missing pushed file"},
		{
			name: "pushed data present but no five_hour",
			pushed: &PushedRateLimits{
				UpdatedAt: fixedNow.Add(-1 * time.Minute).Unix(),
				SevenDay:  &PushedWindow{UsedPercentage: 5, ResetsAt: fixedNow.Add(24 * time.Hour).Unix()},
			},
		},
		{name: "loader error treated as no data", loadErr: fmt.Errorf("boom")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewRateLimitProber()
			p.now = func() time.Time { return fixedNow }
			p.loadPushed = func() (*PushedRateLimits, error) { return tt.pushed, tt.loadErr }

			if got := p.Tick(); (got != nil) != tt.wantInfo {
				t.Errorf("Tick() = %+v, wantInfo %v", got, tt.wantInfo)
			}
		})
	}
}

// TestRateLimitProberGating_UsesPushedInfo verifies that fresh pushed data
// is actually surfaced through Tick's return value (not just used to
// suppress the probe).
func TestRateLimitProberGating_UsesPushedInfo(t *testing.T) {
	fixedNow := time.Unix(2_000_000_000, 0)
	resetsAt := fixedNow.Add(3 * time.Hour).Unix()

	p := NewRateLimitProber()
	p.now = func() time.Time { return fixedNow }
	p.loadPushed = func() (*PushedRateLimits, error) {
		return &PushedRateLimits{
			UpdatedAt: fixedNow.Add(-1 * time.Minute).Unix(),
			FiveHour:  &PushedWindow{UsedPercentage: 42, ResetsAt: resetsAt},
		}, nil
	}

	info := p.Tick()
	if info == nil {
		t.Fatal("expected non-nil info from pushed data")
	}
	if info.ResetsAt != resetsAt {
		t.Errorf("ResetsAt = %d, want %d", info.ResetsAt, resetsAt)
	}
	if info.RateLimitType != "five_hour" {
		t.Errorf("RateLimitType = %q, want %q", info.RateLimitType, "five_hour")
	}
	// The pushed percentage (and Source marker) must actually flow
	// through Tick's return value -- this is what monitor.go's persistState
	// uses instead of the windowTokens/tokenCap estimate.
	if info.Source != "statusline" {
		t.Errorf("Source = %q, want statusline", info.Source)
	}
	if info.FiveHourUsedPct == nil || *info.FiveHourUsedPct != 42 {
		t.Errorf("FiveHourUsedPct = %v, want 42", info.FiveHourUsedPct)
	}
}

// ── pushedToInfo ──────────────────────────────────────────────────

func TestPushedToInfo(t *testing.T) {
	tests := []struct {
		name         string
		pr           *PushedRateLimits
		wantNil      bool
		wantFiveHour float64
		wantSevenDay *float64
	}{
		{
			name:    "nil input",
			pr:      nil,
			wantNil: true,
		},
		{
			name:    "no five_hour window",
			pr:      &PushedRateLimits{SevenDay: &PushedWindow{UsedPercentage: 9}},
			wantNil: true,
		},
		{
			name:         "five_hour only",
			pr:           &PushedRateLimits{FiveHour: &PushedWindow{UsedPercentage: 12, ResetsAt: 100}},
			wantFiveHour: 12,
		},
		{
			name: "five_hour and seven_day both present",
			pr: &PushedRateLimits{
				FiveHour: &PushedWindow{UsedPercentage: 12, ResetsAt: 100},
				SevenDay: &PushedWindow{UsedPercentage: 9, ResetsAt: 200},
			},
			wantFiveHour: 12,
			wantSevenDay: f64Ptr(9),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := pushedToInfo(tt.pr)
			if tt.wantNil {
				if info != nil {
					t.Fatalf("pushedToInfo() = %+v, want nil", info)
				}
				return
			}
			if info == nil {
				t.Fatal("pushedToInfo() = nil, want non-nil")
			}
			if info.Source != "statusline" {
				t.Errorf("Source = %q, want statusline", info.Source)
			}
			if info.FiveHourUsedPct == nil || *info.FiveHourUsedPct != tt.wantFiveHour {
				t.Errorf("FiveHourUsedPct = %v, want %v", info.FiveHourUsedPct, tt.wantFiveHour)
			}
			if tt.wantSevenDay == nil {
				if info.SevenDayUsedPct != nil {
					t.Errorf("SevenDayUsedPct = %v, want nil", info.SevenDayUsedPct)
				}
			} else {
				if info.SevenDayUsedPct == nil || *info.SevenDayUsedPct != *tt.wantSevenDay {
					t.Errorf("SevenDayUsedPct = %v, want %v", info.SevenDayUsedPct, *tt.wantSevenDay)
				}
			}
		})
	}
}

// f64Ptr is a small helper for building *float64 literals in table tests.
func f64Ptr(f float64) *float64 { return &f }
