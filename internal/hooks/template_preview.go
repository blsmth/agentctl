package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/blsmth/agentctl/internal/dispatchplan"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/prompttemplate"
	"github.com/blsmth/agentctl/internal/remoteapproval"
	"github.com/blsmth/agentctl/internal/textutil"
	"github.com/blsmth/agentctl/internal/tui"
)

// Prompt templates (internal/prompttemplate) are dispatch payloads, so this
// file does two things:
//
//   - `agentctl dispatch --template <name>` resolves the template the way the
//     CLI will (the payload cwd's project, shadowing the global dir) so the
//     dispatch popup shows the real prompt; a template it cannot resolve
//     denies the dispatch, like an unresolvable --prompt-key.
//   - `agentctl template add|edit|rm` from an agent session goes through an
//     approval popup showing the new (or removed) content, so an agent cannot
//     quietly rewrite a template a human approves dispatches of. `list` and
//     `show` are not gated. A human in a plain shell never meets this: it
//     is a Claude Code hook.
//
// The file-write side doors are checkTemplateTamper (Bash) and
// templateEditTarget (Write/Edit).

// templateLoad and templateLoadScope read templates. Seams for tests.
var (
	templateLoad      = prompttemplate.Load
	templateLoadScope = prompttemplate.LoadScope
)

// dispatchTemplateFlag returns the last --template value of the gated
// dispatch in command. The values of other value flags (a prompt that
// mentions --template) are skipped.
func dispatchTemplateFlag(command string) (cmdToken, bool) {
	seg := gatedDispatchSegment(command)
	var tok cmdToken
	found := false
	for i := 0; i < len(seg); i++ {
		t := seg[i]
		switch {
		case t.text == "--template":
			if i+1 < len(seg) {
				tok, found = seg[i+1], true
				i++
			}
		case strings.HasPrefix(t.text, "--template="):
			tok = cmdToken{text: strings.TrimPrefix(t.text, "--template="), quoted: t.quoted, expands: t.expands}
			found = true
		case isDispatchValueFlag(t.text):
			i++
		}
	}
	return tok, found
}

// resolveDispatchTemplate loads the --template of the dispatch in command
// from cwd (the Bash tool's directory). It returns nil when the dispatch
// names no template, and a deny reason when the template cannot be shown
// faithfully: a name the shell would expand, a line that changes directory
// or environment before the dispatch (which project's template, or which
// config dir, would win is then unknown), or a template that does not load.
func resolveDispatchTemplate(command, cwd string) (*prompttemplate.Template, string) {
	tok, found := dispatchTemplateFlag(command)
	if !found {
		return nil, ""
	}
	if tok.expands {
		return nil, "dispatch --template: the template name uses shell expansion, so the approval popup " +
			"cannot show which template would run. Pass the literal name."
	}
	if lineChangesContext(command) {
		return nil, fmt.Sprintf("dispatch --template %s: the command changes directory or environment before "+
			"dispatching, so the approval popup cannot tell which template would run. "+
			"Run the dispatch as its own Bash call.", tok.text)
	}
	t, err := templateLoad(tok.text, cwd)
	if err != nil {
		logging.Log("hook-validate-bash: dispatch --template %q: %v", tok.text, err)
		return nil, fmt.Sprintf("dispatch --template %s: %v. Check `agentctl template list`; "+
			"the approval popup must be able to show the template's prompt.", tok.text, err)
	}
	return &t, ""
}

// templateName is t's name, or "" for nil.
func templateName(t *prompttemplate.Template) string {
	if t == nil {
		return ""
	}
	return t.Name
}

// templateValueFlags are the `template` subcommand flags that take a value.
var templateValueFlags = map[string]bool{
	"description": true, "agent": true, "model": true, "dir": true, "effort": true,
	"prompt": true, "prompt-file": true,
}

// templateBoolFlags are its boolean flags.
var templateBoolFlags = map[string]bool{"project": true, "json": true}

// templateGatedSubs are the subcommands that need approval.
var templateGatedSubs = map[string]bool{"add": true, "edit": true, "rm": true}

func isTemplateValueFlag(tok string) bool {
	return strings.HasPrefix(tok, "--") && templateValueFlags[strings.TrimPrefix(tok, "--")]
}

// templateSegments returns every `agentctl template <sub>` invocation in
// command (the cronSegments rules: data heredocs ignored, quoted mentions
// never match).
func templateSegments(command string) []cronSegment {
	tokens := invocationTokens(command)
	var segs []cronSegment
	for i := 0; i+2 < len(tokens); i++ {
		if !strings.HasSuffix(tokens[i].text, "agentctl") || tokens[i+1].text != "template" {
			continue
		}
		sub := tokens[i+2].text
		if trimmed := strings.TrimRight(sub, ";|&"); trimmed != sub {
			segs = append(segs, cronSegment{sub: trimmed})
			continue
		}
		segs = append(segs, cronSegment{sub: sub, tokens: takeSegment(tokens[i+3:])})
	}
	return segs
}

// gatedTemplateSegments returns the template invocations needing approval.
func gatedTemplateSegments(command string) []cronSegment {
	var out []cronSegment
	for _, s := range templateSegments(command) {
		if templateGatedSubs[s.sub] && !segmentHasHelpFlags(s.tokens, isTemplateValueFlag) {
			out = append(out, s)
		}
	}
	return out
}

// isAgentctlTemplateGated reports whether command runs `agentctl template
// add|edit|rm`.
func isAgentctlTemplateGated(command string) bool {
	return len(gatedTemplateSegments(command)) > 0
}

// templateArgs is a parsed template segment.
type templateArgs struct {
	positional []cmdToken
	values     map[string]cmdToken
	project    bool
	unknown    []string
}

func parseTemplateArgs(seg []cmdToken) templateArgs {
	a := templateArgs{values: map[string]cmdToken{}}
	for i := 0; i < len(seg); i++ {
		t := seg[i]
		tok := t.text
		name, inline, hasInline := strings.TrimPrefix(tok, "--"), "", false
		if strings.HasPrefix(tok, "--") {
			if eq := strings.IndexByte(name, '='); eq >= 0 {
				name, inline, hasInline = name[:eq], name[eq+1:], true
			}
		}
		switch {
		case tok == "--" && !t.quoted:
			a.positional = append(a.positional, seg[i+1:]...)
			return a
		case strings.HasPrefix(tok, "--") && templateBoolFlags[name] && !hasInline:
			if name == "project" {
				a.project = true
			}
		case strings.HasPrefix(tok, "--") && templateValueFlags[name]:
			if hasInline {
				a.values[name] = cmdToken{text: inline, quoted: t.quoted, expands: t.expands}
			} else if i+1 < len(seg) {
				a.values[name] = seg[i+1]
				i++
			} else {
				a.unknown = append(a.unknown, tok)
			}
		case strings.HasPrefix(tok, "-"):
			// Short flags, --project=true and anything unmodeled: Kong may
			// act on it, so it cannot be shown faithfully.
			a.unknown = append(a.unknown, tok)
		default:
			a.positional = append(a.positional, t)
		}
	}
	return a
}

// templatePreview is what the popup and the phone show.
type templatePreview struct {
	action string // add, edit, rm
	name   string
	path   string
	agent  string
	body   string
}

// buildTemplatePreview resolves the single gated template invocation in
// command against cwd. A non-empty deny reason means it cannot be shown
// faithfully (fail closed).
func buildTemplatePreview(command, cwd string) (templatePreview, string) {
	segs := gatedTemplateSegments(command)
	switch len(segs) {
	case 0:
		return templatePreview{}, "agentctl template: nothing to approve"
	case 1:
	default:
		return templatePreview{}, "agentctl template: run one template add/edit/rm per Bash call, so each can be shown for approval."
	}
	seg := segs[0]
	if lineChangesContextFor(command, "template") {
		return templatePreview{}, fmt.Sprintf("agentctl template %s: the command changes directory or environment first, "+
			"so the approval popup cannot tell which file it writes. Run it as its own Bash call.", seg.sub)
	}
	a := parseTemplateArgs(seg.tokens)
	if len(a.unknown) > 0 {
		return templatePreview{}, fmt.Sprintf("agentctl template %s: cannot show %q for approval. "+
			"Spell flags as --name value (or --name=value) and use only the documented flags.", seg.sub, a.unknown[0])
	}
	if len(a.positional) != 1 {
		return templatePreview{}, fmt.Sprintf("agentctl template %s: give exactly one template name.", seg.sub)
	}
	if a.positional[0].expands {
		return templatePreview{}, fmt.Sprintf("agentctl template %s: the template name uses shell expansion; pass it literally.", seg.sub)
	}
	for f, v := range a.values {
		if v.expands {
			return templatePreview{}, fmt.Sprintf("agentctl template %s: --%s uses shell expansion ($, backticks or backslashes), "+
				"so the approval popup cannot show what would be saved. Pass a literal value, or --prompt-file <path>.", seg.sub, f)
		}
	}
	name := a.positional[0].text
	scope := prompttemplate.ScopeGlobal
	if a.project {
		scope = prompttemplate.ScopeProject
	}
	path, err := prompttemplate.Path(name, scope, cwd)
	if err != nil {
		return templatePreview{}, fmt.Sprintf("agentctl template %s: %v.", seg.sub, err)
	}
	p := templatePreview{action: seg.sub, name: name, path: path}
	switch seg.sub {
	case "add":
		return buildTemplateAdd(p, a, cwd)
	case "edit":
		return buildTemplateEdit(p, a, scope, cwd)
	}
	old, err := templateLoadScope(name, scope, cwd)
	if err != nil {
		return templatePreview{}, fmt.Sprintf("agentctl template rm: %v.", err)
	}
	p.agent = old.Agent
	p.body = templateBody(p, "Removes this template.", old)
	return p, ""
}

// templatePromptFrom returns the prompt a --prompt or --prompt-file sets,
// read now against cwd, as the CLI does; ok is false when neither is given.
func templatePromptFrom(a templateArgs, sub, cwd string) (prompt string, ok bool, deny string) {
	if v, has := a.values["prompt-file"]; has {
		if _, both := a.values["prompt"]; both {
			return "", false, fmt.Sprintf("agentctl template %s: --prompt and --prompt-file are mutually exclusive.", sub)
		}
		path := paths.ExpandHome(v.text)
		if !filepath.IsAbs(path) && cwd != "" {
			path = filepath.Join(cwd, path)
		}
		// Only a regular file reads the same in the hook and in the CLI
		// later: /dev/stdin, /dev/fd/N or a FIFO would show the hook's
		// (drained) stdin, or hang, while the CLI saves the redirected
		// content. os.Stat follows symlinks (/dev/stdin is one).
		st, err := os.Stat(path)
		if err != nil {
			return "", false, fmt.Sprintf("agentctl template %s: cannot read --prompt-file %s (%v), so there is no prompt to show for approval.", sub, v.text, err)
		}
		if !st.Mode().IsRegular() {
			return "", false, fmt.Sprintf("agentctl template %s: --prompt-file %s is not a regular file (stdin, a pipe or a device), "+
				"so the approval popup cannot show what would be saved. Write the prompt to a file first.", sub, v.text)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", false, fmt.Sprintf("agentctl template %s: cannot read --prompt-file %s (%v), so there is no prompt to show for approval.", sub, v.text, err)
		}
		if strings.TrimSpace(string(data)) == "" {
			return "", false, fmt.Sprintf("agentctl template %s: --prompt-file %s is empty.", sub, v.text)
		}
		return string(data), true, ""
	}
	if v, has := a.values["prompt"]; has {
		return v.text, true, ""
	}
	return "", false, ""
}

// editorDeny is the deny reason for an add/edit that would open $EDITOR.
func editorDeny(sub string) string {
	return fmt.Sprintf("agentctl template %s: with no --prompt or field flags it opens $EDITOR, "+
		"which cannot be shown for approval. Pass --prompt '<text>' or --prompt-file <path>.", sub)
}

func buildTemplateAdd(p templatePreview, a templateArgs, cwd string) (templatePreview, string) {
	prompt, ok, deny := templatePromptFrom(a, "add", cwd)
	if deny != "" {
		return templatePreview{}, deny
	}
	if !ok {
		return templatePreview{}, editorDeny("add")
	}
	t := prompttemplate.Template{
		Name: p.name, Description: a.values["description"].text, Agent: a.values["agent"].text,
		Model: a.values["model"].text, Dir: a.values["dir"].text, Effort: a.values["effort"].text, Prompt: prompt,
	}
	note := "New template."
	if _, err := os.Stat(p.path); err == nil {
		note = "A template already exists here; add will refuse to replace it."
	}
	p.agent = t.Agent
	p.body = templateBody(p, note, t)
	return p, ""
}

func buildTemplateEdit(p templatePreview, a templateArgs, scope prompttemplate.Scope, cwd string) (templatePreview, string) {
	old, err := templateLoadScope(p.name, scope, cwd)
	if err != nil {
		return templatePreview{}, fmt.Sprintf("agentctl template edit: %v.", err)
	}
	t := old
	var changed []string
	for _, f := range []struct {
		flag string
		dst  *string
	}{
		{"description", &t.Description}, {"agent", &t.Agent}, {"model", &t.Model},
		{"dir", &t.Dir}, {"effort", &t.Effort},
	} {
		if v, has := a.values[f.flag]; has {
			if *f.dst != v.text {
				changed = append(changed, f.flag)
			}
			*f.dst = v.text
		}
	}
	prompt, ok, deny := templatePromptFrom(a, "edit", cwd)
	if deny != "" {
		return templatePreview{}, deny
	}
	if ok {
		if t.Prompt != strings.TrimRight(prompt, "\n") {
			changed = append(changed, "prompt")
		}
		t.Prompt = prompt
	}
	if len(a.values) == 0 {
		return templatePreview{}, editorDeny("edit")
	}
	note := "Edit changes nothing."
	if len(changed) > 0 {
		note = "Edit changes: " + strings.Join(changed, ", ") + "."
	}
	p.agent = t.Agent
	p.body = templateBody(p, note, t)
	return p, ""
}

// templateBody renders the popup viewport: a note, where the file is, and
// the file content as it will be (or is, for rm), verbatim.
func templateBody(p templatePreview, note string, t prompttemplate.Template) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**%s**\n\n", note)
	fmt.Fprintf(&b, "- **Template:** %s\n- **File:** %s\n", p.name, p.path)
	if t.Agent != "" {
		fmt.Fprintf(&b, "- **Agent:** %s\n", t.Agent)
	}
	b.WriteString("\n---\n\n````markdown\n")
	b.Write(prompttemplate.Render(t))
	b.WriteString("````\n")
	return b.String()
}

// templatePreviewGate shows the approval popup for a gated `agentctl
// template` command. Its contract is cronPreviewGate's.
func templatePreviewGate(agentName, command, cwd string) (approved, decided bool, denyReason string) {
	if os.Getenv("TMUX") == "" {
		logging.Log("hook-validate-bash: not in tmux session, passing through template")
		return false, false, ""
	}
	release, ok := acquireGateLock(command)
	if !ok {
		logging.Log("hook-validate-bash: template gate already owned by another invocation, staying silent")
		return false, false, ""
	}
	defer release()

	p, deny := buildTemplatePreview(command, askingCwd(cwd))
	if deny != "" {
		return false, false, deny
	}

	contentWidth, vpHeight := dispatchPreviewFit()
	target := resolvePopupTarget()
	args := DispatchPreviewArgs{
		AgentName:      target.label(agentName),
		TargetAgent:    p.agent,
		Prompt:         p.body,
		Command:        command,
		Kind:           PreviewKindTemplate,
		TemplateAction: p.action,
		TemplateName:   p.name,
		TemplatePath:   dispatchplan.AbbrevHome(p.path),
		ContentWidth:   contentWidth,
		VPHeight:       vpHeight,
	}
	tmpPath, err := writePreviewArgs("agentctl-template-preview-*", args)
	if err != nil {
		logging.Log("hook-validate-bash: template preview args: %v", err)
		return false, false, ""
	}
	defer os.Remove(tmpPath)

	sz := clampPopupToClient(tui.DispatchPreviewMaxPopupSize(
		tui.WithContentWidth(contentWidth),
		tui.WithMaxViewportHeight(vpHeight),
	))
	logging.Log("hook-validate-bash: launching template popup agent=%s action=%s template=%s", agentName, p.action, p.name)
	return runPopupWithDeadline(target, "prompt template", "hook-validate-bash: template", sz.Width, sz.Height, tmpPath, templatePopupNotice(agentName, command, p))
}

// templatePopupNotice builds the pending-approval description. The phone
// gets it as a template approval with the template content as its prompt.
func templatePopupNotice(agentName, command string, p templatePreview) popupNotice {
	verb := map[string]string{"add": "add", "edit": "edit", "rm": "remove"}[p.action]
	return popupNotice{
		Agent:  agentName,
		Tool:   "template",
		Detail: textutil.Truncate(verb+" prompt template "+p.name, 120, "..."),
		Action: verb + " prompt template " + p.name,
		Remote: &remoteDetails{
			Kind:        remoteapproval.KindTemplate,
			AskingAgent: agentName,
			TargetAgent: p.agent,
			Prompt:      p.body,
			Command:     command,
			Details:     remoteapproval.Details{Template: p.name, Mode: verb + " template", Agent: p.agent},
		},
	}
}
