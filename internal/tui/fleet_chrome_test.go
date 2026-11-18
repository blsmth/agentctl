package tui

import (
	"os"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// stubHubZoom replaces the tmux zoom seam and records the hide values passed.
func stubHubZoom(t *testing.T) *[]bool {
	t.Helper()
	var calls []bool
	old := hubZoomSync
	t.Cleanup(func() { hubZoomSync = old })
	hubZoomSync = func(hide bool) { calls = append(calls, hide) }
	return &calls
}

func stubHubChromeDefault(t *testing.T, shown bool) {
	t.Helper()
	old := hubChromeDefault
	t.Cleanup(func() { hubChromeDefault = old })
	hubChromeDefault = func() bool { return shown }
}

func TestFleetChromeToggle(t *testing.T) {
	calls := stubHubZoom(t)
	m := newFleetModel()
	if m.HubChrome() {
		t.Fatal("chrome must default to hidden")
	}

	got, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Text: "c"})
	m = got.(FleetModel)
	if !m.HubChrome() {
		t.Fatal("c did not show chrome")
	}
	if cmd == nil {
		t.Fatal("toggle must sync the zoom")
	}
	cmd()

	got, cmd = m.Update(tea.KeyPressMsg{Code: 'c', Text: "c"})
	m = got.(FleetModel)
	if m.HubChrome() {
		t.Fatal("second c did not hide chrome")
	}
	cmd()

	if want := []bool{false, true}; !slices.Equal(*calls, want) {
		t.Errorf("hide calls = %v, want %v", *calls, want)
	}
}

func TestFleetChromeReexecRoundTrip(t *testing.T) {
	for _, shown := range []bool{true, false} {
		t.Run(map[bool]string{true: "shown", false: "hidden"}[shown], func(t *testing.T) {
			// The config default is the opposite, so only the env can carry it.
			stubHubChromeDefault(t, !shown)
			m := newFleetModel()
			m.hubChrome = shown
			var val string
			for _, e := range m.ReexecEnv() {
				if v, ok := cutEnv(e, HubChromeEnv); ok {
					val = v
				}
			}
			if val == "" {
				t.Fatalf("ReexecEnv %v lacks %s", m.ReexecEnv(), HubChromeEnv)
			}
			t.Setenv(HubChromeEnv, val)
			if got := NewFleetModel(); got.HubChrome() != shown {
				t.Errorf("HubChrome after reexec = %v, want %v", got.HubChrome(), shown)
			}
			if _, ok := os.LookupEnv(HubChromeEnv); ok {
				t.Error("env not consumed")
			}
		})
	}
}

func TestFleetChromeConfigDefault(t *testing.T) {
	stubHubChromeDefault(t, true)
	t.Setenv(HubChromeEnv, "garbage")
	if !NewFleetModel().HubChrome() {
		t.Error("config hub_chrome: true not honored")
	}
}

func cutEnv(entry, key string) (string, bool) {
	return strings.CutPrefix(entry, key+"=")
}
