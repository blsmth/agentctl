package demo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// ReplyEntry is one step of a window's scripted turn: a say or a tool call
// (as in a transcript) drawn after Delay.
type ReplyEntry struct {
	Entry `yaml:",inline"`
	// Delay is how long Claude "thinks" before the entry appears (Go
	// duration); the default depends on the entry (see delay).
	Delay string `yaml:"delay"`
}

// Default pauses of a scripted turn.
const (
	firstDelay   = 1500 * time.Millisecond // prompt submitted → first entry
	sayDelay     = 1000 * time.Millisecond
	toolDelay    = 800 * time.Millisecond
	runningPause = 600 * time.Millisecond // a tool shows "Running…" at least this long
	finishDelay  = 900 * time.Millisecond // last entry → turn footer
	maxDelay     = 10 * time.Second
)

// delay is the pause before entry i of a reply.
func (e ReplyEntry) delay(i int) time.Duration {
	if d, err := time.ParseDuration(e.Delay); err == nil && e.Delay != "" {
		return d
	}
	switch {
	case i == 0:
		return firstDelay
	case e.Say != "":
		return sayDelay
	}
	return toolDelay
}

func validateReply(r []ReplyEntry) error {
	for i, e := range r {
		n := 0
		for _, b := range []bool{e.Say != "", e.Tool != ""} {
			if b {
				n++
			}
		}
		switch {
		case e.User != "":
			return fmt.Errorf("entries[%d]: user is what the scene types; a reply has only say and tool", i)
		case n != 1:
			return fmt.Errorf("entries[%d]: exactly one of say or tool is required", i)
		case e.Tool == "" && (e.Input != nil || e.Result != "" || e.Diff != "" || e.Error):
			return fmt.Errorf("entries[%d]: input/result/diff/error need tool", i)
		case e.Running:
			return fmt.Errorf("entries[%d]: running is not valid in a reply (every call finishes)", i)
		}
		if e.Delay != "" {
			if d, err := time.ParseDuration(e.Delay); err != nil || d < 0 || d > maxDelay {
				return fmt.Errorf("entries[%d].delay: %q is not a duration up to %s", i, e.Delay, maxDelay)
			}
		}
	}
	return nil
}

// isDispatch reports whether a Bash command is an `agentctl dispatch`, the
// one command a scripted turn runs for real.
func isDispatch(cmdline string) bool {
	w := splitWords(cmdline)
	return len(w) >= 2 && w[0] == "agentctl" && w[1] == "dispatch"
}

// replyAgents are the --agent/-a values of the reply's dispatch commands, so
// the world has agent files for them.
func replyAgents(r []ReplyEntry) []string {
	var out []string
	for _, e := range r {
		cmd := toolDetail(e.Entry)
		if e.Tool != "Bash" || !isDispatch(cmd) {
			continue
		}
		w := splitWords(cmd)
		for i := 2; i+1 < len(w); i++ {
			if w[i] == "--agent" || w[i] == "-a" {
				out = append(out, w[i+1])
			}
		}
	}
	return out
}

// splitWords splits a command line into words with single and double
// quotes; enough for the commands a scene writes.
func splitWords(s string) []string {
	var out []string
	var cur strings.Builder
	in, quote := false, rune(0)
	for _, r := range s {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote != 0:
			cur.WriteRune(r)
		case r == '\'' || r == '"':
			quote, in = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteRune(r)
			in = true
		}
	}
	if in {
		out = append(out, cur.String())
	}
	return out
}

// turnHooks is how a scripted turn reaches agentctl: Claude Code's hooks
// and the Bash tool. execHooks is the real one; tests record calls.
type turnHooks interface {
	// Fire runs a hook subcommand with a payload, ignoring its outcome.
	Fire(cmd string, payload map[string]any)
	// Validate runs hook-validate-bash and reports whether it allowed the
	// command, with the deny reason.
	Validate(payload map[string]any) (bool, string)
	// Run runs a Bash command and returns its combined output.
	Run(cmdline string) (string, error)
}

// execHooks runs the real agentctl from the window's work dir.
type execHooks struct{ agentctl, dir string }

func (h execHooks) command(name string, args []string, payload map[string]any) (*exec.Cmd, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(name, args...)
	cmd.Dir = h.dir
	cmd.Stdin = bytes.NewReader(data)
	return cmd, nil
}

func (h execHooks) Fire(sub string, payload map[string]any) {
	if cmd, err := h.command(h.agentctl, []string{sub}, payload); err == nil {
		_ = cmd.Run()
	}
}

func (h execHooks) Validate(payload map[string]any) (bool, string) {
	cmd, err := h.command(h.agentctl, []string{"hook-validate-bash"}, payload)
	if err != nil {
		return false, err.Error()
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, runErr := cmd.Output()
	return hookDecision(out, stderr.String(), runErr)
}

// hookDecision reads hook-validate-bash's outcome: a deny decision, a
// failed run, or (no decision output too) an allow.
func hookDecision(out []byte, stderr string, runErr error) (bool, string) {
	var dec struct {
		Out struct {
			Decision string `json:"permissionDecision"`
			Reason   string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	_ = json.Unmarshal(bytes.TrimSpace(out), &dec)
	switch {
	case dec.Out.Decision == "deny":
		return false, firstLine(dec.Out.Reason)
	case runErr != nil:
		return false, firstLine(strings.TrimSpace(stderr))
	}
	return true, ""
}

func (h execHooks) Run(cmdline string) (string, error) {
	cmd := exec.Command("sh", "-c", cmdline)
	cmd.Dir = h.dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// turnState is a scripted turn in progress or finished.
type turnState struct {
	start   time.Time
	entries []Entry // the prompt, then what the reply has drawn so far
	done    bool
	took    time.Duration
}

// hookPayload is the common part of every hook payload of the pane.
func (p *pane) hookPayload(event string) map[string]any {
	mode := "default"
	if p.spec.Window.Mode != "bare" {
		mode = "auto"
	}
	return map[string]any{
		"session_id":      p.spec.SessionID,
		"transcript_path": p.spec.LogPath,
		"cwd":             p.spec.WorkDir,
		"hook_event_name": event,
		"permission_mode": mode,
	}
}

// key handles one byte of input: printable text echoes into the input box,
// Backspace deletes, Enter submits (when the window has a reply), and
// escape sequences (arrows) are swallowed.
func (p *pane) key(b byte) {
	p.mu.Lock()
	switch {
	case p.esc == 1:
		p.esc = 0
		if b == '[' || b == 'O' {
			p.esc = 2
			p.mu.Unlock()
			return
		}
	case p.esc == 2:
		if b >= 0x40 && b <= 0x7e {
			p.esc = 0
		}
		p.mu.Unlock()
		return
	}
	if p.turn != nil && !p.turn.done {
		p.mu.Unlock()
		return // Claude Code is busy; keep the scene simple
	}
	switch {
	case b == 0x1b:
		p.esc = 1
		p.mu.Unlock()
		return
	case b == '\r' || b == '\n':
		prompt := strings.TrimSpace(string(p.input))
		if prompt == "" || p.turn != nil || len(p.spec.Window.Reply) == 0 {
			p.mu.Unlock()
			return
		}
		p.input, p.partial = nil, nil
		p.turn = &turnState{start: p.now(), entries: []Entry{{User: prompt}}}
		p.mu.Unlock()
		p.start(func() { p.play(prompt) })
		return
	case b == 0x7f || b == 0x08:
		if len(p.partial) > 0 {
			p.partial = nil
		} else if n := len(p.input); n > 0 {
			p.input = p.input[:n-1]
		}
	case b < 0x20:
		p.mu.Unlock()
		return
	default:
		p.partial = append(p.partial, b)
		if utf8.FullRune(p.partial) {
			r, _ := utf8.DecodeRune(p.partial)
			p.partial = nil
			if r != utf8.RuneError {
				p.input = append(p.input, r)
			}
		}
	}
	p.mu.Unlock()
	p.draw()
}

// play runs the turn the way Claude Code does: UserPromptSubmit, then each
// entry after its pause with PreToolUse/PostToolUse around tool calls, then
// Stop. Bash `agentctl dispatch` calls go through hook-validate-bash and
// really run; the result line is their first line of output.
func (p *pane) play(prompt string) {
	pay := p.hookPayload("UserPromptSubmit")
	pay["prompt"] = prompt
	p.draw()
	p.hooks.Fire("hook-user-prompt", pay)
	for i, e := range p.spec.Window.Reply {
		p.sleep(e.delay(i))
		if e.Tool == "" {
			p.appendEntry(e.Entry)
			continue
		}
		p.playTool(i, e.Entry)
	}
	p.sleep(finishDelay)
	p.mu.Lock()
	p.turn.done = true
	p.turn.took = p.now().Sub(p.turn.start)
	p.mu.Unlock()
	p.draw()
	stop := p.hookPayload("Stop")
	stop["stop_hook_active"] = false
	p.hooks.Fire("hook-check-done", stop)
}

func (p *pane) appendEntry(e Entry) int {
	p.mu.Lock()
	p.turn.entries = append(p.turn.entries, e)
	n := len(p.turn.entries) - 1
	p.mu.Unlock()
	p.draw()
	return n
}

func (p *pane) playTool(i int, e Entry) {
	shown := e
	shown.Running, shown.Result, shown.Error = true, "", false
	idx := p.appendEntry(shown)

	id := fmt.Sprintf("toolu_demo_reply%02d", i)
	pre := p.hookPayload("PreToolUse")
	pre["tool_name"], pre["tool_input"], pre["tool_use_id"] = e.Tool, nonNil(e.Input), id
	p.hooks.Fire("hook-pre-tool", pre)
	result, failed, denied := e.Result, e.Error, false
	if e.Tool == "Bash" {
		if ok, reason := p.hooks.Validate(pre); !ok {
			result, failed, denied = "Denied: "+reason, true, true
		} else if cmd := toolDetail(e); isDispatch(cmd) {
			out, err := p.hooks.Run(cmd)
			result, failed = resultLine(out), err != nil
		}
	}
	p.sleep(runningPause)
	p.mu.Lock()
	shown.Running, shown.Result, shown.Error = false, result, failed
	p.turn.entries[idx] = shown
	p.mu.Unlock()
	p.draw()
	if denied {
		return // a PreToolUse deny means the tool never ran
	}
	post := p.hookPayload("PostToolUse")
	post["tool_name"], post["tool_input"], post["tool_use_id"] = e.Tool, nonNil(e.Input), id
	post["tool_response"] = map[string]any{"stdout": result, "stderr": "", "interrupted": false}
	sub := "hook-post-tool"
	if failed {
		post["hook_event_name"], post["error"] = "PostToolUseFailure", result
		sub = "hook-post-tool-failure"
	}
	p.hooks.Fire(sub, post)
}

// resultLine is the line a dispatch's result shows: its "Dispatched ..."
// line, else the first non-empty line, without escape codes (the window ID
// is an OSC 8 link).
func resultLine(out string) string {
	var first string
	for _, l := range strings.Split(ansi.Strip(out), "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if strings.HasPrefix(l, "Dispatched") {
			return l
		}
		if first == "" {
			first = l
		}
	}
	return first
}

// spinnerText is the working line of a turn that started at start.
func spinnerText(elapsed time.Duration) string {
	return "Thinking… (" + clockText(elapsed) + " · esc to interrupt)"
}

// clockText is Claude Code's elapsed-time format: 9s, 1m 3s.
func clockText(d time.Duration) string {
	s := int(d.Round(time.Second) / time.Second)
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	return fmt.Sprintf("%dm %ds", s/60, s%60)
}
