package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// resetPathsForTest clears the paths package's global runtime dir so
// paths.DirName() returns "" for the duration of the test.
func resetPathsForTest(t *testing.T) {
	t.Helper()
	paths.ResetForTest()
	t.Setenv("AGENTCTL_RUNTIME_DIR", "")
	t.Cleanup(paths.ResetForTest)
}

// setupProjectDirForTest points paths at a fresh temp project directory
// named projectName (i.e. paths.DirName() will return projectName), for the
// duration of the test.
func setupProjectDirForTest(t *testing.T, projectName string) string {
	t.Helper()
	resetPathsForTest(t)
	base := filepath.Join(t.TempDir(), projectName)
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}
	paths.Init(base)
	return base
}

// ---------------------------------------------------------------------------
// buildAlertContent
// ---------------------------------------------------------------------------

func TestBuildAlertContent(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name             string
		notificationType string
		agentName        string
		sessionName      string
		windowID         string
		fallbackMessage  string
		ws               windowstate.WindowState
		wantTitle        string
		wantSubtitle     string
		wantMessage      string
		wantMessageOmits []string // substrings that must NOT appear in Message
	}{
		{
			name:             "permission prompt with fresh pending tool",
			notificationType: "permission_prompt",
			agentName:        "go-engineer",
			sessionName:      "agentctl-acme",
			windowID:         "@61",
			fallbackMessage:  "Claude needs your permission",
			ws: windowstate.WindowState{
				PendingTool:       "Bash",
				PendingToolDetail: "git push --force origin main",
				PendingToolAt:     now.Add(-30 * time.Second),
				Ticket:            "1030",
			},
			wantTitle:    "go-engineer · acme",
			wantSubtitle: "Needs permission — Bash",
			wantMessage:  "git push --force origin main · #1030 · agentctl-acme:@61",
		},
		{
			name:             "permission prompt with stale pending tool is omitted",
			notificationType: "permission_prompt",
			agentName:        "go-engineer",
			sessionName:      "agentctl-acme",
			windowID:         "@61",
			fallbackMessage:  "Claude needs your permission",
			ws: windowstate.WindowState{
				PendingTool:       "Bash",
				PendingToolDetail: "rm -rf /old/build",
				PendingToolAt:     now.Add(-11 * time.Minute),
				Ticket:            "1030",
			},
			wantTitle:    "go-engineer · acme",
			wantSubtitle: "Needs permission",
			wantMessage:  "#1030 · agentctl-acme:@61",
			wantMessageOmits: []string{
				"rm -rf /old/build",
				"Bash",
			},
		},
		{
			name:             "permission prompt with no pending tool falls back to claude message",
			notificationType: "permission_prompt",
			agentName:        "go-engineer",
			sessionName:      "",
			windowID:         "",
			fallbackMessage:  "Claude needs your permission",
			ws:               windowstate.WindowState{},
			wantTitle:        "go-engineer",
			wantSubtitle:     "Needs permission",
			wantMessage:      "Claude needs your permission",
		},
		{
			name:             "idle prompt",
			notificationType: "idle_prompt",
			agentName:        "go-engineer",
			sessionName:      "agentctl-acme",
			windowID:         "@61",
			fallbackMessage:  "waiting",
			ws:               windowstate.WindowState{},
			wantTitle:        "go-engineer · acme",
			wantSubtitle:     "Idle — waiting for input",
			wantMessage:      "agentctl-acme:@61",
		},
		{
			name:             "repeat block 12 minutes old shows blocked duration",
			notificationType: "permission_prompt",
			agentName:        "go-engineer",
			sessionName:      "agentctl-acme",
			windowID:         "@61",
			fallbackMessage:  "Claude needs your permission",
			ws: windowstate.WindowState{
				Ticket:       "1030",
				BlockedSince: now.Add(-12 * time.Minute),
			},
			wantTitle:    "go-engineer · acme",
			wantSubtitle: "Needs permission",
			wantMessage:  "#1030 · agentctl-acme:@61 · blocked 12m",
		},
		{
			name:             "repeat block 20 seconds old omits blocked duration",
			notificationType: "permission_prompt",
			agentName:        "go-engineer",
			sessionName:      "agentctl-acme",
			windowID:         "@61",
			fallbackMessage:  "Claude needs your permission",
			ws: windowstate.WindowState{
				Ticket:       "1030",
				BlockedSince: now.Add(-20 * time.Second),
			},
			wantTitle:    "go-engineer · acme",
			wantSubtitle: "Needs permission",
			wantMessage:  "#1030 · agentctl-acme:@61",
			wantMessageOmits: []string{
				"blocked",
			},
		},
		{
			name:             "mode fallback when ticket absent",
			notificationType: "permission_prompt",
			agentName:        "go-engineer",
			sessionName:      "agentctl-acme",
			windowID:         "@61",
			fallbackMessage:  "Claude needs your permission",
			ws: windowstate.WindowState{
				Mode: "autonomous",
			},
			wantTitle:    "go-engineer · acme",
			wantSubtitle: "Needs permission",
			wantMessage:  "autonomous · agentctl-acme:@61",
		},
		{
			name:             "neither ticket nor mode present",
			notificationType: "permission_prompt",
			agentName:        "go-engineer",
			sessionName:      "agentctl-acme",
			windowID:         "@61",
			fallbackMessage:  "Claude needs your permission",
			ws:               windowstate.WindowState{},
			wantTitle:        "go-engineer · acme",
			wantSubtitle:     "Needs permission",
			wantMessage:      "agentctl-acme:@61",
			wantMessageOmits: []string{"#", "autonomous"},
		},
		{
			name:             "empty session name gives bare title and bare window target",
			notificationType: "permission_prompt",
			agentName:        "go-engineer",
			sessionName:      "",
			windowID:         "@61",
			fallbackMessage:  "Claude needs your permission",
			ws: windowstate.WindowState{
				Ticket: "1030",
			},
			wantTitle:    "go-engineer",
			wantSubtitle: "Needs permission",
			wantMessage:  "#1030 · @61",
		},
		{
			name:             "session name agentctl-acme: title shows acme, message target unstripped",
			notificationType: "permission_prompt",
			agentName:        "go-engineer",
			sessionName:      "agentctl-acme",
			windowID:         "@61",
			fallbackMessage:  "Claude needs your permission",
			ws: windowstate.WindowState{
				Ticket: "1030",
			},
			wantTitle:    "go-engineer · acme",
			wantSubtitle: "Needs permission",
			wantMessage:  "#1030 · agentctl-acme:@61",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildAlertContent(tt.notificationType, tt.agentName, tt.sessionName, tt.windowID, tt.fallbackMessage, tt.ws, now)

			if got.Title != tt.wantTitle {
				t.Errorf("Title = %q, want %q", got.Title, tt.wantTitle)
			}
			if got.Subtitle != tt.wantSubtitle {
				t.Errorf("Subtitle = %q, want %q", got.Subtitle, tt.wantSubtitle)
			}
			if got.Message != tt.wantMessage {
				t.Errorf("Message = %q, want %q", got.Message, tt.wantMessage)
			}
			if got.Message == "" {
				t.Error("Message must never be empty")
			}
			for _, omit := range tt.wantMessageOmits {
				if strings.Contains(got.Message, omit) {
					t.Errorf("Message %q must not contain %q", got.Message, omit)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// tmuxSwitchTarget
// ---------------------------------------------------------------------------

func TestTmuxSwitchTarget(t *testing.T) {
	tests := []struct {
		name        string
		sessionName string
		windowID    string
		want        string
	}{
		{"both present", "agentctl-acme", "@61", "agentctl-acme:@61"},
		{"session empty", "", "@61", "@61"},
		{"window empty", "agentctl-acme", "", ""},
		{"both empty", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tmuxSwitchTarget(tt.sessionName, tt.windowID); got != tt.want {
				t.Errorf("tmuxSwitchTarget(%q, %q) = %q, want %q", tt.sessionName, tt.windowID, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// compactDuration
// ---------------------------------------------------------------------------

func TestCompactDuration(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"minutes only", 12 * time.Minute, "12m"},
		{"zero", 0, "0m"},
		{"hours and minutes", 1*time.Hour + 5*time.Minute, "1h5m"},
		{"exact hour", 2 * time.Hour, "2h"},
		{"days only", 2 * 24 * time.Hour, "2d"},
		{"days and hours", 2*24*time.Hour + 3*time.Hour, "2d3h"},
		{"negative clamps to zero", -5 * time.Minute, "0m"},
		{"just under an hour", 59 * time.Minute, "59m"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := compactDuration(tt.d); got != tt.want {
				t.Errorf("compactDuration(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// BlockedSince semantics
// ---------------------------------------------------------------------------

// TestNotify_BlockedSinceSetOnTransitionNotRefreshed verifies that
// BlockedSince is set the first time a window transitions into "blocked",
// and left untouched on subsequent permission_prompt firings while it stays
// blocked (prevState == "blocked") -- so elapsed time accumulates across
// repeat firings instead of resetting on every hook invocation.
func TestNotify_BlockedSinceSetOnTransitionNotRefreshed(t *testing.T) {
	setupWindowstate(t)
	d := stubDeps(t)
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	envVars := map[string]string{
		"AGENTCTL":            "1",
		"AGENTCTL_AGENT_NAME": "go-engineer",
		"AGENTCTL_WINDOW_ID":  "@70",
		"TMUX_PANE":           "",
		"AGENTCTL_SESSION_ID": "",
	}
	stdin := `{"notification_type":"permission_prompt","message":"needs approval"}`

	runHookCaptured(t, envVars, stdin, Notify)

	ws, err := windowstate.Read("@70")
	if err != nil {
		t.Fatalf("windowstate.Read: %v", err)
	}
	if ws.BlockedSince.IsZero() {
		t.Fatal("BlockedSince not set on transition into blocked")
	}
	firstBlockedSince := ws.BlockedSince

	// Sleep a moment so a refresh (bug) would be observably different.
	time.Sleep(5 * time.Millisecond)

	runHookCaptured(t, envVars, stdin, Notify)

	ws2, err := windowstate.Read("@70")
	if err != nil {
		t.Fatalf("windowstate.Read: %v", err)
	}
	if !ws2.BlockedSince.Equal(firstBlockedSince) {
		t.Errorf("BlockedSince changed on repeat permission_prompt while already blocked: got %v, want unchanged %v", ws2.BlockedSince, firstBlockedSince)
	}
}

// TestNotify_IdlePromptZeroesBlockedSince verifies that an idle_prompt
// notification clears BlockedSince.
func TestNotify_IdlePromptZeroesBlockedSince(t *testing.T) {
	setupWindowstate(t)
	d := stubDeps(t)
	SetDepsForTest(d)
	t.Cleanup(func() { SetDepsForTest(DefaultDeps()) })

	if err := windowstate.Write("@71", windowstate.WindowState{
		State:        "blocked",
		BlockedSince: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("windowstate.Write: %v", err)
	}

	envVars := map[string]string{
		"AGENTCTL":            "1",
		"AGENTCTL_AGENT_NAME": "go-engineer",
		"AGENTCTL_WINDOW_ID":  "@71",
		"TMUX_PANE":           "",
		"AGENTCTL_SESSION_ID": "",
	}
	stdin := `{"notification_type":"idle_prompt","message":"waiting"}`

	runHookCaptured(t, envVars, stdin, Notify)

	ws, err := windowstate.Read("@71")
	if err != nil {
		t.Fatalf("windowstate.Read: %v", err)
	}
	if !ws.BlockedSince.IsZero() {
		t.Errorf("BlockedSince = %v, want zero after idle_prompt", ws.BlockedSince)
	}
}

// ---------------------------------------------------------------------------
// resolveInstanceSessionName
// ---------------------------------------------------------------------------

func TestResolveInstanceSessionName(t *testing.T) {
	origFn := sessionNameForPane
	t.Cleanup(func() { sessionNameForPane = origFn })

	t.Run("pane query succeeds", func(t *testing.T) {
		sessionNameForPane = func(pane string) string { return "agentctl-acme" }
		if got := resolveInstanceSessionName(); got != "agentctl-acme" {
			t.Errorf("resolveInstanceSessionName() = %q, want %q", got, "agentctl-acme")
		}
	})

	t.Run("pane query fails, falls back to paths.DirName", func(t *testing.T) {
		sessionNameForPane = func(pane string) string { return "" }
		dir := setupProjectDirForTest(t, "myproj")
		_ = dir
		if got := resolveInstanceSessionName(); got != "agentctl-myproj" {
			t.Errorf("resolveInstanceSessionName() = %q, want %q", got, "agentctl-myproj")
		}
	})

	t.Run("both unavailable", func(t *testing.T) {
		sessionNameForPane = func(pane string) string { return "" }
		resetPathsForTest(t)
		if got := resolveInstanceSessionName(); got != "" {
			t.Errorf("resolveInstanceSessionName() = %q, want empty", got)
		}
	})
}
