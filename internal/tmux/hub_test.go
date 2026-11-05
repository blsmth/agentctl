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

// TestHubWindowTarget verifies the session-qualified target string used by
// the link/unlink/select helpers below — must always reference
// HubSessionName, never the package-global SessionName.
func TestHubWindowTarget(t *testing.T) {
	tests := []struct {
		name     string
		windowID string
		want     string
	}{
		{"typical", "@7", "agentctl-hub:@7"},
		{"empty window id", "", "agentctl-hub:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hubWindowTarget(tt.windowID)
			if got != tt.want {
				t.Errorf("hubWindowTarget(%q) = %q, want %q", tt.windowID, got, tt.want)
			}
		})
	}
}

// TestLinkWindowArgs asserts the exact `tmux link-window` argument list:
// `-s <home>:<id>` as the source (the window that stays alive in its home
// session) and `-t <hub>:` as a *session*-only destination (no window index,
// so tmux preserves the window's own ID instead of renumbering it).
func TestLinkWindowArgs(t *testing.T) {
	tests := []struct {
		name        string
		homeSession string
		windowID    string
		want        []string
	}{
		{
			name:        "typical",
			homeSession: "agentctl-myproject",
			windowID:    "@5",
			want:        []string{"link-window", "-s", "agentctl-myproject:@5", "-t", "agentctl-hub:"},
		},
		{
			name:        "different project",
			homeSession: "agentctl-acme",
			windowID:    "@12",
			want:        []string{"link-window", "-s", "agentctl-acme:@12", "-t", "agentctl-hub:"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := linkWindowArgs(tt.homeSession, tt.windowID)
			if !equalStrings(got, tt.want) {
				t.Errorf("linkWindowArgs(%q, %q) = %v, want %v", tt.homeSession, tt.windowID, got, tt.want)
			}
		})
	}
}

// TestUnlinkWindowArgs asserts the exact `tmux unlink-window` argument list
// and — critically — that it never includes `-k`, which would destroy the
// window outright once the hub's link was its last one.
func TestUnlinkWindowArgs(t *testing.T) {
	got := unlinkWindowArgs("@5")
	want := []string{"unlink-window", "-t", "agentctl-hub:@5"}
	if !equalStrings(got, want) {
		t.Errorf("unlinkWindowArgs(@5) = %v, want %v", got, want)
	}
	for _, arg := range got {
		if arg == "-k" {
			t.Fatalf("unlinkWindowArgs must never include -k (would kill the window on its last link): %v", got)
		}
	}
}

// TestSelectHubWindowArgs asserts the exact `tmux select-window` argument
// list used both after a successful link and as the idempotent no-op path.
func TestSelectHubWindowArgs(t *testing.T) {
	got := selectHubWindowArgs("@9")
	want := []string{"select-window", "-t", "agentctl-hub:@9"}
	if !equalStrings(got, want) {
		t.Errorf("selectHubWindowArgs(@9) = %v, want %v", got, want)
	}
}

// TestLinkWindowIntoHub_RefusesHubSourceSession verifies the safety guard
// that runs before any tmux command is shelled out to: linking a window
// whose home session is the hub itself must be refused outright (there is
// nothing to "bring in" — it already belongs to the hub). Deliberately
// testable without a live tmux binary since the guard short-circuits before
// exec.Command.
func TestLinkWindowIntoHub_RefusesHubSourceSession(t *testing.T) {
	err := LinkWindowIntoHub(HubSessionName, "@1")
	if err == nil {
		t.Fatal("expected an error linking a window from the hub session itself, got nil")
	}
}

// TestLinkWindowIntoHub_RefusesEmptyWindowID is the same shape as above for
// the empty-window-ID guard.
func TestLinkWindowIntoHub_RefusesEmptyWindowID(t *testing.T) {
	err := LinkWindowIntoHub("agentctl-myproject", "")
	if err == nil {
		t.Fatal("expected an error linking an empty window id, got nil")
	}
}

// TestParseWindowIDList mirrors TestParseSessionList for the window-ID
// parsing ListHubWindowIDs relies on.
func TestParseWindowIDList(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want map[string]bool
	}{
		{
			name: "single window",
			raw:  "@1\n",
			want: map[string]bool{"@1": true},
		},
		{
			name: "multiple windows",
			raw:  "@1\n@2\n@7\n",
			want: map[string]bool{"@1": true, "@2": true, "@7": true},
		},
		{
			name: "empty output",
			raw:  "",
			want: map[string]bool{},
		},
		{
			name: "blank lines are skipped",
			raw:  "@1\n\n  \n@2\n",
			want: map[string]bool{"@1": true, "@2": true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseWindowIDList(tt.raw)
			if len(got) != len(tt.want) {
				t.Fatalf("parseWindowIDList(%q) = %v, want %v", tt.raw, got, tt.want)
			}
			for id := range tt.want {
				if !got[id] {
					t.Errorf("parseWindowIDList(%q) missing %q", tt.raw, id)
				}
			}
		})
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
