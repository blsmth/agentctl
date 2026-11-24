package tui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/cron"
	"github.com/blsmth/agentctl/internal/launchopts"
)

// schedFind is the screen cell of text on the first screen line inside the
// form that contains both within and text (text right of within). The search
// reads the rendered screen, so a click there tests the hit testing against
// what is actually drawn.
func schedFind(t *testing.T, m FleetModel, within, text string) (x, y int) {
	t.Helper()
	minX := m.schedDrawerX() + 1
	for row, l := range strings.Split(ansi.Strip(m.render()), "\n") {
		from := 0
		for {
			i := strings.Index(l[from:], within)
			if i < 0 {
				break
			}
			wi := from + i
			from = wi + len(within)
			if ansi.StringWidth(l[:wi]) < minX {
				continue
			}
			j := strings.Index(l[wi:], text)
			if j < 0 {
				continue
			}
			return ansi.StringWidth(l[:wi+j]), row
		}
	}
	t.Fatalf("no %q / %q in the form:\n%s", within, text, ansi.Strip(m.render()))
	return 0, 0
}

func schedClickAt(t *testing.T, m FleetModel, x, y int) FleetModel {
	t.Helper()
	next, cmd := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	return schedDrive(t, next.(FleetModel), cmd)
}

// schedClick clicks text on the form line that contains within.
func schedClick(t *testing.T, m FleetModel, within, text string) FleetModel {
	t.Helper()
	x, y := schedFind(t, m, within, text)
	return schedClickAt(t, m, x, y)
}

func schedOpenAdvanced(t *testing.T, m FleetModel) FleetModel {
	t.Helper()
	if !formOf(t, m).advOpen {
		m = schedClick(t, m, "Advanced", "Advanced")
	}
	if !formOf(t, m).advOpen {
		t.Fatal("a click on Advanced should open the fold")
	}
	return m
}

func TestSchedFormClickStepsCyclerAndPreviews(t *testing.T) {
	f := newSchedFixture(t)
	withSchedEnv(t, richSchedEnv())
	m := openNewForm(t, f)
	m = schedOpenAdvanced(t, m)
	gen := formOf(t, m).gen

	m = schedClick(t, m, "Effort", "default (high)")
	fm := formOf(t, m)
	if fm.focus != stopEffort || fm.effort.value() != "low" {
		t.Fatalf("click on the value: focus %d effort %q, want effort focused and stepped to low", fm.focus, fm.effort.value())
	}
	if !fm.touched[stopEffort] || fm.gen == gen || !fm.previewCurrent() {
		t.Errorf("a click change must go the key path: touched=%v gen %d→%d current=%v", fm.touched[stopEffort], gen, fm.gen, fm.previewCurrent())
	}
	if fm.preview.Job.Effort != "low" {
		t.Errorf("preview job effort = %q", fm.preview.Job.Effort)
	}

	m = schedClick(t, m, "Effort", "‹")
	if got := formOf(t, m).effort.value(); got != "" {
		t.Errorf("click on ‹ should step back to the default, got %q", got)
	}

	// The wheel over the focused cycler steps it.
	x, y := schedFind(t, m, "Effort", "default")
	next, cmd := m.Update(tea.MouseWheelMsg{X: x, Y: y, Button: tea.MouseWheelDown})
	m = schedDrive(t, next.(FleetModel), cmd)
	if got := formOf(t, m).effort.value(); got != "low" {
		t.Errorf("wheel down over the focused cycler: effort %q, want low", got)
	}
	// Over an unfocused one it leaves it alone.
	x, y = schedFind(t, m, "Timeout", "none")
	next, cmd = m.Update(tea.MouseWheelMsg{X: x, Y: y, Button: tea.MouseWheelDown})
	m = schedDrive(t, next.(FleetModel), cmd)
	if got := formOf(t, m).tmoutSel.value(); got != "" {
		t.Errorf("wheel over an unfocused cycler changed it to %q", got)
	}
}

func TestSchedFormClickFocusesTextInputs(t *testing.T) {
	f := newSchedFixture(t)
	withSchedEnv(t, testSchedEnv())
	m := openNewForm(t, f)
	m = schedClick(t, m, "Time", "09:00")
	fm := formOf(t, m)
	if fm.focus != stopClock || !fm.clock.Focused() || fm.name.Focused() {
		t.Fatalf("click on the time: focus %d clock focused %v", fm.focus, fm.clock.Focused())
	}
	if fm.touched[stopClock] {
		t.Error("focusing is not a change")
	}
	m = schedClick(t, m, "Prompt", "Prompt")
	if fm = formOf(t, m); fm.focus != stopPrompt || !fm.prompt.Focused() {
		t.Fatalf("click on the prompt label: focus %d", fm.focus)
	}
	m = schedClick(t, m, "Name", "Name")
	if fm = formOf(t, m); fm.focus != stopName || !fm.name.Focused() || fm.prompt.Focused() {
		t.Errorf("click on the name label: focus %d", fm.focus)
	}
}

func TestSchedFormClickSegmentsAndDayChips(t *testing.T) {
	f := newSchedFixture(t)
	withSchedEnv(t, testSchedEnv())
	m := openNewForm(t, f)
	m = schedClick(t, m, "Schedule", "Daily")
	fm := formOf(t, m)
	if fm.focus != stopKind || fm.kind != schedKindDaily || !fm.previewCurrent() {
		t.Fatalf("click on Daily: focus %d kind %d", fm.focus, fm.kind)
	}
	if fm.preview.Job.Schedule != "0 9 * * *" {
		t.Errorf("preview schedule = %q", fm.preview.Job.Schedule)
	}
	m = schedClick(t, m, "Schedule", "Days…")
	if fm = formOf(t, m); fm.kind != schedKindDays {
		t.Fatalf("click on Days…: kind %d", fm.kind)
	}
	// Days… starts on Mon-Fri; the first S chip is Saturday.
	m = schedClick(t, m, "Days", "S")
	fm = formOf(t, m)
	if fm.focus != stopDays || !fm.days[5] || fm.dayCur != 5 {
		t.Fatalf("click on S: focus %d days %v cur %d", fm.focus, fm.days, fm.dayCur)
	}
	if fm.preview.Job.Schedule != "0 9 * * 1,2,3,4,5,6" {
		t.Errorf("preview schedule = %q, want Saturday added", fm.preview.Job.Schedule)
	}
	m = schedClick(t, m, "Days", "M")
	if formOf(t, m).days[0] {
		t.Error("a second click on a chip should turn it off")
	}

	m = schedOpenAdvanced(t, m)
	m = schedClick(t, m, "Catch-up", "none")
	if fm = formOf(t, m); fm.focus != stopCatchup || fm.catchup != 1 {
		t.Errorf("click on none: focus %d catchup %d", fm.focus, fm.catchup)
	}
	m = schedClick(t, m, "Worktree", "worktree")
	if formOf(t, m).worktree {
		t.Error("click on the worktree value should toggle it off")
	}
}

func TestSchedFormClickWhenScrolled(t *testing.T) {
	f := newSchedFixture(t)
	withSchedEnv(t, richSchedEnv())
	m := openNewForm(t, f)
	m.termHeight = 28
	m.relayout()
	m = schedOpenAdvanced(t, m)
	formOf(t, m).focusStop(stopUsage7d)
	if out := ansi.Strip(m.render()); strings.Contains(out, "New schedule") {
		t.Fatalf("the form should be scrolled down to the focus:\n%s", out)
	}
	m = schedClick(t, m, "Overlap", "allow")
	if fm := formOf(t, m); fm.focus != stopOverlap || fm.overlap != 1 {
		t.Errorf("click on allow while scrolled: focus %d overlap %d", fm.focus, fm.overlap)
	}
	m = schedClick(t, m, "Skip above", "global")
	if fm := formOf(t, m); fm.focus != stopUsage5h || fm.u5.value() != "50" {
		t.Errorf("click on the 5h value while scrolled: focus %d value %q", fm.focus, fm.u5.value())
	}
}

func TestSchedFormClickWhenDrawerIsFullWidth(t *testing.T) {
	f := newSchedFixture(t)
	withSchedEnv(t, testSchedEnv())
	m := openNewForm(t, f)
	m.termWidth = 40 // narrower than the minimum: the drawer takes the pane
	m.relayout()
	if m.schedDrawerWidth() != m.contentWidth() || m.schedDrawerX() != 0 {
		t.Fatalf("drawer %d at %d in a %d wide pane", m.schedDrawerWidth(), m.schedDrawerX(), m.contentWidth())
	}
	m = schedClick(t, m, "Schedule", "Hourly")
	if fm := formOf(t, m); fm.kind != schedKindHourly {
		t.Errorf("click on Hourly in the full-width drawer: kind %d", fm.kind)
	}
}

func TestSchedFormClickIgnoredWhileSliding(t *testing.T) {
	f := newSchedFixture(t)
	withSchedEnv(t, testSchedEnv())
	m := openNewForm(t, f)
	x, y := schedFind(t, m, "Schedule", "Hourly")
	m.schedAnimOn, m.schedAnimP = true, 0.5
	before := formOf(t, m).kind
	m = schedClickAt(t, m, x, y)
	if formOf(t, m).kind != before {
		t.Error("a click landed on a control while the drawer was sliding")
	}
}

func TestSchedFormClickOutsideIgnoredAndButtons(t *testing.T) {
	f := newSchedFixture(t)
	withSchedEnv(t, testSchedEnv())
	m := openNewForm(t, f)
	sel := m.selSchedule
	m = schedClickAt(t, m, 2, m.fleetListTop())
	if m.schedForm == nil || m.selSchedule != sel {
		t.Fatal("a click on the list must not leave the form")
	}
	m = schedClick(t, m, "Name", "Name")
	m = schedType(t, m, "tidy")
	m = schedClick(t, m, "Prompt", "Prompt")
	m = schedType(t, m, "Tidy up.")
	// The footer's ^s review opens the review; its n back closes it.
	x, y := schedFooterFind(t, m, "^s review")
	m = schedClickAt(t, m, x, y)
	if !formOf(t, m).review {
		t.Fatalf("^s in the footer should open the review: err %q", formOf(t, m).err)
	}
	m = schedClick(t, m, "schedule it", "n back")
	if formOf(t, m).review {
		t.Fatal("n back on the review should return to the form")
	}
	x, y = schedFooterFind(t, m, "^s review")
	m = schedClickAt(t, m, x, y)
	m = schedClick(t, m, "y schedule it", "y")
	if m.schedForm != nil {
		t.Fatalf("y on the review should save: %+v", m.schedForm.err)
	}
	if j := f.job(t, "tidy"); j.Prompt != "Tidy up." {
		t.Errorf("saved job = %+v", j)
	}
}

func schedFooterFind(t *testing.T, m FleetModel, text string) (x, y int) {
	t.Helper()
	lines := strings.Split(ansi.Strip(m.renderList()), "\n")
	y = len(lines) - 1
	i := strings.Index(lines[y], text)
	if i < 0 {
		t.Fatalf("no %q in the hint bar %q", text, lines[y])
	}
	return ansi.StringWidth(lines[y][:i]), y
}

func TestSchedFormBackendBeforeModel(t *testing.T) {
	f := newSchedFixture(t)
	withSchedEnv(t, richSchedEnv())
	m := openNewForm(t, f)
	m = schedOpenAdvanced(t, m)
	stops := formOf(t, m).stops()
	b, mo := slices.Index(stops, stopBackend), slices.Index(stops, stopModel)
	if b < 0 || mo < 0 || b > mo {
		t.Fatalf("stops %v: want Backend (%d) before Model (%d)", stops, b, mo)
	}
	out := ansi.Strip(m.render())
	if bi, mi := strings.Index(out, "Backend"), strings.Index(out, "Model"); bi < 0 || mi < 0 || bi > mi {
		t.Errorf("rendered Backend at %d, Model at %d:\n%s", bi, mi, out)
	}
	m = focusStopT(t, m, stopWorktree)
	m = schedPress(t, m, "tab")
	if got := formOf(t, m).focus; got != stopBackend {
		t.Errorf("tab after Worktree focuses %d, want Backend", got)
	}
}

func backendSchedEnv() schedEnv {
	e := richSchedEnv()
	e.opts.Models["ollama"] = []string{"llama3", "qwen3"}
	return e
}

func TestSchedFormBackendChangeFitsModel(t *testing.T) {
	cases := []struct {
		name, model, backend string
		steps                []string
		wantBackend          string
		wantModel            string
	}{
		// backend options: default (anthropic), anthropic, ollama
		{"anthropic to ollama resets", "opus", "", []string{"right", "right"}, "ollama", ""},
		{"ollama to anthropic resets", "llama3", "ollama", []string{"left"}, launchopts.BackendNone, ""},
		{"compatible kept", "opus", "", []string{"right"}, launchopts.BackendNone, "opus"},
		{"full anthropic id kept", "claude-opus-4-1", "", []string{"right"}, launchopts.BackendNone, "claude-opus-4-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSchedFixture(t)
			withSchedEnv(t, backendSchedEnv())
			f.create(t, cron.Job{Name: "pinned", Schedule: "0 9 * * *", Model: tc.model, Backend: tc.backend})
			m := showSchedules(t, f.model(t))
			m = selectSched(t, m, "pinned")
			m = schedPress(t, m, "e")
			if got := formOf(t, m).model.value(); got != tc.model {
				t.Fatalf("opened with model %q, want the stored %q", got, tc.model)
			}
			formOf(t, m).focusStop(stopBackend)
			m = schedPress(t, m, tc.steps...)
			fm := formOf(t, m)
			if fm.backend.value() != tc.wantBackend || fm.model.value() != tc.wantModel {
				t.Errorf("backend %q model %q, want %q %q", fm.backend.value(), fm.model.value(), tc.wantBackend, tc.wantModel)
			}
		})
	}
}

func TestSchedFormBackendClickResetsModel(t *testing.T) {
	f := newSchedFixture(t)
	withSchedEnv(t, backendSchedEnv())
	m := openNewForm(t, f)
	m = schedOpenAdvanced(t, m)
	formOf(t, m).focusStop(stopModel)
	m = schedPress(t, m, "right", "right") // inherit, opus
	if got := formOf(t, m).model.value(); got != "opus" {
		t.Fatalf("model = %q", got)
	}
	m = schedClick(t, m, "Backend", "‹") // default → ollama (wraps back)
	fm := formOf(t, m)
	if fm.backend.value() != "ollama" || fm.model.value() != "" {
		t.Fatalf("backend %q model %q, want ollama and the default model", fm.backend.value(), fm.model.value())
	}
	if !slices.ContainsFunc(fm.model.opts, func(o schedOpt) bool { return o.value == "llama3" }) {
		t.Errorf("model list not reloaded for ollama: %+v", fm.model.opts)
	}
	if fm.preview == nil || fm.preview.Job.Model != "" {
		t.Errorf("preview not refreshed with the reset model")
	}
}

func TestSchedFormEditKeepsUnknownModelOnOtherBackend(t *testing.T) {
	f := newSchedFixture(t)
	withSchedEnv(t, backendSchedEnv())
	f.create(t, cron.Job{Name: "odd", Schedule: "0 9 * * *", Model: "mystery", Backend: "ollama"})
	m := showSchedules(t, f.model(t))
	m = selectSched(t, m, "odd")
	m = schedPress(t, m, "e")
	fm := formOf(t, m)
	if fm.model.value() != "mystery" {
		t.Fatalf("edit open changed the stored model to %q", fm.model.value())
	}
	// A change elsewhere (agent, project refresh) keeps it too.
	m = focusStopT(t, m, stopAgent)
	fm = formOf(t, m)
	fm.refreshOptions()
	if fm.model.value() != "mystery" {
		t.Errorf("refresh dropped the stored model: %q", fm.model.value())
	}
}

func TestSchedFormClickEditorHint(t *testing.T) {
	f := newSchedFixture(t)
	withSchedEnv(t, testSchedEnv())
	m := openNewForm(t, f)
	m = schedClick(t, m, "Prompt", "Prompt")
	m = schedClick(t, m, schedEditorHint, schedEditorHint)
	if formOf(t, m).editor == nil {
		t.Fatal("a click on the editor hint should open the prompt editor")
	}
}
