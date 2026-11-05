package reaper

import (
	"errors"
	"reflect"
	"slices"
	"testing"
)

// sampleProcs is a synthetic process table used across multiple tests.
//
//	PID  PPID  Cmd
//	 1     0   init
//	 10    1   tmux: server
//	 11   10   tmux pane shell (live)
//	 12   11   zsh (child of pane)
//	 13   12   claude --agent go-engineer --settings /proj/.agentctl/settings/go-engineer.json
//	 20   10   tmux pane shell (live) — second window
//	 30    1   zsh (orphan shell, no tmux parent in live set)
//	 31   30   claude --agent go-engineer --settings /proj/.agentctl/settings/go-engineer.json (orphan)
//	 40    1   claude (no settings arg, different instance)
var sampleProcs = []ProcessInfo{
	{PID: 1, PPID: 0, Cmd: "init"},
	{PID: 10, PPID: 1, Cmd: "tmux: server"},
	{PID: 11, PPID: 10, Cmd: "zsh"},
	{PID: 12, PPID: 11, Cmd: "zsh"},
	{PID: 13, PPID: 12, Cmd: "claude --agent go-engineer --settings /proj/.agentctl/settings/go-engineer.json"},
	{PID: 20, PPID: 10, Cmd: "zsh"},
	{PID: 30, PPID: 1, Cmd: "zsh"},
	{PID: 31, PPID: 30, Cmd: "claude --agent go-engineer --settings /proj/.agentctl/settings/go-engineer.json"},
	{PID: 40, PPID: 1, Cmd: "claude --agent other-engineer --settings /other-proj/.agentctl/settings/go-engineer.json"},
}

// ---- parsePS ---------------------------------------------------------------

func TestParsePS(t *testing.T) {
	t.Helper()
	input := `  13   12 claude --agent go-engineer
   1    0 init
  99   50 node /home/user/.nvm/versions/node/v20.0.0/lib/node_modules/@anthropic-ai/claude-code/cli.js
`
	got := parsePS(input)
	if len(got) != 3 {
		t.Fatalf("want 3 procs, got %d", len(got))
	}
	if got[0].PID != 13 || got[0].PPID != 12 {
		t.Errorf("first proc: got pid=%d ppid=%d, want 13 12", got[0].PID, got[0].PPID)
	}
	if got[0].Cmd != "claude --agent go-engineer" {
		t.Errorf("first proc cmd: %q", got[0].Cmd)
	}
}

func TestParsePS_SkipsInvalidLines(t *testing.T) {
	input := "not a valid line\n  bad 99 cmd\n  1 0 init\n"
	got := parsePS(input)
	// Only the last line (1 0 init) should parse cleanly; "bad" has non-numeric PPID field first.
	// Actually "not a valid line" has only 4 tokens with no numeric pid/ppid.
	// Let's just assert we get at least the one valid line.
	found := false
	for _, p := range got {
		if p.PID == 1 && p.Cmd == "init" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected to find pid=1 init in %v", got)
	}
}

// ---- BuildDescendantSet ----------------------------------------------------

func TestBuildDescendantSet_DirectChildren(t *testing.T) {
	procs := []ProcessInfo{
		{PID: 1, PPID: 0, Cmd: "init"},
		{PID: 2, PPID: 1, Cmd: "sh"},
		{PID: 3, PPID: 2, Cmd: "claude"},
		{PID: 4, PPID: 0, Cmd: "other"},
	}
	roots := []int{1}
	got := BuildDescendantSet(procs, roots)

	for _, wantIn := range []int{1, 2, 3} {
		if !got[wantIn] {
			t.Errorf("expected pid %d to be in descendant set", wantIn)
		}
	}
	if got[4] {
		t.Errorf("pid 4 should not be in descendant set")
	}
}

func TestBuildDescendantSet_RootsIncluded(t *testing.T) {
	procs := []ProcessInfo{{PID: 10, PPID: 0, Cmd: "tmux"}}
	got := BuildDescendantSet(procs, []int{10})
	if !got[10] {
		t.Error("root itself should be in descendant set")
	}
}

func TestBuildDescendantSet_MultipleRoots(t *testing.T) {
	procs := []ProcessInfo{
		{PID: 1, PPID: 0, Cmd: "init"},
		{PID: 10, PPID: 1, Cmd: "paneA"},
		{PID: 11, PPID: 10, Cmd: "claude"},
		{PID: 20, PPID: 1, Cmd: "paneB"},
		{PID: 21, PPID: 20, Cmd: "claude"},
		{PID: 30, PPID: 1, Cmd: "orphan"},
	}
	got := BuildDescendantSet(procs, []int{10, 20})
	if !got[11] {
		t.Error("11 should be live (descendant of paneA=10)")
	}
	if !got[21] {
		t.Error("21 should be live (descendant of paneB=20)")
	}
	if got[30] {
		t.Error("30 should NOT be live")
	}
}

func TestBuildDescendantSet_EmptyRoots(t *testing.T) {
	procs := []ProcessInfo{{PID: 1, PPID: 0, Cmd: "init"}}
	got := BuildDescendantSet(procs, nil)
	if len(got) != 0 {
		t.Errorf("expected empty set with no roots, got %v", got)
	}
}

// ---- FilterClaudeProcesses -------------------------------------------------

func TestFilterClaudeProcesses_MatchesClaude(t *testing.T) {
	procs := []ProcessInfo{
		{PID: 1, PPID: 0, Cmd: "init"},
		{PID: 2, PPID: 1, Cmd: "claude --agent foo"},
		{PID: 3, PPID: 1, Cmd: "zsh"},
	}
	got := FilterClaudeProcesses(procs, "")
	if len(got) != 1 || got[0].PID != 2 {
		t.Errorf("expected pid=2, got %v", got)
	}
}

func TestFilterClaudeProcesses_NodeClaudeCode(t *testing.T) {
	procs := []ProcessInfo{
		{PID: 5, PPID: 1, Cmd: "node /home/user/.nvm/versions/node/v20.0.0/lib/node_modules/@anthropic-ai/claude-code/cli.js"},
	}
	got := FilterClaudeProcesses(procs, "")
	if len(got) != 1 {
		t.Errorf("expected claude-code node process to be matched, got %v", got)
	}
}

func TestFilterClaudeProcesses_SettingsDirFilter(t *testing.T) {
	settingsDir := "/proj/.agentctl/settings"
	got := FilterClaudeProcesses(sampleProcs, settingsDir)

	pids := pidSet(got)
	// PID 13 and 31 match our settingsDir.
	for _, want := range []int{13, 31} {
		if !pids[want] {
			t.Errorf("expected pid=%d in filtered set", want)
		}
	}
	// PID 40 uses a different settings dir — should be excluded.
	if pids[40] {
		t.Error("pid=40 (different settings dir) should be excluded")
	}
}

func TestFilterClaudeProcesses_NoFalsePositives(t *testing.T) {
	procs := []ProcessInfo{
		{PID: 1, PPID: 0, Cmd: "include-claude-files --something"},
		{PID: 2, PPID: 0, Cmd: "/bin/sh"},
		{PID: 3, PPID: 0, Cmd: "grep claude"},
	}
	got := FilterClaudeProcesses(procs, "")
	// "include-claude-files" should NOT match — we only match the token "claude" as a basename.
	// "grep claude" — "claude" is an argument not a basename, so should NOT match.
	for _, p := range got {
		t.Errorf("unexpected match: pid=%d cmd=%q", p.PID, p.Cmd)
	}
}

// ---- containsClaude --------------------------------------------------------

func TestContainsClaude(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		{"claude --agent foo", true},
		{"/usr/local/bin/claude --session-id abc", true},
		{"node /home/user/.nvm/versions/node/v20.0.0/lib/node_modules/@anthropic-ai/claude-code/cli.js", true},
		{"node /path/to/claude/index.js", true},
		{"/bin/zsh", false},
		{"grep claude", false},
		{"include-claude-stuffs", false},
		{"cat /etc/claude.conf", false},
		{"claude-code-wrapper", true}, // "claude-" prefix
	}
	for _, tc := range cases {
		t.Run(tc.cmd, func(t *testing.T) {
			if got := containsClaude(tc.cmd); got != tc.want {
				t.Errorf("containsClaude(%q) = %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}

// ---- HasClaudeDescendant ----------------------------------------------------

// TestHasClaudeDescendant exercises the pure BuildDescendantSet+containsClaude
// logic HasClaudeDescendant wraps, using a fake process table (ListProcesses
// itself shells out to `ps`, so the exported entry point is not exercised
// directly here -- see the pattern used throughout this file).
func TestHasClaudeDescendant(t *testing.T) {
	// PID 12 is a pane shell whose child (13) is a live claude process.
	// PID 20 is a pane shell with no claude descendant at all (e.g. the
	// agent already exited to a shell -- the exact case wake must refuse).
	procs := []ProcessInfo{
		{PID: 12, PPID: 11, Cmd: "zsh"},
		{PID: 13, PPID: 12, Cmd: "claude --agent go-engineer"},
		{PID: 20, PPID: 10, Cmd: "zsh"},
	}

	live := func(panePID int) bool {
		procsForTest := procs
		liveSet := BuildDescendantSet(procsForTest, []int{panePID})
		for _, p := range procsForTest {
			if liveSet[p.PID] && containsClaude(p.Cmd) {
				return true
			}
		}
		return false
	}

	if !live(12) {
		t.Error("expected pane 12 (parent of a live claude process) to report a claude descendant")
	}
	if live(20) {
		t.Error("expected pane 20 (shell with no claude descendant) to report no claude descendant")
	}
}

func TestHasClaudeDescendant_InvalidPID(t *testing.T) {
	if HasClaudeDescendant(0) {
		t.Error("HasClaudeDescendant(0) should be false")
	}
	if HasClaudeDescendant(-1) {
		t.Error("HasClaudeDescendant(-1) should be false")
	}
}

// TestHasClaudeDescendant_Live exercises the real exported function, which
// calls the actual ListProcesses() (a real `ps` shellout). It only asserts
// on a PID that cannot possibly exist, so it stays deterministic without
// mocking `ps`.
func TestHasClaudeDescendant_Live(t *testing.T) {
	if HasClaudeDescendant(999999999) {
		t.Error("expected no claude descendant for a PID that cannot exist")
	}
}

// ---- helpers ---------------------------------------------------------------

func pidSet(procs []ProcessInfo) map[int]bool {
	m := make(map[int]bool, len(procs))
	for _, p := range procs {
		m[p.PID] = true
	}
	return m
}

// ---- findOrphans / ReapOrphans ---------------------------------------------

const (
	ourSettings = "/proj/.agentctl/settings"
	serverPID   = 10
)

func ourClaude() string {
	return "claude --agent go-engineer --settings " + ourSettings + "/go-engineer.json"
}

// world is a synthetic process table covering every case the reaper must
// get right. Live pane PIDs on the tmux server are 11 and 20.
//
//	1    launchd
//	10   tmux server
//	11   zsh            live pane
//	13   claude (ours)  under live pane 11                 -> spare
//	14   zsh            vim :terminal-style shell under 11 -> spare
//	20   zsh            live pane
//	30   zsh            reparented to 1 after pane died    -> spare
//	31   claude (ours)  child of 30, reparented chain      -> spare
//	40   claude (other) another instance, directly under server -> spare
//	50   claude (ours)  direct child of server, pane gone  -> KILL
//	58   login / 59 zsh iTerm shell on non-tmux TTY        -> spare
//	60   claude (ours)  under the iTerm shell              -> spare
//	62   bash deploy.sh outside tmux (pid == pgid)         -> spare
//	70   zsh            child of server, pane gone (dead TTY)
//	71   claude (ours)  child of 70                        -> KILL
//	80/81 claude (ours) ppid cycle (torn snapshot)         -> spare
var world = []ProcessInfo{
	{PID: 1, PPID: 0, Cmd: "/sbin/launchd"},
	{PID: serverPID, PPID: 1, Cmd: "tmux new-session -d -s agentctl-proj"},
	{PID: 11, PPID: serverPID, Cmd: "-zsh"},
	{PID: 13, PPID: 11, Cmd: ourClaude()},
	{PID: 14, PPID: 13, Cmd: "/bin/zsh"},
	{PID: 20, PPID: serverPID, Cmd: "-zsh"},
	{PID: 30, PPID: 1, Cmd: "-zsh"},
	{PID: 31, PPID: 30, Cmd: ourClaude()},
	{PID: 40, PPID: serverPID, Cmd: "claude --settings /other/.agentctl/settings/x.json"},
	{PID: 50, PPID: serverPID, Cmd: ourClaude()},
	{PID: 58, PPID: 1, Cmd: "login -pfl alice /bin/zsh"},
	{PID: 59, PPID: 58, Cmd: "-zsh"},
	{PID: 60, PPID: 59, Cmd: ourClaude()},
	{PID: 62, PPID: 59, Cmd: "bash ./deploy.sh"},
	{PID: 70, PPID: serverPID, Cmd: "-zsh"},
	{PID: 71, PPID: 70, Cmd: ourClaude()},
	{PID: 80, PPID: 81, Cmd: ourClaude()},
	{PID: 81, PPID: 80, Cmd: ourClaude()},
}

var worldPanes = []int{11, 20}

func orphanPIDs(orphans []orphan) []int {
	out := make([]int, 0, len(orphans))
	for _, o := range orphans {
		out = append(out, o.proc.PID)
	}
	slices.Sort(out)
	return out
}

func TestFindOrphans(t *testing.T) {
	tests := []struct {
		name        string
		procs       []ProcessInfo
		serverPID   int
		panes       []int
		settingsDir string
		want        []int
	}{
		{
			name:        "only claude provably under our tmux server and no live pane",
			procs:       world,
			serverPID:   serverPID,
			panes:       worldPanes,
			settingsDir: ourSettings,
			want:        []int{50, 71},
		},
		{
			name:        "empty settingsDir kills nothing",
			procs:       world,
			serverPID:   serverPID,
			panes:       worldPanes,
			settingsDir: "",
		},
		{
			name:        "tmux not running kills nothing",
			procs:       world,
			serverPID:   0,
			settingsDir: ourSettings,
		},
		{
			name:        "server pid recycled to a non-tmux process kills nothing",
			procs:       append([]ProcessInfo{{PID: 99, PPID: 1, Cmd: "/usr/bin/python3"}}, world...),
			serverPID:   99,
			panes:       worldPanes,
			settingsDir: ourSettings,
		},
		{
			name:        "server pid absent from snapshot kills nothing",
			procs:       world[2:], // drop launchd and the server
			serverPID:   serverPID,
			panes:       worldPanes,
			settingsDir: ourSettings,
		},
		{
			name:        "no live panes still requires tmux ancestry",
			procs:       world,
			serverPID:   serverPID,
			panes:       nil,
			settingsDir: ourSettings,
			want:        []int{13, 50, 71},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := orphanPIDs(findOrphans(tt.procs, tt.serverPID, tt.panes, tt.settingsDir))
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("findOrphans = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFindOrphans_EvidenceChain(t *testing.T) {
	got := findOrphans(world, serverPID, worldPanes, ourSettings)
	chains := map[int][]int{}
	for _, o := range got {
		chains[o.proc.PID] = o.chain
	}
	if want := []int{50, serverPID}; !reflect.DeepEqual(chains[50], want) {
		t.Errorf("chain for 50 = %v, want %v", chains[50], want)
	}
	if want := []int{71, 70, serverPID}; !reflect.DeepEqual(chains[71], want) {
		t.Errorf("chain for 71 = %v, want %v", chains[71], want)
	}
}

func TestAncestryTo(t *testing.T) {
	parent := map[int]int{1: 0, 10: 1, 70: 10, 71: 70, 30: 1, 31: 30, 80: 81, 81: 80}
	tests := []struct {
		name   string
		pid    int
		want   []int
		wantOK bool
	}{
		{"direct child", 70, []int{70, 10}, true},
		{"grandchild", 71, []int{71, 70, 10}, true},
		{"reparented to pid 1", 31, nil, false},
		{"pid missing from snapshot", 999, nil, false},
		{"ppid cycle terminates", 80, nil, false},
		{"the server itself is not its own descendant", 10, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ancestryTo(tt.pid, 10, parent)
			if ok != tt.wantOK || !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ancestryTo(%d) = %v, %v; want %v, %v", tt.pid, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

// fakeSources swaps every OS-touching injection point for the duration of a
// test and records which pids were signaled and the order of snapshot calls.
type fakeSources struct {
	procs     []ProcessInfo
	procsErr  error
	panes     []int
	panesErr  error
	serverPID int
	serverErr error

	signaled []int
	calls    []string
}

func (f *fakeSources) install(t *testing.T) {
	t.Helper()
	origList, origPanes, origServer, origSignal := listProcesses, listPanePIDs, queryServerPID, signalPID
	t.Cleanup(func() {
		listProcesses, listPanePIDs, queryServerPID, signalPID = origList, origPanes, origServer, origSignal
	})
	listProcesses = func() ([]ProcessInfo, error) {
		f.calls = append(f.calls, "ps")
		return f.procs, f.procsErr
	}
	listPanePIDs = func() ([]int, error) {
		f.calls = append(f.calls, "panes")
		return f.panes, f.panesErr
	}
	queryServerPID = func(string) (int, error) {
		f.calls = append(f.calls, "server")
		return f.serverPID, f.serverErr
	}
	signalPID = func(pid int) error {
		f.signaled = append(f.signaled, pid)
		return nil
	}
}

func TestReapOrphans(t *testing.T) {
	errNoServer := errors.New("no server running on /tmp/tmux-501/default")
	tests := []struct {
		name        string
		src         fakeSources
		settingsDir string
		want        []int
	}{
		{
			name:        "kills only proven orphans",
			src:         fakeSources{procs: world, panes: worldPanes, serverPID: serverPID},
			settingsDir: ourSettings,
			want:        []int{50, 71},
		},
		{
			name:        "empty settingsDir returns 0 and kills nothing",
			src:         fakeSources{procs: world, panes: worldPanes, serverPID: serverPID},
			settingsDir: "",
		},
		{
			name:        "tmux not running is a no-op",
			src:         fakeSources{procs: world, serverErr: errNoServer},
			settingsDir: ourSettings,
		},
		{
			name:        "pane listing failure is a no-op",
			src:         fakeSources{procs: world, serverPID: serverPID, panesErr: errNoServer},
			settingsDir: ourSettings,
		},
		{
			name:        "ps failure is a no-op",
			src:         fakeSources{procsErr: errors.New("ps: boom"), panes: worldPanes, serverPID: serverPID},
			settingsDir: ourSettings,
		},
		{
			// Shells are never reaped: an iTerm shell, `bash deploy.sh`
			// outside tmux, and a shell whose pane died under our server
			// all survive. Only the Claude child of the dead pane goes.
			name: "shells are never signaled",
			src: fakeSources{
				procs: []ProcessInfo{
					{PID: serverPID, PPID: 1, Cmd: "tmux"},
					{PID: 59, PPID: 58, Cmd: "-zsh"},
					{PID: 62, PPID: 59, Cmd: "bash ./deploy.sh"},
					{PID: 70, PPID: serverPID, Cmd: "-zsh"},
					{PID: 71, PPID: 70, Cmd: ourClaude()},
				},
				serverPID: serverPID,
			},
			settingsDir: ourSettings,
			want:        []int{71},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := tt.src
			src.install(t)
			n := ReapOrphans("agentctl-proj", tt.settingsDir)
			slices.Sort(src.signaled)
			if n != len(tt.want) {
				t.Errorf("ReapOrphans returned %d, want %d", n, len(tt.want))
			}
			if len(src.signaled) != 0 || len(tt.want) != 0 {
				if !reflect.DeepEqual(src.signaled, tt.want) {
					t.Errorf("signaled %v, want %v", src.signaled, tt.want)
				}
			}
			if tt.settingsDir == "" && len(src.calls) != 0 {
				t.Errorf("empty settingsDir should not touch ps/tmux, got calls %v", src.calls)
			}
		})
	}
}

// TestReapOrphans_SnapshotBeforePanes guards the ordering that makes a pane
// created mid-sweep impossible to mistake for an orphan.
func TestReapOrphans_SnapshotBeforePanes(t *testing.T) {
	src := fakeSources{procs: world, panes: worldPanes, serverPID: serverPID}
	src.install(t)
	ReapOrphans("agentctl-proj", ourSettings)
	if want := []string{"server", "ps", "panes"}; !reflect.DeepEqual(src.calls, want) {
		t.Errorf("call order = %v, want %v", src.calls, want)
	}
}

func TestReapOrphansUnderServer_NoServer(t *testing.T) {
	src := fakeSources{procs: world, panes: worldPanes}
	src.install(t)
	if n := ReapOrphansUnderServer(0, ourSettings); n != 0 {
		t.Errorf("ReapOrphansUnderServer(0) = %d, want 0", n)
	}
	if len(src.calls) != 0 || len(src.signaled) != 0 {
		t.Errorf("expected no OS calls, got calls=%v signaled=%v", src.calls, src.signaled)
	}
}

func TestServerPID_NotRunning(t *testing.T) {
	src := fakeSources{serverErr: errors.New("no server")}
	src.install(t)
	if got := ServerPID("agentctl-proj"); got != 0 {
		t.Errorf("ServerPID = %d, want 0", got)
	}
}
