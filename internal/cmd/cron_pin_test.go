package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	ctxstore "github.com/blsmth/agentctl/internal/context"
	"github.com/blsmth/agentctl/internal/cron"
	"github.com/blsmth/agentctl/internal/dispatchapproval"
)

// stubCronPinCaller makes pinnedKeyText see an agent session (agent true)
// whose approval markers are pins (key: the args joined by spaces), or a
// plain shell. It returns the args each consume was asked for.
func stubCronPinCaller(t *testing.T, agent bool, pins map[string]string) *[][]string {
	t.Helper()
	var asked [][]string
	origAgent, origWin, origConsume, origArgs := cronCallerIsAgent, cronPinWindowID, consumeCronPin, cronPinArgs
	cronCallerIsAgent = func() bool { return agent }
	cronPinWindowID = func() string { return "@9" }
	cronPinArgs = func() []string { return []string{"cron", "test-args"} }
	consumeCronPin = func(win string, args []string) (string, error) {
		asked = append(asked, args)
		k := strings.Join(args, " ")
		text, ok := pins[k]
		if !ok || win != "@9" {
			return "", dispatchapproval.ErrNotApproved
		}
		delete(pins, k)
		return text, nil
	}
	t.Cleanup(func() {
		cronCallerIsAgent, cronPinWindowID, consumeCronPin, cronPinArgs = origAgent, origWin, origConsume, origArgs
	})
	return &asked
}

func setContextKey(t *testing.T, key, content string) {
	t.Helper()
	if err := ctxstore.Set(key, ctxstore.Entry{Content: content}); err != nil {
		t.Fatal(err)
	}
}

func loadOnlyJob(t *testing.T, e cronEnv) cron.Job {
	t.Helper()
	path, _ := e.jobsPath()
	jobs, err := cron.LoadJobs(path)
	if err != nil || len(jobs.Jobs) != 1 {
		t.Fatalf("jobs = %+v, %v", jobs, err)
	}
	return jobs.Jobs[0]
}

func TestCronAddPinsPromptKey(t *testing.T) {
	t.Run("plain shell copies the key's text", func(t *testing.T) {
		var launches []cron.LaunchRequest
		e := testCronEnv(t, &launches)
		setContextKey(t, "spec", "nightly spec")
		var out bytes.Buffer
		if err := e.add(&out, CronAddOptions{Name: "j", Schedule: "@hourly", Agent: "brandon", Dir: t.TempDir(), PromptKey: "spec"}); err != nil {
			t.Fatal(err)
		}
		j := loadOnlyJob(t, e)
		if j.Prompt != "nightly spec" || j.PromptKey != "" || j.PromptSourceKey != "spec" || j.Unpinned() {
			t.Errorf("job = %+v, want pinned", j)
		}
		// Changing the key afterwards does not change what runs.
		setContextKey(t, "spec", "something else")
		opts, err := cronDispatchOptions(cron.LaunchRequest{Job: loadOnlyJob(t, e)})
		if err != nil {
			t.Fatal(err)
		}
		if opts.Prompt != "nightly spec" || opts.PromptKey != "" {
			t.Errorf("fire opts prompt %q key %q, want the pinned text and no key", opts.Prompt, opts.PromptKey)
		}
	})

	t.Run("agent session stores the approved text, not the key's current text", func(t *testing.T) {
		var launches []cron.LaunchRequest
		e := testCronEnv(t, &launches)
		stubCronPinCaller(t, true, map[string]string{"cron test-args": "approved text"})
		setContextKey(t, "spec", "changed after the popup")
		var out bytes.Buffer
		if err := e.add(&out, CronAddOptions{Name: "j", Schedule: "@hourly", Agent: "brandon", Dir: t.TempDir(), PromptKey: "spec"}); err != nil {
			t.Fatal(err)
		}
		if j := loadOnlyJob(t, e); j.Prompt != "approved text" || j.PromptSourceKey != "spec" {
			t.Errorf("job = %+v, want the approved text", j)
		}
	})

	t.Run("agent session without an approval is refused", func(t *testing.T) {
		var launches []cron.LaunchRequest
		e := testCronEnv(t, &launches)
		stubCronPinCaller(t, true, nil)
		setContextKey(t, "spec", "unapproved")
		var out bytes.Buffer
		err := e.add(&out, CronAddOptions{Name: "j", Schedule: "@hourly", Agent: "brandon", Dir: t.TempDir(), PromptKey: "spec"})
		if err == nil || !strings.Contains(err.Error(), "no approved copy") || !errors.Is(err, dispatchapproval.ErrNotApproved) {
			t.Fatalf("err = %v, want a refusal", err)
		}
		path, _ := e.jobsPath()
		if jobs, _ := cron.LoadJobs(path); len(jobs.Jobs) != 0 {
			t.Errorf("job saved without approval: %+v", jobs.Jobs)
		}
	})

	t.Run("a bad flag does not use up the approval", func(t *testing.T) {
		var launches []cron.LaunchRequest
		e := testCronEnv(t, &launches)
		asked := stubCronPinCaller(t, true, map[string]string{"cron test-args": "approved"})
		var out bytes.Buffer
		if err := e.add(&out, CronAddOptions{Name: "j", Agent: "brandon", Dir: t.TempDir(), PromptKey: "spec"}); err == nil {
			t.Fatal("no error without --schedule")
		}
		if len(*asked) != 0 {
			t.Errorf("approval consumed for a failing add: %v", *asked)
		}
	})
}

func TestCronEditPinsLegacyPromptKey(t *testing.T) {
	ptr := func(s string) *string { return &s }
	var launches []cron.LaunchRequest
	e := testCronEnv(t, &launches)
	path, _ := e.jobsPath()
	if err := os.MkdirAll(strings.TrimSuffix(path, cron.SchedulesFile), 0o700); err != nil {
		t.Fatal(err)
	}
	// As an older binary left it: the key is read at every fire.
	legacy := cron.Job{Name: "nightly", Schedule: "0 2 * * *", Dir: t.TempDir(), Agent: "brandon", PromptKey: "spec"}
	if err := cron.SaveJobs(path, &cron.Jobs{Jobs: []cron.Job{legacy}}); err != nil {
		t.Fatal(err)
	}
	setContextKey(t, "spec", "today's spec")

	// The legacy job keeps running from the key (nightly jobs do not break).
	opts, err := cronDispatchOptions(cron.LaunchRequest{Job: loadOnlyJob(t, e)})
	if err != nil || opts.Prompt != "today's spec" {
		t.Fatalf("legacy fire = %q, %v", opts.Prompt, err)
	}
	// cron list marks it and names the pin command.
	var out bytes.Buffer
	if err := e.list(&out, false); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); !strings.Contains(s, "key spec (UNPINNED)") ||
		!strings.Contains(s, "UNPINNED nightly: its fires read context key spec") ||
		!strings.Contains(s, "agentctl cron edit nightly --prompt-key spec") {
		t.Errorf("list:\n%s", s)
	}

	// An edit that leaves the prompt alone keeps it unpinned.
	out.Reset()
	if err := e.edit(&out, CronEditOptions{Name: "nightly", Timeout: ptr("1h")}); err != nil {
		t.Fatal(err)
	}
	if j := loadOnlyJob(t, e); !j.Unpinned() {
		t.Errorf("timeout edit pinned the job: %+v", j)
	}

	// cron edit --prompt-key pins it, from the approval in an agent session.
	stubCronPinCaller(t, true, map[string]string{"cron test-args": "approved spec"})
	out.Reset()
	if err := e.edit(&out, CronEditOptions{Name: "nightly", PromptKey: ptr("spec")}); err != nil {
		t.Fatal(err)
	}
	j := loadOnlyJob(t, e)
	if j.Unpinned() || j.Prompt != "approved spec" || j.PromptSourceKey != "spec" {
		t.Fatalf("pinned job = %+v", j)
	}
	if !strings.Contains(out.String(), "Updated nightly: prompt, prompt_key, prompt_source_key") {
		t.Errorf("output %q", out.String())
	}
	// The edited lifecycle entry records the pin.
	dir, _ := e.stateDir()
	runs, err := cron.ReadRuns(dir, "nightly")
	if err != nil {
		t.Fatal(err)
	}
	var edited *cron.Entry
	for i := range runs {
		if runs[i].Result == cron.ResultEdited && strings.Contains(runs[i].Note, "prompt_source_key") {
			edited = &runs[i]
		}
	}
	if edited == nil || edited.Def == nil || edited.Def.PromptSource != cron.FieldPrompt || edited.Def.PromptSourceKey != "spec" ||
		edited.Def.Prompt != "approved spec" {
		t.Errorf("edited entry = %+v", edited)
	}
	out.Reset()
	if err := e.list(&out, false); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); strings.Contains(s, "UNPINNED") || !strings.Contains(s, "key spec (pinned)") {
		t.Errorf("list after pin:\n%s", s)
	}
}

// TestPinnedJobOnDiskForOldBinaries: a pinned job carries the text in
// prompt and no prompt_key, so a binary that predates prompt_source_key
// (and ignores it) validates it and runs the stored prompt.
func TestPinnedJobOnDiskForOldBinaries(t *testing.T) {
	j := cron.Job{Name: "j", Schedule: "@daily", Dir: "/p", Agent: "brandon"}
	j.PinKey("spec", "pinned text")
	data, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if m["prompt"] != "pinned text" || m["prompt_source_key"] != "spec" {
		t.Errorf("on disk = %s", data)
	}
	if _, ok := m["prompt_key"]; ok {
		t.Errorf("pinned job writes prompt_key: %s", data)
	}
	// An old binary's view: the same struct without the new field.
	delete(m, "prompt_source_key")
	old, _ := json.Marshal(m)
	var back cron.Job
	if err := json.Unmarshal(old, &back); err != nil {
		t.Fatal(err)
	}
	back.Normalize()
	if err := back.Validate(); err != nil || back.PromptSource() != cron.FieldPrompt {
		t.Errorf("old view invalid: %v (source %q)", err, back.PromptSource())
	}
	opts, err := cronDispatchOptions(cron.LaunchRequest{Job: back})
	if err != nil || opts.Prompt != "pinned text" || opts.PromptKey != "" {
		t.Errorf("old view fire = %+v, %v", opts, err)
	}
}

func TestCronImproveThemeFromPinnedKey(t *testing.T) {
	ctxstore.SetPathForTest(t.TempDir())
	setContextKey(t, "theme", "live theme")
	job := cron.Job{Name: "fi", Schedule: "0 22 * * *", Dir: "/p", Mode: cron.ModeImprove}
	job.PinKey("theme", "pinned theme")
	got, err := cronImproveOptions(cron.LaunchRequest{Job: job})
	if err != nil || got.Theme != "pinned theme" {
		t.Errorf("pinned theme = %q, %v", got.Theme, err)
	}
	legacy := cron.Job{Name: "fi", Schedule: "0 22 * * *", Dir: "/p", Mode: cron.ModeImprove, PromptKey: "theme"}
	got, err = cronImproveOptions(cron.LaunchRequest{Job: legacy})
	if err != nil || got.Theme != "live theme" {
		t.Errorf("legacy theme = %q, %v", got.Theme, err)
	}
}

func TestCronArgsFrom(t *testing.T) {
	got := cronArgsFrom([]string{"/bin/agentctl", "cron", "add", "j", "-k", "cron"})
	if !slices.Equal(got, []string{"cron", "add", "j", "-k", "cron"}) {
		t.Errorf("args = %q", got)
	}
	if cronArgsFrom([]string{"agentctl", "list"}) != nil {
		t.Error("no cron word should be nil")
	}
}

func TestCronHealthWarnsUnpinned(t *testing.T) {
	h := cronHealth{jobs: 2, enabled: 2, darwin: true, installed: true, loaded: true,
		unpinned: []cron.Job{{Name: "nightly", PromptKey: "spec"}}}
	lines := cronHealthLines(h, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "UNPINNED nightly") || !strings.Contains(joined, "agentctl cron edit nightly --prompt-key spec") {
		t.Errorf("health:\n%s", joined)
	}
}
