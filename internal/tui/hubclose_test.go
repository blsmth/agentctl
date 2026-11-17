package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestHubCloseModel(t *testing.T) {
	key := func(s string) tea.KeyPressMsg {
		switch s {
		case "esc":
			return tea.KeyPressMsg{Code: tea.KeyEscape}
		case "enter":
			return tea.KeyPressMsg{Code: tea.KeyEnter}
		case "tab":
			return tea.KeyPressMsg{Code: tea.KeyTab}
		}
		return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
	}
	tests := []struct {
		name string
		keys []string
		want bool
	}{
		{"default enter is no", []string{"enter"}, false},
		{"y confirms", []string{"y"}, true},
		{"n cancels", []string{"n"}, false},
		{"esc cancels", []string{"esc"}, false},
		{"tab then enter confirms", []string{"tab", "enter"}, true},
		{"tab twice then enter cancels", []string{"tab", "tab", "enter"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m tea.Model = NewHubCloseModel()
			for _, k := range tt.keys {
				m, _ = m.Update(key(k))
			}
			if got := m.(HubCloseModel).Result().Confirmed; got != tt.want {
				t.Errorf("Confirmed = %v, want %v", got, tt.want)
			}
		})
	}
}
