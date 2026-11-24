package cronedit

import (
	"reflect"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/cron"
)

// The fleet-improver agent exists in a real setup; the test store only knows
// "brandon", so the improve job names it.
func (ts *testStore) improveJob(name string) cron.Job {
	return cron.Job{
		Name: name, Schedule: "0 22 * * *", Dir: ts.projDir, Agent: "brandon",
		Mode: cron.ModeImprove, Model: "opus", Timeout: "3h",
	}
}

func TestImproveJobCreateAndRoundTrip(t *testing.T) {
	ts := newTestStore(t)
	created, err := ts.Create(ts.improveJob("fleet"))
	if err != nil {
		t.Fatal(err)
	}
	if created.Mode != cron.ModeImprove || created.Overlap != cron.OverlapSkip || created.Prompt != "" {
		t.Fatalf("created = %+v", created)
	}
	// An edit that touches one field loses none of the others.
	res, err := ts.Modify("fleet", func(j *cron.Job) error {
		j.Timeout = "4h"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := created
	want.Timeout = "4h"
	if !reflect.DeepEqual(res.Job, want) {
		t.Errorf("after edit = %+v\nwant         %+v", res.Job, want)
	}
	if !reflect.DeepEqual(res.Changed, []string{cron.FieldTimeout}) {
		t.Errorf("changed = %v", res.Changed)
	}
	stored, err := ts.Get("fleet")
	if err != nil || !reflect.DeepEqual(stored, want) {
		t.Errorf("stored = %+v, %v", stored, err)
	}
}

func TestImproveJobSwitchModeShowsInDiff(t *testing.T) {
	ts := newTestStore(t)
	if _, err := ts.Create(ts.job("daily")); err != nil {
		t.Fatal(err)
	}
	edited := ts.job("daily")
	edited.Mode = cron.ModeImprove
	p := ts.Preview(edited, "daily", nil)
	if !p.Valid {
		t.Fatalf("errors = %+v", p.Errors)
	}
	if !reflect.DeepEqual(p.Changed, []string{cron.FieldMode}) {
		t.Errorf("changed = %v", p.Changed)
	}
	if len(p.Diff) != 1 || p.Diff[0].Old != "window" || p.Diff[0].New != "improve" {
		t.Errorf("diff = %+v", p.Diff)
	}
}

func TestImproveJobValidation(t *testing.T) {
	ts := newTestStore(t)

	j := ts.improveJob("fleet")
	j.Agent = "" // the fleet-improver agent, which the test store does not know
	_, errs := ts.Validate(j, "")
	if len(errs) != 1 || errs[0].Field != cron.FieldAgent || !strings.Contains(errs[0].Message, cron.ImproveAgent) {
		t.Errorf("errs = %+v, want the default agent checked", errs)
	}

	j = ts.improveJob("fleet")
	j.Overlap = cron.OverlapAllow
	if _, errs := ts.Validate(j, ""); len(errs) != 1 || errs[0].Field != cron.FieldOverlap {
		t.Errorf("overlap allow: errs = %+v", errs)
	}

	if got := ts.EffectiveAgent(cron.Job{Mode: cron.ModeImprove}); got != cron.ImproveAgent {
		t.Errorf("EffectiveAgent = %q", got)
	}
}

func TestImproveJobDetailAndCheck(t *testing.T) {
	ts := newTestStore(t)
	if _, err := ts.Create(ts.improveJob("fleet")); err != nil {
		t.Fatal(err)
	}
	if errs := ts.Check(ts.improveJob("fleet")); len(errs) != 0 {
		t.Errorf("Check = %+v", errs)
	}
	d, err := ts.Detail("fleet")
	if err != nil {
		t.Fatal(err)
	}
	if want := "Fleet improve, last 24h, agent brandon, model opus"; d.ImproveSummary != want {
		t.Errorf("summary = %q, want %q", d.ImproveSummary, want)
	}
	if d.Invalid != "" {
		t.Errorf("invalid = %q", d.Invalid)
	}
	plain, _ := ts.Create(ts.job("plain"))
	if d2, _ := ts.Detail(plain.Name); d2.ImproveSummary != "" {
		t.Errorf("window job summary = %q", d2.ImproveSummary)
	}
}
