package artifactkit

import (
	"strings"
	"testing"

	"github.com/blsmth/agentctl/plugin/skills/artifacts/kit"
)

// TestTableLayoutRules guards the phone layout: the pinned first column, its
// print reset, and the KPI breakdown label truncation.
func TestTableLayoutRules(t *testing.T) {
	for name, tc := range map[string]struct{ src, want string }{
		"css pins first column":       {kit.CSS, "th.pin,td.pin{position:sticky;left:0;background:var(--bg)"},
		"css pinned header opaque":    {kit.CSS, "thead th.pin{z-index:2;background:var(--card)}"},
		"css print drops the pin":     {kit.CSS, "th.pin,td.pin,thead th.pin{position:static;box-shadow:none}"},
		"css kpi label truncation":    {kit.CSS, ".kpi .breakdown .lab.trunc{white-space:nowrap;overflow:hidden;text-overflow:ellipsis}"},
		"js pins only multi-column":   {kit.JS, "i === 0 && columns.length > 1"},
		"js kpi label has title":      {kit.JS, "lab.title = String(r.label)"},
		"js fallback shares buildTab": {kit.JS, "buildTable(cols, rows, { sortable: false })"},
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(tc.src, tc.want) {
				t.Errorf("missing %q", tc.want)
			}
		})
	}
}
