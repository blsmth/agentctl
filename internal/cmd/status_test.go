package cmd

import (
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/herd"
	"github.com/blsmth/agentctl/internal/state"
)

func TestSessionMatches(t *testing.T) {
	widgetco := herd.Group{Session: "agentctl-widgetco", Project: "widgetco"}
	self := herd.Group{Session: "agentctl-agentctl", Project: "agentctl"}
	ops := herd.Group{Session: "agentctl-acme-ops", Project: "acme-ops"}

	tests := []struct {
		name  string
		group herd.Group
		want  string
		match bool
	}{
		{"empty filter matches everything", widgetco, "", true},
		{"exact session name", widgetco, "agentctl-widgetco", true},
		{"exact project", widgetco, "widgetco", true},
		{"project prefix", widgetco, "widg", true},
		{"unrelated project", widgetco, "side-project", false},
		{"project with dots", ops, "acme", true},

		// The bug this guards: every session is named "agentctl-<project>",
		// so matching the raw session name by substring made `--session
		// agentctl` return every session in existence. A filter that silently
		// returns everything is worse than one that errors — it reads as "no
		// other sessions exist".
		{"'agentctl' must not match another project's session", widgetco, "agentctl", false},
		{"'agentctl' still matches the agentctl project itself", self, "agentctl", true},
		{"'agentctl-' prefix is not a wildcard", ops, "agentctl", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sessionMatches(tt.group, tt.want); got != tt.match {
				t.Errorf("sessionMatches(%q, %q) = %v, want %v",
					tt.group.Session, tt.want, got, tt.match)
			}
		})
	}
}

func row(id, name, st string, done bool) herd.Row {
	return herd.Row{AgentState: state.AgentState{WindowID: id, Name: name, State: st, Done: done}}
}

func TestStateMatches(t *testing.T) {
	tests := []struct {
		name string
		r    herd.Row
		want string
		ok   bool
	}{
		{"no filter", row("@1", "a", "idle", false), "", true},
		{"blocked matches blocked", row("@1", "a", "blocked", false), "blocked", true},
		{"blocked does not match idle", row("@1", "a", "idle", false), "blocked", false},
		{"working matches", row("@1", "a", "working", false), "working", true},
		// "done" is a separate boolean, not a State value — an agent that has
		// signaled done still has State "idle".
		{"done matches the flag, not the state string", row("@1", "a", "idle", true), "done", true},
		{"done does not match a plain idle agent", row("@1", "a", "idle", false), "done", false},
		{"idle matches a done agent's underlying state", row("@1", "a", "idle", true), "idle", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stateMatches(tt.r, tt.want); got != tt.ok {
				t.Errorf("stateMatches() = %v, want %v", got, tt.ok)
			}
		})
	}
}

func TestFindWindow(t *testing.T) {
	rows := []statusRow{
		{row: row("@275", "lead:17", "idle", false), group: herd.Group{Project: "agentctl"}},
		{row: row("@306", "go-engineer:15", "idle", true), group: herd.Group{Project: "agentctl"}},
		{row: row("@10", "remi", "idle", false), group: herd.Group{Project: "widgetco-infra"}},
		{row: row("@181", "alex", "idle", false), group: herd.Group{Project: "widgetco"}},
		{row: row("@280", "alex", "idle", false), group: herd.Group{Project: "acme-ops"}},
	}

	t.Run("by window ID", func(t *testing.T) {
		got, err := findWindow(rows, "@306")
		if err != nil || got.row.Name != "go-engineer:15" {
			t.Fatalf("findWindow(@306) = %+v, %v", got.row, err)
		}
	})

	// Bare numbers are what you get from copy-pasting a window index.
	t.Run("bare number gets an @ prefix", func(t *testing.T) {
		got, err := findWindow(rows, "275")
		if err != nil || got.row.WindowID != "@275" {
			t.Fatalf("findWindow(275) = %+v, %v", got.row, err)
		}
	})

	t.Run("by unique agent name", func(t *testing.T) {
		got, err := findWindow(rows, "remi")
		if err != nil || got.row.WindowID != "@10" {
			t.Fatalf("findWindow(remi) = %+v, %v", got.row, err)
		}
	})

	// The same agent name exists in several projects. Silently picking the
	// first would mean acting on a window in the wrong repo.
	t.Run("ambiguous name is an error, not a first match", func(t *testing.T) {
		_, err := findWindow(rows, "alex")
		if err == nil {
			t.Fatal("expected an ambiguity error")
		}
		for _, want := range []string{"ambiguous", "@181", "@280", "widgetco", "acme-ops"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err, want)
			}
		}
	})

	t.Run("unknown window", func(t *testing.T) {
		if _, err := findWindow(rows, "@999"); err == nil {
			t.Fatal("expected an error for an unknown window")
		}
	})

	t.Run("empty reference", func(t *testing.T) {
		if _, err := findWindow(rows, ""); err == nil {
			t.Fatal("expected an error for an empty window reference")
		}
	})
}

// Activity lines come straight from pane output and carry multi-byte runes.
// The package's other truncate is byte-based and would split one mid-sequence.
func TestTruncateDisplay(t *testing.T) {
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"short string untouched", "hello", 10, "hello"},
		{"exact length untouched", "hello", 5, "hello"},
		{"ascii truncation", "hello world", 8, "hello w…"},
		{"multi-byte runes are not split", "PR opened: ✓ résumé — done", 14, "PR opened: ✓ …"},
		{"emoji", "**v0.0.53 is out.** 🏷️ tag", 12, "**v0.0.53 i…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateDisplay(tt.in, tt.n)
			if got != tt.want {
				t.Errorf("truncateDisplay(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
			}
			if !isValidUTF8(got) {
				t.Errorf("truncateDisplay produced invalid UTF-8: %q", got)
			}
			if n := len([]rune(got)); n > tt.n {
				t.Errorf("truncateDisplay returned %d runes, want at most %d", n, tt.n)
			}
		})
	}
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

func TestWhoamiResolution(t *testing.T) {
	pane := whoamiSource{Name: "TMUX_PANE", WindowID: "@283", Trusted: true}
	sess := whoamiSource{Name: "AGENTCTL_SESSION_ID", WindowID: "@275"}
	win := whoamiSource{Name: "AGENTCTL_WINDOW_ID", WindowID: "@274"}
	empty := whoamiSource{Name: "AGENTCTL_SESSION_ID", WindowID: ""}

	t.Run("trusted source wins over untrusted ones", func(t *testing.T) {
		got := resolveWhoami([]whoamiSource{sess, win, pane})
		if got != "@283" {
			t.Errorf("resolveWhoami() = %q, want @283", got)
		}
	})

	t.Run("falls back to an untrusted answer when that is all there is", func(t *testing.T) {
		if got := resolveWhoami([]whoamiSource{empty, win}); got != "@274" {
			t.Errorf("resolveWhoami() = %q, want @274", got)
		}
	})

	t.Run("no sources", func(t *testing.T) {
		if got := resolveWhoami(nil); got != "" {
			t.Errorf("resolveWhoami(nil) = %q, want empty", got)
		}
	})

	// The real-world case: a background session inherits AGENTCTL_SESSION_ID
	// from its parent, so the "reliable" lookup names the parent window while
	// TMUX_PANE names the caller's own. That disagreement must be reported.
	t.Run("disagreement is detected", func(t *testing.T) {
		if whoamiAgree([]whoamiSource{pane, sess, win}) {
			t.Error("whoamiAgree() = true for three different window IDs")
		}
	})

	t.Run("agreement ignores sources with no answer", func(t *testing.T) {
		if !whoamiAgree([]whoamiSource{pane, empty, {Name: "AGENTCTL_WINDOW_ID", WindowID: "@283"}}) {
			t.Error("whoamiAgree() = false when the only answers agree")
		}
	})

	t.Run("hasTrustedAnswer requires a window ID, not just the flag", func(t *testing.T) {
		if hasTrustedAnswer([]whoamiSource{{Name: "TMUX_PANE", WindowID: "", Trusted: true}}) {
			t.Error("hasTrustedAnswer() = true for a trusted source that resolved nothing")
		}
		if !hasTrustedAnswer([]whoamiSource{sess, pane}) {
			t.Error("hasTrustedAnswer() = false when TMUX_PANE resolved")
		}
	})
}

func TestDashIfEmpty(t *testing.T) {
	if got := dashIfEmpty(""); got != "-" {
		t.Errorf("dashIfEmpty(\"\") = %q, want -", got)
	}
	if got := dashIfEmpty("adhoc"); got != "adhoc" {
		t.Errorf("dashIfEmpty(adhoc) = %q", got)
	}
}
