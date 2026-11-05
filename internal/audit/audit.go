// Package audit is the hook-sourced audit trail for agentctl. It replaces
// the old shell-sidecar writer (fired pre-eval from shell-prefix.sh, Bash
// only, no outcome) with records appended directly from the PostToolUse and
// PostToolUseFailure hook handlers in internal/hooks, which see every tool
// Claude Code invokes -- Bash, Read, Edit, Write, WebFetch, Agent, etc. --
// and know whether it succeeded. It also records what was REFUSED: the two
// PreToolUse deny emitters in internal/hooks/validate.go (denyCommand,
// denyBashPopup) append an EventToolDenied record before they exit(2), since
// a hook that blocks via exit 2 never reaches PostToolUse and would
// otherwise leave the highest-signal event in a security audit log --
// "this was refused" -- unrecorded.
//
// Records are written as JSONL to <runtimeDir>/audit/<session-id>.jsonl (see
// paths.AuditDir), one file per Claude Code session, so the file layout is
// unchanged from the old writer. The schema is not: every record here carries
// a "v" schema version field, so a reader can skip pre-existing lines from
// the old writer (which never had one) rather than erroring on them.
package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/safefs"
)

// SchemaVersion is the current audit record schema version.
//
// v2 adds Project/RepoRoot. That is an additive, omitempty field -- it breaks
// no existing reader -- but the version bump still matters: it is the only
// way a reader can tell "no project because this record predates the field"
// (v1) apart from "no project because resolution failed at write time" (v2
// with Project empty). Those are genuinely different conditions. Readers must
// keep accepting v1 records without error; see readRecordsFile in query.go.
//
// v3 adds EventToolDenied and Record.DeniedBy. hook-validate-bash denies at
// PreToolUse by exiting 2, which means PostToolUse never fires and (before
// this version) no audit record was ever written for a refused command --
// the highest-signal event a security audit log can record was systematically
// dropped. DeniedBy is additive and omitempty like Project/RepoRoot before
// it, so the version bump exists for the same reason: it lets a reader tell
// "this record predates denial tracking" (v1/v2) apart from "this is an v3
// non-denial record with DeniedBy legitimately empty". Readers must keep
// accepting v1 and v2 records without error.
const SchemaVersion = 3

// Event names.
//
// EventTool and EventToolFailure are written from PostToolUse/
// PostToolUseFailure and cover every tool Claude Code invokes. EventToolDenied
// is written from the two PreToolUse deny emitters in internal/hooks/
// validate.go (denyCommand, denyBashPopup) for a command refused before it
// ever ran -- see Record.DeniedBy to distinguish a rule-based policy block
// from a human popup refusal. "prompt", "session_start", and "session_end"
// remain explicitly out of scope (separate increments).
const (
	EventTool        = "tool"
	EventToolFailure = "tool_failure"
	EventToolDenied  = "tool_denied"
)

// Size limits. Now that the log stores tool output (not just command
// strings), it needs discipline: input/result are capped individually, and
// the whole marshaled line is capped so a single oversized record can't blow
// up the file or a reader that loads a line into memory.
//
// Input and Result are capped asymmetrically. Measured on the live corpus,
// 255 of 4740 records (5.4%) had their Input replaced by a 200-byte
// truncation head under the old single 4KB cap -- for a security log the
// input is the evidence (the exact command/args that ran or were denied) and
// the result is comparatively noise, so Input gets a much larger allowance.
const (
	maxInputBytes  = 16 * 1024 // per-field cap for Input -- the evidence
	maxResultBytes = 4 * 1024  // per-field cap for Result -- unchanged from v2
	maxLineBytes   = 32 * 1024 // cap for the whole marshaled JSONL line -- raised to fit the larger Input
)

// Record is one line of the audit trail. Every exported field maps 1:1 to a
// JSON tag; field order here matches the canonical field list in the spec.
type Record struct {
	SchemaVersion int    `json:"v"`
	Timestamp     string `json:"ts"` // RFC3339Nano -- sub-second precision so intra-second ordering is recoverable
	Event         string `json:"event"`

	Tool      string `json:"tool,omitempty"`
	ToolUseID string `json:"tool_use_id,omitempty"`

	Session    string `json:"session,omitempty"`
	PromptID   string `json:"prompt_id,omitempty"`
	Transcript string `json:"transcript,omitempty"`

	Agent    string `json:"agent,omitempty"`
	Instance string `json:"instance,omitempty"`
	Window   string `json:"window,omitempty"`
	User     string `json:"user,omitempty"`
	Cwd      string `json:"cwd,omitempty"`

	// Project and RepoRoot identify the project this record belongs to,
	// independent of the file's location on disk. Populate these from
	// paths.ProjectDir()/paths.DirName() at the call site, NEVER from Cwd --
	// Cwd is correct for "where the tool ran" but wrong for "which project
	// owns this record" when the tool ran inside a worktree dispatch
	// (<repo>/.worktrees/<slug>), which is exactly the case attribution
	// matters most for. Both are "" (omitempty) when paths was never
	// initialized; that is a real, distinct condition, not something to
	// paper over with a placeholder.
	Project  string `json:"project,omitempty"`   // project directory basename, e.g. "agentctl"
	RepoRoot string `json:"repo_root,omitempty"` // absolute project root

	PermissionMode string `json:"permission_mode,omitempty"`
	DurationMS     int64  `json:"duration_ms,omitempty"`

	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Summary string `json:"summary,omitempty"`

	// DeniedBy distinguishes the two PreToolUse deny emitters for an
	// EventToolDenied record: "rule" for a policy-based deny (global,
	// per-agent, or project deny list -- denyCommand) and "human" for a
	// popup refusal (denyBashPopup). Empty for every other event -- a
	// reviewer must be able to tell a policy block from a human refusal
	// apart, since they mean very different things.
	DeniedBy string `json:"denied_by,omitempty"`

	Input  json.RawMessage `json:"input,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}

// Append writes rec as a single JSON line to
// <runtimeDir>/audit/<rec.Session>.jsonl (or ".../unknown.jsonl" if
// rec.Session is empty), creating the directory and file as needed.
//
// Append fills in SchemaVersion and Timestamp when they are zero, and
// enforces the size caps documented on Record before writing. Callers in
// internal/hooks must treat any returned error as non-fatal -- an audit
// failure must never block or fail a tool call.
func Append(rec Record) error {
	runtimeDir := paths.RuntimeDir()
	if runtimeDir == "" {
		return fmt.Errorf("audit: paths not initialized (empty runtime dir)")
	}

	if rec.SchemaVersion == 0 {
		rec.SchemaVersion = SchemaVersion
	}
	if rec.Timestamp == "" {
		rec.Timestamp = time.Now().Format(time.RFC3339Nano)
	}

	line, err := marshalCapped(rec)
	if err != nil {
		return fmt.Errorf("audit: marshal record: %w", err)
	}

	sessionFile := rec.Session
	if sessionFile == "" {
		sessionFile = "unknown"
	}
	auditDir := paths.AuditDir()
	if auditDir == "" {
		auditDir = filepath.Join(runtimeDir, "audit")
	}
	auditPath := filepath.Join(auditDir, sessionFile+".jsonl")

	// 0600, not 0644: this file now contains command output, not just command
	// strings. The audit dir sits in the runtime dir inside the user's
	// repository, which a `git pull` can turn into a symlink mid-session, so
	// the open refuses a symlink anywhere from the runtime dir down
	// (missing dirs are created 0700).
	f, err := safefs.OpenNoFollow(runtimeDir, auditPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("audit: open %s: %w", auditPath, err)
	}
	defer f.Close() //nolint:errcheck

	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("audit: write %s: %w", auditPath, err)
	}
	return nil
}

// marshalCapped truncates rec's Input/Result to maxFieldBytes each, then
// marshals rec. If the resulting line still exceeds maxLineBytes (large
// Summary/Error strings, pathological field values, etc.) it shrinks the
// truncation markers and free-text fields further and re-marshals. As an
// absolute last resort it hard-truncates the marshaled bytes -- this
// produces invalid JSON, but only in a case that should never happen given
// the caps above, and a corrupt final line is preferable to an unbounded
// one.
func marshalCapped(rec Record) ([]byte, error) {
	rec.Input = truncateJSON(rec.Input, maxInputBytes)
	rec.Result = truncateJSON(rec.Result, maxResultBytes)

	line, err := json.Marshal(rec)
	if err != nil {
		return nil, err
	}
	if len(line) <= maxLineBytes {
		return line, nil
	}

	rec.Input = truncateJSON(rec.Input, 256)
	rec.Result = truncateJSON(rec.Result, 256)
	rec.Summary = truncateString(rec.Summary, 512)
	rec.Error = truncateString(rec.Error, 512)

	line, err = json.Marshal(rec)
	if err != nil {
		return nil, err
	}
	if len(line) > maxLineBytes {
		line = line[:maxLineBytes]
	}
	return line, nil
}

// truncatedMarker is substituted for an oversized Input or Result value.
type truncatedMarker struct {
	Truncated     bool   `json:"truncated"`
	OriginalBytes int    `json:"original_bytes"`
	Head          string `json:"head"`
}

// truncateJSON returns raw unchanged if it already fits within limit bytes.
// Otherwise it returns a marker object recording that the value was
// truncated, its original byte count, and a short head excerpt -- the full
// body is recoverable from the transcript (see Record.Transcript) while it
// still exists.
func truncateJSON(raw json.RawMessage, limit int) json.RawMessage {
	if len(raw) <= limit {
		return raw
	}
	headLen := 200
	if headLen > len(raw) {
		headLen = len(raw)
	}
	marker := truncatedMarker{
		Truncated:     true,
		OriginalBytes: len(raw),
		Head:          string(raw[:headLen]),
	}
	out, err := json.Marshal(marker)
	if err != nil {
		// truncatedMarker's shape can't fail to marshal; this is unreachable
		// in practice, but fall back to a minimal valid marker rather than
		// propagating an error from what is meant to be a safety net.
		return json.RawMessage(`{"truncated":true}`)
	}
	return out
}

// truncateString caps a plain string field to limit bytes, appending a
// marker suffix when truncated.
func truncateString(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…(truncated)"
}
