package demo

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"

	"github.com/blsmth/agentctl/internal/statusline"
)

// StatuslinePayload is the statusLine JSON Claude Code would push for the
// window, built from the real statusline.Payload struct.
func StatuslinePayload(spec PaneSpec, now time.Time) statusline.Payload {
	w := spec.Window
	f := func(v float64) *float64 { return &v }
	str := func(v string) *string { return &v }
	ctx := float64(w.CtxPct)
	size := 200000.0
	used := size * ctx / 100
	display := modelDisplay(w.Model)
	reset5 := float64(now.Add(2*time.Hour + 14*time.Minute).Unix())
	reset7 := float64(now.Add(3*24*time.Hour + 6*time.Hour).Unix())
	return statusline.Payload{
		SessionID:      str(spec.SessionID),
		TranscriptPath: str(spec.LogPath),
		Cwd:            str(spec.WorkDir),
		Version:        str("2.4.1"),
		Model:          &statusline.Model{ID: str(w.Model), DisplayName: str(display)},
		Workspace:      &statusline.Workspace{CurrentDir: str(spec.WorkDir), ProjectDir: str(spec.WorkDir)},
		Cost: &statusline.Cost{
			TotalCostUSD:      f(w.CostUSD),
			TotalDurationMS:   f(float64((23*time.Minute + 41*time.Second).Milliseconds())),
			TotalLinesAdded:   f(148),
			TotalLinesRemoved: f(37),
		},
		ContextWindow: &statusline.ContextWindow{
			ContextWindowSize:    f(size),
			UsedPercentagePushed: f(ctx),
			RemainingPercentage:  f(100 - ctx),
			CurrentUsage: &statusline.CurrentUsage{
				InputTokens: f(6), OutputTokens: f(412),
				CacheCreationInputTokens: f(1800), CacheReadInputTokens: f(max(used-1806, 0)),
			},
		},
		RateLimits: &statusline.RateLimits{
			FiveHour: &statusline.RateLimitWindow{UsedPercentage: f(34), ResetsAt: f(reset5)},
			SevenDay: &statusline.RateLimitWindow{UsedPercentage: f(61), ResetsAt: f(reset7)},
		},
	}
}

func modelDisplay(id string) string {
	switch {
	case strings.Contains(id, "opus"):
		return "Opus 5.5"
	case strings.Contains(id, "sonnet"):
		return "Sonnet 5.5"
	case strings.Contains(id, "haiku"):
		return "Haiku 5.5"
	}
	return id
}

// runStatusline pipes the payload through the real `agentctl statusline`,
// which also records ctx/model/cost in the window's windowstate as it does
// for a real session.
func runStatusline(spec PaneSpec) string {
	data, err := json.Marshal(StatuslinePayload(spec, time.Now()))
	if err != nil {
		return ""
	}
	cmd := exec.Command(spec.Agentctl, "statusline")
	cmd.Dir = spec.WorkDir
	cmd.Stdin = bytes.NewReader(data)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// pane is a running fake pane.
type pane struct {
	spec   PaneSpec
	out    io.Writer
	mu     sync.Mutex
	status string
	extra  []string
	busy   bool
}

// RunPane draws the fake Claude Code screen and idles, redrawing on resize.
// With an approval command, Enter runs the real PreToolUse hook for it.
func RunPane(specPath string) error {
	spec, err := ReadPaneSpec(specPath)
	if err != nil {
		return err
	}
	p := &pane{spec: spec, out: os.Stdout}
	restore := quietTerminal(int(os.Stdin.Fd()))
	defer restore()
	fmt.Fprint(p.out, "\x1b[?25l") // Claude Code draws its own cursor
	if spec.Window.State != "blocked" {
		p.status = runStatusline(spec)
	}
	p.draw()

	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	keys := make(chan byte)
	go readKeys(os.Stdin, keys)
	for {
		select {
		case <-winch:
			p.draw()
		case k, ok := <-keys:
			if !ok {
				select {} // stdin closed: keep the screen up until killed
			}
			if (k == '\r' || k == '\n') && spec.Window.Approval != "" {
				p.startApproval()
			}
		}
	}
}

func readKeys(r io.Reader, keys chan<- byte) {
	br := bufio.NewReader(r)
	for {
		b, err := br.ReadByte()
		if err != nil {
			close(keys)
			return
		}
		keys <- b
	}
}

// quietTerminal turns off echo and line buffering so keypresses neither
// print nor wait for Enter. Output processing stays on.
func quietTerminal(fd int) func() {
	if !term.IsTerminal(fd) {
		return func() {}
	}
	old, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return func() {}
	}
	t := *old
	t.Lflag &^= unix.ECHO | unix.ICANON
	t.Cc[unix.VMIN], t.Cc[unix.VTIME] = 1, 0
	if err := unix.IoctlSetTermios(fd, ioctlSetTermios, &t); err != nil {
		return func() {}
	}
	return func() { _ = unix.IoctlSetTermios(fd, ioctlSetTermios, old) }
}

func (p *pane) draw() {
	p.mu.Lock()
	defer p.mu.Unlock()
	width := 100
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		width = w
	}
	fmt.Fprint(p.out, "\x1b[H\x1b[2J\x1b[3J"+Render(p.spec, width, p.status, p.extra))
}

// startApproval runs the dispatch through hook-validate-bash, as Claude Code
// runs its PreToolUse hook: agentctl marks the window blocked and opens the
// approval popup; the pane shows the outcome when the popup is answered.
func (p *pane) startApproval() {
	p.mu.Lock()
	if p.busy {
		p.mu.Unlock()
		return
	}
	p.busy = true
	cmdline := p.spec.Window.Approval
	p.extra = []string{
		cDim + "⏺" + cReset + " " + cBold + "Bash" + cReset + "(" + cmdline + ")",
		"  " + cDim + "⎿  Waiting for approval in the agentctl popup…" + cReset,
	}
	p.mu.Unlock()
	p.draw()
	go func() {
		ok, reason := p.runHook(cmdline)
		p.mu.Lock()
		dot, msg := cGreen+"⏺", "Dispatched (approved in agentctl)"
		if !ok {
			dot, msg = cRed+"⏺", "Denied: "+reason
		}
		p.extra = []string{
			dot + cReset + " " + cBold + "Bash" + cReset + "(" + cmdline + ")",
			"  " + cDim + "⎿  " + msg + cReset,
		}
		p.busy = false
		p.mu.Unlock()
		p.draw()
	}()
}

// runHook feeds a PreToolUse payload to `agentctl hook-validate-bash` and
// reports whether it allowed the command (no decision output also allows).
func (p *pane) runHook(cmdline string) (bool, string) {
	payload, err := json.Marshal(map[string]any{
		"session_id":      p.spec.SessionID,
		"transcript_path": p.spec.LogPath,
		"cwd":             p.spec.WorkDir,
		"hook_event_name": "PreToolUse",
		"permission_mode": "default",
		"tool_name":       "Bash",
		"tool_input":      map[string]any{"command": cmdline, "description": "Dispatch a worker"},
	})
	if err != nil {
		return false, err.Error()
	}
	cmd := exec.Command(p.spec.Agentctl, "hook-validate-bash")
	cmd.Dir = p.spec.WorkDir
	cmd.Stdin = bytes.NewReader(payload)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, runErr := cmd.Output()
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
		return false, firstLine(strings.TrimSpace(stderr.String()))
	}
	return true, ""
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	if s == "" {
		return "denied"
	}
	return s
}
