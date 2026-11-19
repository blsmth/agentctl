package hooks

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/dispatchapproval"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/paths"
)

// The dispatch_approval "non-review" exemption trusts the NAME code-reviewer,
// but discovery lets a project-local .claude/agents/code-reviewer.md shadow
// the global one, and this hook cannot see the directory the CLI will run in
// (a `cd` earlier in the same command). So enforcement lives in the CLI
// (internal/cmd/dispatch_review.go): a review whose reviewer is not the
// global file launches from an agent session only with a dispatchapproval
// marker, which this hook records when a human approves the popup. The
// checks here only make the popup show in the common cases, so an agent is
// refused less often.

// recordApproval is the seam ValidateBash records an approved dispatch
// through; tests swap it.
var recordApproval = dispatchapproval.Record

// recordDispatchApprovals records one approval marker per dispatch
// invocation in command, for the CLI to consume. A segment whose words the
// shell would expand cannot be keyed the way the CLI will see it, so it is
// skipped (the CLI then refuses it if it needs an approval).
func recordDispatchApprovals(windowID, command string) {
	if windowID == "" {
		logging.Log("hook-validate-bash: approved dispatch has no window; no approval marker recorded")
		return
	}
	for _, seg := range dispatchSegments(command) {
		if segmentHasHelp(seg) {
			continue
		}
		args, ok := segmentArgs(seg)
		if !ok {
			logging.Log("hook-validate-bash: approved dispatch has shell-expanded words; no approval marker recorded")
			continue
		}
		if err := recordApproval(windowID, args); err != nil {
			logging.Warn("hook-validate-bash: recording dispatch approval: %v", err)
		}
	}
}

// segmentArgs returns a dispatch segment's words as the dispatch CLI will
// receive them, or false when any word expands.
func segmentArgs(seg []cmdToken) ([]string, bool) {
	args := make([]string, 0, len(seg))
	for _, t := range seg {
		if t.expands || (!t.quoted && strings.ContainsAny(t.text, "*?[")) {
			return nil, false
		}
		args = append(args, t.text)
	}
	return args, true
}

// reviewerGlobalFrom reports whether the review agent resolves to its global
// definition from dir, as the CLI's discovery would. A var for tests.
var reviewerGlobalFrom = func(cfg *config.Config, dir string) bool {
	res := agent.ResolvesGlobal(cfg, config.DefaultReviewAgent, dir)
	if !res.Global {
		logging.Log("hook-validate-bash: %s from %s is not the global definition: %s", config.DefaultReviewAgent, dir, res.Reason)
	}
	return res.Global
}

// reviewExemptionTrusted reports whether a command that dispatchNeedsApproval
// exempted under "non-review" can keep the exemption: the line runs in the
// directory and environment this hook sees (lineChangesContext is false) and
// from cwd the reviewer resolves to the global definition. When false the
// dispatch is gated, so the human sees it and the CLI finds an approval.
func reviewExemptionTrusted(cfg *config.Config, command, cwd string) bool {
	if lineChangesContext(command) {
		logging.Log("hook-validate-bash: review dispatch line changes directory or environment; gating")
		return false
	}
	return reviewerGlobalFrom(cfg, cwd)
}

// contextCommands change the directory or environment the rest of the line
// runs in, or run a command line this scanner cannot see into.
var contextCommands = map[string]bool{
	"cd": true, "pushd": true, "popd": true,
	"env": true, "export": true, "unset": true, "declare": true, "typeset": true, "readonly": true, "local": true, "set": true,
	"source": true, ".": true, "eval": true, "exec": true, "command": true, "builtin": true,
	"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "fish": true,
	"sudo": true, "doas": true, "su": true, "xargs": true,
}

var assignmentRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\+?=`)

// lineChangesContext reports whether some simple command in the line could
// make a later agentctl dispatch run in another directory or environment: a
// command word in contextCommands, a leading variable assignment, a subshell
// or group, or a command word the shell expands. It is a scanner, not a
// shell parser; anything odd counts as a change (fail toward the popup).
func lineChangesContext(command string) bool {
	atCmd := true
	for _, t := range tokenizeQuoted(strings.TrimSpace(command)) {
		if t.quoted {
			if atCmd && (t.expands || isContextWord(t.text)) {
				return true
			}
			atCmd = false
			continue
		}
		if strings.ContainsAny(t.text, "(){}`") || t.expands && atCmd {
			return true
		}
		// Split on attached control operators ("x&&cd"): each piece after
		// an operator is a command word.
		for _, piece := range splitControl(t.text) {
			if piece == "" {
				atCmd = true
				continue
			}
			if atCmd && isContextWord(piece) {
				return true
			}
			if !isRedirection(piece) {
				atCmd = false
			}
		}
	}
	return false
}

// splitControl splits an unquoted word at the control operator bytes ; & |,
// returning "" in place of each operator run (a command boundary).
func splitControl(w string) []string {
	var out []string
	start := 0
	for i := 0; i < len(w); i++ {
		if strings.IndexByte(";&|", w[i]) < 0 {
			continue
		}
		if i > start {
			out = append(out, w[start:i])
		}
		for i+1 < len(w) && strings.IndexByte(";&|", w[i+1]) >= 0 {
			i++
		}
		out = append(out, "")
		start = i + 1
	}
	if start < len(w) {
		out = append(out, w[start:])
	}
	return out
}

func isContextWord(w string) bool {
	return contextCommands[filepath.Base(w)] || assignmentRE.MatchString(w)
}

// approvalTamperReason is the deny reason for commands that write the
// dispatch approval markers directly.
const approvalTamperReason = "\U0001F6D1 BLOCKED: this writes agentctl's dispatch approval records directly. " +
	"Those record a human's approval in the dispatch popup; run the dispatch as its own Bash call to get one."

// approvalTargetPattern matches any reference to the marker directory: its
// name is distinctive enough to match anywhere.
var approvalTargetPattern = regexp.QuoteMeta(dispatchapproval.DirName) + `\b`

// approvalStateDir is the resolved marker directory. A var so tests can pin
// it.
var approvalStateDir = func() string {
	dir, err := dispatchapproval.Dir()
	if err != nil {
		return ""
	}
	return dir
}

// checkApprovalTamper reports whether command writes the dispatch approval
// markers: a redirect, write verb, in-place edit or interpreter naming the
// marker directory (the cron tamper templates). Like the cron rule it is a
// speed bump, not a sandbox.
func checkApprovalTamper(command string) string {
	dir := approvalStateDir()
	if !strings.Contains(command, dispatchapproval.DirName) && (dir == "" || !strings.Contains(command, dir)) {
		return ""
	}
	targets := []string{approvalTargetPattern}
	if dir != "" {
		targets = append(targets, regexp.QuoteMeta(dir)+`\b`)
	}
	if matchesWriteTemplates(maskAgentctlArgs(command), targets) {
		return approvalTamperReason
	}
	return ""
}

// approvalEditTarget returns the path a file-editing tool call would write
// when it is inside the dispatch approval marker directory (by resolved
// path, or any path with a component of that name), else "".
func approvalEditTarget(toolName string, rawInput json.RawMessage) string {
	if !fileEditTools[toolName] || len(rawInput) == 0 {
		return ""
	}
	var in struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
	}
	if err := json.Unmarshal(rawInput, &in); err != nil {
		return ""
	}
	p := in.FilePath
	if p == "" {
		p = in.NotebookPath
	}
	if p == "" {
		return ""
	}
	p = filepath.Clean(paths.ExpandHome(p))
	for _, part := range strings.Split(p, string(filepath.Separator)) {
		if part == dispatchapproval.DirName {
			return p
		}
	}
	if dir := approvalStateDir(); dir != "" {
		dir = filepath.Clean(dir)
		if p == dir || strings.HasPrefix(p, dir+string(filepath.Separator)) {
			return p
		}
		if real, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
			if realDir, err := filepath.EvalSymlinks(dir); err == nil &&
				(real == realDir || strings.HasPrefix(real, realDir+string(filepath.Separator))) {
				return p
			}
		}
	}
	return ""
}

// approvalEditDenyReason is shown to the model when it tries to write the
// approval markers with a file-editing tool.
func approvalEditDenyReason(path string) string {
	return fmt.Sprintf("\U0001F6D1 BLOCKED: %s is agentctl's record of dispatches a human approved. "+
		"Run the dispatch as its own Bash call to get the approval popup.", path)
}
