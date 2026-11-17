package tmux

import (
	"strings"
	"testing"
)

// TestHubNewSessionArgs_ClearsRuntimeDir verifies the hub session's
// tmux new-session args always explicitly clear AGENTCTL_RUNTIME_DIR, so the
// hub can never silently inherit a project's runtime dir from whatever
// process happened to invoke it and start writing into that project's
// .agentctl/ instead of hubDir().
func TestHubNewSessionArgs_ClearsRuntimeDir(t *testing.T) {
	args := hubNewSessionArgs("/home/user/.config/agentctl/hub", "/usr/local/bin/agentctl")

	found := false
	for i := 0; i+1 < len(args); i++ {
		if args[i] != "-e" {
			continue
		}
		if args[i+1] == "AGENTCTL_RUNTIME_DIR=" {
			found = true
		}
		// Guard against a stray non-empty value slipping in.
		if strings.HasPrefix(args[i+1], "AGENTCTL_RUNTIME_DIR=") && args[i+1] != "AGENTCTL_RUNTIME_DIR=" {
			t.Errorf("hubNewSessionArgs: AGENTCTL_RUNTIME_DIR set to non-empty value %q, want cleared", args[i+1])
		}
	}
	if !found {
		t.Error("hubNewSessionArgs: missing explicit -e AGENTCTL_RUNTIME_DIR= (empty) to prevent inheriting a stray value")
	}
}

// TestParseSessionList verifies the parsing of `tmux list-sessions -F
// '#{session_name}'` output, mirroring how TestListWindowsParsing tests
// ListWindows in tmux_test.go.
func TestParseSessionList(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{
			name: "single session",
			raw:  "agentctl-myproject\n",
			want: []string{"agentctl-myproject"},
		},
		{
			name: "multiple sessions",
			raw:  "agentctl-myproject\nagentctl-hub\nagentctl-other\n",
			want: []string{"agentctl-myproject", "agentctl-hub", "agentctl-other"},
		},
		{
			name: "no server running — empty output",
			raw:  "",
			want: nil,
		},
		{
			name: "blank lines are skipped",
			raw:  "agentctl-a\n\n  \nagentctl-b\n",
			want: []string{"agentctl-a", "agentctl-b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseSessionList(tt.raw)
			if len(got) != len(tt.want) {
				t.Fatalf("parseSessionList(%q) = %v, want %v", tt.raw, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("session[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestSessionWindowTarget verifies the session-qualified target string used
// by SwitchToSessionWindow, which must reference the hub's *destination*
// session, not the package-global SessionName.
func TestSessionWindowTarget(t *testing.T) {
	tests := []struct {
		name     string
		session  string
		windowID string
		want     string
	}{
		{"typical", "agentctl-myproject", "@5", "agentctl-myproject:@5"},
		{"hub session", "agentctl-hub", "@0", "agentctl-hub:@0"},
		{"empty window id", "agentctl-acme", "", "agentctl-acme:"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sessionWindowTarget(tt.session, tt.windowID)
			if got != tt.want {
				t.Errorf("sessionWindowTarget(%q, %q) = %q, want %q", tt.session, tt.windowID, got, tt.want)
			}
		})
	}
}

// TestHubSessionNameConstant guards against an accidental rename — the hub
// session name is referenced by both applyConfig's summon keybind and
// jump-next-blocked, and must stay in sync.
func TestHubSessionNameConstant(t *testing.T) {
	if HubSessionName != "agentctl-hub" {
		t.Errorf("HubSessionName = %q, want %q", HubSessionName, "agentctl-hub")
	}
}

// TestHerdListWindowNameConstant guards against an accidental rename — the
// herd-list window name is referenced by both StartHubSession (which pins
// it) and SelectHerdListWindow (which targets it), and must stay in sync.
func TestHerdListWindowNameConstant(t *testing.T) {
	if HerdListWindowName != "herd-list" {
		t.Errorf("HerdListWindowName = %q, want %q", HerdListWindowName, "herd-list")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestTrimTrailingBlankLines covers the parsing/trimming CapturePane applies
// to raw `tmux capture-pane -p` output: trailing blank lines (which tmux
// pads its result with to fill the pane's full height), empty output, and
// content that already has no trailing blanks.
func TestTrimTrailingBlankLines(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "trailing blank lines trimmed",
			in:   "line one\nline two\n\n\n\n",
			want: "line one\nline two",
		},
		{
			name: "trailing whitespace-only lines trimmed",
			in:   "line one\n   \n\t\n",
			want: "line one",
		},
		{
			name: "empty output",
			in:   "",
			want: "",
		},
		{
			name: "all blank",
			in:   "\n\n\n",
			want: "",
		},
		{
			name: "no trailing blanks — unchanged",
			in:   "line one\nline two",
			want: "line one\nline two",
		},
		{
			name: "blank lines in the middle are preserved",
			in:   "line one\n\nline two\n\n",
			want: "line one\n\nline two",
		},
		{
			name: "single line, no trailing newline",
			in:   "just one line",
			want: "just one line",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := trimTrailingBlankLines(tt.in)
			if got != tt.want {
				t.Errorf("trimTrailingBlankLines(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestKillSessionWindow_Guards(t *testing.T) {
	tests := []struct {
		name, session, window string
	}{
		{"hub session refused", HubSessionName, "@1"},
		{"empty session refused", "", "@1"},
		{"non window id refused", "agentctl-x", "shell"},
		{"empty window refused", "agentctl-x", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := KillSessionWindow(tt.session, tt.window); err == nil {
				t.Error("expected refusal")
			}
		})
	}
}

func TestParseListPane(t *testing.T) {
	if got := parseListPane("%1 1\n%0 \n"); got != "%0" {
		t.Errorf("got %q, want %%0", got)
	}
	if got := parseListPane("%1 1\n"); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestSendKeyToWindow(t *testing.T) {
	old := sendKeyFn
	t.Cleanup(func() { sendKeyFn = old })
	var gotTarget, gotKey string
	sendKeyFn = func(target, key string) error { gotTarget, gotKey = target, key; return nil }

	if err := SendKeyToWindow("agentctl-proj", "@523", "Escape"); err != nil {
		t.Fatal(err)
	}
	if gotTarget != "agentctl-proj:@523" || gotKey != "Escape" {
		t.Errorf("sent %q to %q", gotKey, gotTarget)
	}
	want := []string{"send-keys", "-t", "s:@1", "--", "-X"}
	if got := sendKeyArgs("s:@1", "-X"); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("sendKeyArgs = %q, want %q", got, want)
	}
}
