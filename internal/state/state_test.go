package state

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	SetStatePathForTest(filepath.Join(dir, "state.json"))

	s := &State{
		Agents: []AgentState{
			{Name: "go-engineer", AgentName: "go-engineer", SessionID: "abc123", WindowID: "@1", State: "idle"},
			{Name: "go-engineer~2", AgentName: "go-engineer", SessionID: "def456", WindowID: "@2", State: "working"},
		},
		Timestamp: time.Now().Truncate(time.Millisecond),
	}

	if err := Save(s); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if loaded == nil {
		t.Fatal("Load returned nil, want state")
	}

	if len(loaded.Agents) != 2 {
		t.Fatalf("got %d agents, want 2", len(loaded.Agents))
	}
	if loaded.Agents[0].Name != "go-engineer" {
		t.Errorf("agents[0].Name = %q, want %q", loaded.Agents[0].Name, "go-engineer")
	}
	if loaded.Agents[1].SessionID != "def456" {
		t.Errorf("agents[1].SessionID = %q, want %q", loaded.Agents[1].SessionID, "def456")
	}
	if loaded.Agents[1].State != "working" {
		t.Errorf("agents[1].State = %q, want %q", loaded.Agents[1].State, "working")
	}
}

func TestLoadNonExistent(t *testing.T) {
	dir := t.TempDir()
	SetStatePathForTest(filepath.Join(dir, "does-not-exist.json"))

	s, err := Load()
	if err != nil {
		t.Fatalf("Load returned error for non-existent file: %v", err)
	}
	if s != nil {
		t.Errorf("Load returned non-nil state for non-existent file: %+v", s)
	}
}

func TestClear(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	SetStatePathForTest(path)

	s := &State{
		Agents:    []AgentState{{Name: "test", SessionID: "s1"}},
		Timestamp: time.Now(),
	}
	if err := Save(s); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("state file should exist after Save: %v", err)
	}

	if err := Clear(); err != nil {
		t.Fatalf("Clear error: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("state file should be gone after Clear, stat err = %v", err)
	}
}

func TestSnapshotByWindowID(t *testing.T) {
	dir := t.TempDir()
	SetStatePathForTest(filepath.Join(dir, "state.json"))

	s := &State{
		Agents: []AgentState{
			{Name: "alpha", SessionID: "s1", WindowID: "@10", State: "idle"},
			{Name: "beta", SessionID: "s2", WindowID: "@11", State: "working"},
		},
		Timestamp: time.Now(),
	}
	if err := Save(s); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	snap, err := LoadSnapshot()
	if err != nil {
		t.Fatalf("LoadSnapshot error: %v", err)
	}
	if snap == nil {
		t.Fatal("LoadSnapshot returned nil")
	}

	got := snap.ByWindowID("@10")
	if got == nil {
		t.Fatal("ByWindowID('@10') returned nil")
	}
	if got.Name != "alpha" {
		t.Errorf("ByWindowID('@10').Name = %q, want %q", got.Name, "alpha")
	}

	if snap.ByWindowID("@99") != nil {
		t.Error("ByWindowID('@99') should return nil for unknown window")
	}
}

func TestSnapshotByName(t *testing.T) {
	dir := t.TempDir()
	SetStatePathForTest(filepath.Join(dir, "state.json"))

	s := &State{
		Agents: []AgentState{
			{Name: "alpha", SessionID: "s1", WindowID: "@1"},
			{Name: "beta", SessionID: "s2", WindowID: "@2"},
		},
		Timestamp: time.Now(),
	}
	if err := Save(s); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	snap, err := LoadSnapshot()
	if err != nil {
		t.Fatalf("LoadSnapshot error: %v", err)
	}

	got := snap.ByName("beta")
	if got == nil {
		t.Fatal("ByName('beta') returned nil")
	}
	if got.SessionID != "s2" {
		t.Errorf("ByName('beta').SessionID = %q, want %q", got.SessionID, "s2")
	}

	if snap.ByName("nobody") != nil {
		t.Error("ByName('nobody') should return nil")
	}
}

func TestSnapshotIsStale(t *testing.T) {
	dir := t.TempDir()
	SetStatePathForTest(filepath.Join(dir, "state.json"))

	// Fresh timestamp — not stale.
	fresh := &State{
		Agents:    []AgentState{{Name: "a", SessionID: "s1", WindowID: "@1"}},
		Timestamp: time.Now(),
	}
	if err := Save(fresh); err != nil {
		t.Fatalf("Save error: %v", err)
	}
	snap, err := LoadSnapshot()
	if err != nil {
		t.Fatalf("LoadSnapshot error: %v", err)
	}
	if snap.IsStale(5 * time.Second) {
		t.Error("IsStale(5s) = true for fresh snapshot, want false")
	}

	// Old timestamp — stale.
	old := &State{
		Agents:    []AgentState{{Name: "a", SessionID: "s1", WindowID: "@1"}},
		Timestamp: time.Now().Add(-10 * time.Minute),
	}
	if err := Save(old); err != nil {
		t.Fatalf("Save error: %v", err)
	}
	snap2, err := LoadSnapshot()
	if err != nil {
		t.Fatalf("LoadSnapshot error: %v", err)
	}
	if !snap2.IsStale(5 * time.Second) {
		t.Error("IsStale(5s) = false for 10-minute-old snapshot, want true")
	}
}

func TestNilSnapshotSafe(t *testing.T) {
	var snap *Snapshot

	if snap.ByWindowID("@1") != nil {
		t.Error("nil.ByWindowID should return nil")
	}
	if snap.ByName("x") != nil {
		t.Error("nil.ByName should return nil")
	}
	if !snap.IsStale(time.Second) {
		t.Error("nil.IsStale should return true")
	}
	if snap.ActiveWindowID() != "" {
		t.Error("nil.ActiveWindowID should return empty string")
	}
	if snap.PrefixActive() {
		t.Error("nil.PrefixActive should return false")
	}
}

func TestNewAgentStateFieldsRoundtrip(t *testing.T) {
	dir := t.TempDir()
	SetStatePathForTest(filepath.Join(dir, "state.json"))

	s := &State{
		Agents: []AgentState{
			{
				Name:              "go-engineer",
				AgentName:         "go-engineer",
				SessionID:         "abc",
				WindowID:          "@1",
				State:             "working",
				CacheReadTokens:   8000,
				CacheCreateTokens: 2000,
				ModelName:         "claude-sonnet-4-20250514",
			},
		},
		Timestamp:      time.Now().Truncate(time.Millisecond),
		ActiveWindowID: "@1",
		PrefixActive:   true,
	}

	if err := Save(s); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if loaded == nil {
		t.Fatal("Load returned nil")
	}

	a := loaded.Agents[0]
	if a.CacheReadTokens != 8000 {
		t.Errorf("CacheReadTokens = %d, want 8000", a.CacheReadTokens)
	}
	if a.CacheCreateTokens != 2000 {
		t.Errorf("CacheCreateTokens = %d, want 2000", a.CacheCreateTokens)
	}
	if a.ModelName != "claude-sonnet-4-20250514" {
		t.Errorf("ModelName = %q, want 'claude-sonnet-4-20250514'", a.ModelName)
	}
	if loaded.ActiveWindowID != "@1" {
		t.Errorf("ActiveWindowID = %q, want '@1'", loaded.ActiveWindowID)
	}
	if !loaded.PrefixActive {
		t.Error("PrefixActive = false, want true")
	}
}

// TestAgentStateContextTokensAndWindowRoundtrip verifies the absolute-token
// fields (ContextTokens, ContextWindow) survive a Save/Load
// round-trip.
func TestAgentStateContextTokensAndWindowRoundtrip(t *testing.T) {
	dir := t.TempDir()
	SetStatePathForTest(filepath.Join(dir, "state.json"))

	s := &State{
		Agents: []AgentState{
			{
				Name:          "go-engineer",
				AgentName:     "go-engineer",
				WindowID:      "@1",
				State:         "working",
				ContextPct:    42,
				ContextTokens: 68_000,
				ContextWindow: 136_000,
			},
		},
		Timestamp: time.Now().Truncate(time.Millisecond),
	}

	if err := Save(s); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if loaded == nil {
		t.Fatal("Load returned nil")
	}

	a := loaded.Agents[0]
	if a.ContextTokens != 68_000 {
		t.Errorf("ContextTokens = %d, want 68000", a.ContextTokens)
	}
	if a.ContextWindow != 136_000 {
		t.Errorf("ContextWindow = %d, want 136000", a.ContextWindow)
	}
}

// TestAgentStateContextTokensAndWindowOmitEmpty verifies that ContextTokens
// and ContextWindow are omitted from the marshaled JSON when zero, so old
// readers of state.json (and diffs/log output) aren't cluttered by fields
// that are always absent for agents whose model isn't known yet.
func TestAgentStateContextTokensAndWindowOmitEmpty(t *testing.T) {
	a := AgentState{Name: "go-engineer", State: "idle"}

	data, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	for _, field := range []string{`"context_tokens"`, `"context_window"`} {
		if bytes.Contains(data, []byte(field)) {
			t.Errorf("marshaled AgentState should omit zero-value %s; got: %s", field, data)
		}
	}
}

func TestSnapshotActiveWindowIDAndPrefixActive(t *testing.T) {
	dir := t.TempDir()
	SetStatePathForTest(filepath.Join(dir, "state.json"))

	s := &State{
		Agents: []AgentState{
			{Name: "alpha", SessionID: "s1", WindowID: "@5"},
		},
		Timestamp:      time.Now(),
		ActiveWindowID: "@5",
		PrefixActive:   true,
	}
	if err := Save(s); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	snap, err := LoadSnapshot()
	if err != nil {
		t.Fatalf("LoadSnapshot error: %v", err)
	}
	if snap == nil {
		t.Fatal("LoadSnapshot returned nil")
	}

	if got := snap.ActiveWindowID(); got != "@5" {
		t.Errorf("ActiveWindowID() = %q, want '@5'", got)
	}
	if !snap.PrefixActive() {
		t.Error("PrefixActive() = false, want true")
	}
}
