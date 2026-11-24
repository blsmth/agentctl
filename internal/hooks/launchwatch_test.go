package hooks

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/mailbox"
	"github.com/blsmth/agentctl/internal/notify"
	"github.com/blsmth/agentctl/internal/windowstate"
	"github.com/blsmth/agentctl/internal/winname"
)

const lwFolder = "/Users/brendansmith/code/personal/weather/.worktrees/agent-typescript-engineer-adhoc-03de1d67"

// lwTrustPane is the live trust dialog capture from the bug report.
func lwTrustPane(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "panestate", "testdata", "trust-166.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// launchWatchFake drives LaunchWatch with a fake clock and pane.
type launchWatchFake struct {
	now      time.Time
	sleeps   int
	captures int
	// pane returns the pane text for the n-th capture (0-based).
	pane     func(n int) (string, error)
	notified []notify.Options
	mail     []mailbox.Message
	renames  []string
}

func setupLaunchWatch(t *testing.T, f *launchWatchFake, cfg *config.Config) {
	t.Helper()
	setupWindowstate(t)
	if f.now.IsZero() {
		f.now = time.Date(2026, 11, 24, 16, 0, 0, 0, time.UTC)
	}
	oldNow, oldSleep, oldPane, oldCapture, oldSession := lwNow, lwSleep, lwPaneOf, lwCapture, lwSession
	lwNow = func() time.Time { return f.now }
	lwSleep = func(d time.Duration) { f.sleeps++; f.now = f.now.Add(d) }
	lwPaneOf = func(string) string { return "%42" }
	lwCapture = func(string) (string, error) {
		n := f.captures
		f.captures++
		return f.pane(n)
	}
	lwSession = func(string) string { return "agentctl-weather" }
	t.Cleanup(func() {
		lwNow, lwSleep, lwPaneOf, lwCapture, lwSession = oldNow, oldSleep, oldPane, oldCapture, oldSession
	})

	d := stubDeps(t)
	if cfg != nil {
		d.LoadConfig = func() *config.Config { return cfg }
	}
	d.Notify = func(o notify.Options) { f.notified = append(f.notified, o) }
	d.EnqueueMailboxMessage = func(m mailbox.Message) (string, error) { f.mail = append(f.mail, m); return "id", nil }
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })
	restore := winname.SetSeamsForTest(func(_, name string) error { f.renames = append(f.renames, name); return nil }, nil, nil)
	t.Cleanup(restore)
}

// seedLaunched writes the record tmux.LaunchAgent bootstraps for a dispatch.
func seedLaunched(t *testing.T, windowID string) {
	t.Helper()
	if err := windowstate.Update(windowID, func(ws *windowstate.WindowState) {
		ws.Instance = "typescript-engineer"
		ws.RenderedName = "typescript-engineer"
		ws.AgentName = "typescript-engineer"
		ws.SpawnedBy = "@1772"
		ws.SpawnedBySession = "parent-sid"
		ws.WorktreeDir = lwFolder
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLaunchWatchReportsTrustDialog(t *testing.T) {
	trust := lwTrustPane(t)
	f := &launchWatchFake{pane: func(n int) (string, error) {
		if n < 2 {
			return "", nil // Claude Code still starting
		}
		return trust, nil
	}}
	setupLaunchWatch(t, f, nil)
	seedLaunched(t, "@1774")

	LaunchWatch("@1774")

	ws, _ := windowstate.Read("@1774")
	if ws.State != "blocked" || ws.PendingTool != windowstate.PendingTrust || ws.PendingToolDetail != lwFolder {
		t.Fatalf("state=%q pending=%q detail=%q", ws.State, ws.PendingTool, ws.PendingToolDetail)
	}
	if ws.BlockedSince.IsZero() || ws.PendingToolAt.IsZero() {
		t.Error("blocked_since / pending_tool_at not set")
	}
	if len(f.renames) != 1 || f.renames[0] != "⏸ typescript-engineer" {
		t.Errorf("renames = %q", f.renames)
	}
	if len(f.notified) != 1 {
		t.Fatalf("notifications = %d, want 1", len(f.notified))
	}
	n := f.notified[0]
	if n.Env["AGENTCTL_NOTIFICATION_TYPE"] != trustNotificationType || n.Env["AGENTCTL_SWITCH_TARGET"] != "agentctl-weather:@1774" {
		t.Errorf("notify env = %v", n.Env)
	}
	if !strings.Contains(n.Subtitle, "Trust this folder?") || !strings.Contains(n.Subtitle, "weather") {
		t.Errorf("subtitle = %q", n.Subtitle)
	}
	if len(f.mail) != 1 {
		t.Fatalf("mail = %d, want 1", len(f.mail))
	}
	m := f.mail[0]
	if m.To != "@1772" || m.ToSession != "parent-sid" || m.From != "@1774" || m.Kind != mailbox.KindBlocked {
		t.Errorf("mail = %+v", m)
	}
	if !strings.HasPrefix(m.Body, "agentctl notice: ") || !strings.Contains(m.Body, "folder trust prompt for "+lwFolder) || !strings.Contains(m.Body, "A human must answer it") {
		t.Errorf("body = %q", m.Body)
	}

	// A second watcher (or a re-run) sends nothing more.
	f.captures = 0
	LaunchWatch("@1774")
	reportTrustPrompt("@1774", "%42", lwFolder)
	if len(f.notified) != 1 || len(f.mail) != 1 {
		t.Errorf("repeat: notifications=%d mail=%d, want 1 and 1", len(f.notified), len(f.mail))
	}
}

func TestLaunchWatchStops(t *testing.T) {
	tests := []struct {
		name string
		// seed adjusts the launched record; nil seeds nothing (no record).
		seed       func(*windowstate.WindowState)
		noRecord   bool
		pane       func(n int) (string, error)
		wantMaxCap int
	}{
		{
			name:       "session already started",
			seed:       func(ws *windowstate.WindowState) { ws.SessionID = "sid" },
			pane:       func(int) (string, error) { return "", nil },
			wantMaxCap: 0,
		},
		{
			name:       "no record",
			noRecord:   true,
			pane:       func(int) (string, error) { return "", nil },
			wantMaxCap: 0,
		},
		{
			name:       "window gone",
			pane:       func(int) (string, error) { return "", errors.New("can't find pane") },
			wantMaxCap: launchWatchMaxFails,
		},
		{
			name:       "deadline passes with no dialog",
			pane:       func(int) (string, error) { return "❯ \n", nil },
			wantMaxCap: int(launchWatchFor/launchWatchEvery) + 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &launchWatchFake{pane: tt.pane}
			setupLaunchWatch(t, f, nil)
			if !tt.noRecord {
				seedLaunched(t, "@5")
				if tt.seed != nil {
					if err := windowstate.Update("@5", tt.seed); err != nil {
						t.Fatal(err)
					}
				}
			}
			LaunchWatch("@5")
			if f.captures > tt.wantMaxCap {
				t.Errorf("captures = %d, want <= %d", f.captures, tt.wantMaxCap)
			}
			if len(f.notified) != 0 || len(f.mail) != 0 {
				t.Errorf("notified=%d mail=%d, want none", len(f.notified), len(f.mail))
			}
			if ws, _ := windowstate.Read("@5"); ws.State == "blocked" {
				t.Error("window marked blocked")
			}
		})
	}
}

// The session can start between the pane read and the mark: the mark is
// made only while the record still has no SessionID.
func TestReportTrustPromptSkipsStartedSession(t *testing.T) {
	f := &launchWatchFake{pane: func(int) (string, error) { return "", nil }}
	setupLaunchWatch(t, f, nil)
	seedLaunched(t, "@5")
	_ = windowstate.Update("@5", func(ws *windowstate.WindowState) { ws.SessionID = "sid" })
	reportTrustPrompt("@5", "%42", lwFolder)
	if ws, _ := windowstate.Read("@5"); ws.State == "blocked" || len(f.notified) != 0 || len(f.mail) != 0 {
		t.Errorf("state=%q notified=%d mail=%d", ws.State, len(f.notified), len(f.mail))
	}
}

func TestReportTrustPromptRespectsAlertConfig(t *testing.T) {
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		"typescript-engineer": {Alerts: true, AlertConfig: config.AlertConfig{Idle: true}},
	}}
	f := &launchWatchFake{pane: func(int) (string, error) { return "", nil }}
	setupLaunchWatch(t, f, cfg)
	seedLaunched(t, "@5")
	// A bare window: no dispatcher to tell.
	_ = windowstate.Update("@5", func(ws *windowstate.WindowState) { ws.SpawnedBy, ws.SpawnedBySession = "", "" })
	reportTrustPrompt("@5", "%42", lwFolder)
	ws, _ := windowstate.Read("@5")
	if ws.State != "blocked" {
		t.Errorf("state = %q, want blocked (the state is written whatever the alert config)", ws.State)
	}
	if len(f.notified) != 0 {
		t.Error("notified with alert_config.blocked off")
	}
	if len(f.mail) != 0 {
		t.Error("mailed a bare window's nonexistent dispatcher")
	}
}

// trustBlocked seeds a window the watcher marked blocked on folder trust.
func trustBlocked(t *testing.T, windowID string) {
	t.Helper()
	seedLaunched(t, windowID)
	if err := windowstate.Update(windowID, func(ws *windowstate.WindowState) {
		ws.State = "blocked"
		ws.BlockedSince = time.Now()
		ws.RenderedName = "⏸ typescript-engineer"
		ws.PendingTool, ws.PendingToolDetail, ws.PendingToolAt = windowstate.PendingTrust, lwFolder, time.Now()
	}); err != nil {
		t.Fatal(err)
	}
}

// Regression: SessionStart wrote session_id but left the watcher's blocked
// state, so an interactive launch (no first prompt, so no UserPromptSubmit)
// stayed ⏸ after the human trusted the folder.
func TestSessionStartClearsTrustBlock(t *testing.T) {
	f := &launchWatchFake{pane: func(int) (string, error) { return "", nil }}
	setupLaunchWatch(t, f, nil)
	trustBlocked(t, "@8")

	t.Setenv("AGENTCTL", "1")
	t.Setenv("CLAUDE_ENV_FILE", filepath.Join(t.TempDir(), "env.sh"))
	t.Setenv("AGENTCTL_WINDOW_ID", "@8")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")
	t.Setenv("AGENTCTL_AGENT_NAME", "")
	runHookCaptured(t, nil, `{"session_id":"new-sid","cwd":"/tmp"}`, SessionStart)

	ws, _ := windowstate.Read("@8")
	if ws.SessionID != "new-sid" {
		t.Errorf("session_id = %q", ws.SessionID)
	}
	if ws.State != "idle" || ws.PendingTool != "" || ws.PendingToolDetail != "" || !ws.BlockedSince.IsZero() {
		t.Errorf("state=%q pending=%q detail=%q blocked_since=%v", ws.State, ws.PendingTool, ws.PendingToolDetail, ws.BlockedSince)
	}
	if len(f.renames) == 0 || f.renames[len(f.renames)-1] != "typescript-engineer" {
		t.Errorf("renames = %q, want the ⏸ dropped", f.renames)
	}
}

// SessionStart leaves another blocked state alone.
func TestSessionStartKeepsOtherBlock(t *testing.T) {
	f := &launchWatchFake{pane: func(int) (string, error) { return "", nil }}
	setupLaunchWatch(t, f, nil)
	seedLaunched(t, "@8")
	_ = windowstate.Update("@8", func(ws *windowstate.WindowState) { ws.State, ws.PendingTool = "blocked", "Bash" })

	t.Setenv("AGENTCTL", "1")
	t.Setenv("CLAUDE_ENV_FILE", filepath.Join(t.TempDir(), "env.sh"))
	t.Setenv("AGENTCTL_WINDOW_ID", "@8")
	t.Setenv("TMUX_PANE", "")
	runHookCaptured(t, nil, `{"session_id":"sid","cwd":"/tmp","source":"resume"}`, SessionStart)

	if ws, _ := windowstate.Read("@8"); ws.State != "blocked" || ws.PendingTool != "Bash" {
		t.Errorf("state=%q pending=%q", ws.State, ws.PendingTool)
	}
}

func TestUserPromptClearsTrustBlock(t *testing.T) {
	f := &launchWatchFake{pane: func(int) (string, error) { return "", nil }}
	setupLaunchWatch(t, f, nil)
	trustBlocked(t, "@9")

	t.Setenv("AGENTCTL", "1")
	t.Setenv("AGENTCTL_WINDOW_ID", "@9")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("AGENTCTL_SESSION_ID", "")
	t.Setenv("AGENTCTL_AGENT_NAME", "typescript-engineer")
	runHookCaptured(t, nil, `{}`, UserPrompt)

	ws, _ := windowstate.Read("@9")
	if ws.State != "working" || ws.PendingTool != "" || !ws.BlockedSince.IsZero() {
		t.Errorf("state=%q pending=%q blocked_since=%v", ws.State, ws.PendingTool, ws.BlockedSince)
	}
}

func TestTrustFolderFallsBackToWindowDir(t *testing.T) {
	ws := windowstate.WindowState{WorktreeDir: "/w", WorkDir: "/d"}
	cut := "Accessing workspace:\n\n/Users/x/very/long…\n\n❯ No, exit\n  Yes, I trust this folder\n"
	if got := trustFolder(cut, ws); got != "/w" {
		t.Errorf("truncated path: got %q, want /w", got)
	}
	if got := trustFolder(cut, windowstate.WindowState{WorkDir: "/d"}); got != "/d" {
		t.Errorf("no worktree: got %q, want /d", got)
	}
	full := strings.Replace(cut, "/Users/x/very/long…", "/repo", 1)
	if got := trustFolder(full, ws); got != "/repo" {
		t.Errorf("full path: got %q, want /repo", got)
	}
}
