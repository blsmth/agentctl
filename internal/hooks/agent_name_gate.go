package hooks

import (
	"path/filepath"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/paths"
)

// agentNameCheck reports an error when name is not an agent a dispatch or
// cron job from dir can launch. nil disables the check. internal/dispatch
// imports this package, so main wires dispatch.ValidateAgentName in through
// SetAgentNameCheck (a discovery failure must be reported as nil there: the
// gate then falls through to the popup).
var agentNameCheck func(name, dir string) error

// SetAgentNameCheck installs the agent-name validator the dispatch and cron
// gates use to deny an unknown --agent before any popup is shown.
func SetAgentNameCheck(fn func(name, dir string) error) { agentNameCheck = fn }

// unknownAgentDeny returns the deny reason when an explicit --agent token
// names an agent the CLI would reject, else "". It is an extra deny only: a
// missing, shell-expanded or unverifiable name (or directory) yields "" and
// the existing gate runs. dirTok is the --dir flag ("" token when absent);
// noDirOK says whether a missing --dir means cwd (dispatch, cron add).
func unknownAgentDeny(agentTok, dirTok cmdToken, cwd string, noDirOK, changesContext bool) string {
	if agentNameCheck == nil || agentTok.text == "" || agentTok.expands || dirTok.expands {
		return ""
	}
	dir := cwd
	if dirTok.text != "" {
		dir = paths.ExpandHome(dirTok.text)
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(cwd, dir)
		}
	} else if !noDirOK {
		return ""
	}
	if changesContext {
		return ""
	}
	if err := agentNameCheck(agentTok.text, dir); err != nil {
		logging.Log("hook-validate-bash: denying unknown agent %q: %v", agentTok.text, err)
		return err.Error()
	}
	return ""
}

// dispatchUnknownAgentDeny is unknownAgentDeny for the gated dispatch in
// command. The last --agent/-a and --dir win, as in the CLI.
func dispatchUnknownAgentDeny(command, cwd string) string {
	var agentTok, dirTok cmdToken
	seg := gatedDispatchSegment(command)
	for i := 0; i < len(seg); i++ {
		t := seg[i]
		name, val, hasEq := t.text, cmdToken{}, false
		if n, v, ok := cutFlag(t.text); ok {
			name, val, hasEq = n, cmdToken{text: v, quoted: t.quoted, expands: t.expands}, true
		}
		if name == "--mode" {
			return "" // improve mode supplies its own agent
		}
		isAgent, isDir := name == "--agent" || name == "-a", name == "--dir"
		if !hasEq && (isDispatchValueFlag(name)) && i+1 < len(seg) {
			val = seg[i+1]
			i++
		} else if !hasEq {
			continue
		}
		switch {
		case isAgent:
			agentTok = val
		case isDir:
			dirTok = val
		}
	}
	return unknownAgentDeny(agentTok, dirTok, cwd, true, lineChangesContextFor(command, "dispatch"))
}

// cronUnknownAgentDeny is unknownAgentDeny for a cron add or edit. An edit
// without --dir keeps the stored job's dir, which is not read here.
func cronUnknownAgentDeny(command, cwd string) string {
	segs := gatedCronSegments(command)
	if len(segs) != 1 || (segs[0].sub != "add" && segs[0].sub != "edit") {
		return ""
	}
	a := segs[0].parse()
	if len(a.unknown) > 0 || a.improve || a.value("mode") != "" {
		return ""
	}
	return unknownAgentDeny(a.values["agent"], a.values["dir"], cwd, segs[0].sub == "add",
		lineChangesContextFor(command, "cron"))
}

// cutFlag splits "--name=value" into its parts.
func cutFlag(tok string) (name, value string, ok bool) {
	if len(tok) < 3 || tok[0] != '-' || tok[1] != '-' {
		return "", "", false
	}
	for i := 2; i < len(tok); i++ {
		if tok[i] == '=' {
			return tok[:i], tok[i+1:], true
		}
	}
	return "", "", false
}
