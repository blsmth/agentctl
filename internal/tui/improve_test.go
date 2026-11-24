package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestImprovePopupIsThemeOnly(t *testing.T) {
	press := func(m ImproveModel, msg tea.Msg) ImproveModel {
		next, _ := m.Update(msg)
		return next.(ImproveModel)
	}
	t.Run("enter on empty is a general review", func(t *testing.T) {
		m := press(NewImproveModel("go-engineer"), tea.KeyPressMsg{Code: tea.KeyEnter})
		if r := m.Result(); r.Cancelled || r.Theme != "" {
			t.Errorf("result = %+v", r)
		}
	})
	t.Run("typed focus is the theme", func(t *testing.T) {
		m := NewImproveModel("go-engineer")
		for _, r := range "tests" {
			m = press(m, tea.KeyPressMsg{Code: r, Text: string(r)})
		}
		m = press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
		if r := m.Result(); r.Cancelled || r.Theme != "tests" {
			t.Errorf("result = %+v", r)
		}
	})
	t.Run("esc cancels", func(t *testing.T) {
		m := press(NewImproveModel("go-engineer"), tea.KeyPressMsg{Code: tea.KeyEscape})
		if !m.Result().Cancelled {
			t.Error("esc did not cancel")
		}
	})
}
