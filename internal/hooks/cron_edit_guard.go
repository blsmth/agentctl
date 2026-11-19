package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/blsmth/agentctl/internal/audit"
	"github.com/blsmth/agentctl/internal/cron"
	"github.com/blsmth/agentctl/internal/harness"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/paths"
)

// fileEditTools are the tools that write a file named in their input. Read,
// Glob and Grep also carry a path and must stay allowed.
var fileEditTools = map[string]bool{"Write": true, "Edit": true, "MultiEdit": true, "NotebookEdit": true}

// cronJobsPath is the jobs file the edit guard protects. Seam for tests.
var cronJobsPath = cron.JobsPath

// cronEditTarget returns the protected cron path a file-editing tool call
// would write ("" when it is not one): the jobs file, or anything under the
// cron state dir. It is the Edit/Write counterpart of checkCronTamper, and
// the same speed bump: the jobs file is matched through symlinks and case
// (sameFilePath), the state dir by path only.
func cronEditTarget(toolName string, rawInput json.RawMessage) string {
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
	if jobs, err := cronJobsPath(); err == nil && jobs != "" && sameFilePath(p, filepath.Clean(jobs)) {
		return p
	}
	if dir := cronStateDir(); dir != "" {
		dir = filepath.Clean(dir)
		if p == dir || strings.HasPrefix(p, dir+string(filepath.Separator)) {
			return p
		}
	}
	return ""
}

// cronEditDenyReason is shown to the model when it tries to edit the cron
// jobs file or state directly.
func cronEditDenyReason(path string) string {
	return fmt.Sprintf("\U0001F6D1 BLOCKED: %s is agentctl's cron schedule or state. "+
		"Scheduled jobs need human approval: use `agentctl cron add` (shown for approval), "+
		"`agentctl cron pause|rm` to stop one, and `agentctl cron list` to read them.", path)
}

// denyFileEdit blocks a file-editing tool call from PreToolUse. Like
// denyCommand, exit 2 is the enforcement and the JSON is presentation; the
// audit record is best-effort. what names the protected file in the audit
// summary.
func denyFileEdit(toolName, path, what, reason string, rawInput json.RawMessage) {
	agentName := os.Getenv("AGENTCTL_AGENT_NAME")
	logging.Log("hook-pre-tool: DENIED agent=%s tool=%s path=%s", agentName, toolName, path)
	emitDecision(harness.Decision{Kind: harness.Deny, HookEvent: "PreToolUse", Reason: reason})
	appendAuditRecord(audit.Record{
		Event:    audit.EventToolDenied,
		Tool:     toolName,
		Agent:    agentName,
		Instance: os.Getenv("AGENTCTL_INSTANCE_NAME"),
		Window:   resolveHookWindowID(),
		OK:       false,
		Error:    reason,
		Summary:  truncateSummary(fmt.Sprintf("%s denied by rule: %s %s", toolName, what, path)),
		DeniedBy: deniedByRule,
		Input:    rawInput,
	})
	fmt.Fprint(os.Stderr, reason)
	exitFunc(2)
}
