package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/cron"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/remoteapproval"
	"github.com/blsmth/agentctl/internal/textutil"
	"github.com/blsmth/agentctl/internal/tui"
)

// This file gates the `agentctl cron` subcommands that authorize future
// autonomous runs (docs/design/cron.md, "Safety and approval"). One approved
// `cron add` means unlimited unattended fires, so it gets the same preview
// popup, lock, deadline, blocked state and phone approval as a dispatch:
//
//   - cron add: always gated (except a --help lookup).
//   - cron resume <name>: gated; re-enabling a paused job restarts its fires.
//   - cron run <name> --force: gated; --force skips overlap, max_concurrent
//     and the usage gate.
//
// Plain `cron run <name>` is not gated: the job was approved when it was
// added (or resumed), and without --force the run honors every throttle a
// scheduled fire does, so it is no more than an early fire of an approved
// job. list, runs, pause, rm, install and uninstall cannot start anything
// that was not already approved and are not gated either.

// Cron actions shown in the popup title (tui.CronPreview.Action).
const (
	cronActionAdd    = "add"
	cronActionResume = "resume"
	cronActionForce  = "run --force"
)

// cronValueFlags are the cron subcommand flags that take a value, so the
// word after them is never a help flag, a positional or a subcommand. Keys
// are the long names; cronShortFlags maps the short forms.
var cronValueFlags = map[string]bool{
	"schedule": true, "agent": true, "dir": true, "prompt": true, "prompt-key": true,
	"prompt-file": true, "model": true, "effort": true, "backend": true, "account": true,
	"timeout": true, "overlap": true, "catchup": true, "limit": true,
}

var cronShortFlags = map[string]string{"-a": "agent", "-p": "prompt", "-k": "prompt-key", "-m": "model", "-n": "limit"}

// cronSegment is one `agentctl cron <sub> ...` invocation in a command line.
type cronSegment struct {
	sub    string
	tokens []cmdToken
}

// cronSegments returns every `agentctl cron <sub>` invocation in command,
// with the tokens after the subcommand up to the first control operator. Like
// dispatchSegments it matches absolute paths and the bare name, and a quoted
// string containing the words ("echo 'agentctl cron add'") is one token and
// never matches. Data heredoc bodies are ignored (invocationTokens).
func cronSegments(command string) []cronSegment {
	tokens := invocationTokens(command)
	var segs []cronSegment
	for i := 0; i+2 < len(tokens); i++ {
		if !strings.HasSuffix(tokens[i].text, "agentctl") || tokens[i+1].text != "cron" {
			continue
		}
		sub := tokens[i+2].text
		if trimmed := strings.TrimRight(sub, ";|&"); trimmed != sub {
			// "agentctl cron resume;" -- the invocation ends at the word.
			segs = append(segs, cronSegment{sub: trimmed})
			continue
		}
		segs = append(segs, cronSegment{sub: sub, tokens: takeSegment(tokens[i+3:])})
	}
	return segs
}

// gated reports whether the invocation needs human approval (see the file
// comment for the rules).
func (s cronSegment) gated() bool {
	if segmentHasHelpFlags(s.tokens, isCronValueFlag) {
		return false
	}
	switch s.sub {
	case "add", "resume":
		return true
	case "run":
		return s.parse().force
	}
	return false
}

// action is the popup action for a gated segment.
func (s cronSegment) action() string {
	switch s.sub {
	case "resume":
		return cronActionResume
	case "run":
		return cronActionForce
	}
	return cronActionAdd
}

// gatedCronSegments returns the invocations in command that need approval.
func gatedCronSegments(command string) []cronSegment {
	var out []cronSegment
	for _, s := range cronSegments(command) {
		if s.gated() {
			out = append(out, s)
		}
	}
	return out
}

// isAgentctlCronGated reports whether command runs a gated cron subcommand.
func isAgentctlCronGated(command string) bool {
	return len(gatedCronSegments(command)) > 0
}

// isCronValueFlag reports whether tok is a cron flag that takes a separate
// value word.
func isCronValueFlag(tok string) bool {
	if long, ok := cronShortFlags[tok]; ok {
		tok = long
	} else if !strings.HasPrefix(tok, "--") {
		return false
	}
	return cronValueFlags[strings.TrimPrefix(tok, "--")]
}

// cronArgs is a parsed cron segment. values holds each value flag's token by
// long name (the last occurrence wins, as with Kong).
type cronArgs struct {
	positional []cmdToken
	values     map[string]cmdToken
	noWorktree bool
	force      bool
}

// value returns the text of a value flag ("" when absent).
func (a cronArgs) value(name string) string { return a.values[name].text }

// parse splits the segment into flags and positionals. It handles
// "--flag value", "--flag=value" and the short forms; anything after "--" is
// positional.
func (s cronSegment) parse() cronArgs {
	a := cronArgs{values: map[string]cmdToken{}}
	for i := 0; i < len(s.tokens); i++ {
		t := s.tokens[i]
		tok := t.text
		switch {
		case tok == "--" && !t.quoted:
			a.positional = append(a.positional, s.tokens[i+1:]...)
			return a
		case tok == "--force" || strings.HasPrefix(tok, "--force="):
			// Any spelling of --force is treated as forcing (fail safe).
			a.force = true
		case tok == "--no-worktree":
			a.noWorktree = true
		case isCronValueFlag(tok):
			name := strings.TrimPrefix(tok, "--")
			if long, ok := cronShortFlags[tok]; ok {
				name = long
			}
			if i+1 < len(s.tokens) {
				a.values[name] = s.tokens[i+1]
				i++
			}
		case strings.HasPrefix(tok, "--") && strings.Contains(tok, "="):
			name, val, _ := strings.Cut(strings.TrimPrefix(tok, "--"), "=")
			if cronValueFlags[name] {
				a.values[name] = cmdToken{text: val, quoted: t.quoted, expands: t.expands}
			}
		case strings.HasPrefix(tok, "-") && !t.quoted && tok != "-":
			// Some other flag; Kong rejects unknown ones.
		default:
			a.positional = append(a.positional, t)
		}
	}
	return a
}

// cronPreview is what the popup and the phone show for a gated command.
type cronPreview struct {
	action   string
	job      string
	agent    string
	account  string
	schedule string // one-line summary for the metadata row
	body     string // markdown job details plus the prompt
}

// cronJobsLoader reads the stored jobs for resume / run --force previews. A
// var so tests use a temp file.
var cronJobsLoader = func() (*cron.Jobs, error) {
	path, err := cron.JobsPath()
	if err != nil {
		return nil, err
	}
	return cron.LoadJobs(path)
}

// cronNow is the clock for the next-fire line. Seam for tests.
var cronNow = time.Now

// buildCronPreview resolves the single gated cron invocation in command into
// what the popup shows. A non-empty deny reason means it cannot be shown
// faithfully and the command must be denied (fail closed).
func buildCronPreview(command string) (cronPreview, string) {
	segs := gatedCronSegments(command)
	switch len(segs) {
	case 0:
		return cronPreview{}, "agentctl cron: nothing to approve" // unreachable from ValidateBash
	case 1:
	default:
		return cronPreview{}, "agentctl cron: run one cron add/resume/run --force per Bash call, " +
			"so each can be shown for approval."
	}
	seg := segs[0]
	args := seg.parse()
	if seg.sub == "add" {
		return buildCronAddPreview(args, command)
	}
	return buildStoredCronPreview(seg.sub, seg.action(), args, command)
}

// cronExpansionReason is the deny reason for a value the shell would expand.
func cronExpansionReason(sub, flag string) string {
	what := "--" + flag
	if flag == "" {
		what = "the job name"
	}
	return fmt.Sprintf("agentctl cron %s: %s uses shell expansion ($, backticks or backslashes), "+
		"so the approval popup cannot show what would be scheduled. Pass a literal value; "+
		"for a long prompt use --prompt-file <path> or --prompt-key <key>.", sub, what)
}

// cronPromptKeyDenyReason is promptKeyDenyReason for cron add.
func cronPromptKeyDenyReason(key string) string {
	return fmt.Sprintf("cron add --prompt-key %s: key not found in the context store. "+
		"Run `agentctl context set %s ...` as a separate Bash call first, then cron add, "+
		"so the approval popup can show the prompt.", key, key)
}

// buildCronAddPreview previews `cron add`.
func buildCronAddPreview(a cronArgs, command string) (cronPreview, string) {
	if len(a.positional) > 0 && a.positional[0].expands {
		return cronPreview{}, cronExpansionReason("add", "")
	}
	for _, f := range []string{"schedule", "agent", "dir", "prompt", "prompt-key", "prompt-file", "account",
		"model", "effort", "backend", "timeout"} {
		if a.values[f].expands {
			return cronPreview{}, cronExpansionReason("add", f)
		}
	}
	name := ""
	if len(a.positional) > 0 {
		name = a.positional[0].text
	}
	schedule, err := cron.Summary(a.value("schedule"), name, cronNow())
	if err != nil {
		return cronPreview{}, fmt.Sprintf("agentctl cron add: %v. Fix the schedule and retry.", err)
	}

	prompt, source, deny := cronAddPrompt(a, command)
	if deny != "" {
		return cronPreview{}, deny
	}
	wt := !a.noWorktree
	job := cron.Job{
		Name: name, Schedule: a.value("schedule"), Agent: a.value("agent"), Dir: displayDir(a.value("dir")),
		Account: a.value("account"), Model: a.value("model"), Effort: a.value("effort"), Backend: a.value("backend"),
		Timeout: a.value("timeout"), Overlap: a.value("overlap"), Catchup: a.value("catchup"), Worktree: &wt,
	}
	job.Normalize()
	return cronPreview{
		action:   cronActionAdd,
		job:      name,
		agent:    job.Agent,
		account:  job.Account,
		schedule: schedule,
		body:     cronJobBody(job, schedule, "", source, prompt),
	}, ""
}

// cronAddPrompt resolves the prompt of a cron add: inline, from the context
// store (or a literal `context set` earlier in command, see
// resolvePromptKey), or from --prompt-file (read now, as cron add copies it
// at add time).
func cronAddPrompt(a cronArgs, command string) (prompt, source, deny string) {
	switch {
	case a.value("prompt-file") != "":
		path := a.value("prompt-file")
		data, err := os.ReadFile(paths.ExpandHome(path))
		if err != nil {
			return "", "", fmt.Sprintf("agentctl cron add: cannot read --prompt-file %s (%v), "+
				"so there is no prompt to show for approval.", path, err)
		}
		if strings.TrimSpace(string(data)) == "" {
			return "", "", fmt.Sprintf("agentctl cron add: --prompt-file %s is empty.", path)
		}
		return string(data), fmt.Sprintf("--prompt-file %s (copied into the job now)", path), ""
	case a.value("prompt-key") != "":
		key := a.value("prompt-key")
		content, ok := resolvePromptKey(key, command)
		if !ok {
			return "", "", cronPromptKeyDenyReason(key)
		}
		return content, fmt.Sprintf("context key %s (read at every fire; it can change after approval)", key), ""
	case a.values["prompt"].text != "" || a.values["prompt"].quoted:
		return a.value("prompt"), "--prompt", ""
	}
	return "", "", ""
}

// displayDir is dir as cron add will resolve it, against the hook's working
// directory (the agent's), with the value as given when they differ.
func displayDir(dir string) string {
	if dir == "" {
		return ""
	}
	abs := paths.ExpandHome(dir)
	if !filepath.IsAbs(abs) {
		if cwd, err := os.Getwd(); err == nil {
			abs = filepath.Join(cwd, abs)
		}
	}
	if abs = filepath.Clean(abs); abs != dir {
		return fmt.Sprintf("%s (as given: %s)", abs, dir)
	}
	return abs
}

// buildStoredCronPreview previews resume / run --force from the stored job.
func buildStoredCronPreview(sub, action string, a cronArgs, command string) (cronPreview, string) {
	if len(a.positional) == 0 {
		return cronPreview{}, fmt.Sprintf("agentctl cron %s: no job name given.", sub)
	}
	if a.positional[0].expands {
		return cronPreview{}, cronExpansionReason(sub, "")
	}
	name := a.positional[0].text
	jobs, err := cronJobsLoader()
	if err != nil {
		return cronPreview{}, fmt.Sprintf("agentctl cron %s: cannot read the jobs to show for approval: %v", sub, err)
	}
	i := jobs.Find(name)
	if i < 0 {
		return cronPreview{}, fmt.Sprintf("agentctl cron %s: no cron job named %q.", sub, name)
	}
	job := jobs.Jobs[i]
	schedule, err := cron.Summary(job.Schedule, job.Name, cronNow())
	if err != nil {
		schedule = fmt.Sprintf("%s (invalid: %v)", job.Schedule, err)
	}
	note := "Resuming re-enables scheduled fires; fires missed while paused are not caught up."
	if action == cronActionForce {
		note = "Force run: fires now, ignoring overlap, max_concurrent and the usage gate."
	}
	prompt, source := job.Prompt, "stored prompt"
	if job.PromptKey != "" {
		source = fmt.Sprintf("context key %s (read at every fire)", job.PromptKey)
		if content, ok := resolvePromptKey(job.PromptKey, command); ok {
			prompt = content
		} else {
			prompt = fmt.Sprintf("(context key %s is not set; fires will fail until it is)", job.PromptKey)
		}
	}
	return cronPreview{
		action:   action,
		job:      job.Name,
		agent:    job.Agent,
		account:  job.Account,
		schedule: schedule,
		body:     cronJobBody(job, schedule, note, source, prompt),
	}, ""
}

// cronJobBody renders the job details and prompt as markdown, for the popup
// viewport and the phone (which has no metadata rows, so the job name and
// schedule are repeated here).
func cronJobBody(job cron.Job, schedule, note, source, prompt string) string {
	var b strings.Builder
	if note != "" {
		fmt.Fprintf(&b, "**%s**\n\n", note)
	}
	row := func(label, value string) {
		if value != "" {
			fmt.Fprintf(&b, "- **%s:** %s\n", label, value)
		}
	}
	row("Job", orDash(job.Name))
	row("Schedule", schedule)
	row("Agent", orDash(job.Agent))
	row("Dir", orDash(job.Dir))
	row("Account", job.Account)
	row("Harness", job.Harness)
	row("Mode", job.Mode)
	worktree := "on (a worktree per fire)"
	if !job.UseWorktree() {
		worktree = "off (runs in the project checkout)"
	}
	row("Worktree", worktree)
	row("Timeout", orNone(job.Timeout))
	row("Model", job.Model)
	row("Effort", job.Effort)
	row("Backend", job.Backend)
	row("Overlap", job.Overlap)
	row("Catch-up", job.Catchup)
	row("Prompt from", source)
	b.WriteString("\n---\n\n")
	if strings.TrimSpace(prompt) == "" {
		prompt = "(no prompt)"
	}
	b.WriteString(prompt)
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "(not set)"
	}
	return s
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// cronPreviewGate shows the approval popup for a gated `agentctl cron`
// command. Its contract is dispatchPreviewGate's: (approved, decided,
// denyReason), decided=false (stay silent) when no popup was shown, and a
// non-empty denyReason when the job cannot be shown faithfully or the popup
// went unanswered.
func cronPreviewGate(agentName, command string) (approved, decided bool, denyReason string) {
	if os.Getenv("TMUX") == "" {
		logging.Log("hook-validate-bash: not in tmux session, passing through cron")
		return false, false, ""
	}
	release, ok := acquireGateLock(command)
	if !ok {
		logging.Log("hook-validate-bash: cron gate already owned by another invocation, staying silent")
		return false, false, ""
	}
	defer release()

	p, deny := buildCronPreview(command)
	if deny != "" {
		return false, false, deny
	}

	contentWidth, vpHeight := dispatchPreviewFit()
	target := resolvePopupTarget()
	args := DispatchPreviewArgs{
		AgentName:    target.label(agentName),
		TargetAgent:  p.agent,
		Prompt:       p.body,
		Command:      command,
		Account:      p.account,
		Kind:         PreviewKindCron,
		CronAction:   p.action,
		CronJob:      p.job,
		CronSchedule: p.schedule,
		ContentWidth: contentWidth,
		VPHeight:     vpHeight,
	}
	tmpPath, err := writePreviewArgs("agentctl-cron-preview-*", args)
	if err != nil {
		logging.Log("hook-validate-bash: cron preview args: %v", err)
		return false, false, ""
	}
	defer os.Remove(tmpPath)

	sz := clampPopupToClient(tui.DispatchPreviewMaxPopupSize(
		tui.WithContentWidth(contentWidth),
		tui.WithMaxViewportHeight(vpHeight),
	))
	logging.Log("hook-validate-bash: launching cron popup agent=%s action=%q job=%s", agentName, p.action, p.job)
	return runPopupWithDeadline(target, "cron job", "hook-validate-bash: cron", sz.Width, sz.Height, tmpPath, cronPopupNotice(agentName, command, p))
}

// cronPopupNotice builds the pending-approval description for a cron gate.
func cronPopupNotice(agentName, command string, p cronPreview) popupNotice {
	verb := "schedule"
	switch p.action {
	case cronActionResume:
		verb = "resume"
	case cronActionForce:
		verb = "force-run"
	}
	detail := p.job
	if p.agent != "" {
		detail += " (" + p.agent + ")"
	}
	if p.schedule != "" {
		detail += ": " + p.schedule
	}
	return popupNotice{
		Agent:  agentName,
		Tool:   "cron",
		Detail: textutil.Truncate(detail, 120, "..."),
		Action: verb + " cron job " + p.job,
		Remote: &remoteDetails{
			Kind:        remoteapproval.KindCron,
			AskingAgent: agentName,
			TargetAgent: p.agent,
			Prompt:      p.body,
			Command:     command,
		},
	}
}

// writePreviewArgs writes args to a new temp file and returns its path.
func writePreviewArgs(pattern string, args DispatchPreviewArgs) (string, error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", fmt.Errorf("creating temp file: %w", err)
	}
	if err := json.NewEncoder(f).Encode(args); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", fmt.Errorf("writing temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("closing temp file: %w", err)
	}
	return f.Name(), nil
}
