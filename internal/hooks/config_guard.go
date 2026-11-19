package hooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/paths"
)

// The dispatch_approval guard keeps agents from loosening the global
// dispatch_approval setting themselves (setting it to "off" would grant them
// unattended dispatch). Orchestrators legitimately edit the global config for
// other keys, so the Write/Edit side only denies an edit whose result is less
// strict than the file today; the Bash side denies writes to the file
// outright, like the cron tamper rules. The project-local
// .agentctl/config.json needs no guard: a project may only tighten the
// setting (config.sanitizeProjectConfig). Like the cron and serve rules this
// is a speed bump, not a sandbox: symlinks are not resolved and a program
// written to disk can still write the file.

// configTamperReason is the deny reason for a change to dispatch_approval in
// the global config, or a Bash write to that file.
const configTamperReason = "\U0001F6D1 BLOCKED: dispatch_approval in the global agentctl config is human-only: " +
	"agents may not loosen it or write that file from the shell. " +
	"Edit other keys with the Edit tool (changes that keep or tighten dispatch_approval are allowed), " +
	"read it with the Read tool, and ask the human to change dispatch_approval."

// globalConfigFile is the global config file the guard protects. Seam for
// tests.
var globalConfigFile = config.GlobalFilePath

// readGlobalConfig reads the protected file's current content. Seam for
// tests.
var readGlobalConfig = os.ReadFile

// configTargetPattern matches a reference to the global config file in the
// default layout; the resolved path is matched literally on top. The project
// file (.agentctl/config.json) does not match.
const configTargetPattern = `(?:\.config|XDG_CONFIG_HOME\}?)/agentctl/config\.json\b`

// configDirPattern matches a reference to the global config directory.
const configDirPattern = `(?:\.config|XDG_CONFIG_HOME\}?)/agentctl\b`

// checkConfigTamper reports whether command writes the global config file
// (the same write shapes checkCronTamper denies: redirects, write commands,
// in-place sed/perl, interpreters naming it), or mentions dispatch_approval
// together with the file or its directory. Reads (cat, grep, jq without a
// redirect into it) pass. Arguments to agentctl are ignored
// (maskAgentctlArgs).
func checkConfigTamper(command string) string {
	if !strings.Contains(command, "config.json") && !strings.Contains(command, "dispatch_approval") {
		return ""
	}
	command = maskAgentctlArgs(command)
	files, dirs := []string{configTargetPattern}, []string{configDirPattern}
	if p, err := globalConfigFile(); err == nil && p != "" {
		files = append(files, homeRelativeAlternatives(p)...)
		dirs = append(dirs, homeRelativeAlternatives(filepath.Dir(p))...)
	}
	file := "(?:" + strings.Join(files, "|") + ")"
	for _, tmpl := range cronTamperTemplates {
		re, err := regexp.Compile(fmt.Sprintf(tmpl, file))
		if err != nil {
			logging.Log("hook-validate-bash: bad config tamper pattern: %v", err)
			continue
		}
		if re.MatchString(command) {
			return configTamperReason
		}
	}
	if strings.Contains(command, "dispatch_approval") {
		re, err := regexp.Compile("(?:" + strings.Join(append(files, dirs...), "|") + ")")
		if err == nil && re.MatchString(command) {
			return configTamperReason
		}
	}
	return ""
}

// homeRelativeAlternatives returns regexp alternatives matching p literally,
// plus its ~, $HOME and ${HOME} forms when p is under the home directory.
func homeRelativeAlternatives(p string) []string {
	alts := []string{regexp.QuoteMeta(p) + `\b`}
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(p, home+"/") {
		rel := regexp.QuoteMeta(strings.TrimPrefix(p, home+"/"))
		alts = append(alts, `(?:~|\$HOME|\$\{HOME\})/`+rel+`\b`)
	}
	return alts
}

// configEditInput is the subset of Write/Edit/MultiEdit/NotebookEdit input
// the guard reads.
type configEditInput struct {
	FilePath     string `json:"file_path"`
	NotebookPath string `json:"notebook_path"`
	Content      string `json:"content"`
	editOp
	Edits []editOp `json:"edits"`
}

type editOp struct {
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

// globalConfigEditTarget returns the global config path when a file-editing
// tool call would loosen its dispatch_approval, else "". The result of the
// edit is computed against the current file and compared by
// config.DispatchApprovalStrictness (unset = non-review, unknown = all).
// When the result does not parse, the edit is denied if its text mentions
// dispatch_approval, and otherwise judged as unset (that is how the loader
// reads a broken file).
func globalConfigEditTarget(toolName string, rawInput json.RawMessage) string {
	if !fileEditTools[toolName] || len(rawInput) == 0 {
		return ""
	}
	var in configEditInput
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
	target, err := globalConfigFile()
	if err != nil || target == "" || p != filepath.Clean(target) {
		return ""
	}

	current, err := readGlobalConfig(p)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		logging.Log("hook-pre-tool: reading %s: %v", p, err)
	}
	// An unreadable or unparseable current file loads as an empty config,
	// so it is effectively unset.
	before, _ := config.ParseDispatchApproval(current)

	var result string
	switch toolName {
	case "Write":
		result = in.Content
	case "Edit":
		result = applyEdit(string(current), in.editOp)
	case "MultiEdit":
		result = string(current)
		for _, op := range in.Edits {
			result = applyEdit(result, op)
		}
	default:
		// A notebook edit of a JSON config file is not a real use.
		return p
	}

	after, err := config.ParseDispatchApproval([]byte(result))
	if err != nil {
		if strings.Contains(result, "dispatch_approval") {
			return p
		}
		// The loader reads an unparseable file as an empty config, so the
		// result is effectively unset: still no looser than today.
		after = ""
	}
	if config.DispatchApprovalStrictness(after) < config.DispatchApprovalStrictness(before) {
		return p
	}
	return ""
}

// applyEdit applies an Edit tool operation the way the tool does: replace
// the first occurrence of OldString, or every one with ReplaceAll. A missing
// OldString leaves the content unchanged (the tool call fails).
func applyEdit(content string, op editOp) string {
	if op.OldString == "" {
		return content
	}
	if op.ReplaceAll {
		return strings.ReplaceAll(content, op.OldString, op.NewString)
	}
	return strings.Replace(content, op.OldString, op.NewString, 1)
}
