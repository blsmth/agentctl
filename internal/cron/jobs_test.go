package cron

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
)

func validJob() Job {
	j := Job{Name: "triage", Schedule: "30 7 * * 1-5", Dir: "/tmp/proj", Agent: "brandon", Prompt: "triage"}
	j.Normalize()
	return j
}

func TestJobValidate(t *testing.T) {
	tests := []struct {
		name    string
		mut     func(j *Job)
		wantErr string
	}{
		{name: "valid", mut: func(*Job) {}},
		{name: "prompt key instead", mut: func(j *Job) { j.Prompt, j.PromptKey = "", "spec" }},
		{name: "bad name", mut: func(j *Job) { j.Name = "../x" }, wantErr: "invalid job name"},
		{name: "bad schedule", mut: func(j *Job) { j.Schedule = "nope" }, wantErr: "needs 5 fields, has 1"},
		{name: "relative dir", mut: func(j *Job) { j.Dir = "proj" }, wantErr: "absolute"},
		{name: "no name", mut: func(j *Job) { j.Name = "" }, wantErr: "give the job a name"},
		{name: "no agent", mut: func(j *Job) { j.Agent = "" }, wantErr: "pick an agent"},
		{name: "no prompt", mut: func(j *Job) { j.Prompt = "" }, wantErr: "write a prompt"},
		{name: "both prompts", mut: func(j *Job) { j.PromptKey = "k" }, wantErr: "not both"},
		{name: "template instead", mut: func(j *Job) { j.Prompt, j.Template = "", "triage" }},
		{name: "template supplies the agent", mut: func(j *Job) { j.Prompt, j.Template, j.Agent = "", "triage", "" }},
		{name: "template and prompt", mut: func(j *Job) { j.Template = "triage" }, wantErr: "template or a prompt"},
		{name: "template and prompt key", mut: func(j *Job) { j.Prompt, j.PromptKey, j.Template = "", "k", "t" }, wantErr: "template or a prompt"},
		{name: "bad template name", mut: func(j *Job) { j.Prompt, j.Template = "", "../x" }, wantErr: "invalid template"},
		{name: "other harness", mut: func(j *Job) { j.Harness = "codex" }, wantErr: "harness"},
		{name: "headless", mut: func(j *Job) { j.Mode = "headless" }, wantErr: "mode"},
		{name: "bad overlap", mut: func(j *Job) { j.Overlap = "queue" }, wantErr: "overlap"},
		{name: "bad catchup", mut: func(j *Job) { j.Catchup = "all" }, wantErr: "catchup"},
		{name: "bad timeout", mut: func(j *Job) { j.Timeout = "soon" }, wantErr: "timeout"},
		{name: "bad effort", mut: func(j *Job) { j.Effort = "extreme" }, wantErr: "effort"},
		{name: "nous model slug", mut: func(j *Job) { j.Backend = "nous"; j.Model = "deepseek/deepseek-v4.1-flash-1017:batch" }},
		{name: "bad model", mut: func(j *Job) { j.Model = "opus; rm -rf" }, wantErr: "model"},
		{name: "bad agent", mut: func(j *Job) { j.Agent = "a b" }, wantErr: "agent"},
		{name: "bad threshold", mut: func(j *Job) { j.SkipIfUsageAbove = &config.CronUsageThresholds{FiveHour: 120} }, wantErr: "between"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := validJob()
			tt.mut(&j)
			err := j.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestJobFieldErrorsTemplateField(t *testing.T) {
	j := validJob()
	j.Template = "t"
	errs := j.FieldErrors()
	if len(errs) != 1 || errs[0].Field != FieldTemplate {
		t.Errorf("errs = %+v, want one %s error", errs, FieldTemplate)
	}
}

func TestJobPromptSource(t *testing.T) {
	for _, tt := range []struct {
		j    Job
		want string
	}{
		{Job{Prompt: "p"}, "prompt"},
		{Job{PromptKey: "k"}, "prompt_key"},
		{Job{Template: "t"}, "template"},
	} {
		if got := tt.j.PromptSource(); got != tt.want {
			t.Errorf("PromptSource(%+v) = %q, want %q", tt.j, got, tt.want)
		}
	}
}

func TestJobDefaults(t *testing.T) {
	var j Job
	j.Normalize()
	if j.Harness != HarnessClaude || j.Mode != ModeWindow || j.Overlap != OverlapSkip || j.Catchup != CatchupLatest {
		t.Errorf("Normalize = %+v", j)
	}
	if !j.UseWorktree() || !j.IsEnabled() || j.TimeoutDuration() != 0 {
		t.Error("worktree/enabled default true, timeout none")
	}
	global := config.CronUsageThresholds{FiveHour: 80, SevenDay: 90}
	if got := j.UsageThresholds(global); got != global {
		t.Errorf("UsageThresholds = %v", got)
	}
	j.SkipIfUsageAbove = &config.CronUsageThresholds{FiveHour: 70}
	if got := j.UsageThresholds(global); got != (config.CronUsageThresholds{FiveHour: 70, SevenDay: 90}) {
		t.Errorf("UsageThresholds override = %v", got)
	}
}

func TestJobsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg", SchedulesFile)
	f, err := LoadJobs(path)
	if err != nil || len(f.Jobs) != 0 {
		t.Fatalf("missing file = %v, %v", f, err)
	}
	no := false
	j := validJob()
	j.Worktree = &no
	f.Jobs = append(f.Jobs, j)
	if err := SaveJobs(path, f); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
	got, err := LoadJobs(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Find("triage") != 0 || got.Jobs[0].UseWorktree() || got.Jobs[0].Harness != HarnessClaude {
		t.Errorf("round trip = %+v", got.Jobs)
	}
	if err := os.WriteFile(path, []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadJobs(path); err == nil {
		t.Error("corrupt file loaded without error")
	}
}

func TestPinKey(t *testing.T) {
	j := Job{Name: "j", Schedule: "@daily", Dir: "/p", Agent: "a", PromptKey: "spec", Template: ""}
	if !j.Unpinned() || j.PinCommand() != "agentctl cron edit j --prompt-key spec" {
		t.Fatalf("legacy job: unpinned %v, pin %q", j.Unpinned(), j.PinCommand())
	}
	j.PinKey("spec", "the text")
	j.Normalize()
	if j.Unpinned() || j.Prompt != "the text" || j.PromptSourceKey != "spec" || j.PromptKey != "" || j.PromptSource() != FieldPrompt {
		t.Errorf("pinned = %+v", j)
	}
	if err := j.Validate(); err != nil {
		t.Errorf("pinned job invalid: %v", err)
	}
	// The source key is a label for a stored prompt; without one it is wrong.
	bad := j
	bad.Prompt = ""
	if errs := bad.FieldErrors(); !slices.ContainsFunc(errs, func(e FieldError) bool { return strings.Contains(e.Message, "prompt_source_key") }) {
		t.Errorf("source key without prompt: %v", errs)
	}
	bad.Mode = ModeImprove
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "prompt_source_key") {
		t.Errorf("improve source key without prompt: %v", err)
	}

	// The lifecycle snapshot keeps the source key, and an excerpt.
	snap := SnapshotOf(j)
	if snap.PromptSource != FieldPrompt || snap.Prompt != "the text" || snap.PromptSourceKey != "spec" {
		t.Errorf("snapshot = %+v", snap)
	}
	if back := snap.Job(); back.PromptSourceKey != "spec" || back.Prompt != "the text" || back.Unpinned() {
		t.Errorf("snapshot job = %+v", back)
	}
}
