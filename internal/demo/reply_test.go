package demo

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// fakeHooks records every hook and Bash call of a turn, in order.
type fakeHooks struct {
	calls    []string
	payloads []map[string]any
	deny     string // non-empty: Validate denies with this reason
	runOut   string
	runErr   error
}

func (f *fakeHooks) Fire(cmd string, payload map[string]any) {
	f.calls = append(f.calls, cmd)
	f.payloads = append(f.payloads, payload)
}

func (f *fakeHooks) Validate(payload map[string]any) (bool, string) {
	f.calls = append(f.calls, "hook-validate-bash")
	f.payloads = append(f.payloads, payload)
	return f.deny == "", f.deny
}

func (f *fakeHooks) Run(cmdline string) (string, error) {
	f.calls = append(f.calls, "run: "+cmdline)
	f.payloads = append(f.payloads, nil)
	return f.runOut, f.runErr
}

// fakeClock advances only when the pane sleeps.
type fakeClock struct {
	t      time.Time
	sleeps []time.Duration
}

func (c *fakeClock) now() time.Time { return c.t }
func (c *fakeClock) sleep(d time.Duration) {
	c.sleeps = append(c.sleeps, d)
	c.t = c.t.Add(d)
}

// testPane is a pane with every seam stubbed: no terminal, no real sleeps,
// no processes, and the turn played synchronously on Enter.
func testPane(t *testing.T, w Window) (*pane, *fakeHooks, *fakeClock, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	h := &fakeHooks{runOut: "Creating worktree…\n\x1b]8;;x\x1b\\Dispatched prompt → go-engineer (window @12)\x1b]8;;\x1b\\\n"}
	c := &fakeClock{t: time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)}
	p := &pane{
		spec:  PaneSpec{Window: w, WorkDir: "/w", SessionID: "sid", LogPath: "/w/log.jsonl"},
		out:   &out,
		width: func() int { return 120 },
		now:   c.now, sleep: c.sleep,
		start: func(f func()) { f() },
		hooks: h,
	}
	return p, h, c, &out
}

func typeString(p *pane, s string) {
	for _, b := range []byte(s) {
		p.key(b)
	}
}

// lastScreen is the last full redraw, without escape codes.
func lastScreen(out *bytes.Buffer) string {
	s := out.String()
	if i := strings.LastIndex(s, "\x1b[3J"); i >= 0 {
		s = s[i+len("\x1b[3J"):]
	}
	return ansi.Strip(s)
}

func replyWindow() Window {
	return Window{Mode: "bare", State: "idle", Reply: []ReplyEntry{
		{Entry: Entry{Say: "Splitting this in two."}},
		{Entry: Entry{Tool: "Bash", Input: map[string]any{"command": `agentctl dispatch --agent go-engineer --prompt "Add a limiter"`}}},
		{Entry: Entry{Tool: "Read", Input: map[string]any{"file_path": "router.go"}, Result: "Read 40 lines"}, Delay: "2s"},
		{Entry: Entry{Say: "Both are running."}},
	}}
}

func TestPaneTyping(t *testing.T) {
	p, h, _, out := testPane(t, replyWindow())
	typeString(p, "Add rate limiting é")
	if got := string(p.input); got != "Add rate limiting é" {
		t.Fatalf("input = %q", got)
	}
	if !strings.Contains(lastScreen(out), "❯ Add rate limiting é") {
		t.Errorf("input line not echoed:\n%s", lastScreen(out))
	}
	typeString(p, "\x7f\x7f")
	typeString(p, "\x1b[A\x1b[D") // arrows are swallowed
	typeString(p, "\x08x")
	if got := string(p.input); got != "Add rate limitinx" {
		t.Errorf("after backspaces and arrows input = %q", got)
	}
	p.input = []rune("   ")
	p.key('\r')
	if p.turn != nil || len(h.calls) != 0 {
		t.Error("Enter on a blank prompt started a turn")
	}
}

func TestPaneWithoutReplyIgnoresEnter(t *testing.T) {
	p, h, _, _ := testPane(t, Window{Mode: "bare", State: "idle"})
	typeString(p, "hi\r")
	if p.turn != nil || len(h.calls) != 0 {
		t.Error("a pane without a reply played a turn")
	}
}

func TestPanePlaysReply(t *testing.T) {
	p, h, c, out := testPane(t, replyWindow())
	typeString(p, "Add rate limiting\r")

	wantCalls := []string{
		"hook-user-prompt",
		"hook-pre-tool", "hook-validate-bash",
		`run: agentctl dispatch --agent go-engineer --prompt "Add a limiter"`,
		"hook-post-tool",
		"hook-pre-tool", "hook-post-tool",
		"hook-check-done",
	}
	if !reflect.DeepEqual(h.calls, wantCalls) {
		t.Fatalf("calls =\n%q\nwant\n%q", h.calls, wantCalls)
	}
	events := []string{"UserPromptSubmit", "PreToolUse", "PreToolUse", "", "PostToolUse", "PreToolUse", "PostToolUse", "Stop"}
	for i, pay := range h.payloads {
		if events[i] == "" {
			continue
		}
		if pay["hook_event_name"] != events[i] || pay["session_id"] != "sid" || pay["cwd"] != "/w" {
			t.Errorf("call %d (%s) payload = %v", i, h.calls[i], pay)
		}
	}
	if h.payloads[0]["prompt"] != "Add rate limiting" {
		t.Errorf("prompt = %v", h.payloads[0]["prompt"])
	}
	if h.payloads[1]["tool_name"] != "Bash" || h.payloads[5]["tool_name"] != "Read" {
		t.Errorf("tool names = %v, %v", h.payloads[1]["tool_name"], h.payloads[5]["tool_name"])
	}
	resp, _ := h.payloads[4]["tool_response"].(map[string]any)
	if resp["stdout"] != "Dispatched prompt → go-engineer (window @12)" {
		t.Errorf("post-tool response = %v", resp)
	}

	wantSleeps := []time.Duration{firstDelay, toolDelay, runningPause, 2 * time.Second, runningPause, sayDelay, finishDelay}
	if !reflect.DeepEqual(c.sleeps, wantSleeps) {
		t.Errorf("sleeps = %v, want %v", c.sleeps, wantSleeps)
	}

	screen := lastScreen(out)
	order := []string{"❯ Add rate limiting", "⏺ Splitting this in two.", "⏺ Bash(agentctl dispatch", "⎿  Dispatched prompt → go-engineer (window @12)",
		"⏺ Read(router.go)", "Read 40 lines", "⏺ Both are running.", "✻ Brewed for 7s"}
	at := 0
	for _, w := range order {
		i := strings.Index(screen[at:], w)
		if i < 0 {
			t.Fatalf("missing %q after offset %d in:\n%s", w, at, screen)
		}
		at += i + len(w)
	}
	if strings.Contains(screen, "Thinking") || strings.Contains(screen, "Running…") {
		t.Errorf("finished turn still shows the spinner or a running call:\n%s", screen)
	}
	if len(p.input) != 0 || strings.Count(screen, "Add rate limiting") != 1 {
		t.Errorf("input box was not cleared (input %q):\n%s", string(p.input), screen)
	}

	// Mid-turn screens show the spinner counting up and the call running.
	all := ansi.Strip(out.String())
	for _, w := range []string{"✻ Thinking… (0s · esc to interrupt)", "✻ Thinking… (3s · esc to interrupt)", "Running…"} {
		if !strings.Contains(all, w) {
			t.Errorf("no frame shows %q", w)
		}
	}

	// One turn only: a second prompt is echoed but not played.
	n := len(h.calls)
	typeString(p, "again\r")
	if len(h.calls) != n {
		t.Error("a second Enter played the reply again")
	}
}

func TestPaneReplyDeniedAndFailed(t *testing.T) {
	t.Run("denied", func(t *testing.T) {
		p, h, _, out := testPane(t, replyWindow())
		h.deny = "dispatch denied in agentctl\nmore"
		typeString(p, "go\r")
		for _, c := range h.calls {
			if strings.HasPrefix(c, "run: ") {
				t.Errorf("a denied command ran: %s", c)
			}
		}
		// The denied Bash gets no PostToolUse; the Read still does.
		want := []string{"hook-user-prompt", "hook-pre-tool", "hook-validate-bash", "hook-pre-tool", "hook-post-tool", "hook-check-done"}
		if !reflect.DeepEqual(h.calls, want) {
			t.Errorf("calls = %q", h.calls)
		}
		if !strings.Contains(lastScreen(out), "Denied: dispatch denied in agentctl") {
			t.Errorf("deny reason not shown:\n%s", lastScreen(out))
		}
	})
	t.Run("failed", func(t *testing.T) {
		p, h, _, out := testPane(t, replyWindow())
		h.runOut, h.runErr = "Error: unknown agent\n", errors.New("exit status 1")
		typeString(p, "go\r")
		if !slices.Contains(h.calls, "hook-post-tool-failure") {
			t.Errorf("calls = %q, want a hook-post-tool-failure", h.calls)
		}
		if !strings.Contains(lastScreen(out), "Error: unknown agent") {
			t.Errorf("failure output not shown:\n%s", lastScreen(out))
		}
	})
}

func TestStartWorking(t *testing.T) {
	p, h, c, out := testPane(t, Window{Mode: "auto", State: "idle"})
	p.spec.Agentctl = "agentctl"
	p.startWorking("Add a limiter")
	c.sleep(65 * time.Second)
	p.draw()
	if !reflect.DeepEqual(h.calls, []string{"hook-user-prompt"}) || h.payloads[0]["prompt"] != "Add a limiter" {
		t.Errorf("calls = %q %v", h.calls, h.payloads)
	}
	if !p.playing() {
		t.Error("a stub turn finished")
	}
	s := lastScreen(out)
	for _, w := range []string{"❯ Add a limiter", "✻ Thinking… (1m 5s · esc to interrupt)", "⏵⏵ auto mode on"} {
		if !strings.Contains(s, w) {
			t.Errorf("missing %q in:\n%s", w, s)
		}
	}
}

func TestParseStubArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want stubArgs
	}{
		{"hub launch", []string{"--agent", "go", "-n", "go-acme-1", "--session-id", "S", "--settings", "/s.json"}, stubArgs{mode: "bare", sessionID: "S"}},
		{"worker dispatch", []string{"--agent", "go", "--session-id", "S", "--permission-mode", "auto", "--append-system-prompt-file", "/p", "Do the thing"}, stubArgs{mode: "auto", sessionID: "S", prompt: "Do the thing"}},
		{"default mode", []string{"--permission-mode", "default"}, stubArgs{mode: "bare"}},
		{"bool flag before prompt", []string{"--fork-session", "hello"}, stubArgs{mode: "bare", prompt: "hello"}},
		{"double dash", []string{"--model", "opus", "--", "-starts with dash"}, stubArgs{mode: "bare", prompt: "-starts with dash"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseStubArgs(tt.args); got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestStubPrompt(t *testing.T) {
	if got := stubPrompt("Add a limiter\nto orders.\n\nExecute autonomously."); got != "Add a limiter to orders." {
		t.Errorf("got %q", got)
	}
	if got := []rune(stubPrompt(strings.Repeat("x", 500))); len(got) != 240 || got[239] != '…' {
		t.Errorf("long prompt is %d runes", len(got))
	}
}

func TestAbbreviateHome(t *testing.T) {
	real := t.TempDir()
	home := filepath.Join(real, "home")
	if err := os.MkdirAll(filepath.Join(home, "code", "acme-api"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	viaLink := filepath.Join(link, "home")
	tests := []struct{ name, dir, home, want string }{
		{"plain", filepath.Join(home, "code", "acme-api"), home, "~/code/acme-api"},
		{"cwd behind symlink", filepath.Join(viaLink, "code", "acme-api"), home, "~/code/acme-api"},
		{"home behind symlink", filepath.Join(home, "code", "acme-api"), viaLink, "~/code/acme-api"},
		{"home itself", home, viaLink, "~"},
		{"sibling with the same prefix", home + "2", home, home + "2"},
		{"outside", "/etc", home, "/etc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := abbreviateHome(tt.dir, tt.home); got != tt.want {
				t.Errorf("abbreviateHome(%q, %q) = %q, want %q", tt.dir, tt.home, got, tt.want)
			}
		})
	}
}

func TestReplyHelpers(t *testing.T) {
	if !isDispatch(`agentctl dispatch --prompt "x"`) || isDispatch("agentctl status") || isDispatch("echo agentctl dispatch") {
		t.Error("isDispatch")
	}
	got := splitWords(`agentctl dispatch -a code-reviewer --prompt "Review PR 138 --agent x" 'a b'`)
	want := []string{"agentctl", "dispatch", "-a", "code-reviewer", "--prompt", "Review PR 138 --agent x", "a b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("splitWords = %q", got)
	}
	r := []ReplyEntry{
		{Entry: Entry{Tool: "Bash", Input: map[string]any{"command": `agentctl dispatch --agent go-engineer --prompt "x"`}}},
		{Entry: Entry{Tool: "Bash", Input: map[string]any{"command": `agentctl dispatch -a code-reviewer --prompt "--agent nope"`}}},
		{Entry: Entry{Tool: "Bash", Input: map[string]any{"command": "go test --agent ./..."}}},
	}
	if got := replyAgents(r); !reflect.DeepEqual(got, []string{"go-engineer", "code-reviewer"}) {
		t.Errorf("replyAgents = %q", got)
	}
	if got := resultLine("\n  first line\nDispatched prompt → x (window @3)\n"); got != "Dispatched prompt → x (window @3)" {
		t.Errorf("resultLine = %q", got)
	}
	if got := resultLine("\nError: boom\nmore\n"); got != "Error: boom" {
		t.Errorf("resultLine = %q", got)
	}
	if ok, reason := hookDecision([]byte(`{"hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"no\nmore"}}`), "", nil); ok || reason != "no" {
		t.Errorf("deny decision = %v %q", ok, reason)
	}
	if ok, _ := hookDecision(nil, "", nil); !ok {
		t.Error("no output should allow")
	}
}
