package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/cron"
)

func TestCyclerStepFilterAndUnknown(t *testing.T) {
	opts := []schedOpt{{value: "", label: "agent default (sonnet)"}, {value: "opus", label: "opus"}, {value: "opus-4", label: "opus-4"}, {value: "haiku", label: "haiku"}}
	c := newCycler(opts, "", false, nil)
	c.step(1)
	if c.value() != "opus" {
		t.Fatalf("step → %q", c.value())
	}
	c.step(-2)
	if c.value() != "haiku" { // wraps
		t.Fatalf("wrap → %q", c.value())
	}
	c.typeText("op")
	if c.value() != "opus" || len(c.visible()) != 2 {
		t.Fatalf("filter op → %q, %d visible", c.value(), len(c.visible()))
	}
	c.step(1)
	if c.value() != "opus-4" {
		t.Fatalf("step in filter → %q", c.value())
	}
	c.typeText("zz")
	if got := ansi.Strip(c.view(true)); !strings.Contains(got, "no match") {
		t.Fatalf("no match view = %q", got)
	}

	// A stored value outside the list stays, marked unknown.
	u := newCycler(opts, "opsu", false, nil)
	if u.value() != "opsu" || !u.current().unknown || !strings.Contains(ansi.Strip(u.view(false)), "opsu (unknown)") {
		t.Fatalf("unknown: %q %+v %q", u.value(), u.current(), ansi.Strip(u.view(false)))
	}
	p := newCycler(opts, "a:b", true, plainAgent)
	if p.current().unknown {
		t.Error("a plugin agent is not unknown")
	}

	// Free cyclers take typed text.
	fr := newCycler(opts, "", true, nil)
	fr.typeText("claude-x")
	if fr.value() != "claude-x" || !strings.Contains(ansi.Strip(fr.view(true)), "(custom)") {
		t.Fatalf("custom = %q %q", fr.value(), ansi.Strip(fr.view(true)))
	}
	for range "claude-x" {
		fr.backspace()
	}
	if fr.value() != "" || len(fr.opts) != len(opts) {
		t.Fatalf("after clearing: %q, %d opts", fr.value(), len(fr.opts))
	}

	// The count shows past six options.
	many := make([]schedOpt, 12)
	for i := range many {
		many[i] = schedOpt{value: string(rune('a' + i)), label: string(rune('a' + i))}
	}
	mc := newCycler(many, "c", false, nil)
	if !strings.Contains(ansi.Strip(mc.view(false)), "3/12") {
		t.Errorf("count missing: %q", ansi.Strip(mc.view(false)))
	}
}

func richSchedEnv() schedEnv {
	e := testSchedEnv()
	e.opts.Backends = []string{"ollama"}
	e.opts.Accounts = []string{"personal", "work"}
	e.defaultAgent = "go-engineer"
	e.agentCfg = func(a string) config.AgentConfig {
		if a == "go-engineer" {
			return config.AgentConfig{Model: "sonnet", EffortLevel: "high"}
		}
		return config.AgentConfig{}
	}
	e.account = func(string, string) string { return "work" }
	return e
}

func withSchedEnv(t *testing.T, e schedEnv) {
	t.Helper()
	old := fleetSchedEnvLoad
	fleetSchedEnvLoad = func() schedEnv { return e }
	t.Cleanup(func() { fleetSchedEnvLoad = old })
}

func openNewForm(t *testing.T, f *schedFixture) FleetModel {
	t.Helper()
	m := showSchedules(t, f.model(t))
	m = selectSched(t, m, fleetSchedNewSel)
	return schedPress(t, m, "n")
}

func TestFormNamesResolvedDefaultsAndHidesEmptyRows(t *testing.T) {
	f := newSchedFixture(t)
	withSchedEnv(t, richSchedEnv())
	m := openNewForm(t, f)
	fm := formOf(t, m)
	if fm.agent.value() != "go-engineer" {
		t.Errorf("agent = %q, want the project's first agent", fm.agent.value())
	}
	m = schedPress(t, m, "tab", "tab", "tab", "tab", "tab", "tab", "tab") // open Advanced via focus
	fm = formOf(t, m)
	fm.focusStop(stopModel)
	out := ansi.Strip(m.render())
	for _, s := range []string{"agent default (sonnet)", "default (high)", "default (work)", "global (80%)", "global (90%)", "default (anthropic)", "What", "When", "Advanced"} {
		if !strings.Contains(out, s) {
			t.Errorf("form lacks %q:\n%s", s, out)
		}
	}

	withSchedEnv(t, testSchedEnv())
	m = openNewForm(t, f)
	fm = formOf(t, m)
	fm.focusStop(stopModel)
	out = ansi.Strip(m.render())
	if strings.Contains(out, "Backend") || strings.Contains(out, "Account") {
		t.Errorf("backend/account rows should be hidden without any configured:\n%s", out)
	}
}

func TestFormNewDefaultAgentFallsBackToDefaultAgent(t *testing.T) {
	f := newSchedFixture(t)
	e := richSchedEnv()
	e.defaultAgent = "reviewer"
	withSchedEnv(t, e)
	m := showSchedules(t, f.model(t))
	m.groups = nil // no agent in any project
	m.rebuildLines()
	m = selectSched(t, m, fleetSchedNewSel)
	m = schedPress(t, m, "n")
	if got := formOf(t, m).agent.value(); got != "reviewer" {
		t.Errorf("agent = %q, want default_agent", got)
	}
}

func TestFormErrorsOnlyAfterEditOrReview(t *testing.T) {
	f := newSchedFixture(t)
	withSchedEnv(t, testSchedEnv())
	m := openNewForm(t, f)
	if out := ansi.Strip(m.render()); strings.Contains(out, "invalid job name") || strings.Contains(out, "write a prompt") {
		t.Fatalf("errors before any keystroke:\n%s", out)
	}
	m = schedPress(t, m, "ctrl+s")
	if out := ansi.Strip(m.render()); !strings.Contains(out, "write a prompt") {
		t.Errorf("ctrl+s should reveal the errors:\n%s", out)
	}
}

func TestFormTimeAndTimeoutControls(t *testing.T) {
	f := newSchedFixture(t)
	withSchedEnv(t, testSchedEnv())
	m := openNewForm(t, f)
	m = focusStopT(t, m, stopClock)
	m = schedPress(t, m, "up")
	if got := formOf(t, m).clock.Value(); got != "09:15" {
		t.Errorf("↑ → %q, want 09:15", got)
	}
	m = schedPress(t, m, "down", "down")
	if got := formOf(t, m).clock.Value(); got != "08:45" {
		t.Errorf("↓↓ → %q, want 08:45", got)
	}
	fm := formOf(t, m)
	fm.focusStop(stopTimeout)
	m = schedPress(t, m, "right", "right")
	if got := formOf(t, m).timeoutValue(); got != "30m" {
		t.Errorf("timeout = %q", got)
	}
	for range 4 {
		m = schedPress(t, m, "right")
	}
	fm = formOf(t, m)
	if !fm.timeoutCustom() {
		t.Fatalf("custom… not reached: %q", fm.tmoutSel.value())
	}
	m = schedPress(t, m, "tab")
	m = schedType(t, m, "90m")
	if got := formOf(t, m).timeoutValue(); got != "90m" {
		t.Errorf("custom timeout = %q", got)
	}
}

func TestFormKeepsUnknownStoredModelRed(t *testing.T) {
	f := newSchedFixture(t)
	withSchedEnv(t, testSchedEnv())
	f.create(t, cron.Job{Name: "typo", Schedule: "0 9 * * *", Model: "opsu", Timeout: "60m"})
	m := showSchedules(t, f.model(t))
	m = selectSched(t, m, "typo")
	m = schedPress(t, m, "e")
	fm := formOf(t, m)
	if fm.model.value() != "opsu" || !fm.model.current().unknown {
		t.Fatalf("model = %+v", fm.model.current())
	}
	if fm.timeoutValue() != "60m" || !fm.timeoutCustom() {
		t.Errorf("a stored non-preset timeout should show as custom: %q", fm.timeoutValue())
	}
	j, _ := fm.job()
	if j.Model != "opsu" || j.Timeout != "60m" {
		t.Errorf("round trip lost values: %+v", j)
	}
}

func TestEnterOpensEditAndCardAlwaysHasDetail(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	m := showSchedules(t, f.model(t))
	m = selectSched(t, m, "upcoming")
	if m.sched.detail == nil || m.sched.detail.Name != "upcoming" {
		t.Fatal("card has no full detail on selection")
	}
	m = schedPress(t, m, "enter")
	if m.schedForm == nil || m.schedForm.mode != schedFormEdit {
		t.Fatal("enter should open the edit form")
	}
}

func TestPanePadsEveryLineToItsWidth(t *testing.T) {
	f := newSchedFixture(t)
	f.seedFour(t)
	m := showSchedules(t, f.model(t))
	m = selectSched(t, m, "nightly")
	pw := m.schedPaneWidth()
	for i, l := range strings.Split(m.render(), "\n") {
		if i == 0 {
			continue
		}
		if got := len([]rune(ansi.Strip(l))); got < m.contentWidth()+1+pw+2 {
			t.Errorf("line %d is %d cells, want it padded to the pane edge (%d): %q", i, got, m.contentWidth()+1+pw+2, ansi.Strip(l))
			break
		}
	}
}

func TestTildifyResolvesSymlinkedHome(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "home")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("no symlinks:", err)
	}
	if err := os.MkdirAll(filepath.Join(real, "code", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", link)
	for _, p := range []string{filepath.Join(real, "code", "x"), filepath.Join(link, "code", "x")} {
		if got := tildify(p); got != "~/code/x" {
			t.Errorf("tildify(%q) = %q", p, got)
		}
	}
	if got := tildify("/etc/hosts"); got != "/etc/hosts" {
		t.Errorf("outside path changed: %q", got)
	}
}

func TestReviewShowsOldToNewWithNamedDefault(t *testing.T) {
	f := newSchedFixture(t)
	withSchedEnv(t, richSchedEnv())
	f.create(t, cron.Job{Name: "pin", Schedule: "0 9 * * *"})
	m := showSchedules(t, f.model(t))
	m = selectSched(t, m, "pin")
	m = schedPress(t, m, "e")
	fm := formOf(t, m)
	fm.focusStop(stopModel)
	m = schedPress(t, m, "right", "ctrl+s") // inherit
	out := strings.Join(strings.Fields(ansi.Strip(m.render())), " ")
	if !strings.Contains(out, "model agent default (sonnet) → inherit") {
		t.Errorf("review lacks the old → new line:\n%s", out)
	}
}

func TestSegmentedReadsWithoutColour(t *testing.T) {
	plain := ansi.Strip(segmented([]string{"Hourly", "Daily"}, 1, false, 40))
	if !strings.Contains(plain, "●Daily") || strings.Contains(plain, "●Hourly") {
		t.Errorf("segments = %q", plain)
	}
}
