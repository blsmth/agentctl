package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/blsmth/agentctl/internal/claude"
	"github.com/blsmth/agentctl/internal/metrics"
	"github.com/blsmth/agentctl/internal/watcher"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// writeAgentLog creates <projectsDir>/<slug>/<sessionID>.jsonl whose first
// line carries agentSetting=agent, and returns its path.
func writeAgentLog(t *testing.T, projectsDir, slug, sessionID, agent string) string {
	t.Helper()
	dir := filepath.Join(projectsDir, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, sessionID+".jsonl")
	if err := os.WriteFile(p, []byte(`{"agentSetting":"`+agent+`"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func newLinkLogsModel(t *testing.T, projectsDir string) *monitorModel {
	t.Helper()
	windowstate.SetDirForTest(t.TempDir())
	t.Cleanup(func() { windowstate.SetDirForTest("") })
	return &monitorModel{
		windows:          make(map[string]*instanceState),
		watcher:          watcher.New(),
		metricsCollector: metrics.NewCollector(prometheus.NewRegistry()),
		projectsDir:      projectsDir,
	}
}

func TestLinkLogs(t *testing.T) {
	const (
		agent     = "lead"
		windowID  = "@78"
		mySession = "f2cbcc84-0000-0000-0000-000000000001"
		other     = "bbc0bf67-0000-0000-0000-000000000002"
		now       = int64(1_000_000)
	)
	myPane := "/Users/x/src/acme/widgetco"
	mySlug := claude.ProjectSlug(myPane)

	tests := []struct {
		name string
		// setup returns the initial inst.logPath, the windowstate to use and
		// the expected inst.logPath afterwards.
		run func(t *testing.T, proj string) (inst *instanceState, ws windowstate.WindowState, want string)
	}{
		{
			name: "session id set: newer same-agent log in another project does not rotate",
			run: func(t *testing.T, proj string) (*instanceState, windowstate.WindowState, string) {
				mine := writeAgentLog(t, proj, mySlug, mySession, agent)
				writeAgentLog(t, proj, "-Users-x-src-acme-other", other, agent)
				inst := &instanceState{instance: agent, base: agent, logPath: mine, lastChange: now - 100}
				return inst, windowstate.WindowState{SessionID: mySession, LogPath: mine}, mine
			},
		},
		{
			name: "session id set, empty log path: relinks via session id",
			run: func(t *testing.T, proj string) (*instanceState, windowstate.WindowState, string) {
				mine := writeAgentLog(t, proj, mySlug, mySession, agent)
				writeAgentLog(t, proj, "-Users-x-src-acme-other", other, agent)
				inst := &instanceState{instance: agent, base: agent}
				return inst, windowstate.WindowState{SessionID: mySession}, mine
			},
		},
		{
			name: "session id set, unknown session: nothing linked, no scan",
			run: func(t *testing.T, proj string) (*instanceState, windowstate.WindowState, string) {
				writeAgentLog(t, proj, mySlug, other, agent)
				inst := &instanceState{instance: agent, base: agent}
				return inst, windowstate.WindowState{SessionID: mySession}, ""
			},
		},
		{
			name: "poisoned inst.logPath is reset and relinked from session id",
			run: func(t *testing.T, proj string) (*instanceState, windowstate.WindowState, string) {
				mine := writeAgentLog(t, proj, mySlug, mySession, agent)
				bad := writeAgentLog(t, proj, "-Users-x-src-acme", other, agent)
				inst := &instanceState{instance: agent, base: agent, logPath: bad, lastChange: now}
				return inst, windowstate.WindowState{SessionID: mySession}, mine
			},
		},
		{
			name: "no session id: scan is limited to the window's project dir",
			run: func(t *testing.T, proj string) (*instanceState, windowstate.WindowState, string) {
				mine := writeAgentLog(t, proj, mySlug, mySession, agent)
				writeAgentLog(t, proj, "-Users-x-src-acme-other", other, agent)
				inst := &instanceState{instance: agent, base: agent}
				return inst, windowstate.WindowState{}, mine
			},
		},
		{
			name: "no session id: log only in another project is not linked",
			run: func(t *testing.T, proj string) (*instanceState, windowstate.WindowState, string) {
				writeAgentLog(t, proj, "-Users-x-src-acme-other", other, agent)
				inst := &instanceState{instance: agent, base: agent}
				return inst, windowstate.WindowState{}, ""
			},
		},
		{
			name: "no session id: worktree dir takes precedence over pane path",
			run: func(t *testing.T, proj string) (*instanceState, windowstate.WindowState, string) {
				wt := "/Users/x/src/acme/widgetco/.worktrees/agent-1"
				mine := writeAgentLog(t, proj, claude.ProjectSlug(wt), mySession, agent)
				writeAgentLog(t, proj, mySlug, other, agent)
				inst := &instanceState{instance: agent, base: agent}
				return inst, windowstate.WindowState{WorktreeDir: wt}, mine
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proj := t.TempDir()
			m := newLinkLogsModel(t, proj)
			inst, ws, want := tt.run(t, proj)
			m.windows[windowID] = inst
			if err := windowstate.Update(windowID, func(w *windowstate.WindowState) { *w = ws }); err != nil {
				t.Fatal(err)
			}
			// windowstate.Read may drop inconsistent paths; mirror the tick.
			read, _ := windowstate.Read(windowID)

			m.linkLogs(inst, windowID, read, now, myPane)

			if inst.logPath != want {
				t.Errorf("inst.logPath = %q, want %q", inst.logPath, want)
			}
		})
	}
}

func TestLinkLogs_BackfillsWindowstateLogPath(t *testing.T) {
	const windowID, session = "@5", "aaaa1111-0000-0000-0000-000000000001"
	proj := t.TempDir()
	m := newLinkLogsModel(t, proj)
	mine := writeAgentLog(t, proj, "-p", session, "lead")
	inst := &instanceState{instance: "lead", base: "lead"}
	m.windows[windowID] = inst
	if err := windowstate.Update(windowID, func(w *windowstate.WindowState) { w.SessionID = session }); err != nil {
		t.Fatal(err)
	}
	ws, _ := windowstate.Read(windowID)
	m.linkLogs(inst, windowID, ws, 1_000_000, "")
	got, _ := windowstate.Read(windowID)
	if got.LogPath != mine {
		t.Errorf("windowstate LogPath = %q, want %q", got.LogPath, mine)
	}
}
