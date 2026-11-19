package tui

import (
	"strings"
	"testing"
)

func TestKillModelChildrenLine(t *testing.T) {
	tests := []struct {
		name     string
		children []string
		want     string
	}{
		{"none", nil, ""},
		{"one", []string{"go-engineer-3"}, "Also closes 1 dispatched session: go-engineer-3"},
		{"two", []string{"go-engineer-3", "code-reviewer-1"}, "Also closes 2 dispatched sessions: go-engineer-3, code-reviewer-1"},
		{"capped", []string{"a", "b", "c", "d", "e"}, "Also closes 5 dispatched sessions: a, b, c +2 more"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := NewKillModel("orchestrator").WithChildren(tc.children)
			if got := m.childrenLine(); got != tc.want {
				t.Errorf("childrenLine() = %q, want %q", got, tc.want)
			}
			view := stripANSI(m.View().Content)
			if tc.want == "" {
				if strings.Contains(view, "Also closes") {
					t.Errorf("view mentions children with none:\n%s", view)
				}
				return
			}
			if !strings.Contains(strings.Join(strings.Fields(view), " "), "Also closes") {
				t.Errorf("view missing children note:\n%s", view)
			}
		})
	}
}
