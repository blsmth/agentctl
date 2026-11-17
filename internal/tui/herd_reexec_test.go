package tui

import (
	"os"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestHerdTickReexec(t *testing.T) {
	t.Run("changed quits with reexec flag", func(t *testing.T) {
		m := newHerdModel()
		m.exeChanged = func() bool { return true }
		got, cmd := m.Update(herdTickMsg{})
		if !got.(HerdModel).Reexec() {
			t.Error("reexec flag not set")
		}
		if cmd == nil {
			t.Fatal("expected tea.Quit cmd")
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Errorf("cmd did not return QuitMsg")
		}
	})
	t.Run("unchanged does nothing", func(t *testing.T) {
		m := newHerdModel()
		m.exeChanged = func() bool { return false }
		got, _ := m.Update(herdTickMsg{})
		if got.(HerdModel).Reexec() {
			t.Error("reexec flag set for unchanged binary")
		}
	})
}

func TestHubSelectRoundTrip(t *testing.T) {
	m := newHerdModel()
	m.selSession, m.selWindowID = "agentctl-foo", "@41"
	env := m.SelectionEnv()
	if env != "AGENTCTL_HUB_SELECT=agentctl-foo|@41" {
		t.Fatalf("env = %q", env)
	}
	t.Setenv(HubSelectEnv, env[len(HubSelectEnv)+1:])
	got := NewHerdModel()
	if p := got.pendingFocus; p == nil || p.session != "agentctl-foo" || p.windowID != "@41" {
		t.Errorf("pendingFocus = %+v", p)
	}
	if v, ok := lookupEnvForTest(); ok {
		t.Errorf("env var not consumed: %q", v)
	}

	m.selIsHeader = true
	if m.SelectionEnv() != "" {
		t.Error("header selection must not export env")
	}
}

func TestParseHubSelect(t *testing.T) {
	for _, bad := range []string{"", "x", "|@1", "s|"} {
		if _, _, ok := parseHubSelect(bad); ok {
			t.Errorf("parseHubSelect(%q) ok", bad)
		}
	}
}

func lookupEnvForTest() (string, bool) { return os.LookupEnv(HubSelectEnv) }

func TestHubLeftEnv(t *testing.T) {
	t.Run("seeds userLeft and clears env", func(t *testing.T) {
		t.Setenv(HubLeftEnv, "72")
		m := NewHerdModel()
		if m.userLeft != 72 {
			t.Errorf("userLeft = %d, want 72", m.userLeft)
		}
		if _, ok := os.LookupEnv(HubLeftEnv); ok {
			t.Error("env not cleared")
		}
	})
	for _, v := range []string{"abc", "0", "-5", ""} {
		t.Run("ignores "+v, func(t *testing.T) {
			t.Setenv(HubLeftEnv, v)
			if m := NewHerdModel(); m.userLeft != 0 {
				t.Errorf("userLeft = %d, want 0", m.userLeft)
			}
		})
	}
	t.Run("ReexecEnv includes left only when set", func(t *testing.T) {
		m := newHerdModel()
		if env := m.ReexecEnv(); slices.ContainsFunc(env, func(e string) bool { return strings.HasPrefix(e, HubLeftEnv+"=") }) {
			t.Errorf("env = %v, want no %s", env, HubLeftEnv)
		}
		m.userLeft = 65
		env := m.ReexecEnv()
		if !slices.Contains(env, HubLeftEnv+"=65") {
			t.Errorf("env = %v", env)
		}
	})
}
