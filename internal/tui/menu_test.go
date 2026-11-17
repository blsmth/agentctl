package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/launchopts"
)

func testBackends() map[string]config.BackendConfig {
	return map[string]config.BackendConfig{
		"local": {BaseURL: "http://127.0.0.1:11434", Model: "qwen3:latest"},
		"nous": {Kind: config.BackendKindNous, Model: "qwen/qwen3-coder-next",
			Models: &config.BackendModels{Opus: "anthropic/claude-opus-x"}},
	}
}

func menuTestAgents() []MenuAgent {
	return []MenuAgent{
		{Name: "u1", Description: "user one", Section: SectionUser, Dir: "/work"},
		{Name: "lead", Description: "pinned lead", Section: SectionPinned, Dir: "/work", Model: "opus", Effort: "high"},
		{Name: "proj", Description: "project agent", Section: SectionProject, Dir: "/proj"},
		{Name: "u2", Description: "second user", Section: SectionUser, Dir: "/work", Backend: "nous"},
		{Name: "oll", Description: "local one", Section: SectionUser, Dir: "/work", Backend: "local"},
		{Name: "Explore", Section: SectionBuiltin, Dir: "/work"},
	}
}

func newTestMenu(t *testing.T, mut func(*MenuConfig)) MenuModel {
	t.Helper()
	cfg := MenuConfig{
		Agents:     menuTestAgents(),
		Backends:   testBackends(),
		NousModels: func() []string { return []string{"deep/seek", "qwen/qwen3-coder-next"} },
		Now:        func() time.Time { return time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC) },
	}
	if mut != nil {
		mut(&cfg)
	}
	return NewMenuModel(cfg)
}

func mkey(k string) tea.KeyPressMsg {
	switch k {
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	}
	if strings.HasPrefix(k, "ctrl+") && len(k) == len("ctrl+")+1 {
		return tea.KeyPressMsg{Code: rune(k[len(k)-1]), Mod: tea.ModCtrl}
	}
	r := []rune(k)
	return tea.KeyPressMsg{Code: r[0], Text: k}
}

// press feeds keys to m and returns the final model and every command issued.
func press(t *testing.T, m MenuModel, keys ...string) (MenuModel, []tea.Cmd) {
	t.Helper()
	var cmds []tea.Cmd
	for _, k := range keys {
		next, cmd := m.Update(mkey(k))
		m = next.(MenuModel)
		cmds = append(cmds, cmd)
	}
	return m, cmds
}

// typeText sends each rune of s as a key press.
func typeText(t *testing.T, m MenuModel, s string) MenuModel {
	t.Helper()
	for _, r := range s {
		m, _ = press(t, m, string(r))
	}
	return m
}

// drain runs cmds (flattening batches) and feeds the resulting messages back.
func drain(t *testing.T, m MenuModel, cmds ...tea.Cmd) MenuModel {
	t.Helper()
	var run func(c tea.Cmd)
	run = func(c tea.Cmd) {
		if c == nil {
			return
		}
		switch msg := c().(type) {
		case tea.BatchMsg:
			for _, sub := range msg {
				run(sub)
			}
		case ollamaMsg, sessionsMsg:
			next, _ := m.Update(msg)
			m = next.(MenuModel)
		}
	}
	for _, c := range cmds {
		run(c)
	}
	return m
}

// moveTo highlights the agent called name with up/down.
func moveTo(t *testing.T, m MenuModel, name string) MenuModel {
	t.Helper()
	for i := 0; i < len(m.agents)+1; i++ {
		m, _ = press(t, m, "up")
	}
	for i := 0; i < len(m.agents)+1; i++ {
		if a, ok := m.current(); ok && a.Name == name {
			return m
		}
		m, _ = press(t, m, "down")
	}
	t.Fatalf("agent %q not found", name)
	return m
}

// visibleNames lists the names of the ranked, visible agents.
func visibleNames(m MenuModel) []string {
	var out []string
	for _, idx := range m.visible {
		out = append(out, m.agents[idx].Name)
	}
	return out
}

func headers(m MenuModel) []string {
	rows, _ := m.listRows(60)
	var out []string
	for _, r := range rows {
		if r.header {
			if h := strings.Fields(ansi.Strip(r.text)); len(h) > 0 && !strings.HasPrefix(h[0], "─") {
				out = append(out, h[0])
			}
		}
	}
	return out
}

func TestSectionGrouping(t *testing.T) {
	tests := []struct {
		name   string
		agents []MenuAgent
		want   []string
	}{
		{"all sections in order", menuTestAgents(), []string{"PINNED", "PROJECT", "USER", "BUILT-IN"}},
		{"empty sections skipped", []MenuAgent{
			{Name: "b", Section: SectionBuiltin}, {Name: "u", Section: SectionUser}}, []string{"USER", "BUILT-IN"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestMenu(t, func(c *MenuConfig) { c.Agents = tc.agents })
			got := headers(m)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("headers = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFilterNarrows(t *testing.T) {
	m := newTestMenu(t, nil)
	m = typeText(t, m, "user")
	if len(m.visible) != 2 {
		t.Fatalf("visible = %d, want 2 (u1, u2)", len(m.visible))
	}
	if got := headers(m); len(got) != 0 {
		t.Errorf("section headers shown while filtering: %v", got)
	}
	if m.cursor != 0 {
		t.Errorf("cursor = %d, want top match", m.cursor)
	}
	m, _ = press(t, m, "esc")
	if len(m.visible) != len(menuTestAgents()) || m.filter.Value() != "" {
		t.Errorf("esc should clear the filter, visible = %d", len(m.visible))
	}
	_, cmds := press(t, m, "esc")
	if cmds[0] == nil {
		t.Error("second esc should cancel")
	}
	m = typeText(t, m, "zz")
	m, _ = press(t, m, "ctrl+u")
	if m.filter.Value() != "" {
		t.Error("ctrl+u should clear the filter")
	}
}

func TestFilterRanking(t *testing.T) {
	m := newTestMenu(t, func(c *MenuConfig) {
		c.Agents = []MenuAgent{
			{Name: "go-engineer-opus", Description: "opus variant", Section: SectionUser},
			{Name: "docs", Description: "writes go docs", Section: SectionPinned},
			{Name: "mongo", Section: SectionUser},
			{Name: "go-engineer", Description: "senior", Section: SectionUser},
		}
	})
	m = typeText(t, m, "go")
	got := strings.Join(visibleNames(m), ",")
	if want := "go-engineer,go-engineer-opus,mongo,docs"; got != want {
		t.Errorf("ranking = %s, want %s", got, want)
	}
	if a, _ := m.current(); a.Name != "go-engineer" {
		t.Errorf("cursor on %q, want the top match", a.Name)
	}
}

func TestBackendChangeResetsModels(t *testing.T) {
	m := newTestMenu(t, nil)
	m = moveTo(t, m, "lead") // anthropic, model opus
	if m.opts.model != "opus" || m.opts.backend != backendAnthropic {
		t.Fatalf("lead defaults = %q/%q", m.opts.backend, m.opts.model)
	}
	m, _ = press(t, m, "tab") // backend field
	m, _ = press(t, m, "right")
	if m.opts.backend != "local" || m.opts.model != "qwen3:latest" {
		t.Errorf("after cycling: backend=%q model=%q", m.opts.backend, m.opts.model)
	}
	m, _ = press(t, m, "right")
	if m.opts.backend != "nous" || m.opts.model != "qwen/qwen3-coder-next" {
		t.Errorf("nous: backend=%q model=%q", m.opts.backend, m.opts.model)
	}
	var vals []string
	for _, c := range m.modelChoices() {
		vals = append(vals, c.Value)
	}
	want := "qwen/qwen3-coder-next,anthropic/claude-opus-x,deep/seek"
	if strings.Join(vals, ",") != want {
		t.Errorf("nous choices = %v, want %s", vals, want)
	}
	// Pick a model through the picker, then switch backend: the pick resets.
	m, _ = press(t, m, "down", "space")
	if m.view != viewPicker {
		t.Fatal("space on Model should open the picker")
	}
	m = typeText(t, m, "deep")
	m, _ = press(t, m, "enter")
	if m.view != viewSheet || m.opts.model != "deep/seek" {
		t.Fatalf("picker pick: view=%v model=%q", m.view, m.opts.model)
	}
	m, _ = press(t, m, "up", "right") // wraps to anthropic
	if m.opts.backend != backendAnthropic || m.opts.model != "opus" {
		t.Errorf("wrapped: backend=%q model=%q", m.opts.backend, m.opts.model)
	}
}

func TestOllamaStates(t *testing.T) {
	calls := 0
	var fetchErr error
	models := []string{"llama3:8b", "qwen3:latest"}
	m := newTestMenu(t, func(c *MenuConfig) {
		c.FetchOllama = func(ctx context.Context, name string) ([]string, error) {
			calls++
			if _, ok := ctx.Deadline(); !ok {
				t.Error("fetch has no deadline")
			}
			return models, fetchErr
		}
	})
	m = moveTo(t, m, "oll")
	// Moving onto the agent started the fetch: pending state, no model list yet.
	if st := m.ollama["local"]; st == nil || st.done {
		t.Fatalf("expected a pending state, got %+v", st)
	}
	if n := strings.Join(m.modelNotes(), "|"); !strings.Contains(n, "loading") {
		t.Errorf("pending notes = %q", n)
	}
	if got := len(m.modelChoices()); got != 1 {
		t.Errorf("choices while loading = %d, want only the configured model", got)
	}

	pickerValues := func(mm MenuModel) []string {
		mm, _ = press(t, mm, "tab", "down", "space")
		var vals []string
		for _, it := range mm.picker.items {
			vals = append(vals, it.value)
		}
		return vals
	}

	t.Run("cmd runs the injected fetcher once", func(t *testing.T) {
		calls = 0
		fresh := newTestMenu(t, func(c *MenuConfig) {
			c.FetchOllama = func(ctx context.Context, name string) ([]string, error) {
				calls++
				if _, ok := ctx.Deadline(); !ok {
					t.Error("fetch has no deadline")
				}
				return models, nil
			}
		})
		var all []tea.Cmd
		for i := 0; i < 6; i++ { // walk the list onto and past "oll"
			var cs []tea.Cmd
			fresh, cs = press(t, fresh, "down")
			all = append(all, cs...)
		}
		fresh = drain(t, fresh, all...)
		if calls != 1 {
			t.Errorf("fetcher calls = %d, want 1", calls)
		}
		if st := fresh.ollama["local"]; st == nil || !st.done || len(st.models) != 2 {
			t.Errorf("state = %+v", st)
		}
	})
	t.Run("success", func(t *testing.T) {
		next, _ := m.Update(ollamaMsg{backend: "local", models: models})
		mm := next.(MenuModel)
		if got := len(mm.modelChoices()); got != 2 { // qwen3 dedupes with :latest
			t.Errorf("choices = %d, want 2", got)
		}
		if got := strings.Join(pickerValues(mm), ","); got != "qwen3:latest,llama3:8b" {
			t.Errorf("picker items = %s", got)
		}
		if w := mm.modelWarning(); w != "" {
			t.Errorf("unexpected warning %q", w)
		}
	})
	t.Run("picker updates when the list arrives while open", func(t *testing.T) {
		mm := moveTo(t, newTestMenu(t, nil), "oll")
		mm, _ = press(t, mm, "tab", "down", "space")
		if len(mm.picker.items) != 1 {
			t.Fatalf("items while loading = %d", len(mm.picker.items))
		}
		next, _ := mm.Update(ollamaMsg{backend: "local", models: models})
		if got := len(next.(MenuModel).picker.items); got != 2 {
			t.Errorf("items after load = %d, want 2", got)
		}
	})
	t.Run("default missing warns", func(t *testing.T) {
		next, _ := m.Update(ollamaMsg{backend: "local", models: []string{"llama3:8b"}})
		mm := next.(MenuModel)
		if w := mm.modelWarning(); !strings.Contains(w, "preflight will fail") {
			t.Errorf("warning = %q", w)
		}
		if got := mm.modelChoices(); !got[0].Default || got[0].Value != "qwen3:latest" {
			t.Errorf("configured model must stay first and default: %+v", got)
		}
	})
	t.Run("error keeps configured model", func(t *testing.T) {
		next, _ := m.Update(ollamaMsg{backend: "local", err: errors.New("connection refused")})
		mm := next.(MenuModel)
		if n := strings.Join(mm.modelNotes(), "|"); !strings.Contains(n, "server unreachable: connection refused") {
			t.Errorf("notes = %q", n)
		}
		if got := len(mm.modelChoices()); got != 1 {
			t.Errorf("choices = %d, want 1", got)
		}
	})
	t.Run("one fetch per backend", func(t *testing.T) {
		calls = 0
		// The fetch for "local" already started; moving away and back must not
		// start another one.
		m2 := moveTo(t, m, "u1")
		m2 = moveTo(t, m2, "oll")
		drain(t, m2, m2.fetchCmds())
		if calls != 0 {
			t.Errorf("fetcher ran %d times for an already-started fetch", calls)
		}
	})
}

func TestNousBareModelRejected(t *testing.T) {
	m := newTestMenu(t, nil)
	m = moveTo(t, m, "u2") // nous default
	m, _ = press(t, m, "tab", "down", "space")
	if m.view != viewPicker {
		t.Fatalf("view = %v, want picker", m.view)
	}
	m = typeText(t, m, "opus")
	if m.picker.err != "" {
		t.Errorf("warning shown while matches exist: %q", m.picker.err)
	}
	for _, r := range m.picker.rows {
		if r.kind == pickTyped {
			t.Fatal("as-typed row offered for a bare nous model")
		}
	}
	m.picker.input.SetValue("")
	m = typeText(t, m, "zzqq")
	if !strings.Contains(m.picker.err, "not a Nous model slug") {
		t.Fatalf("bare model with no matches not rejected: err=%q", m.picker.err)
	}
	m.picker.input.SetValue("")
	m = typeText(t, m, "vendor/model-x")
	last := m.picker.rows[len(m.picker.rows)-1]
	if last.kind != pickTyped || last.typed != "vendor/model-x" {
		t.Fatalf("as-typed row missing: %+v", m.picker.rows)
	}
	m, _ = press(t, m, "enter")
	if m.view != viewSheet || m.opts.model != "vendor/model-x" || m.errMsg != "" {
		t.Fatalf("slug rejected: view=%v model=%q err=%q", m.view, m.opts.model, m.errMsg)
	}
}

func TestModelPickerFuzzy(t *testing.T) {
	var ids []string
	for i := 0; i < 402; i++ {
		ids = append(ids, fmt.Sprintf("org%d/model-%03d", i%7, i))
	}
	ids = append(ids, "acme/coder-pro", "zeta/some-coder-lite")
	m := newTestMenu(t, func(c *MenuConfig) { c.NousModels = func() []string { return ids } })
	m = moveTo(t, m, "u2")
	m, _ = press(t, m, "tab", "down", "space")
	if len(m.picker.items) < 402 {
		t.Fatalf("items = %d", len(m.picker.items))
	}
	if h := m.picker.rows[0]; h.kind != pickHeader || h.text != "DEFAULT" {
		t.Errorf("first row = %+v, want DEFAULT header", h)
	}
	if v, _ := m.picker.selected(); v != "qwen/qwen3-coder-next" {
		t.Errorf("empty-query cursor on %q, want the current model", v)
	}

	m = typeText(t, m, "coder")
	v, ok := m.picker.selected()
	if !ok || !strings.Contains(v, "coder") {
		t.Errorf("top match for coder = %q", v)
	}
	if len(m.picker.rows) > 40 {
		t.Errorf("query should narrow 402 ids, got %d rows", len(m.picker.rows))
	}
	m.picker.input.SetValue("")
	m = typeText(t, m, "claude-opus")
	if v, _ := m.picker.selected(); v != "anthropic/claude-opus-x" {
		t.Errorf("top match for claude-opus = %q", v)
	}
	// Multi-word queries match every term, in any case.
	m.picker.input.SetValue("")
	m = typeText(t, m, "qwen coder")
	if v, _ := m.picker.selected(); v != "qwen/qwen3-coder-next" {
		t.Errorf("top match for %q = %q", "qwen coder", v)
	}
	if m.picker.err != "" {
		t.Errorf("as-typed warning shown despite matches: %q", m.picker.err)
	}
	m.picker.input.SetValue("")
	m = typeText(t, m, "coder zzzz")
	if _, ok := m.picker.selected(); ok && m.picker.rows[m.picker.cursor].kind == pickModel {
		t.Error("a term with no match must exclude the item")
	}
	// An id nothing fuzzy-matches but validateModel accepts is offered as typed.
	m.picker.input.SetValue("")
	m = typeText(t, m, "q/xyz")
	if v, _ := m.picker.selected(); v != "q/xyz" {
		t.Errorf("as-typed selection = %q", v)
	}
	// Windowed rendering stays within bounds, even for the full list.
	m.picker.input.SetValue("")
	m.picker.refilter(m, m.opts.model)
	for _, h := range []int{3, 8, 30} {
		lines := m.pickerLines(60, h)
		if len(lines) != h {
			t.Errorf("height %d: %d lines", h, len(lines))
		}
		for _, l := range lines {
			if w := ansi.StringWidth(l); w > 60 {
				t.Errorf("height %d: line width %d", h, w)
			}
		}
	}
	// Moving by key never lands on a header.
	for i := 0; i < 5; i++ {
		m, _ = press(t, m, "down")
		if m.picker.rows[m.picker.cursor].kind == pickHeader {
			t.Fatal("cursor on a header")
		}
	}
}

func TestLaunchResult(t *testing.T) {
	tests := []struct {
		name  string
		agent string
		keys  []string
		want  MenuResult
	}{
		{"defaults defer to config", "u1", []string{"enter"},
			MenuResult{Action: "launch", Agent: "u1", Dir: "/work"}},
		{"anthropic model effort worktree", "u1",
			[]string{"tab", "down", "space", "o", "p", "u", "s", "enter", "down", "right", "right", "down", "down", "right", "enter"},
			MenuResult{Action: "launch", Agent: "u1", Dir: "/work", Model: "opus", Effort: "medium", NoWorktree: true}},
		{"anthropic for backend agent is none", "u2",
			[]string{"tab", "left", "left", "enter"},
			MenuResult{Action: "launch", Agent: "u2", Dir: "/work", Backend: launchopts.BackendNone}},
		{"other backend", "u1",
			[]string{"tab", "right", "enter"},
			MenuResult{Action: "launch", Agent: "u1", Dir: "/work", Backend: "local"}},
		{"effort inherit clears configured", "lead",
			[]string{"tab", "down", "down", "right", "right", "enter"},
			MenuResult{Action: "launch", Agent: "lead", Dir: "/work", Effort: launchopts.Inherit}},
		{"model inherit clears configured", "lead",
			[]string{"tab", "down", "space", "i", "n", "h", "e", "r", "i", "t", "enter", "enter"},
			MenuResult{Action: "launch", Agent: "lead", Dir: "/work", Model: launchopts.Inherit}},
		{"ctrl+u resets the sheet", "lead",
			[]string{"tab", "right", "down", "down", "right", "ctrl+u", "enter"},
			MenuResult{Action: "launch", Agent: "lead", Dir: "/work"}},
		{"create row via no match", "",
			[]string{"z", "z", "q", "enter"}, MenuResult{Action: "create"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestMenu(t, nil)
			if tc.agent != "" {
				m = moveTo(t, m, tc.agent)
			}
			m, _ = press(t, m, tc.keys...)
			if got := m.Result(); got != tc.want {
				t.Errorf("result = %+v\nwant     %+v", got, tc.want)
			}
		})
	}
}

func TestEffortAndDirEditing(t *testing.T) {
	m := newTestMenu(t, nil)
	m = moveTo(t, m, "u1")
	m, _ = press(t, m, "tab", "down", "down", "down", "space")
	if m.editing != editDir {
		t.Fatal("space on Dir should edit it")
	}
	m.edit.SetValue("")
	m = typeText(t, m, "/tmp/x")
	m, _ = press(t, m, "enter", "enter")
	if got := m.Result(); got.Dir != "/tmp/x" || got.Action != "launch" {
		t.Errorf("result = %+v", got)
	}
}

func TestChangedMarkers(t *testing.T) {
	m := newTestMenu(t, nil)
	m = moveTo(t, m, "u1")
	m, _ = press(t, m, "tab")
	if strings.Contains(ansi.Strip(strings.Join(m.sheetLines(80, 12), "\n")), "•") {
		t.Error("fresh sheet should show no changed markers")
	}
	m, _ = press(t, m, "down", "down", "right")
	if !strings.Contains(ansi.Strip(strings.Join(m.sheetLines(80, 12), "\n")), "• ") {
		t.Error("changed effort should be marked")
	}
}

func TestResumeFlow(t *testing.T) {
	now := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	m := newTestMenu(t, func(c *MenuConfig) {
		c.LoadSessions = func(string) []MenuSession {
			return []MenuSession{
				{ID: "old", When: now.Add(-3 * time.Hour)},
				{ID: "live", When: now.Add(-time.Minute), Live: true},
				{ID: "new", When: now.Add(-10 * time.Minute)},
			}
		}
	})
	m = drain(t, m, m.Init())
	ss := m.currentSessions()
	if len(ss) != 2 || ss[0].ID != "new" || ss[1].ID != "old" {
		t.Fatalf("sessions = %+v", ss)
	}
	m, _ = press(t, m, "ctrl+r", "down", "enter")
	got := m.Result()
	if got.Action != "resume" || got.SessionID != "old" || got.Dir == "" {
		t.Errorf("result = %+v", got)
	}
}

func TestResumeWithoutSessionsHints(t *testing.T) {
	m := newTestMenu(t, nil)
	m, _ = press(t, m, "ctrl+r")
	if m.view != viewList || m.notice == "" {
		t.Errorf("view=%v notice=%q, want list with a hint", m.view, m.notice)
	}
}

func TestEscBacksOutBeforeCancel(t *testing.T) {
	m := newTestMenu(t, func(c *MenuConfig) {
		c.LoadSessions = func(string) []MenuSession {
			return []MenuSession{{ID: "s", When: time.Date(2026, 1, 10, 11, 0, 0, 0, time.UTC)}}
		}
	})
	m = drain(t, m, m.Init())
	m, _ = press(t, m, "ctrl+r")
	if m.view != viewResume {
		t.Fatal("ctrl+r should show sessions")
	}
	m, _ = press(t, m, "esc")
	if m.view != viewList || m.Result().Cancelled {
		t.Fatal("esc should return to the list first")
	}
	m, _ = press(t, m, "tab", "down", "space")
	if m.view != viewPicker {
		t.Fatal("space on Model should open the picker")
	}
	m, _ = press(t, m, "esc")
	if m.view != viewSheet {
		t.Fatal("esc should leave the picker only")
	}
	m, _ = press(t, m, "esc")
	if m.view != viewList {
		t.Fatal("esc should leave the sheet")
	}
	m = typeText(t, m, "u1")
	m, _ = press(t, m, "esc")
	if m.filter.Value() != "" || m.Result().Cancelled {
		t.Fatal("esc should clear the filter first")
	}
	m, _ = press(t, m, "esc")
	if !m.Result().Cancelled {
		t.Fatal("esc on an empty list filter should cancel")
	}
}

func TestSwitchToRunning(t *testing.T) {
	m := newTestMenu(t, func(c *MenuConfig) {
		c.Agents = []MenuAgent{
			{Name: "lead", Section: SectionPinned, Dir: "/work", Instances: []string{"lead:2", "lead"}},
			{Name: "idle", Section: SectionUser, Dir: "/work"},
		}
	})
	m = moveTo(t, m, "idle")
	m, cmds := press(t, m, "ctrl+g")
	if m.notice == "" || cmds[0] != nil || m.Result().Action != "" {
		t.Errorf("not-running agent should hint, got notice=%q result=%+v", m.notice, m.Result())
	}
	m = moveTo(t, m, "lead")
	m, _ = press(t, m, "ctrl+g")
	want := MenuResult{Action: "switch", Agent: "lead", Instance: "lead:2"}
	if got := m.Result(); got != want {
		t.Errorf("result = %+v, want %+v", got, want)
	}
}

func TestSelectionVisibleInOptions(t *testing.T) {
	m := newTestMenu(t, nil)
	m = moveTo(t, m, "proj")
	for _, keys := range [][]string{nil, {"tab"}, {"down", "space"}, {"esc", "esc"}} {
		m, _ = press(t, m, keys...)
		rows, cur := m.listRows(80)
		if !rows[cur].sel {
			t.Errorf("after %v: highlighted row not marked selected", keys)
		}
		if a, ok := m.current(); !ok || a.Name != "proj" {
			t.Errorf("after %v: highlight moved to %v", keys, a.Name)
		}
	}
	m, _ = press(t, m, "tab")
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "▌ Backend") || !strings.Contains(view, "Launch proj") {
		t.Errorf("sheet should show the focused field and the agent:\n%s", view)
	}
}

func TestRowDisplayWidth(t *testing.T) {
	names := []string{"agent-with-a-rather-long-name", "日本語のエージェント名前がとても長い", "short"}
	labels := []string{"nous · qwen/qwen3-coder-next", "ローカル · 日本語モデル—“quotes”", "inherit", ""}
	for _, width := range []int{98, 80, 64, 59, 55, 49, 38} {
		for _, sel := range []bool{false, true} {
			for _, name := range names {
				for _, label := range labels {
					for _, inst := range [][]string{nil, {"a"}, {"a", "a~2", "a~3"}} {
						a := MenuAgent{Name: name, Instances: inst}
						row := renderAgentRow(a, label, len(inst) == 0, 24, sel, width)
						if w := ansi.StringWidth(row); w != width {
							t.Errorf("w=%d sel=%v name=%q label=%q n=%d: width %d", width, sel, name, label, len(inst), w)
						}
					}
				}
			}
		}
	}
	if got := truncW("日本語日本語", 7); ansi.StringWidth(got) > 7 || !strings.HasSuffix(got, "…") {
		t.Errorf("truncW = %q", got)
	}
}

func TestRowColumns(t *testing.T) {
	a := MenuAgent{Name: "go-engineer", Instances: []string{"a", "b", "c"}}
	wide := ansi.Strip(renderAgentRow(a, "sonnet", false, 12, false, 80))
	if !strings.Contains(wide, "3 running") || !strings.Contains(wide, "sonnet") {
		t.Errorf("wide row = %q", wide)
	}
	mid := ansi.Strip(renderAgentRow(a, "sonnet", false, 12, false, 55))
	if strings.Contains(mid, "running") || !strings.Contains(mid, "×3") || !strings.Contains(mid, "sonnet") {
		t.Errorf("mid row = %q", mid)
	}
	narrow := ansi.Strip(renderAgentRow(a, "sonnet", false, 12, false, 45))
	if strings.Contains(narrow, "sonnet") || !strings.Contains(narrow, "go-engineer ×3") {
		t.Errorf("narrow row = %q", narrow)
	}
}

func TestModelLabel(t *testing.T) {
	m := newTestMenu(t, nil)
	tests := []struct {
		a    MenuAgent
		want string
	}{
		{MenuAgent{Name: "x"}, "inherit"},
		{MenuAgent{Name: "x", Model: "opus"}, "opus"},
		{MenuAgent{Name: "x", Backend: "local"}, "local · qwen3:latest"},
		{MenuAgent{Name: "x", Backend: "nous"}, "nous · qwen/qwen3-coder-next"},
	}
	for _, tc := range tests {
		if got := m.modelLabel(tc.a); got != tc.want {
			t.Errorf("modelLabel(%+v) = %q, want %q", tc.a, got, tc.want)
		}
	}
}

func TestNormalizeSessions(t *testing.T) {
	now := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	in := []MenuSession{
		{ID: "12m", When: now.Add(-12 * time.Minute)},
		{ID: "live", When: now.Add(-time.Second), Live: true},
		{ID: "11m", When: now.Add(-11 * time.Minute)},
		{ID: "ancient", When: now.Add(-30 * 24 * time.Hour)},
		{ID: "nowhen"},
		{ID: "1h", When: now.Add(-time.Hour)},
	}
	got := NormalizeSessions(in, now, 7*24*time.Hour, 3)
	var ids []string
	for _, s := range got {
		ids = append(ids, s.ID)
	}
	if strings.Join(ids, ",") != "11m,12m,1h" {
		t.Errorf("ids = %v", ids)
	}
}

func TestPriceHint(t *testing.T) {
	m := newTestMenu(t, nil)
	m = moveTo(t, m, "oll")
	if got := m.priceHint(); !strings.Contains(got, "free") {
		t.Errorf("ollama hint = %q", got)
	}
	m = moveTo(t, m, "u2")
	if got := m.priceHint(); !strings.Contains(got, "metered") {
		t.Errorf("nous hint = %q", got)
	}
	if got := m.priceHint(); !strings.Contains(got, "price unknown") && !strings.Contains(got, "per MTok") {
		t.Errorf("nous hint = %q", got)
	}
}

func TestViewFits(t *testing.T) {
	for _, size := range [][2]int{{118, 38}, {98, 38}, {88, 38}, {80, 30}, {60, 20}, {40, 12}} {
		for _, keys := range [][]string{nil, {"tab"}, {"tab", "down", "space"}, {"ctrl+r"}} {
			m := newTestMenu(t, func(c *MenuConfig) {
				c.LoadSessions = func(string) []MenuSession {
					return []MenuSession{{ID: "s", When: time.Date(2026, 1, 10, 11, 0, 0, 0, time.UTC), Preview: "Fix chrome bar flicker on resize"}}
				}
			})
			next, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			m = moveTo(t, next.(MenuModel), "oll")
			sessMsg, _ := m.Update(sessionsMsg{agent: "oll", list: m.cfg.LoadSessions("oll")})
			m = sessMsg.(MenuModel)
			m, _ = press(t, m, keys...)
			lines := strings.Split(m.View().Content, "\n")
			if len(lines) != size[1] {
				t.Errorf("%v %v: %d lines, want %d", size, keys, len(lines), size[1])
			}
			for _, l := range lines {
				if w := ansi.StringWidth(l); w > size[0] {
					t.Errorf("%v %v: line width %d exceeds %q", size, keys, w, l)
				}
			}
			if last := ansi.Strip(lines[len(lines)-1]); strings.TrimSpace(last) == "" {
				t.Errorf("%v %v: footer missing on the last line", size, keys)
			}
		}
	}
}

func TestViewListLayout(t *testing.T) {
	m := newTestMenu(t, nil)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 98, Height: 38})
	m = next.(MenuModel)
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	if !strings.Contains(lines[0], "Launch agent") || !strings.Contains(lines[0], "agents ·") {
		t.Errorf("header = %q", lines[0])
	}
	if !strings.Contains(lines[1], "> ") || !strings.Contains(lines[1], "type to filter") {
		t.Errorf("filter line = %q", lines[1])
	}
	all := strings.Join(lines, "\n")
	for _, want := range []string{"PINNED", "BUILT-IN", "+ new agent…", "ctrl+g go to running"} {
		if !strings.Contains(all, want) {
			t.Errorf("view lacks %q:\n%s", want, all)
		}
	}
}

func TestFooterDropsWholeSegments(t *testing.T) {
	m := newTestMenu(t, nil)
	full := ansi.Strip(m.footer(200))
	for w := ansi.StringWidth(full); w >= 8; w-- {
		got := ansi.Strip(m.footer(w))
		if ansi.StringWidth(got) > w {
			t.Fatalf("footer(%d) too wide: %q", w, got)
		}
		if !strings.HasPrefix(full, got) {
			t.Fatalf("footer(%d) = %q is not a prefix of whole segments of %q", w, got, full)
		}
		if got != full && !strings.HasPrefix(full[len(got):], "  ") {
			t.Fatalf("footer(%d) = %q cut mid-hint of %q", w, got, full)
		}
	}
}

func TestViewTooSmall(t *testing.T) {
	m := newTestMenu(t, nil)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 39, Height: 9})
	got := next.(MenuModel).View().Content
	if !strings.Contains(got, "too small") || strings.Contains(got, "\n") {
		t.Errorf("view = %q", got)
	}
}

func TestAnthropicPickerListsFullIDs(t *testing.T) {
	m := newTestMenu(t, nil)
	m = moveTo(t, m, "u1")
	m, _ = press(t, m, "tab", "down", "space")
	var headers []string
	for _, r := range m.picker.rows {
		if r.kind == pickHeader {
			headers = append(headers, r.text)
		}
	}
	if got := strings.Join(headers, ","); got != "DEFAULT,ALIASES,MODELS" {
		t.Fatalf("headers = %s", got)
	}
	items := map[string]pickItem{}
	for _, it := range m.picker.items {
		items[it.value] = it
	}
	if it, ok := items["fable"]; !ok || !it.alias || it.price != "sub" {
		t.Errorf("fable alias = %+v, ok=%v", it, ok)
	}
	it, ok := items["claude-opus-5-5"]
	if !ok || it.alias || it.price == "" || it.note != "opus-5.5" {
		t.Errorf("claude-opus-5-5 = %+v, ok=%v", it, ok)
	}
	for _, legacy := range []string{"claude-opus-4", "claude-sonnet-4", "claude-haiku-3-5"} {
		if _, ok := items[legacy]; ok {
			t.Errorf("legacy key %s listed", legacy)
		}
	}

	m = typeText(t, m, "sonnet 5")
	found := false
	for _, r := range m.picker.rows {
		if r.kind == pickModel && m.picker.items[r.item].value == "claude-sonnet-5-5" {
			found = true
		}
	}
	if !found {
		t.Error(`query "sonnet 5" did not find claude-sonnet-5-5`)
	}
}

func TestLaunchFullAnthropicID(t *testing.T) {
	m := newTestMenu(t, nil)
	m = moveTo(t, m, "u1")
	m, _ = press(t, m, "tab", "down", "space")
	m = typeText(t, m, "claude-opus-5-5")
	m, _ = press(t, m, "enter", "enter")
	want := MenuResult{Action: "launch", Agent: "u1", Dir: "/work", Model: "claude-opus-5-5"}
	if got := m.Result(); got != want {
		t.Errorf("result = %+v, want %+v", got, want)
	}
}

func TestModelLabelFullID(t *testing.T) {
	m := newTestMenu(t, nil)
	if got := m.modelLabel(MenuAgent{Name: "x", Model: "claude-opus-5-5"}); got != "opus-5.5" {
		t.Errorf("label = %q", got)
	}
}
