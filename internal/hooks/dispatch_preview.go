package hooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	ctxstore "github.com/blsmth/agentctl/internal/context"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/shellquote"
	"github.com/blsmth/agentctl/internal/tui"
)

// PreviewKind distinguishes what the dispatch-preview popup is showing.
// PreviewKindDispatch (the zero value, for backward compatibility with
// serialized args from before this field existed) is the full "agentctl
// dispatch" preview with target agent / issue / prompt metadata.
// PreviewKindBash is a plain Bash command preview -- gated behind the
// bash_preview config setting rather than always shown -- which renders just
// the command itself, since the dispatch-specific metadata rows don't apply.
type PreviewKind string

const (
	PreviewKindDispatch PreviewKind = "dispatch"
	PreviewKindBash     PreviewKind = "bash"
)

// DispatchPreviewArgs is the JSON structure written to the temp file passed to
// the agentctl dispatch-preview popup.
type DispatchPreviewArgs struct {
	AgentName   string `json:"agent_name"`
	TargetAgent string `json:"target_agent"`
	Issue       int    `json:"issue"`
	Prompt      string `json:"prompt"`
	PromptKey   string `json:"prompt_key"`
	Command     string `json:"command"`
	// Kind selects which popup layout to render -- see PreviewKind. Empty
	// (the zero value) is treated as PreviewKindDispatch for backward
	// compatibility with args files written before this field existed.
	Kind PreviewKind `json:"kind,omitempty"`

	// ContentWidth and VPHeight are the popup-sizing INPUTS resolved from the
	// attached tmux client's dimensions by dispatchPreviewFit(), at the same
	// moment this hook fixes the tmux popup's own size. They're threaded
	// through so the "dispatch-preview" subprocess builds its model with the
	// EXACT SAME inputs the hook used to size the tmux popup -- see
	// tui.DispatchPreviewMaxPopupSize's doc comment for why the two can never
	// be allowed to drift apart. Zero values fall back to the model's own
	// defaults (88/22) -- see tui.WithContentWidth / tui.WithMaxViewportHeight.
	ContentWidth int `json:"content_width"`
	VPHeight     int `json:"vp_height"`
}

// isAgentctlDispatch reports whether the given bash command invokes
// "agentctl dispatch" in a way that must be gated. It handles absolute paths
// and bare binary names. Help lookups (--help / -h within the dispatch
// invocation itself) are read-only and return false. Every other invocation,
// even one with no recognizable flags, is gated (fail-safe).
func isAgentctlDispatch(command string) bool {
	for _, seg := range dispatchSegments(command) {
		if !segmentHasHelp(seg) {
			return true
		}
	}
	return false
}

// cmdToken is a shell word plus whether any part of it was quoted.
type cmdToken struct {
	text   string
	quoted bool
	// expands is true when the shell would not pass text through verbatim:
	// it contains $, a backtick or a backslash outside single quotes.
	expands bool
}

// dispatchSegments returns, for every "agentctl dispatch" occurrence in the
// command, the tokens belonging to that invocation: everything after the
// "dispatch" token up to the first unquoted shell control operator.
// Redirection tokens (e.g. 2>&1) are dropped. This is deliberately not a
// full shell parser.
func dispatchSegments(command string) [][]cmdToken {
	tokens := tokenizeQuoted(strings.TrimSpace(command))
	var segs [][]cmdToken
	for i := 0; i+1 < len(tokens); i++ {
		if strings.HasSuffix(tokens[i].text, "agentctl") && tokens[i+1].text == "dispatch" {
			segs = append(segs, takeSegment(tokens[i+2:]))
		}
	}
	return segs
}

func takeSegment(tokens []cmdToken) []cmdToken {
	var seg []cmdToken
	for _, t := range tokens {
		if t.quoted {
			seg = append(seg, t)
			continue
		}
		if isRedirection(t.text) {
			continue
		}
		switch t.text {
		case "|", "||", "&&", ";", "&", "|&", ";;":
			return seg
		}
		if last := t.text[len(t.text)-1]; last == ';' || last == '|' || last == '&' {
			// Attached terminator (e.g. "5;"): keep the word, end the segment.
			if w := strings.TrimRight(t.text, ";|&"); w != "" {
				seg = append(seg, cmdToken{text: w})
			}
			return seg
		}
		seg = append(seg, t)
	}
	return seg
}

// isRedirection reports whether an unquoted token is a redirection such as
// 2>&1, >file, >>out, <in or &>log.
func isRedirection(tok string) bool {
	tok = strings.TrimLeft(tok, "0123456789")
	return strings.HasPrefix(tok, ">") || strings.HasPrefix(tok, "<") || strings.HasPrefix(tok, "&>")
}

// segmentHasHelp reports whether a dispatch segment asks for help. Values of
// value-taking flags and quoted words are never treated as help flags.
func segmentHasHelp(seg []cmdToken) bool {
	for i := 0; i < len(seg); i++ {
		t := seg[i]
		if t.quoted {
			continue
		}
		switch t.text {
		case "--agent", "-a", "--issue", "-i", "--prompt", "-p", "--prompt-key", "-k":
			i++ // skip the value
		case "--help", "-h":
			return true
		}
	}
	return false
}

// dispatchPreviewGate shows the popup and reports whether the user approved.
// Returns (approved, decided, denyReason). A non-empty denyReason means the
// prompt could not be shown, so the command must be denied (fail closed) with
// that reason; approved and decided are false. Otherwise decided is false when no popup was shown (not
// in tmux, popup failed to launch, another invocation owns it) and the
// caller must stay silent -- emitting nothing is how ValidateBash's PreToolUse
// hook signals "allow" to Claude Code, same as the existing deny checks in
// validate.go signal "allow" by simply returning without writing anything.
func dispatchPreviewGate(agentName, command string) (approved, decided bool, denyReason string) {
	// Check if we're in a tmux session — can't show popup otherwise.
	// Pass through rather than allowing: an unreviewed dispatch is exactly
	// what this popup exists to prevent. See PermissionRequest's doc comment.
	if os.Getenv("TMUX") == "" {
		logging.Log("hook-validate-bash: not in tmux session, passing through dispatch")
		return false, false, ""
	}

	// Exactly one of the (typically two -- the agentctl hook map is
	// registered both in the project .claude/settings.local.json and in the
	// per-agent --settings file, and Claude Code merges both, so it fires
	// PreToolUse twice for a single tool call) simultaneous invocations for
	// this exact command may show a popup. The loser must produce no output
	// at all. Fixing the double-registration itself is tracked separately;
	// this lock is the mitigation for its effect on this gate.
	release, ok := acquireGateLock(command)
	if !ok {
		logging.Log("hook-validate-bash: dispatch gate already owned by another invocation, staying silent")
		return false, false, ""
	}
	defer release()

	// Parse flags from the dispatch command.
	targetAgent, issue, prompt, promptKey := parseDispatchFlags(command)

	// Resolve the popup's sizing inputs from the attached client BEFORE
	// building the tmux popup, so the same values can be threaded through to
	// the "dispatch-preview" subprocess below (see the ContentWidth/VPHeight
	// doc comment on DispatchPreviewArgs).
	contentWidth, vpHeight := dispatchPreviewFit()

	// Build the args struct.
	args := DispatchPreviewArgs{
		AgentName:    agentName,
		TargetAgent:  targetAgent,
		Issue:        issue,
		Prompt:       prompt,
		PromptKey:    promptKey,
		Command:      command,
		Kind:         PreviewKindDispatch,
		ContentWidth: contentWidth,
		VPHeight:     vpHeight,
	}

	// Resolve --prompt-key to its actual content so the human reviewing the
	// popup sees the real prompt, not the key name. This is the entire point
	// of supporting --prompt-key here: unlike a shell substitution such as
	// `--prompt "$(cat spec.md)"` (which this hook receives as the raw,
	// unexpanded command string and can never resolve), a context store key
	// CAN be resolved because this hook binary and the context store live in
	// the same process image.
	//
	// ctxstore.Init() is called here — lazily, only when a prompt key is
	// present — rather than in main.go's hook fast path. That fast path
	// (main.go, around the isHookCmd branch) deliberately skips ctxstore.Init
	// for every hook to keep per-hook latency minimal; this is the one hook
	// that actually needs the store, so it pays the init cost itself instead
	// of taxing every other hook invocation.
	if promptKey != "" {
		content, ok := resolvePromptKey(promptKey, command)
		if !ok {
			return false, false, promptKeyDenyReason(promptKey)
		}
		args.Prompt = content
	}

	// Write to a temp JSON file.
	tmpFile, err := os.CreateTemp("", "agentctl-dispatch-preview-*")
	if err != nil {
		logging.Log("hook-validate-bash: failed to create dispatch temp file: %v", err)
		return false, false, ""
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if err := json.NewEncoder(tmpFile).Encode(args); err != nil {
		tmpFile.Close()
		logging.Log("hook-validate-bash: failed to write dispatch temp file: %v", err)
		return false, false, ""
	}
	tmpFile.Close()

	// Build popup dimensions from the worst-case size the model itself
	// computes (tui.DispatchPreviewMaxPopupSize), fed by the contentWidth/
	// vpHeight resolved from the client above -- so this hook and the
	// model's chrome arithmetic can never drift apart — see the doc comment
	// on DispatchPreviewMaxPopupSize for why the hook can't just measure the
	// real content: tmux fixes the popup's size before the model is built.
	// clampPopupToClient is still applied as a final defensive shrink: the
	// readability floors in dispatchPreviewFit (dpMinContentWidth,
	// dpMinVPHeight) can still legitimately exceed a genuinely tiny client.
	sz := clampPopupToClient(tui.DispatchPreviewMaxPopupSize(
		tui.WithContentWidth(contentWidth),
		tui.WithMaxViewportHeight(vpHeight),
	))
	popupW := sz.Width
	popupH := sz.Height

	logging.Log("hook-validate-bash: launching dispatch popup agent=%s target=%s issue=%d",
		agentName, targetAgent, issue)

	if err := dispatchPopupRunner(popupW, popupH, tmpPath); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode := exitErr.ExitCode()
			logging.Log("hook-validate-bash: dispatch popup exit code=%d", exitCode)
			if exitCode == 1 {
				return false, true, ""
			}
		}
		logging.Log("hook-validate-bash: dispatch popup error: %v, passing through", err)
		return false, false, ""
	}

	return true, true, ""
}

// promptKeyDenyReason is the deny message shown to the model when the prompt
// behind --prompt-key cannot be displayed in the approval popup.
func promptKeyDenyReason(key string) string {
	return fmt.Sprintf("dispatch --prompt-key %s: key not found in the context store. "+
		"Run `agentctl context set %s ...` as a separate Bash call first, then dispatch, "+
		"so the approval popup can show the prompt.", key, key)
}

// resolvePromptKey returns the prompt text for key. An `agentctl context set
// <key> <value>` earlier in the same command line wins over the store, since
// it is what will be stored by the time dispatch runs -- PreToolUse fires
// before any part of the command executes, so the store alone would miss it.
// ok is false when the prompt cannot be shown faithfully.
func resolvePromptKey(key, command string) (content string, ok bool) {
	if v, found, literal := inlineContextSet(key, command); found {
		if !literal || strings.TrimSpace(v) == "" {
			logging.Log("hook-validate-bash: inline context set for %q is not literal or empty", key)
			return "", false
		}
		return v, true
	}
	ctxstore.Init()
	entry, found, err := ctxstore.Get(key)
	switch {
	case err != nil:
		logging.Log("hook-validate-bash: failed to resolve prompt key %q: %v", key, err)
	case !found:
		logging.Log("hook-validate-bash: prompt key %q not found", key)
	case strings.TrimSpace(entry.Content) == "":
		logging.Log("hook-validate-bash: prompt key %q is empty", key)
	default:
		return entry.Content, true
	}
	return "", false
}

// inlineContextSet scans command for `agentctl context set <key> <value>` as
// a command of its own (start of the line or right after a control operator)
// and returns the last such value. literal is false when the value is subject
// to shell expansion and so cannot be read as written.
func inlineContextSet(key, command string) (value string, found, literal bool) {
	tokens := tokenizeQuoted(strings.TrimSpace(command))
	for i := 0; i+4 < len(tokens); i++ {
		if tokens[i].quoted || !strings.HasSuffix(tokens[i].text, "agentctl") ||
			tokens[i+1].text != "context" || tokens[i+2].text != "set" || tokens[i+3].text != key {
			continue
		}
		if i > 0 {
			switch tokens[i-1].text {
			case "&&", ";", "||", "&", "|":
			default:
				continue
			}
		}
		v := tokens[i+4]
		value, found, literal = v.text, true, !v.expands
	}
	return value, found, literal
}

// dispatchPopupRunner launches the tmux popup running "agentctl
// dispatch-preview --args-file <argsFilePath>" sized popupW x popupH, and
// returns whatever error cmd.Run() produced (nil on approve, an
// *exec.ExitError with code 1 on deny). It is a package-level var so tests
// can fake the popup outcome without shelling out to a real tmux client --
// same seam pattern as clientDimensions in popup_clamp.go.
var dispatchPopupRunner = runDispatchPopupCmd

func runDispatchPopupCmd(popupW, popupH int, argsFilePath string) error {
	agentctlBin := agentctlBinaryPath()
	cmd := exec.Command("tmux", "display-popup",
		"-E",
		"-w", strconv.Itoa(popupW),
		"-h", strconv.Itoa(popupH),
		dispatchPreviewPopupCmd(agentctlBin, argsFilePath),
	)
	return cmd.Run()
}

// dispatchPreviewPopupCmd returns the shell command line tmux display-popup
// runs for the dispatch preview, with every part quoted individually.
func dispatchPreviewPopupCmd(agentctlBin, argsFilePath string) string {
	return shellquote.Join(agentctlBin, "dispatch-preview", "--args-file", argsFilePath)
}

// parseDispatchFlags extracts --agent/-a, --issue/-i, --prompt/-p, and
// --prompt-key/-k flags from the "agentctl dispatch" segment of a command string; anything after
// a shell control operator is ignored. An attached terminator such as
// "--issue 5;" still parses (the ";" is stripped).
//
// The --prompt-key cases are checked before the --prompt cases. This isn't
// load-bearing today — "--prompt-key=foo" has the prefix "--prompt" but NOT
// the prefix "--prompt=", so there's no actual ambiguity — but ordering them
// this way keeps the switch defensive against future flag additions that
// share a longer common prefix.
func parseDispatchFlags(command string) (targetAgent string, issue int, prompt string, promptKey string) {
	segs := dispatchSegments(command)
	var seg []cmdToken
	for _, c := range segs {
		if !segmentHasHelp(c) {
			seg = c
			break
		}
	}
	if seg == nil && len(segs) > 0 {
		seg = segs[0]
	}
	tokens := make([]string, len(seg))
	for i, t := range seg {
		tokens[i] = t.text
	}
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		switch {
		case tok == "--agent" || tok == "-a":
			if i+1 < len(tokens) {
				targetAgent = tokens[i+1]
				i++
			}
		case strings.HasPrefix(tok, "--agent="):
			targetAgent = strings.TrimPrefix(tok, "--agent=")
		case tok == "--issue" || tok == "-i":
			if i+1 < len(tokens) {
				fmt.Sscanf(tokens[i+1], "%d", &issue) //nolint:errcheck
				i++
			}
		case strings.HasPrefix(tok, "--issue="):
			fmt.Sscanf(strings.TrimPrefix(tok, "--issue="), "%d", &issue) //nolint:errcheck
		case tok == "--prompt-key" || tok == "-k":
			if i+1 < len(tokens) {
				promptKey = tokens[i+1]
				i++
			}
		case strings.HasPrefix(tok, "--prompt-key="):
			promptKey = strings.TrimPrefix(tok, "--prompt-key=")
		case tok == "--prompt" || tok == "-p":
			if i+1 < len(tokens) {
				prompt = tokens[i+1]
				i++
			}
		case strings.HasPrefix(tok, "--prompt="):
			prompt = strings.TrimPrefix(tok, "--prompt=")
		}
	}
	return
}

// tokenizeCommand splits a shell command string into tokens, handling
// single-quoted and double-quoted strings.
func tokenizeCommand(s string) []string {
	qt := tokenizeQuoted(s)
	tokens := make([]string, len(qt))
	for i, t := range qt {
		tokens[i] = t.text
	}
	return tokens
}

// tokenizeQuoted is tokenizeCommand but records whether each token had any
// quoted part, and keeps empty quoted tokens ("").
func tokenizeQuoted(s string) []cmdToken {
	var tokens []cmdToken
	var cur strings.Builder
	inQuote, hadQuote, expands := false, false, false
	var quoteChar byte

	flush := func() {
		if cur.Len() > 0 || hadQuote {
			tokens = append(tokens, cmdToken{text: cur.String(), quoted: hadQuote, expands: expands})
		}
		cur.Reset()
		hadQuote, expands = false, false
	}

	for i := 0; i < len(s); i++ {
		c := s[i]
		if inQuote {
			if c == quoteChar {
				inQuote = false
			} else {
				if quoteChar == '"' && (c == '$' || c == '`' || c == '\\') {
					expands = true
				}
				cur.WriteByte(c)
			}
			continue
		}
		switch c {
		case '\'', '"':
			inQuote = true
			hadQuote = true
			quoteChar = c
		case ' ', '\t', '\n':
			flush()
		default:
			if c == '$' || c == '`' || c == '\\' {
				expands = true
			}
			cur.WriteByte(c)
		}
	}
	flush()
	return tokens
}
