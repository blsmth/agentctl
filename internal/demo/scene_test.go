package demo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const minimalScene = `
projects:
  - name: acme-api
windows:
  - key: orch
    project: acme-api
    agent: orchestrator
    state: working
  - key: worker
    project: acme-api
    agent: go-engineer
    mode: dispatched
    ticket: 142
    spawned_by: orch
    state: done
  - key: worker2
    project: acme-api
    agent: go-engineer
    mode: dispatched
steps:
  - attach: orch
  - prefix: g
  - screenshot
`

func TestParseSceneDefaults(t *testing.T) {
	s, err := ParseScene([]byte(minimalScene))
	if err != nil {
		t.Fatal(err)
	}
	if s.Terminal.Cols != 160 || s.Terminal.Rows != 45 || s.Output != "png" || s.Config.DispatchApproval != "all" {
		t.Errorf("terminal/output defaults = %+v %q %q", s.Terminal, s.Output, s.Config.DispatchApproval)
	}
	if got := s.Projects[0].Branch; got != "main" {
		t.Errorf("project branch = %q, want main", got)
	}
	tests := []struct {
		key, instance, branch string
		worktree, seen        bool
	}{
		{"orch", "orchestrator", "", false, true},
		{"worker", "go-engineer", "agent/go-engineer/issue-142", true, false},
		{"worker2", "go-engineer:2", "agent/go-engineer/adhoc-worker2", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			w, ok := s.Window(tt.key)
			if !ok {
				t.Fatal("window missing")
			}
			if w.Instance != tt.instance || w.Branch != tt.branch || w.HasWorktree() != tt.worktree || w.IsSeen() != tt.seen {
				t.Errorf("got instance=%q branch=%q worktree=%v seen=%v", w.Instance, w.Branch, w.HasWorktree(), w.IsSeen())
			}
		})
	}
	if !s.Steps[2].IsScreenshot() {
		t.Error("scalar screenshot step not recognized")
	}
}

func TestParseSceneErrors(t *testing.T) {
	tests := []struct {
		name, edit, want string
	}{
		{"unknown top-level key", "colour: blue\n", "field colour not found"},
		{"unknown window key", strings.Replace(minimalScene, "    state: working", "    state: working\n    staet: idle", 1), "field staet not found"},
		{"bad state", strings.Replace(minimalScene, "state: working", "state: busy", 1), `windows[0] (orch).state: "busy"`},
		{"bad mode", strings.Replace(minimalScene, "mode: dispatched\n    ticket", "mode: robot\n    ticket", 1), `.mode: "robot"`},
		{"dangling spawned_by", strings.Replace(minimalScene, "spawned_by: orch", "spawned_by: nobody", 1), `spawned_by: no window with key "nobody"`},
		{"bare with parent", strings.Replace(minimalScene, "  - key: worker2\n    project: acme-api\n    agent: go-engineer\n    mode: dispatched", "  - key: worker2\n    project: acme-api\n    agent: go-engineer\n    spawned_by: orch", 1), "bare windows have no dispatcher"},
		{"unknown project", strings.Replace(minimalScene, "    project: acme-api\n    agent: orchestrator", "    project: nope\n    agent: orchestrator", 1), `.project: unknown project "nope"`},
		{"duplicate key", strings.Replace(minimalScene, "key: worker2", "key: worker", 1), `duplicate key "worker"`},
		{"blocked without tool", strings.Replace(minimalScene, "state: working", "state: blocked", 1), "pending_tool: required"},
		{"pending tool when not blocked", strings.Replace(minimalScene, "state: working", "state: working\n    pending_tool: Bash", 1), "only valid with state blocked"},
		{"two actions in a step", strings.Replace(minimalScene, "  - prefix: g", "  - prefix: g\n    sleep: 1s", 1), "exactly one action per step"},
		{"unknown scalar step", strings.Replace(minimalScene, "  - screenshot", "  - snapshot", 1), `unknown step "snapshot"`},
		{"attach unknown window", strings.Replace(minimalScene, "attach: orch", "attach: ghost", 1), `attach: no window with key "ghost"`},
		{"bad key name", strings.Replace(minimalScene, "  - prefix: g", "  - key: F13", 1), `key: "F13"`},
		{"bad duration", strings.Replace(minimalScene, "state: working", "state: working\n    since: soon", 1), `since: "soon"`},
		{"png without screenshot", strings.Replace(minimalScene, "  - screenshot", "  - sleep: 1s", 1), "needs at least one screenshot"},
		{"bad agentctl theme", "config:\n  theme: no-such-theme\n" + minimalScene, `config.theme: unknown agentctl theme "no-such-theme"`},
		{"bad default_agent", "config:\n  default_agent: Go Engineer\n" + minimalScene, "config.default_agent"},
		{"bad dispatch_approval", "config:\n  dispatch_approval: sometimes\n" + minimalScene, "config.dispatch_approval"},
		{"inline transcript unknown key", strings.Replace(minimalScene, "state: working", "state: working\n    transcript:\n      entries:\n        - usr: hi", 1), "field usr not found"},
		{"bad schedule", "schedules:\n  - {name: n1, project: acme-api, agent: go-engineer, schedule: \"61 * * * *\", prompt: x}\n" + minimalScene, "schedules[0] (n1).schedule"},
		{"schedule unknown project", "schedules:\n  - {name: n1, project: nope, agent: go-engineer, schedule: \"0 9 * * *\", prompt: x}\n" + minimalScene, `schedules[0] (n1).project: unknown project "nope"`},
		{"schedule without prompt", "schedules:\n  - {name: n1, project: acme-api, agent: go-engineer, schedule: \"0 9 * * *\"}\n" + minimalScene, "schedules[0] (n1).prompt"},
		{"schedule bad run", "schedules:\n  - {name: n1, project: acme-api, agent: go-engineer, schedule: \"0 9 * * *\", prompt: x, runs: [{ago: 1h, result: ok}]}\n" + minimalScene, `runs[0].result: "ok"`},
		{"duplicate schedule", "schedules:\n  - {name: n1, project: acme-api, agent: go-engineer, schedule: \"0 9 * * *\", prompt: x}\n  - {name: n1, project: acme-api, agent: go-engineer, schedule: \"0 9 * * *\", prompt: x}\n" + minimalScene, `duplicate schedule "n1"`},
		{"dangling scheduled_by", strings.Replace(minimalScene, "state: working", "state: working\n    scheduled_by: ghost", 1), `scheduled_by: no schedule named "ghost"`},
		{"inline transcript two kinds", strings.Replace(minimalScene, "state: working", "state: working\n    transcript:\n      entries:\n        - user: hi\n          say: there", 1), "exactly one of user, say or tool"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := tt.edit
			if strings.HasSuffix(tt.edit, "\n") && !strings.Contains(tt.edit, "projects:") {
				data = tt.edit + minimalScene
			}
			_, err := ParseScene([]byte(data))
			if err == nil {
				t.Fatalf("ParseScene succeeded, want error containing %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v\nwant it to contain %q", err, tt.want)
			}
		})
	}
}

// TestCheckedInScenes keeps every scene in demo/scenes valid, with every
// fixture it names resolvable.
func TestCheckedInScenes(t *testing.T) {
	root := filepath.Join("..", "..", "demo")
	paths, err := filepath.Glob(filepath.Join(root, "scenes", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) < 5 {
		t.Fatalf("found %d scenes, want at least 5", len(paths))
	}
	for _, p := range paths {
		t.Run(filepath.Base(p), func(t *testing.T) {
			s, err := LoadScene(p)
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range s.Windows {
				if _, err := w.Transcript.Resolve(filepath.Join(root, "transcripts")); err != nil {
					t.Errorf("window %s: %v", w.Key, err)
				}
			}
		})
	}
	fixtures, _ := filepath.Glob(filepath.Join(root, "transcripts", "*.yaml"))
	for _, f := range fixtures {
		name := strings.TrimSuffix(filepath.Base(f), ".yaml")
		if _, err := LoadFixture(filepath.Dir(f), name); err != nil {
			t.Errorf("fixture %s: %v", name, err)
		}
	}
}

func TestLoadSceneSetsName(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bug-123.yaml")
	if err := os.WriteFile(p, []byte(minimalScene), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadScene(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "bug-123" {
		t.Errorf("Name = %q, want bug-123", s.Name)
	}
}

func TestPhoneBlock(t *testing.T) {
	withPhone := func(phone string) string { return minimalScene + phone }
	s, err := ParseScene([]byte(withPhone(`
phone:
  steps:
    - open: fleet
    - open: window:orch
    - tap: Send
    - wait: css:.pane
    - screenshot
    - screenshot: detail
`)))
	if err != nil {
		t.Fatal(err)
	}
	if v := s.Phone.Viewport; v.Width != 393 || v.Height != 852 || v.DPR != 3 || s.Phone.Theme != "light" {
		t.Errorf("defaults = %+v %q", v, s.Phone.Theme)
	}
	plan, outs, err := BuildPhonePlan(s, "/out/x")
	if err != nil {
		t.Fatal(err)
	}
	if len(outs) != 2 || outs[0] != "/out/x-phone-1.png" || outs[1] != "/out/x-phone-detail.png" {
		t.Errorf("outs = %v", outs)
	}
	if len(plan.Steps) != 6 || plan.Steps[0].Kind != "open" || plan.Steps[0].Arg != "fleet" {
		t.Errorf("plan = %+v", plan.Steps)
	}

	s, _ = ParseScene([]byte(withPhone("phone:\n  steps:\n    - screenshot\n")))
	if _, outs, _ := BuildPhonePlan(s, "/out/x"); len(outs) != 1 || outs[0] != "/out/x-phone.png" {
		t.Errorf("single outs = %v", outs)
	}

	bad := map[string]string{
		"unknown key":                "phone:\n  bogus: 1\n  steps: [screenshot]\n",
		"bad theme":                  "phone:\n  theme: sepia\n  steps: [screenshot]\n",
		"no steps":                   "phone:\n  theme: dark\n",
		"no screenshot":              "phone:\n  steps:\n    - open: fleet\n",
		"bad open":                   "phone:\n  steps:\n    - open: nowhere\n    - screenshot\n",
		"unknown window":             "phone:\n  steps:\n    - open: window:nope\n    - screenshot\n",
		"approval without approval:": "phone:\n  steps:\n    - open: approval:orch\n    - screenshot\n",
		"two actions":                "phone:\n  steps:\n    - {open: fleet, tap: x}\n    - screenshot\n",
		"bad scalar":                 "phone:\n  steps:\n    - tap\n",
		"bad dpr":                    "phone:\n  viewport: {dpr: 9}\n  steps: [screenshot]\n",
	}
	for name, p := range bad {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseScene([]byte(withPhone(p))); err == nil {
				t.Error("want an error")
			}
		})
	}
}
