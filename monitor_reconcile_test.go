package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// newReconcileMonitor returns a monitor tracking one live window "@7" with
// windowstate on disk.
func newReconcileMonitor(t *testing.T) (*monitorModel, string) {
	t.Helper()
	dir := t.TempDir()
	windowstate.SetDirForTest(dir)
	t.Cleanup(func() { windowstate.SetDirForTest("") })
	if err := windowstate.Update("@7", func(ws *windowstate.WindowState) { ws.State = "idle" }); err != nil {
		t.Fatal(err)
	}
	m := &monitorModel{windows: map[string]*instanceState{
		"@7": {instance: "shell", base: "shell"},
	}}
	return m, dir
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestReconcileListed_ListErrorRemovesNothing(t *testing.T) {
	m, dir := newReconcileMonitor(t)
	for i := 0; i < 5; i++ {
		if m.reconcileListed(&config.Config{}, nil, errors.New("tmux down"), 0) {
			t.Fatal("reconcileListed reported ok on list error")
		}
	}
	if _, ok := m.windows["@7"]; !ok {
		t.Error("window dropped on list error")
	}
	if !exists(filepath.Join(dir, "@7.json")) || exists(filepath.Join(dir, "@7.gone")) {
		t.Error("state removed or tombstoned on list error")
	}
}

func TestReconcileListed_SingleTickAbsenceRemovesNothing(t *testing.T) {
	m, dir := newReconcileMonitor(t)
	live := []tmux.WindowInfo{{ID: "@7", Name: "shell"}}

	m.reconcileListed(&config.Config{}, nil, nil, 0)  // absent once
	m.reconcileListed(&config.Config{}, live, nil, 0) // back: counter resets
	m.reconcileListed(&config.Config{}, nil, nil, 0)  // absent once again

	if _, ok := m.windows["@7"]; !ok {
		t.Error("window dropped after non-consecutive absences")
	}
	if !exists(filepath.Join(dir, "@7.json")) || exists(filepath.Join(dir, "@7.gone")) {
		t.Error("state removed or tombstoned after single-tick absence")
	}
}

func TestReconcileListed_TwoTickAbsenceRemovesAndTombstones(t *testing.T) {
	m, dir := newReconcileMonitor(t)
	m.reconcileListed(&config.Config{}, nil, nil, 0)
	m.reconcileListed(&config.Config{}, nil, nil, 0)

	if _, ok := m.windows["@7"]; ok {
		t.Error("window still tracked after two absent ticks")
	}
	if exists(filepath.Join(dir, "@7.json")) || !exists(filepath.Join(dir, "@7.gone")) {
		t.Error("state not removed/tombstoned after two absent ticks")
	}
}
