package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/blsmth/agentctl/internal/config"
	ctxstore "github.com/blsmth/agentctl/internal/context"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/prompttemplate"
	"github.com/blsmth/agentctl/internal/remoteapproval"
	"github.com/blsmth/agentctl/internal/shellquote"
	"github.com/blsmth/agentctl/internal/textutil"
	"github.com/blsmth/agentctl/internal/tui"
)

// PreviewKind distinguishes what the dispatch-preview popup is showing.
// PreviewKindDispatch (the zero value, for backward compatibility with
// serialized args from before this field existed) is the full "agentctl
// dispatch" preview with target agent / issue / prompt metadata.
// PreviewKindBash is a plain Bash command preview -- gated behind the
// bash_preview config setting rather than dispatch_approval -- which renders just
// the command itself, since the dispatch-specific metadata rows don't apply.
// PreviewKindCron is an `agentctl cron add/resume/run --force` preview (see
// cron_preview.go): job name and schedule rows, job details and prompt in
// the viewport.
type PreviewKind string

const (
	PreviewKindDispatch PreviewKind = "dispatch"
	PreviewKindBash     PreviewKind = "bash"
	PreviewKindCron     PreviewKind = "cron"
	// PreviewKindTemplate is an `agentctl template add/edit/rm` preview
	// (see template_preview.go): the template name and file in the rows,
	// its new content (or the content being removed) in the viewport.
	PreviewKindTemplate PreviewKind = "template"
)

// DispatchPreviewArgs is the JSON structure written to the temp file passed to
// the agentctl dispatch-preview popup.
type DispatchPreviewArgs struct {
	AgentName   string `json:"agent_name"`
	TargetAgent string `json:"target_agent"`
	Issue       int    `json:"issue"`
	// ReviewPR is the `--review-pr` number; 0 for every other dispatch.
	ReviewPR  int    `json:"review_pr,omitempty"`
	Prompt    string `json:"prompt"`
	PromptKey string `json:"prompt_key"`
	// Template is the `dispatch --template` name; Prompt is then the
	// template's resolved prompt (plus any --prompt instructions).
	Template string `json:"template,omitempty"`
	Command  string `json:"command"`
	// Account is the Claude account the dispatched agent resolves to, as
	// "name (label)"; empty when no accounts are configured.
	Account string `json:"account,omitempty"`
	// Project is the project the dispatched agent runs in (the phone's
	// details.project); empty when unknown.
	Project string `json:"project,omitempty"`
	// Details is the whole resolved plan (dir, model, backend, harness,
	// worktree, permission mode, ...); the popup shows it as rows under
	// "Dispatching". Zero for older args files and non-dispatch kinds.
	Details remoteapproval.Details `json:"details"`
	// Kind selects which popup layout to render -- see PreviewKind. Empty
	// (the zero value) is treated as PreviewKindDispatch for backward
	// compatibility with args files written before this field existed.
	Kind PreviewKind `json:"kind,omitempty"`

	// TemplateAction, TemplateName and TemplatePath fill a
	// PreviewKindTemplate popup's title and rows (see tui.TemplatePreview).
	TemplateAction string `json:"template_action,omitempty"`
	TemplateName   string `json:"template_name,omitempty"`
	TemplatePath   string `json:"template_path,omitempty"`

	// CronAction, CronJob and CronSchedule fill a PreviewKindCron popup's
	// title and rows (see tui.CronPreview).
	CronAction   string `json:"cron_action,omitempty"`
	CronJob      string `json:"cron_job,omitempty"`
	CronSchedule string `json:"cron_schedule,omitempty"`

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

// dispatchNeedsApproval reports whether a command that isAgentctlDispatch
// must show the dispatch approval popup under the given (normalized)
// dispatch_approval mode. "off" gates nothing; "non-review" gates the command
// unless EVERY non-help dispatch segment is an exempt reviewer dispatch (see
// segmentIsReview), so one ordinary dispatch in the line keeps the gate for
// the whole command; "all" and anything unrecognized gate everything (fail
// closed).
func dispatchNeedsApproval(command, mode string) bool {
	switch mode {
	case config.DispatchApprovalOff:
		return false
	case config.DispatchApprovalNonReview:
		for _, seg := range dispatchSegments(command) {
			if !segmentHasHelp(seg) && !segmentIsReview(seg) {
				return true
			}
		}
		return false
	default:
		return true
	}
}

// segmentIsReview reports whether a dispatch segment is exempt from the
// "non-review" gate: the code-reviewer agent reviewing PR N, and nothing
// else.
//
//   - Its last --review-pr flag ("--review-pr N" or "--review-pr=N"; the CLI
//     keeps the last one) carries a literal positive integer. A missing,
//     zero, non-numeric or shell-expanded value is not a review: 0 means
//     "unset" to dispatch, so "--review-pr=0" would otherwise exempt an
//     ordinary dispatch.
//   - Its agent resolves to config.DefaultReviewAgent: no --agent/-a at all
//     (the CLI default for --review-pr), or a last --agent/-a/--agent=
//     whose literal value is that name.
//   - Every flag is in reviewExemptValueFlags or reviewExemptBoolFlags. Any
//     other flag (--issue, --dir, which picks where the agent body is
//     resolved, --backend, --account, --harness, --no-worktree) or a stray
//     word means it is not exempt.
//
// Quoting does not matter for flags (the shell strips it). It fails closed
// on anything the shell could turn into extra or different arguments: an
// expanding word in flag position, an unquoted expanding value (word
// splitting), or an attached short flag ("-ax").
func segmentIsReview(seg []cmdToken) bool {
	found, ok := false, false
	agent, agentOK := config.DefaultReviewAgent, true
	for i := 0; i < len(seg); i++ {
		t := seg[i]
		if t.expands {
			return false
		}
		name, inline, hasInline := t.text, "", false
		if strings.HasPrefix(name, "--") {
			if eq := strings.IndexByte(name, '='); eq > 0 {
				name, inline, hasInline = name[:eq], name[eq+1:], true
			}
		}
		if reviewExemptBoolFlags[name] && !hasInline {
			continue
		}
		if !reviewExemptValueFlags[name] {
			return false
		}
		value := cmdToken{text: inline}
		if !hasInline {
			if i+1 >= len(seg) {
				return false
			}
			i++
			value = seg[i]
			if value.expands && !value.quoted {
				return false
			}
		}
		switch name {
		case "--review-pr":
			found, ok = true, positiveLiteral(value)
		case "--agent", "-a":
			agent, agentOK = value.text, !value.expands
		}
	}
	return found && ok && agentOK && agent == config.DefaultReviewAgent
}

// reviewExemptValueFlags are the value-taking dispatch flags an exempt
// review dispatch may carry. --prompt/--prompt-key are review focus;
// --model only picks the model (orchestrators pass --model sonnet for small
// PRs).
var reviewExemptValueFlags = map[string]bool{
	"--review-pr": true, "--agent": true, "-a": true, "--author": true,
	"--prompt": true, "-p": true, "--prompt-key": true, "-k": true,
	"--repo": true, "--timeout": true, "--model": true, "-m": true,
}

// reviewExemptBoolFlags are the boolean dispatch flags an exempt review
// dispatch may carry.
var reviewExemptBoolFlags = map[string]bool{"--wait": true}

// positiveLiteral reports whether t is a non-expanding positive integer.
func positiveLiteral(t cmdToken) bool {
	return !t.expands && positiveInt(t.text)
}

func positiveInt(s string) bool {
	n, err := strconv.Atoi(s)
	return err == nil && n > 0
}

// cmdToken is a shell word plus whether any part of it was quoted.
type cmdToken struct {
	text   string
	quoted bool
	// expands is true when the shell would not pass text through verbatim:
	// it contains $ or a backtick outside single quotes, a backslash inside
	// double quotes, or a trailing backslash. An unquoted backslash escape
	// (\x) is literal and marks the word quoted instead.
	expands bool
	// fromSource marks the `agentctl` token that stands for a go run of
	// the agentctl root package (rewriteGoRunAgentctl).
	fromSource bool
	// unquotedTail is, for a quoted token, the offset in text where the
	// bytes after its last quoted part start ("'cd /tmp';" -> the ";").
	unquotedTail int
}

// dispatchSegments returns, for every "agentctl dispatch" occurrence in the
// command (a go run of the agentctl root package counts, see
// rewriteGoRunAgentctl), the tokens belonging to that invocation: everything after the
// "dispatch" token up to the first unquoted shell control operator.
// Redirection tokens (e.g. 2>&1) are dropped. This is deliberately not a
// full shell parser. Data heredoc bodies are removed first (see
// invocationTokens), so a test file that mentions a dispatch is not one.
func dispatchSegments(command string) [][]cmdToken {
	tokens := invocationTokens(command)
	var segs [][]cmdToken
	for i := 0; i+1 < len(tokens); i++ {
		if strings.HasSuffix(tokens[i].text, "agentctl") && tokens[i+1].text == "dispatch" {
			segs = append(segs, takeSegment(tokens[i+2:]))
		}
	}
	return segs
}

// invocationTokens tokenizes command for finding agentctl invocations
// (invocationText): the bodies of data-only heredocs are stripped, since text
// written to a file through `cat > f <<EOF` or passed to `gh pr create
// --body-file -` is not a command, and quoted command lines (a "$(...)", a
// shell's -c argument, eval) are scanned on their own, since each would
// otherwise be one word. Every gate that looks for dispatch or cron
// invocations uses it, so the popup, the plan, the account lookup and the
// approval marker all see the same view. Anything that does not parse is
// tokenized raw. A go run of the agentctl root package becomes one
// `agentctl` token (rewriteGoRunAgentctl), so it is gated like the binary.
func invocationTokens(command string) []cmdToken {
	return rewriteGoRunAgentctl(rawInvocationTokens(command))
}

// rawInvocationTokens is invocationTokens without the go run rewrite.
func rawInvocationTokens(command string) []cmdToken {
	return tokenizeQuoted(strings.TrimSpace(invocationText(command)))
}

func takeSegment(tokens []cmdToken) []cmdToken {
	var seg []cmdToken
	skipTarget := false
	for _, t := range tokens {
		if skipTarget {
			// The target of a redirection written as a separate word
			// ("> out.txt") is not an argument either.
			skipTarget = false
			continue
		}
		if t.quoted {
			seg = append(seg, t)
			continue
		}
		if isRedirection(t.text) {
			skipTarget = redirectionNeedsTarget(t.text)
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

// redirectionNeedsTarget reports whether a redirection token is only the
// operator (">", "2>>", "&>"), so its target is the next word. "2>&1" and
// ">file" carry their target.
func redirectionNeedsTarget(tok string) bool {
	return strings.TrimLeft(strings.TrimLeft(tok, "0123456789"), "<>&|") == ""
}

// segmentHasHelp reports whether a dispatch segment asks for help. Values of
// value-taking flags and quoted words are never treated as help flags.
func segmentHasHelp(seg []cmdToken) bool {
	return segmentHasHelpFlags(seg, isDispatchValueFlag)
}

// isDispatchValueFlag reports whether tok is a dispatch flag whose value is
// the next word.
func isDispatchValueFlag(tok string) bool {
	switch tok {
	case "--agent", "-a", "--issue", "-i", "--prompt", "-p", "--prompt-key", "-k", "--review-pr", "--author",
		"--dir", "--model", "-m", "--backend", "--account", "--harness", "--repo", "--timeout", "--template":
		return true
	}
	return false
}

// segmentHasHelpFlags reports whether a segment asks for help, skipping the
// value word of every flag valueFlag reports. Quoted words are never help
// flags.
func segmentHasHelpFlags(seg []cmdToken, valueFlag func(string) bool) bool {
	for i := 0; i < len(seg); i++ {
		t := seg[i]
		if t.quoted {
			continue
		}
		switch {
		case valueFlag(t.text):
			i++ // skip the value
		case t.text == "--help" || t.text == "-h":
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
//
// cwd is the directory the Bash command runs in (the payload's cwd; "" falls
// back to the asking window's), which the where/how rows resolve against.
func dispatchPreviewGate(agentName, command, cwd string) (approved, decided bool, denyReason string) {
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
	reviewPR := parseDispatchReviewPR(command)
	if reviewPR > 0 && targetAgent == "" {
		targetAgent = config.DefaultReviewAgent
	}

	// Resolve the popup's sizing inputs from the attached client BEFORE
	// building the tmux popup, so the same values can be threaded through to
	// the "dispatch-preview" subprocess below (see the ContentWidth/VPHeight
	// doc comment on DispatchPreviewArgs).
	contentWidth, vpHeight := dispatchPreviewFit()

	target := resolvePopupTarget()

	// A --template is the prompt: resolve it the way the CLI will, or deny.
	bashCwd := askingCwd(cwd)
	tmpl, deny := resolveDispatchTemplate(command, bashCwd)
	if deny != "" {
		return false, false, deny
	}
	if tmpl != nil {
		prompt = prompttemplate.Compose(tmpl.Prompt, prompt)
		if targetAgent == "" {
			targetAgent = tmpl.Agent
		}
	}

	// Where and how the agent will run. Best effort: it never fails the gate.
	details := resolveDispatchDetails(config.Load(), command, bashCwd, tmpl)
	vpHeight = dispatchPreviewVPHeight(vpHeight, details)

	// Build the args struct.
	args := DispatchPreviewArgs{
		AgentName:    target.label(agentName),
		TargetAgent:  targetAgent,
		Issue:        issue,
		ReviewPR:     reviewPR,
		Prompt:       prompt,
		PromptKey:    promptKey,
		Command:      command,
		Kind:         PreviewKindDispatch,
		Template:     templateName(tmpl),
		ContentWidth: contentWidth,
		VPHeight:     vpHeight,
		Account:      details.Account,
		Project:      details.Project,
		Details:      details,
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
		tui.WithDetails(details),
	))
	popupW := sz.Width
	popupH := sz.Height

	logging.Log("hook-validate-bash: launching dispatch popup agent=%s target=%s issue=%d",
		agentName, targetAgent, issue)

	notice := dispatchPopupNotice(agentName, targetAgent, issue, args.Prompt)
	if reviewPR > 0 {
		notice.Detail = fmt.Sprintf("%s reviewing PR #%d", targetAgent, reviewPR)
		notice.Action = notice.Detail
	}
	notice.Remote = &remoteDetails{
		Kind:        remoteapproval.KindDispatch,
		AskingAgent: agentName,
		TargetAgent: targetAgent,
		Issue:       issue,
		Prompt:      args.Prompt,
		Command:     command,
		Details:     details,
	}
	return runPopupWithDeadline(target, "dispatch", "hook-validate-bash: dispatch", popupW, popupH, tmpPath, notice)
}

// dispatchPreviewVPHeight shrinks the viewport budget by the detail rows the
// popup adds under "Dispatching" (tui.DispatchDetailRows), so the popup stays
// within the height dispatchPreviewFit budgeted for the client. It never goes
// below dpMinVPHeight; on a client too short even for that, the popup opens
// collapsed (DispatchPreviewModel.fitsExpanded) so the hint bar stays visible.
func dispatchPreviewVPHeight(vpHeight int, details remoteapproval.Details) int {
	return max(vpHeight-tui.DispatchDetailRows(details), dpMinVPHeight)
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
	tokens := invocationTokens(command)
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
// returns whatever error cmd.Run() produced. The error is NOT the verdict
// (tmux exits 0 without running the popup when another overlay is open); the
// popup records its decision in popupResultPath(argsFilePath). It is a package-level var so tests
// can fake the popup outcome without shelling out to a real tmux client --
// same seam pattern as clientDimensions in popup_clamp.go.
var dispatchPopupRunner = runDispatchPopupCmd

func runDispatchPopupCmd(ctx context.Context, client string, popupW, popupH int, argsFilePath string) error {
	agentctlBin := agentctlBinaryPath()
	args := append([]string{"display-popup"}, popupTarget{Client: client}.clientArgs()...)
	args = append(args,
		"-E",
		"-w", strconv.Itoa(popupW),
		"-h", strconv.Itoa(popupH),
		dispatchPreviewPopupCmd(agentctlBin, argsFilePath, popupResultPath(argsFilePath)),
	)
	return exec.CommandContext(ctx, "tmux", args...).Run()
}

// dispatchPreviewPopupCmd returns the shell command line tmux display-popup
// runs for the dispatch preview, with every part quoted individually.
func dispatchPreviewPopupCmd(agentctlBin, argsFilePath, resultFile string) string {
	return shellquote.Join(agentctlBin, "dispatch-preview", "--args-file", argsFilePath, "--result-file", resultFile)
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
	tokens := dispatchTokens(command)
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

// dispatchTokens returns the words of the gated dispatch invocation in
// command: the first segment that is not a help lookup, else the first.
func dispatchTokens(command string) []string {
	seg := gatedDispatchSegment(command)
	tokens := make([]string, len(seg))
	for i, t := range seg {
		tokens[i] = t.text
	}
	return tokens
}

// gatedDispatchSegment is the segment dispatchTokens reads, with quoting
// and expansion kept.
func gatedDispatchSegment(command string) []cmdToken {
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
	return seg
}

// parseDispatchReviewPR returns the --review-pr number of the dispatch in
// command, or 0. Issue titles are not fetched for the popup, so neither is
// the PR title: the popup shows the number.
func parseDispatchReviewPR(command string) int {
	tokens := dispatchTokens(command)
	n := 0
	for i := 0; i < len(tokens); i++ {
		switch tok := tokens[i]; {
		case tok == "--review-pr":
			if i+1 < len(tokens) {
				fmt.Sscanf(tokens[i+1], "%d", &n) //nolint:errcheck
				i++
			}
		case strings.HasPrefix(tok, "--review-pr="):
			fmt.Sscanf(strings.TrimPrefix(tok, "--review-pr="), "%d", &n) //nolint:errcheck
		case isDispatchValueFlag(tok):
			i++ // a value such as a prompt mentioning --review-pr is not the flag
		}
	}
	return n
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
// quoted part, and keeps empty quoted tokens (""). Unquoted shell comments are
// dropped and an unquoted newline becomes a ";" token.
func tokenizeQuoted(s string) []cmdToken {
	var tokens []cmdToken
	var cur strings.Builder
	inQuote, hadQuote, expands := false, false, false
	var quoteChar byte
	// afterMeta is true when the last byte written to cur was an unquoted
	// shell metacharacter (;&|()<>). The tokenizer keeps those inside words,
	// but to the shell they end a word, so a '#' right after one starts a
	// comment ("x;# ...") even though cur is not empty.
	afterMeta := false
	// tail is where the bytes after the last quoted part start in cur.
	tail := 0

	flush := func() {
		if cur.Len() > 0 || hadQuote {
			t := cmdToken{text: cur.String(), quoted: hadQuote, expands: expands}
			if hadQuote {
				t.unquotedTail = tail
			}
			tokens = append(tokens, t)
		}
		cur.Reset()
		hadQuote, expands, afterMeta, tail = false, false, false, 0
	}

	for i := 0; i < len(s); i++ {
		c := s[i]
		if inQuote {
			if c == quoteChar {
				inQuote = false
				tail = cur.Len()
			} else {
				if quoteChar == '"' && (c == '$' || c == '`' || c == '\\') {
					expands = true
				}
				cur.WriteByte(c)
				afterMeta = false
			}
			continue
		}
		switch c {
		case '\'', '"':
			inQuote = true
			hadQuote = true
			quoteChar = c
		case ' ', '\t':
			flush()
		case '\n':
			// An unquoted newline ends the command like ';' does, so a
			// segment never runs on into the next line.
			flush()
			tokens = append(tokens, cmdToken{text: ";"})
		case '#':
			if (cur.Len() > 0 || hadQuote) && !afterMeta {
				cur.WriteByte(c) // mid-word, e.g. a#b: not a comment
				continue
			}
			// A comment runs to the end of the line; the shell never passes
			// it to the command, so neither does the tokenizer. Bytes already
			// in cur ("x;") stay, so takeSegment still sees an attached
			// terminator.
			for i+1 < len(s) && s[i+1] != '\n' {
				i++
			}
		case '\\':
			// Outside quotes a backslash makes the next character literal
			// ('don'\''t' is don't), and backslash-newline is a line
			// continuation. A trailing backslash is left for the shell.
			if i+1 < len(s) {
				i++
				if s[i] != '\n' {
					// The escaped character is quoted: \> or \; is a
					// word, never a redirection or control operator.
					cur.WriteByte(s[i])
					hadQuote = true
					afterMeta = false
					tail = cur.Len()
				}
				continue
			}
			expands = true
			cur.WriteByte(c)
			afterMeta = false
		default:
			if c == '$' || c == '`' {
				expands = true
			}
			cur.WriteByte(c)
			afterMeta = strings.IndexByte(";&|()<>", c) >= 0
		}
	}
	flush()
	return tokens
}

// dispatchPopupNotice builds the pending-approval description for a dispatch.
func dispatchPopupNotice(agentName, targetAgent string, issue int, prompt string) popupNotice {
	target := targetAgent
	if target == "" {
		target = "an agent"
	}
	detail := target
	if issue > 0 {
		target = fmt.Sprintf("%s on #%d", target, issue)
		detail = target
	} else if first := strings.TrimSpace(strings.SplitN(strings.TrimSpace(prompt), "\n", 2)[0]); first != "" {
		detail = target + ": " + textutil.Truncate(first, 80, "...")
	}
	return popupNotice{Agent: agentName, Tool: "dispatch", Detail: detail, Action: "dispatch " + target}
}
