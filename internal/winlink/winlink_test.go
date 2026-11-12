package winlink

import (
	"os"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestWrap(t *testing.T) {
	tests := []struct {
		name, id, visible, want string
	}{
		{"valid", "@65", "@65", "\x1b]8;;agentctl://window/@65\x1b\\@65\x1b]8;;\x1b\\"},
		{"styled visible", "@7", "\x1b[1m@7\x1b[m", "\x1b]8;;agentctl://window/@7\x1b\\\x1b[1m@7\x1b[m\x1b]8;;\x1b\\"},
		{"empty id", "", "-", "-"},
		{"no at", "65", "65", "65"},
		{"non-digit", "@6a", "@6a", "@6a"},
		{"injection", "@1\x1b\\evil", "x", "x"},
		{"bare at", "@", "@", "@"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Wrap(tc.id, tc.visible); got != tc.want {
				t.Errorf("Wrap(%q, %q) = %q, want %q", tc.id, tc.visible, got, tc.want)
			}
		})
	}
}

func TestWrapVisibleWidthUnchanged(t *testing.T) {
	got := Wrap("@123", "@123")
	if w := ansi.StringWidth(got); w != 4 {
		t.Errorf("visible width = %d, want 4", w)
	}
	if s := ansi.Strip(got); s != "@123" {
		t.Errorf("stripped = %q, want %q", s, "@123")
	}
}

func setTTY(t *testing.T, tty bool) {
	t.Helper()
	prev := stdoutIsTerminal
	stdoutIsTerminal = func() bool { return tty }
	t.Cleanup(func() { stdoutIsTerminal = prev })
}

func TestEnabled(t *testing.T) {
	tests := []struct {
		name    string
		tty     bool
		noColor bool
		term    string
		want    bool
	}{
		{"tty", true, false, "xterm-ghostty", true},
		{"piped", false, false, "xterm-ghostty", false},
		{"NO_COLOR", true, true, "xterm-ghostty", false},
		{"dumb term", true, false, "dumb", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setTTY(t, tc.tty)
			t.Setenv("TERM", tc.term)
			t.Setenv("NO_COLOR", "")
			if !tc.noColor {
				// t.Setenv registers restoration; Unsetenv makes it absent.
				unsetenv(t, "NO_COLOR")
			}
			if got := Enabled(); got != tc.want {
				t.Errorf("Enabled() = %v, want %v", got, tc.want)
			}
			wantID := "@9"
			if tc.want {
				wantID = Wrap("@9", "@9")
			}
			if got := ID("@9"); got != wantID {
				t.Errorf("ID(@9) = %q, want %q", got, wantID)
			}
		})
	}
}

func unsetenv(t *testing.T, key string) {
	t.Helper()
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
}
