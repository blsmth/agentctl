package cronedit

import (
	"fmt"
	"strings"

	"github.com/blsmth/agentctl/internal/cron"
)

// FieldChange is one field of an edit as old → new. Values are raw: "" means
// unset (the default applies), and a UI names the resolved default itself.
type FieldChange struct {
	Field string `json:"field"`
	Old   string `json:"old"`
	New   string `json:"new"`
}

// Diff lists the fields that differ between the stored job and the edited
// one, in form order, with their values as text.
func Diff(old, edited cron.Job) []FieldChange {
	old, edited = normalize(old), normalize(edited)
	var out []FieldChange
	for _, f := range ChangedFields(old, edited) {
		out = append(out, FieldChange{Field: f, Old: FieldText(old, f), New: FieldText(edited, f)})
	}
	return out
}

// FieldText is a job field as plain text: the JSON value for strings, "on"
// or "off" for worktree, "enabled" or "paused" for enabled, "5h 80% 7d 90%"
// for the usage gate. Unset is "".
func FieldText(j cron.Job, field string) string {
	switch field {
	case cron.FieldName:
		return j.Name
	case cron.FieldSchedule:
		return j.Schedule
	case cron.FieldAt:
		if j.IsOnce() {
			return cron.OnceSummary(j)
		}
		return ""
	case "state":
		return j.State
	case "result":
		return j.Result
	case cron.FieldDir:
		return j.Dir
	case cron.FieldAgent:
		return j.Agent
	case cron.FieldPrompt:
		return j.Prompt
	case "prompt_key":
		return j.PromptKey
	case cron.FieldTemplate:
		return j.Template
	case cron.FieldHarness:
		return j.Harness
	case cron.FieldMode:
		return j.Mode
	case cron.FieldModel:
		return j.Model
	case cron.FieldEffort:
		return j.Effort
	case cron.FieldBackend:
		return j.Backend
	case cron.FieldAccount:
		return j.Account
	case cron.FieldTimeout:
		return j.Timeout
	case cron.FieldOverlap:
		return j.Overlap
	case cron.FieldCatchup:
		return j.Catchup
	case "worktree":
		if j.UseWorktree() {
			return "on"
		}
		return "off"
	case "enabled":
		if j.IsEnabled() {
			return "enabled"
		}
		return "paused"
	case cron.FieldSkipIfUsageAbove:
		t := j.SkipIfUsageAbove
		if t == nil {
			return ""
		}
		var p []string
		if t.FiveHour > 0 {
			p = append(p, fmt.Sprintf("5h %g%%", t.FiveHour))
		}
		if t.SevenDay > 0 {
			p = append(p, fmt.Sprintf("7d %g%%", t.SevenDay))
		}
		return strings.Join(p, " ")
	}
	return ""
}
