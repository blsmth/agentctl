package tui

import (
	"context"
	"errors"
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

// moveTo highlights the agent called name.
func moveTo(t *testing.T, m MenuModel, name string) MenuModel {
	t.Helper()
	for i := 0; i < len(m.agents)+1; i++ {
		m, _ = press(t, m, "k")
	}
	for i := 0; i < len(m.agents)+1; i++ {
		if a, ok := m.current(); ok && a.Name == name {
			return m
		}
		m, _ = press(t, m, "j")
	}
	t.Fatalf("agent %q not found", name)
	return m
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
		{"all sections in order", menuTestAgents(), []string{"Pinned", "Project", "User", "Built-in"}},
		{"empty sections skipped", []MenuAgent{
			{Name: "b", Section: SectionBuiltin}, {Name: "u", Section: SectionUser}}, []string{"User", "Built-in"}},
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
	m, _ = press(t, m, "/")
	if !m.filtering {
		t.Fatal("/ did not start filtering")
	}
	m = typeText(t, m, "user")
	if len(m.visible) != 2 {
		t.Fatalf("visible = %d, want 2 (u1, u2)", len(m.visible))
	}
	if got := strings.Join(headers(m), ","); got != "User" {
		t.Errorf("headers after filter = %q, want User only", got)
	}
	m, _ = press(t, m, "enter")
	if m.filtering || len(m.visible) != 2 {
		t.Errorf("enter should keep the filter and leave input mode")
	}
	m, _ = press(t, m, "esc")
	if len(m.visible) != len(menuTestAgents()) {
		t.Errorf("esc should clear the filter, visible = %d", len(m.visible))
	}
	_, cmds := press(t, m, "esc")
	if cmds[0] == nil {
		t.Error("second esc should cancel")
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
	m, _ = press(t, m, "right") // wraps to anthropic
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
			fresh, cs = press(t, fresh, "j")
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
		if w := mm.modelWarning(); w != "" {
			t.Errorf("unexpected warning %q", w)
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
	m, _ = press(t, m, "tab", "j")
	if m.field != fModel {
		t.Fatalf("field = %v", m.field)
	}
	m, _ = press(t, m, "e")
	m.edit.SetValue("")
	m = typeText(t, m, "opus")
	m, _ = press(t, m, "enter")
	if m.editing != editModel || !strings.Contains(m.errMsg, "not a Nous model slug") {
		t.Fatalf("bare alias accepted: editing=%v err=%q", m.editing, m.errMsg)
	}
	m.edit.SetValue("")
	m = typeText(t, m, "vendor/model-x")
	m, _ = press(t, m, "enter")
	if m.editing != editNone || m.opts.model != "vendor/model-x" || m.errMsg != "" {
		t.Fatalf("slug rejected: editing=%v model=%q err=%q", m.editing, m.opts.model, m.errMsg)
	}
	if !hasModel(m.modelChoices(), "vendor/model-x") {
		t.Error("typed model should become a choice")
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
			[]string{"tab", "j", "right", "j", "right", "right", "j", "j", "right", "enter"},
			MenuResult{Action: "launch", Agent: "u1", Dir: "/work", Model: "opus", Effort: "medium", NoWorktree: true}},
		{"anthropic for backend agent is none", "u2",
			[]string{"tab", "left", "left", "enter"},
			MenuResult{Action: "launch", Agent: "u2", Dir: "/work", Backend: launchopts.BackendNone}},
		{"other backend", "u1",
			[]string{"tab", "right", "enter"},
			MenuResult{Action: "launch", Agent: "u1", Dir: "/work", Backend: "local"}},
		{"effort inherit clears configured", "lead",
			[]string{"tab", "j", "j", "right", "right", "enter"},
			MenuResult{Action: "launch", Agent: "lead", Dir: "/work", Effort: launchopts.Inherit}},
		{"model inherit clears configured", "lead",
			[]string{"tab", "j", "right", "enter"},
			MenuResult{Action: "launch", Agent: "lead", Dir: "/work", Model: launchopts.Inherit}},
		{"create row", "",
			[]string{"n"}, MenuResult{Action: "create"}},
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
	m, _ = press(t, m, "d")
	if m.editing != editDir {
		t.Fatal("d should edit the dir")
	}
	m.edit.SetValue("")
	m = typeText(t, m, "/tmp/x")
	m, _ = press(t, m, "enter", "enter")
	if got := m.Result(); got.Dir != "/tmp/x" || got.Action != "launch" {
		t.Errorf("result = %+v", got)
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
	m, _ = press(t, m, "r", "j", "enter")
	got := m.Result()
	if got.Action != "resume" || got.SessionID != "old" || got.Dir == "" {
		t.Errorf("result = %+v", got)
	}
}

func TestEscBacksOutBeforeCancel(t *testing.T) {
	m := newTestMenu(t, func(c *MenuConfig) {
		c.LoadSessions = func(string) []MenuSession {
			return []MenuSession{{ID: "s", When: time.Date(2026, 1, 10, 11, 0, 0, 0, time.UTC)}}
		}
	})
	m = drain(t, m, m.Init())
	m, _ = press(t, m, "r")
	if m.focus != menuFocusSessions {
		t.Fatal("r should focus sessions")
	}
	m, _ = press(t, m, "esc")
	if m.focus != menuFocusList || m.Result().Cancelled {
		t.Fatal("esc should return to the list first")
	}
	m, _ = press(t, m, "tab", "e") // field backend: e is a no-op
	m, _ = press(t, m, "j", "e")   // model field: starts edit
	if m.editing != editModel {
		t.Fatal("e should edit the model")
	}
	m, _ = press(t, m, "esc")
	if m.editing != editNone || m.focus != menuFocusOpts {
		t.Fatal("esc should leave edit mode only")
	}
	m, _ = press(t, m, "esc")
	if m.focus != menuFocusList {
		t.Fatal("esc should leave options")
	}
	m, _ = press(t, m, "esc")
	if !m.Result().Cancelled {
		t.Fatal("esc on the list should cancel")
	}
}

func TestRowDisplayWidth(t *testing.T) {
	descs := []string{
		"Plain ascii description that is quite long and will be truncated somewhere",
		"日本語の説明文がとても長いのでここで切り詰められる — em–dash “quotes” ellipsis…",
		"short — ok",
		"",
	}
	for _, sel := range []bool{false, true} {
		for _, d := range descs {
			a := MenuAgent{Name: "agent-with-a-rather-long-name", Description: d, Instances: []string{"a", "a~2"}}
			row := renderAgentRow(a, "nous qwen/qwen3-coder-next", sel, 64)
			if w := ansi.StringWidth(row); w != 64 {
				t.Errorf("sel=%v desc=%q: width %d, want 64", sel, d, w)
			}
		}
	}
	if got := truncW("日本語日本語", 7); ansi.StringWidth(got) > 7 || !strings.HasSuffix(got, "…") {
		t.Errorf("truncW = %q", got)
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
	for _, size := range [][2]int{{118, 38}, {80, 30}} {
		m := newTestMenu(t, nil)
		next, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m = moveTo(t, next.(MenuModel), "oll")
		lines := strings.Split(m.View().Content, "\n")
		if len(lines) > size[1] {
			t.Errorf("%v: %d lines exceed height", size, len(lines))
		}
		for _, l := range lines {
			if w := ansi.StringWidth(l); w > size[0] {
				t.Errorf("%v: line width %d exceeds %q", size, w, l)
			}
		}
	}
}
