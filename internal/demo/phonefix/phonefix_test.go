package phonefix

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/demo"
	"github.com/blsmth/agentctl/internal/serve/servetest"
	"github.com/blsmth/agentctl/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

const scene = `
projects:
  - name: acme-api
  - name: billing-svc
windows:
  - key: orch
    project: acme-api
    agent: orchestrator
    state: working
    title: Rolling Out
    cost_usd: 4.12
    ctx_pct: 38
    since: 2m
    spinner: "Waiting… (esc to interrupt)"
    transcript:
      recap: Dispatched two workers.
      entries:
        - user: Roll out the limiter.
        - say: On it.
  - key: worker
    project: acme-api
    agent: go-engineer
    mode: dispatched
    state: done
    ticket: 142
    ticket_title: Add limiter
    kind: go
    title: Adding Limiter
    pr: 151
    spawned_by: orch
    transcript:
      entries:
        - user: Add the limiter.
        - tool: Bash
          input: {command: go test ./...}
          result: ok
  - key: rev
    project: acme-api
    agent: code-reviewer
    mode: review-pr
    review_pr: 138
    title: Reviewing
    spawned_by: orch
  - key: bare
    project: billing-svc
    agent: docs
    state: done
  - key: blocked
    project: billing-svc
    agent: go-engineer
    mode: dispatched
    state: blocked
    pending_tool: Bash
    pending_detail: psql -f m.sql
    since: 4m
    permission:
      command: psql -f m.sql
  - key: idle
    project: billing-svc
    agent: docs
    last_tool: ls
steps:
  - screenshot
`

func load(t *testing.T) (map[string]servetest.Window, map[string]Ref, []servetest.Group) {
	t.Helper()
	s, err := demo.ParseScene([]byte(scene))
	if err != nil {
		t.Fatal(err)
	}
	groups, refs, err := Convert(s, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ws, rs := map[string]servetest.Window{}, map[string]Ref{}
	for _, r := range refs {
		rs[r.Key] = r
		for _, g := range groups {
			for _, w := range g.Windows {
				if w.ID == r.WindowID {
					ws[r.Key] = w
				}
			}
		}
	}
	return ws, rs, groups
}

func TestConvertStates(t *testing.T) {
	ws, _, groups := load(t)
	if len(groups) != 2 || groups[0].Session != "agentctl-acme-api" {
		t.Fatalf("groups = %+v", groups)
	}
	tests := []struct {
		key, state string
		done       bool
		turnDone   bool
	}{
		{"orch", "working", false, false},
		{"worker", "idle", true, false},
		{"rev", "idle", false, false},
		{"bare", "idle", false, true},
		{"blocked", "blocked", false, false},
		{"idle", "idle", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			w := ws[tt.key]
			if w.State != tt.state || w.Done != tt.done || w.TurnDone != tt.turnDone {
				t.Errorf("state=%q done=%v turnDone=%v, want %q %v %v", w.State, w.Done, w.TurnDone, tt.state, tt.done, tt.turnDone)
			}
			if w.PaneText == "" {
				t.Error("no pane text")
			}
		})
	}
}

func TestConvertFields(t *testing.T) {
	ws, _, _ := load(t)
	w := ws["worker"]
	if w.Parent != ws["orch"].ID || w.Mode != "dispatch" || w.Issue != 142 || w.Title != "Add limiter" ||
		w.ShortTitle != "Adding Limiter" || w.ShortKind != "go" || w.PRNumber != 151 ||
		w.PRURL != "https://github.com/acme/acme-api/pull/151" || w.Prompt != "Add the limiter." ||
		w.Activity != "go test ./..." || w.Recap != "#142 Add limiter" || w.RecapSource != "task" {
		t.Errorf("worker = %+v", w)
	}
	if o := ws["orch"]; o.Recap != "Dispatched two workers." || o.RecapSource != "summary" || o.CostUSD != 4.12 ||
		o.ContextPct != 38 || o.SinceSec != 120 || o.Mode != "bare" || o.Branch != "" {
		t.Errorf("orch = %+v", o)
	}
	if r := ws["rev"]; r.Mode != "review-pr" || r.Task != "Review PR #138" || r.ShortKind != "review" {
		t.Errorf("rev = %+v", r)
	}
	if ws["idle"].Activity != "ls" {
		t.Errorf("idle activity = %q", ws["idle"].Activity)
	}
}

func TestConvertApproval(t *testing.T) {
	ws, refs, _ := load(t)
	r := refs["blocked"]
	if !r.Blocked || r.Command != "psql -f m.sql" || r.Session != "agentctl-billing-svc" {
		t.Errorf("ref = %+v", r)
	}
	if refs["idle"].Blocked {
		t.Error("idle window marked blocked")
	}
	w := ws["blocked"]
	if w.PendingTool != "Bash" || w.PendingDetail != "psql -f m.sql" || w.SinceSec != 240 {
		t.Errorf("blocked = %+v", w)
	}
	if !strings.Contains(w.PaneText, "Do you want to proceed?") || !strings.Contains(w.PaneText, "psql -f m.sql") {
		t.Errorf("blocked pane lacks the permission dialog:\n%s", w.PaneText)
	}
}

// TestPaneIsTerminalRender pins that the phone's pane text is demo.Render's.
func TestPaneIsTerminalRender(t *testing.T) {
	ws, _, _ := load(t)
	p := ws["worker"].PaneText
	for _, want := range []string{"Claude Code", "v2.1", "Add the limiter.", "go test ./...", "❯"} {
		if !strings.Contains(p, want) {
			t.Errorf("pane lacks %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "\r") {
		t.Error("pane has carriage returns")
	}
}

func TestConvertUnknownFixture(t *testing.T) {
	s, err := demo.ParseScene([]byte(strings.Replace(scene, "    title: Rolling Out\n", "    title: Rolling Out\n    transcript: nope\n", 0)))
	if err != nil {
		t.Fatal(err)
	}
	s.Windows[0].Transcript = demo.TranscriptRef{Name: "nope"}
	if _, _, err := Convert(s, t.TempDir()); err == nil {
		t.Error("want an error for a missing transcript fixture")
	}
}

// TestHubSceneServes converts the checked-in hub scene and starts the fixture
// harness on it, which validates the fleet.
func TestHubSceneServes(t *testing.T) {
	root := filepath.Join("..", "..", "..", "demo")
	s, err := demo.LoadScene(filepath.Join(root, "scenes", "hub.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	groups, _, err := Convert(s, filepath.Join(root, "transcripts"))
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "phonefix-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	h, err := servetest.New(servetest.Options{Dir: dir, Fleet: groups})
	if err != nil {
		t.Fatal(err)
	}
	h.Close()
}
