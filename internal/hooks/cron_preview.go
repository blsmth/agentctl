package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/cron"
	"github.com/blsmth/agentctl/internal/cronedit"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/prompttemplate"
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
//   - cron edit <name>: always gated; the popup shows the job as it will be
//     after the edit and which fields change.
//   - cron resume <name>: gated; re-enabling a paused job restarts its fires.
//   - cron run <name> --force: gated; --force skips overlap, max_concurrent
//     and the usage gate.
//
// A job with a prompt template (--template, or a stored template job) shows
// the template's prompt as a fire would read it: from the job dir's project
// (a relative --dir resolved against the payload cwd), shadowing the global
// templates. A --template that does not resolve denies the add or edit, as
// an unresolvable `dispatch --template` does.
//
// Plain `cron run <name>` is not gated: the job was approved when it was
// added (or resumed), and without --force the run honors every throttle a
// scheduled fire does, so it is no more than an early fire of an approved
// job. list, runs, pause, rm, install and uninstall cannot start anything
// that was not already approved and are not gated either.

// Cron actions shown in the popup title (tui.CronPreview.Action).
const (
	cronActionAdd    = "add"
	cronActionEdit   = "edit"
	cronActionResume = "resume"
	cronActionForce  = "run --force"
)

// cronValueFlags are the cron subcommand flags that take a value, so the
// word after them is never a help flag, a positional or a subcommand. Keys
// are the long names; cronShortFlags maps the short forms.
var cronValueFlags = map[string]bool{
	"schedule": true, "at": true, "agent": true, "dir": true, "prompt": true, "prompt-key": true,
	"prompt-file": true, "model": true, "effort": true, "backend": true, "account": true,
	"timeout": true, "overlap": true, "catchup": true, "limit": true, "template": true, "mode": true,
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
	case "add", "edit", "resume":
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
	case "edit":
		return cronActionEdit
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
	worktree   bool
	force      bool
	// improve is --improve-fleet (mode improve).
	improve bool
	// unknown lists dash words parse does not model; a gated segment with
	// any is denied (see buildCronPreview).
	unknown []string
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
		case tok == "--improve-fleet":
			a.improve = true
		case tok == "--no-worktree":
			a.noWorktree = true
		case tok == "--worktree":
			a.worktree = true
		case isCronValueFlag(tok):
			name := strings.TrimPrefix(tok, "--")
			if long, ok := cronShortFlags[tok]; ok {
				name = long
			}
			if i+1 < len(s.tokens) {
				a.values[name] = s.tokens[i+1]
				i++
			}
		case strings.HasPrefix(tok, "--") && strings.Contains(tok, "=") &&
			cronValueFlags[strings.TrimPrefix(strings.SplitN(tok, "=", 2)[0], "--")]:
			name, val, _ := strings.Cut(strings.TrimPrefix(tok, "--"), "=")
			a.values[name] = cmdToken{text: val, quoted: t.quoted, expands: t.expands}
		case strings.HasPrefix(tok, "-"):
			// A flag spelling the parser does not model: an attached short
			// value (-pTEXT, -p=TEXT), --no-worktree=true, or any other
			// flag. Kong would act on some of these, so the segment cannot
			// be shown faithfully. Quoting does not help: the shell strips
			// the quotes before Kong sees the word.
			a.unknown = append(a.unknown, tok)
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

// buildCronPreview is buildCronPreviewIn from the hook's own directory.
func buildCronPreview(command string) (cronPreview, string) {
	return buildCronPreviewIn(command, "")
}

// buildCronPreviewIn resolves the single gated cron invocation in command
// into what the popup shows; cwd is the payload cwd (the Bash tool's
// directory, see askingCwd). A non-empty deny reason means it cannot be
// shown faithfully and the command must be denied (fail closed).
func buildCronPreviewIn(command, cwd string) (cronPreview, string) {
	cwd = askingCwd(cwd)
	segs := gatedCronSegments(command)
	switch len(segs) {
	case 0:
		return cronPreview{}, "agentctl cron: nothing to approve" // unreachable from ValidateBash
	case 1:
	default:
		return cronPreview{}, "agentctl cron: run one cron add/edit/resume/run --force per Bash call, " +
			"so each can be shown for approval."
	}
	seg := segs[0]
	args := seg.parse()
	if len(args.unknown) > 0 {
		return cronPreview{}, fmt.Sprintf("agentctl cron %s: cannot show %q for approval. "+
			"Spell flags as --name value (or --name=value), with no attached short-flag values, "+
			"and use only the documented flags.", seg.sub, args.unknown[0])
	}
	switch seg.sub {
	case "add":
		return buildCronAddPreview(args, command, cwd)
	case "edit":
		return buildCronEditPreview(args, command, cwd)
	}
	return buildStoredCronPreview(seg.sub, seg.action(), args, command)
}

// cronAbsDir is a --dir value as `cron add`/`edit` store it: ~ expanded and
// a relative path joined to cwd. "" stays "".
func cronAbsDir(dir, cwd string) string {
	if dir == "" {
		return ""
	}
	abs := paths.ExpandHome(dir)
	if !filepath.IsAbs(abs) && cwd != "" {
		abs = filepath.Join(cwd, abs)
	}
	return filepath.Clean(abs)
}

// cronFlagTemplate resolves a --template flag of a cron add or edit from
// dir (the job dir a fire reads it from), or returns a deny reason: a name
// the shell would expand, a line that changes directory or environment
// first, or a template that does not load.
func cronFlagTemplate(sub string, tok cmdToken, command, dir string) (*prompttemplate.Template, string) {
	if tok.expands {
		return nil, cronExpansionReason(sub, "template")
	}
	if lineChangesContextFor(command, "cron") {
		return nil, fmt.Sprintf("agentctl cron %s --template %s: the command changes directory or environment "+
			"first, so the approval popup cannot tell which template would run. Run cron %s as its own Bash call.",
			sub, tok.text, sub)
	}
	t, err := templateLoad(tok.text, dir)
	if err != nil {
		logging.Log("hook-validate-bash: cron %s --template %q from %s: %v", sub, tok.text, dir, err)
		return nil, fmt.Sprintf("agentctl cron %s: %s. Check `agentctl template list` from the job dir; "+
			"the approval popup must be able to show the template's prompt.", sub, cronedit.TemplateErrorText(tok.text, err))
	}
	return &t, ""
}

// cronTemplateSource is the "Prompt from" row of a template job.
func cronTemplateSource(t *prompttemplate.Template, note string) string {
	return fmt.Sprintf("template %s (%s, %s; read at every fire%s)", t.Name, t.Scope, t.Path, note)
}

// cronStoredTemplate resolves a stored template job's template for display.
// A template that no longer resolves is shown, not denied: the action does
// not change it, and fires fail (recorded as failed:template) until it
// exists again.
func cronStoredTemplate(job cron.Job, note string) (prompt, source string, t *prompttemplate.Template) {
	tm, err := templateLoad(job.Template, job.Dir)
	if err != nil {
		msg := cronedit.TemplateErrorText(job.Template, err)
		return fmt.Sprintf("(%s; fires will fail until it exists)", msg), fmt.Sprintf("template %s (%s)", job.Template, msg), nil
	}
	return tm.Prompt, cronTemplateSource(&tm, note), &tm
}

// withTemplateDefaults is job as it will run with template t: the unset
// agent, model and effort show the template's value, marked as such.
func withTemplateDefaults(job cron.Job, t *prompttemplate.Template) cron.Job {
	if t == nil {
		return job
	}
	fill := func(dst *string, v string) {
		if *dst == "" && v != "" {
			*dst = v + " (from template)"
		}
	}
	fill(&job.Agent, t.Agent)
	fill(&job.Model, t.Model)
	fill(&job.Effort, t.Effort)
	return job
}

// effectiveAgent is the agent a job runs (its own, else its template's).
func effectiveAgent(job cron.Job, t *prompttemplate.Template) string {
	if job.IsImprove() {
		return job.ImproveAgentName()
	}
	if job.Agent == "" && t != nil {
		return t.Agent
	}
	return job.Agent
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
func buildCronAddPreview(a cronArgs, command, cwd string) (cronPreview, string) {
	if len(a.positional) > 0 && a.positional[0].expands {
		return cronPreview{}, cronExpansionReason("add", "")
	}
	for _, f := range []string{"schedule", "at", "agent", "dir", "prompt", "prompt-key", "prompt-file", "account",
		"model", "effort", "backend", "timeout", "template", "mode"} {
		if a.values[f].expands {
			return cronPreview{}, cronExpansionReason("add", f)
		}
	}
	name := ""
	if len(a.positional) > 0 {
		name = a.positional[0].text
	}
	at, deny := cronFlagAt("add", a)
	if deny != "" {
		return cronPreview{}, deny
	}
	_, hasAt := a.values["at"]
	if hasAt == (a.value("schedule") != "") {
		return cronPreview{}, "agentctl cron add: give exactly one of --schedule (recurring) or --at (one-shot)."
	}
	schedule, err := cron.JobSummary(cron.Job{Name: name, Schedule: a.value("schedule"), At: at}, cronNow())
	if err != nil {
		return cronPreview{}, fmt.Sprintf("agentctl cron add: %v. Fix the schedule and retry.", err)
	}

	prompt, source, deny := cronAddPrompt(a, command)
	if deny != "" {
		return cronPreview{}, deny
	}
	var tmpl *prompttemplate.Template
	if tok, ok := a.values["template"]; ok {
		if source != "" {
			return cronPreview{}, "agentctl cron add: --template is the prompt; do not combine it with " +
				"--prompt, --prompt-key or --prompt-file."
		}
		if tmpl, deny = cronFlagTemplate("add", tok, command, cronAbsDir(a.value("dir"), cwd)); deny != "" {
			return cronPreview{}, deny
		}
		prompt, source = tmpl.Prompt, cronTemplateSource(tmpl, "; it changes only through an approved template edit")
	}
	wt := !a.noWorktree
	job := cron.Job{
		Name: name, Schedule: a.value("schedule"), At: at, Agent: a.value("agent"), Dir: displayDir(a.value("dir")),
		Account: a.value("account"), Model: a.value("model"), Effort: a.value("effort"), Backend: a.value("backend"),
		Timeout: a.value("timeout"), Overlap: a.value("overlap"), Catchup: a.value("catchup"), Worktree: &wt,
		Template: a.value("template"), Mode: a.value("mode"),
	}
	if a.improve {
		job.Mode = cron.ModeImprove
	}
	job.Normalize()
	return cronPreview{
		action:   cronActionAdd,
		job:      name,
		agent:    effectiveAgent(job, tmpl),
		account:  job.Account,
		schedule: schedule,
		body:     cronJobBody(withTemplateDefaults(job, tmpl), schedule, "", source, prompt),
	}, ""
}

// cronFlagAt parses a literal --at of a cron add or edit into its stored
// form ("" when absent), or returns a deny reason: a time that does not
// parse or is not in the future (the command would fail on it).
func cronFlagAt(sub string, a cronArgs) (string, string) {
	tok, ok := a.values["at"]
	if !ok {
		return "", ""
	}
	t, err := cron.ParseAt(tok.text)
	if err != nil {
		return "", fmt.Sprintf("agentctl cron %s: %v.", sub, err)
	}
	if !t.After(cronNow()) {
		return "", fmt.Sprintf("agentctl cron %s: --at %s is in the past.", sub, tok.text)
	}
	return cron.FormatAt(t), ""
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

// cronEditFlags are the value flags `cron edit` can change, checked for
// shell expansion like cron add's.
var cronEditFlags = []string{"schedule", "at", "agent", "dir", "prompt", "prompt-key", "prompt-file", "account",
	"model", "effort", "backend", "timeout", "overlap", "catchup", "template", "mode"}

// buildCronEditPreview previews `cron edit`: the stored job with the given
// flags applied, the fields that change, and the prompt it will run.
func buildCronEditPreview(a cronArgs, command, cwd string) (cronPreview, string) {
	if len(a.positional) == 0 {
		return cronPreview{}, "agentctl cron edit: no job name given."
	}
	if a.positional[0].expands {
		return cronPreview{}, cronExpansionReason("edit", "")
	}
	if _, ok := a.values["template"]; ok && (a.values["prompt"].text != "" || a.values["prompt"].quoted ||
		a.value("prompt-key") != "" || a.value("prompt-file") != "") {
		return cronPreview{}, "agentctl cron edit: --template replaces the prompt; do not combine it with " +
			"--prompt, --prompt-key or --prompt-file."
	}
	for _, f := range cronEditFlags {
		if a.values[f].expands {
			return cronPreview{}, cronExpansionReason("edit", f)
		}
	}
	name := a.positional[0].text
	_, hasAt := a.values["at"]
	if _, hasSchedule := a.values["schedule"]; hasAt && hasSchedule {
		return cronPreview{}, "agentctl cron edit: --schedule and --at are mutually exclusive."
	}
	at, deny := cronFlagAt("edit", a)
	if deny != "" {
		return cronPreview{}, deny
	}
	jobs, err := cronJobsLoader()
	if err != nil {
		return cronPreview{}, fmt.Sprintf("agentctl cron edit: cannot read the jobs to show for approval: %v", err)
	}
	i := jobs.Find(name)
	if i < 0 {
		return cronPreview{}, fmt.Sprintf("agentctl cron edit: no cron job named %q.", name)
	}
	old := jobs.Jobs[i]
	job := old
	_, has := a.values["prompt"]
	promptSet := has || a.value("prompt-file") != "" || a.value("prompt-key") != ""
	var prompt, source string
	if promptSet {
		var deny string
		prompt, source, deny = cronAddPrompt(a, command)
		if deny != "" {
			return cronPreview{}, strings.Replace(deny, "cron add", "cron edit", 1)
		}
		if a.value("prompt-key") != "" {
			job.Prompt, job.PromptKey, job.Template = "", a.value("prompt-key"), ""
		} else {
			job.Prompt, job.PromptKey, job.Template = prompt, "", ""
		}
	}
	_, templateSet := a.values["template"]
	if templateSet {
		job.Prompt, job.PromptKey, job.Template = "", "", a.value("template")
	}
	for flag, dst := range map[string]*string{
		"schedule": &job.Schedule, "agent": &job.Agent, "account": &job.Account, "model": &job.Model,
		"effort": &job.Effort, "backend": &job.Backend, "timeout": &job.Timeout, "overlap": &job.Overlap,
		"catchup": &job.Catchup, "mode": &job.Mode,
	} {
		if t, ok := a.values[flag]; ok {
			*dst = t.text
		}
	}
	if _, ok := a.values["schedule"]; ok {
		job.At = ""
		job.ClearOutcome()
	}
	if hasAt {
		job.Schedule, job.At = "", at
		job.ClearOutcome()
	}
	// The template is read from the dir the job will have.
	templateDir := old.Dir
	if t, ok := a.values["dir"]; ok {
		job.Dir = displayDir(t.text)
		templateDir = cronAbsDir(t.text, cwd)
		// A relative --dir behind a cd resolves elsewhere than cwd, so the
		// template a fire reads from it cannot be shown (an explicit
		// --template is checked by cronFlagTemplate).
		if job.Template != "" && !filepath.IsAbs(paths.ExpandHome(t.text)) && lineChangesContextFor(command, "cron") {
			return cronPreview{}, fmt.Sprintf("agentctl cron edit %s --dir %s: the command changes directory or environment "+
				"first, so the approval popup cannot tell which template the job would read. Pass an absolute --dir, "+
				"or run cron edit as its own Bash call.", name, t.text)
		}
	}
	var tmpl *prompttemplate.Template
	switch {
	case templateSet:
		var deny string
		if tmpl, deny = cronFlagTemplate("edit", a.values["template"], command, templateDir); deny != "" {
			return cronPreview{}, deny
		}
		prompt, source = tmpl.Prompt, cronTemplateSource(tmpl, "; it changes only through an approved template edit")
	case !promptSet && old.Template != "":
		lookup := old
		lookup.Dir = templateDir
		prompt, source, tmpl = cronStoredTemplate(lookup, "; unchanged")
	}
	if a.worktree || a.noWorktree {
		wt := a.worktree && !a.noWorktree
		job.Worktree = &wt
	}
	if a.improve {
		job.Mode = cron.ModeImprove
	}
	job.Normalize()
	schedule, err := cron.JobSummary(job, cronNow())
	if err != nil {
		return cronPreview{}, fmt.Sprintf("agentctl cron edit: %v. Fix the schedule and retry.", err)
	}
	if !promptSet && job.Template == "" {
		prompt, source = old.Prompt, "stored prompt (unchanged)"
		if old.PromptKey != "" {
			source = fmt.Sprintf("context key %s (unchanged; read at every fire)", old.PromptKey)
			if content, ok := resolvePromptKey(old.PromptKey, command); ok {
				prompt = content
			} else {
				prompt = fmt.Sprintf("(context key %s is not set; fires will fail until it is)", old.PromptKey)
			}
		}
	}
	note := "Edit changes nothing."
	if changed := cronedit.ChangedFields(old, job); len(changed) > 0 {
		note = "Edit changes: " + strings.Join(changed, ", ") + "."
	}
	switch {
	case hasAt:
		note += " The job becomes a one-shot: it fires once, then is kept as done."
	case strings.TrimSpace(old.Schedule) != strings.TrimSpace(job.Schedule):
		note += " The schedule restarts from now; earlier fires are not caught up."
	}
	return cronPreview{
		action:   cronActionEdit,
		job:      job.Name,
		agent:    effectiveAgent(job, tmpl),
		account:  job.Account,
		schedule: schedule,
		body:     cronJobBody(withTemplateDefaults(job, tmpl), schedule, note, source, prompt),
	}, ""
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
	if job.IsDone() {
		return cronPreview{}, fmt.Sprintf("agentctl cron %s: %v.", sub, cron.OnceDoneError(job))
	}
	schedule, err := cron.JobSummary(job, cronNow())
	if err != nil {
		schedule = fmt.Sprintf("%s (invalid: %v)", cron.ScheduleText(job), err)
	}
	note := "Resuming re-enables scheduled fires; fires missed while paused are not caught up."
	if action == cronActionForce {
		note = "Force run: fires now, ignoring overlap, max_concurrent and the usage gate."
	}
	prompt, source := job.Prompt, "stored prompt"
	var tmpl *prompttemplate.Template
	if job.Template != "" {
		prompt, source, tmpl = cronStoredTemplate(job, "")
	}
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
		agent:    effectiveAgent(job, tmpl),
		account:  job.Account,
		schedule: schedule,
		body:     cronJobBody(withTemplateDefaults(job, tmpl), schedule, note, source, prompt),
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
	row("Template", job.Template)
	row("Dir", orDash(job.Dir))
	row("Account", job.Account)
	row("Harness", job.Harness)
	row("Mode", job.Mode)
	if job.IsImprove() {
		return improveJobBody(&b, job, source, prompt)
	}
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

// improveJobBody finishes the popup body of an improve job: the rows that
// apply, then what a fire does in place of a prompt. A theme or template
// body the job carries is shown after it.
func improveJobBody(b *strings.Builder, job cron.Job, source, prompt string) string {
	row := func(label, value string) {
		if value != "" {
			fmt.Fprintf(b, "- **%s:** %s\n", label, value)
		}
	}
	row("Timeout", orNone(job.Timeout))
	row("Model", job.Model)
	row("Effort", job.Effort)
	row("Backend", job.Backend)
	row("Overlap", job.Overlap)
	row("Catch-up", job.Catchup)
	row("Theme from", source)
	b.WriteString("\n---\n\n")
	b.WriteString(job.ImproveSummary(cronNow()))
	b.WriteString("\n\nUnattended: the agent reads the fleet evidence, edits agent definitions and settings through the history store, and dispatches engineers for repo changes.")
	if strings.TrimSpace(prompt) != "" {
		b.WriteString("\n\nAdded to the prompt:\n\n" + prompt)
	}
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
func cronPreviewGate(agentName, command, cwd string) (approved, decided bool, denyReason string) {
	if deny := cronUnknownAgentDeny(command, askingCwd(cwd)); deny != "" {
		return false, false, deny
	}
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

	p, deny := buildCronPreviewIn(command, cwd)
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
	case cronActionEdit:
		verb = "edit"
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
