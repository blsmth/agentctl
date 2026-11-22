package cronedit

import (
	"slices"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/cron"
)

func testOptions() Options {
	return Options{
		Backends: []string{"nous", "ollama"},
		Accounts: []string{"personal", "work"},
		Models: map[string][]string{
			"anthropic": {"opus", "sonnet", "haiku", "claude-opus-4-1"},
			"ollama":    {"qwen3:8b"},
		},
		AgentBackend: func(a string) string {
			if a == "local" {
				return "ollama"
			}
			return ""
		},
	}
}

func TestOptionsCheck(t *testing.T) {
	tests := []struct {
		name      string
		mut       func(*cron.Job)
		wantErr   map[string]string // field -> message substring
		wantWarn  string
		wantClean bool
	}{
		{name: "defaults are fine", mut: func(*cron.Job) {}, wantClean: true},
		{name: "alias", mut: func(j *cron.Job) { j.Model = "opus" }, wantClean: true},
		{name: "alias with context suffix", mut: func(j *cron.Job) { j.Model = "opus[1m]" }, wantClean: true},
		{name: "full id outside the registry", mut: func(j *cron.Job) { j.Model = "claude-sonnet-9-9" }, wantClean: true},
		{name: "claude code aliases", mut: func(j *cron.Job) { j.Model = "opusplan" }, wantClean: true},
		{name: "default alias", mut: func(j *cron.Job) { j.Model = "default" }, wantClean: true},
		{name: "bedrock id warns", mut: func(j *cron.Job) { j.Model = "us.anthropic.claude-x-v1:0" }, wantWarn: "not a known Anthropic"},
		{name: "backend without a list does not warn", mut: func(j *cron.Job) { j.Backend = "nous"; j.Model = "whatever" }, wantClean: true},
		{name: "nous slug", mut: func(j *cron.Job) { j.Backend = "nous"; j.Model = "deepseek/deepseek-v4.1-flash-1017:batch" }, wantClean: true},
		{name: "typo", mut: func(j *cron.Job) { j.Model = "opsu" }, wantErr: map[string]string{"model": `unknown model "opsu"`}},
		{name: "unknown backend", mut: func(j *cron.Job) { j.Backend = "nope" }, wantErr: map[string]string{"backend": "use nous, ollama"}},
		{name: "backend none", mut: func(j *cron.Job) { j.Backend = "none" }, wantClean: true},
		{name: "unknown account", mut: func(j *cron.Job) { j.Account = "soon" }, wantErr: map[string]string{"account": "use personal, work"}},
		{name: "known account", mut: func(j *cron.Job) { j.Account = "work" }, wantClean: true},
		{name: "model on a listed ollama backend", mut: func(j *cron.Job) { j.Backend = "ollama"; j.Model = "qwen3:8b" }, wantClean: true},
		{name: "model outside an ollama list warns", mut: func(j *cron.Job) { j.Backend = "ollama"; j.Model = "llama9" }, wantWarn: "not in the model list"},
		{name: "agent default backend decides the model list", mut: func(j *cron.Job) { j.Agent = "local"; j.Model = "llama9" }, wantWarn: "backend ollama"},
		{name: "bad backend does not also flag the model", mut: func(j *cron.Job) { j.Backend = "nope"; j.Model = "opsu" }, wantErr: map[string]string{"backend": "unknown backend"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := cron.Job{Name: "j", Schedule: "0 9 * * *", Dir: "/x", Agent: "brandon", Prompt: "p"}
			tt.mut(&j)
			errs, warns := testOptions().check(normalize(j))
			if tt.wantClean && (len(errs) > 0 || len(warns) > 0) {
				t.Fatalf("errs=%v warns=%v, want clean", errs, warns)
			}
			for field, sub := range tt.wantErr {
				i := slices.IndexFunc(errs, func(e cron.FieldError) bool { return e.Field == field })
				if i < 0 || !strings.Contains(errs[i].Message, sub) {
					t.Errorf("errs = %v, want %s containing %q", errs, field, sub)
				}
			}
			if len(errs) != len(tt.wantErr) {
				t.Errorf("errs = %v, want only %v", errs, tt.wantErr)
			}
			if tt.wantWarn != "" && (len(warns) != 1 || !strings.Contains(warns[0].Message, tt.wantWarn)) {
				t.Errorf("warns = %v, want %q", warns, tt.wantWarn)
			}
			if tt.wantWarn == "" && len(warns) > 0 {
				t.Errorf("unexpected warns %v", warns)
			}
		})
	}
}

func TestValidateRejectsUnknownPins(t *testing.T) {
	ts := newTestStore(t)
	ts.Options = testOptions
	j := ts.job("pins")
	j.Model, j.Account = "opsu", "soon"
	_, errs := ts.Validate(j, "")
	if got := fields(errs); !slices.Equal(got, []string{"model", "account"}) {
		t.Fatalf("fields = %v (%v)", got, errs)
	}
	if _, err := ts.Create(j); err == nil {
		t.Fatal("Create accepted a job with unknown pins")
	}
	if p := ts.Preview(j, "", nil); p.Valid {
		t.Fatal("Preview of unknown pins is valid")
	}
}

func TestPreviewWarningsDoNotBlock(t *testing.T) {
	ts := newTestStore(t)
	ts.Options = testOptions
	j := ts.job("warn")
	j.Backend, j.Model = "ollama", "llama9"
	p := ts.Preview(j, "", nil)
	if !p.Valid || len(p.Warnings) != 1 || p.Warnings[0].Field != "model" {
		t.Fatalf("valid=%v warnings=%v", p.Valid, p.Warnings)
	}
}

func TestOverviewFlagsInvalidPins(t *testing.T) {
	ts := newTestStore(t)
	j := ts.job("cli-written")
	j.Model = "opsu"
	if _, err := ts.Create(j); err != nil { // no Options seam: stored as the CLI of an older build would
		t.Fatal(err)
	}
	ts.Options = testOptions
	ov, err := ts.Overview()
	if err != nil {
		t.Fatal(err)
	}
	if len(ov.Jobs) != 1 || !strings.Contains(ov.Jobs[0].Invalid, "opsu") {
		t.Fatalf("Invalid = %+v", ov.Jobs)
	}
}

func TestPreviewDiff(t *testing.T) {
	ts := newTestStore(t)
	if _, err := ts.Create(ts.job("d")); err != nil {
		t.Fatal(err)
	}
	j := ts.job("d")
	j.Model, j.Timeout = "opus", "45m"
	p := ts.Preview(j, "d", nil)
	want := []FieldChange{{"model", "", "opus"}, {"timeout", "", "45m"}}
	if !slices.Equal(p.Diff, want) {
		t.Fatalf("Diff = %+v, want %+v", p.Diff, want)
	}
	f := false
	j.Worktree = &f
	d := Diff(ts.job("d"), j)
	if i := slices.IndexFunc(d, func(c FieldChange) bool { return c.Field == "worktree" }); i < 0 || d[i].Old != "on" || d[i].New != "off" {
		t.Fatalf("Diff = %+v", d)
	}
}

func TestNousModelHasNoFieldError(t *testing.T) {
	j := cron.Job{Name: "j", Schedule: "0 9 * * *", Dir: "/x", Agent: "brandon", Prompt: "p",
		Backend: "nous", Model: "deepseek/deepseek-v4.1-flash-1017:batch"}
	j = normalize(j)
	if err := j.Validate(); err != nil {
		t.Fatalf("Validate = %v", err)
	}
}
