package tui

import (
	"testing"

	"github.com/blsmth/agentctl/internal/cron"
)

// TestTabKeyNeverOpensNewSession: T cycles the group's tabs wherever the
// cursor is, and never opens the N prompt.
func TestTabKeyNeverOpensNewSession(t *testing.T) {
	f := newSchedFixture(t)
	f.create(t, cron.Job{Name: "babysit", Schedule: "0 6 * * *", Dir: f.project})
	s := artStore(t)
	addArt(t, s, f.project, "Q4 report")
	m := f.twoGroups(t)
	m = schedDrive(t, m, m.schedLoadCmd())
	m = withArtifacts(t, m, s)

	const sess = "agentctl-acme-api"
	find := func(m FleetModel, pred func(fleetLine) bool) int {
		for i, l := range m.lines {
			if (l.group.Session == sess || l.row.Session == sess) && pred(l) {
				return i
			}
		}
		return -1
	}
	cases := []struct {
		name string
		tab  fleetTab
		line func(fleetLine) bool
		want fleetTab
	}{
		{"header", tabKindSessions, func(l fleetLine) bool { return l.kind == fleetLineHeader }, tabKindSched},
		{"agent row", tabKindSessions, func(l fleetLine) bool { return l.kind == fleetLineRow }, tabKindSched},
		{"job row", tabKindSched, func(l fleetLine) bool { return l.kind == fleetLineSchedule && !l.schedNew }, tabKindArtifacts},
		{"artifact row", tabKindArtifacts, func(l fleetLine) bool { return l.kind == fleetLineArtifact }, tabKindSessions},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mm := m
			for i := 0; i < 3 && tabKey(mm, sess) != c.tab; i++ {
				mm = schedPress(t, mm, fleetSchedulesKey)
			}
			idx := find(mm, c.line)
			if idx < 0 {
				t.Fatalf("no %s line", c.name)
			}
			mm.splitPane.SetCursor(idx)
			mm = schedPress(t, mm, "T")
			if mm.newSessionOpen() {
				t.Fatal("T opened the new-session prompt")
			}
			if got := tabKey(mm, sess); got != c.want {
				t.Fatalf("tab = %d, want %d", got, c.want)
			}
		})
	}
}
