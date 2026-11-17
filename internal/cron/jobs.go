// Package cron implements `agentctl cron`: scheduled agent runs launched by a
// short-lived, flock-guarded `agentctl cron tick` that launchd runs every
// minute. See docs/design/cron.md.
//
// The package owns the jobs file, the per-job fire state, the run history and
// the tick's decisions (due fires, catch-up, overlap, max_concurrent, usage
// gate, timeouts). Every side effect a tick has on the outside world (tmux,
// dispatch, notifications) goes through Deps, so the decisions are testable
// without spawning anything.
package cron

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"time"

	"github.com/blsmth/agentctl/internal/agentname"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/launchopts"
	"github.com/blsmth/agentctl/internal/safefs"
)

// Accepted enum values. harness and mode ship now so the schema does not
// change when headless mode and other harnesses land.
const (
	HarnessClaude = "claude"
	ModeWindow    = "window"
	OverlapSkip   = "skip"
	OverlapAllow  = "allow"
	CatchupLatest = "latest"
	CatchupNone   = "none"
)

// SchedulesFile is the jobs file name inside the global config dir.
const SchedulesFile = "schedules.json"

// Job is one scheduled run. Optional enum fields are normalized by
// Normalize; read Worktree and Enabled through UseWorktree and IsEnabled.
type Job struct {
	Name     string `json:"name"`
	Schedule string `json:"schedule"`
	Dir      string `json:"dir"`
	Agent    string `json:"agent"`
	// Prompt or PromptKey (context store), exactly one. --prompt-file is
	// copied into Prompt at add time.
	Prompt    string `json:"prompt,omitempty"`
	PromptKey string `json:"prompt_key,omitempty"`
	Harness   string `json:"harness"`
	Mode      string `json:"mode"`
	// Worktree runs each fire in its own worktree (default true).
	Worktree *bool  `json:"worktree,omitempty"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
	Backend  string `json:"backend,omitempty"`
	Account  string `json:"account,omitempty"`
	Overlap  string `json:"overlap"`
	Catchup  string `json:"catchup"`
	// Timeout is a Go duration; empty means none.
	Timeout          string                      `json:"timeout,omitempty"`
	SkipIfUsageAbove *config.CronUsageThresholds `json:"skip_if_usage_above,omitempty"`
	Enabled          *bool                       `json:"enabled,omitempty"`
}

// UseWorktree reports whether fires get their own worktree (default true).
func (j Job) UseWorktree() bool { return j.Worktree == nil || *j.Worktree }

// IsEnabled reports whether the job is active (default true).
func (j Job) IsEnabled() bool { return j.Enabled == nil || *j.Enabled }

// TimeoutDuration returns the parsed timeout, or 0 for none.
func (j Job) TimeoutDuration() time.Duration {
	if j.Timeout == "" {
		return 0
	}
	d, err := time.ParseDuration(j.Timeout)
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

// UsageThresholds returns the job's usage gate: its own non-zero fields over
// the global defaults.
func (j Job) UsageThresholds(global config.CronUsageThresholds) config.CronUsageThresholds {
	t := global
	if j.SkipIfUsageAbove != nil {
		if j.SkipIfUsageAbove.FiveHour > 0 {
			t.FiveHour = j.SkipIfUsageAbove.FiveHour
		}
		if j.SkipIfUsageAbove.SevenDay > 0 {
			t.SevenDay = j.SkipIfUsageAbove.SevenDay
		}
	}
	return t
}

// Normalize fills the defaults for unset enum fields.
func (j *Job) Normalize() {
	if j.Harness == "" {
		j.Harness = HarnessClaude
	}
	if j.Mode == "" {
		j.Mode = ModeWindow
	}
	if j.Overlap == "" {
		j.Overlap = OverlapSkip
	}
	if j.Catchup == "" {
		j.Catchup = CatchupLatest
	}
}

var (
	jobNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,47}$`)
	modelRE   = regexp.MustCompile(`^[A-Za-z0-9._:\[\]-]+$`)
	// backendRE and accountRE match config map keys; both end up in env
	// and log lines, never a shell.
	backendRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// Validate checks a normalized job. It does not check min_interval (see
// CheckMinInterval) or that the agent exists (the caller discovers agents).
func (j Job) Validate() error {
	if !jobNameRE.MatchString(j.Name) {
		return fmt.Errorf("invalid job name %q: use letters, digits, - and _ (max 48, starting with a letter or digit)", j.Name)
	}
	if _, err := ParseSchedule(j.Schedule); err != nil {
		return err
	}
	if j.Dir == "" {
		return errors.New("dir is required")
	}
	if !filepath.IsAbs(j.Dir) {
		return fmt.Errorf("dir %q must be an absolute path", j.Dir)
	}
	if !agentname.ValidLaunch(j.Agent) {
		return fmt.Errorf("invalid agent name %q", j.Agent)
	}
	if (j.Prompt == "") == (j.PromptKey == "") {
		return errors.New("exactly one of prompt or prompt_key is required")
	}
	if j.Harness != HarnessClaude {
		return fmt.Errorf("harness %q is not supported yet (only %q)", j.Harness, HarnessClaude)
	}
	if j.Mode != ModeWindow {
		return fmt.Errorf("mode %q is not supported yet (only %q)", j.Mode, ModeWindow)
	}
	if !slices.Contains([]string{OverlapSkip, OverlapAllow}, j.Overlap) {
		return fmt.Errorf("overlap %q: want skip or allow", j.Overlap)
	}
	if !slices.Contains([]string{CatchupLatest, CatchupNone}, j.Catchup) {
		return fmt.Errorf("catchup %q: want latest or none", j.Catchup)
	}
	if j.Model != "" && !modelRE.MatchString(j.Model) {
		return fmt.Errorf("invalid model %q", j.Model)
	}
	if j.Effort != "" {
		if err := launchopts.ValidateEffort(j.Effort); err != nil {
			return err
		}
	}
	if j.Backend != "" && !backendRE.MatchString(j.Backend) {
		return fmt.Errorf("invalid backend %q", j.Backend)
	}
	if j.Account != "" && !backendRE.MatchString(j.Account) {
		return fmt.Errorf("invalid account %q", j.Account)
	}
	if j.Timeout != "" {
		if d, err := time.ParseDuration(j.Timeout); err != nil || d <= 0 {
			return fmt.Errorf("invalid timeout %q: want a positive Go duration such as 45m", j.Timeout)
		}
	}
	if t := j.SkipIfUsageAbove; t != nil {
		for _, v := range []float64{t.FiveHour, t.SevenDay} {
			if v < 0 || v > 100 {
				return fmt.Errorf("skip_if_usage_above values must be between 0 and 100, got %v", v)
			}
		}
	}
	return nil
}

// Jobs is the on-disk shape of schedules.json.
type Jobs struct {
	Jobs []Job `json:"jobs"`
}

// Find returns the index of the named job, or -1.
func (f *Jobs) Find(name string) int {
	return slices.IndexFunc(f.Jobs, func(j Job) bool { return j.Name == name })
}

// JobsPath returns <GlobalDir>/schedules.json. Jobs are global only: nothing
// in a project-local .agentctl/ can define one.
func JobsPath() (string, error) {
	dir, err := config.GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, SchedulesFile), nil
}

// LoadJobs reads the jobs file. A missing file is an empty list; a corrupt
// one is an error so a write never clobbers a file mid-edit. Jobs are
// normalized; invalid ones are returned as-is for the caller to report.
func LoadJobs(path string) (*Jobs, error) {
	f := &Jobs{}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return f, nil
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if err := json.Unmarshal(data, f); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	for i := range f.Jobs {
		f.Jobs[i].Normalize()
	}
	return f, nil
}

// SaveJobs writes the jobs file atomically (temp + rename), mode 0600.
func SaveJobs(path string, f *Jobs) error {
	if f.Jobs == nil {
		f.Jobs = []Job{}
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding jobs: %w", err)
	}
	data = append(data, '\n')
	if err := safefs.ReplaceFile(path, data, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
