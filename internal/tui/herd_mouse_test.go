package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/herd"
)

// mouseTestModel builds a loaded hub with lines:
// 0 header(s1), 1 dispatched label, 2 row a, 3 row b, 4 divider, 5 header(s2),
// 6 dispatched label, 7 row c.
func mouseTestModel(t *testing.T, width, height int) HerdModel {
	t.Helper()
	InitStyles()
	row := func(name, win, sess string) herd.Row {
		r := testRow(name, sess, "idle", "")
		r.WindowID = win
		r.Dispatched = true // parentless dispatched rows sit under a "dispatched (N)" label
		return r
	}
	m := NewHerdModel()
	m.termWidth, m.termHeight = width, height
	m.configureSplitPane()
	m.loaded = true
	m.groups = []herd.Group{
		{Session: "s1", Rows: []herd.Row{row("a", "@1", "s1"), row("b", "@2", "s1")}},
		{Session: "s2", Rows: []herd.Row{row("c", "@3", "s2")}},
	}
	m.rebuildLines()
	m.resolveSelection()
	return m
}

func TestLineAt(t *testing.T) {
	tests := []struct {
		name     string
		height   int
		scrollTo int // select this line first (-1 = none)
		x, y     int
		wantIdx  int
		wantOK   bool
	}{
		{"subheader", 40, -1, 5, 4, 1, true},
		{"first row", 40, -1, 5, 5, 2, true},
		{"row b", 40, -1, 5, 6, 3, true},
		{"divider", 40, -1, 5, 7, 4, true},
		{"header", 40, -1, 5, 8, 5, true},
		{"row c", 40, -1, 5, 10, 7, true},
		{"above list", 40, -1, 5, 2, -1, false},
		{"below last line", 40, -1, 5, 11, -1, false},
		{"scrolled", 10, 5, 5, 3, 3, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := mouseTestModel(t, 120, tt.height)
			if tt.scrollTo >= 0 {
				m.selectLine(tt.scrollTo)
			}
			idx, ok := m.lineAt(tt.x, tt.y)
			if idx != tt.wantIdx || ok != tt.wantOK {
				t.Errorf("lineAt(%d,%d) = (%d,%v), want (%d,%v)",
					tt.x, tt.y, idx, ok, tt.wantIdx, tt.wantOK)
			}
		})
	}
}

func TestLineAt_NotLoaded(t *testing.T) {
	m := mouseTestModel(t, 120, 40)
	m.loaded = false
	if _, ok := m.lineAt(5, 3); ok {
		t.Error("click while loading should be ignored")
	}
}

func TestClickDecision(t *testing.T) {
	tests := []struct {
		name    string
		x, y    int
		sel     int
		want    herdClickAction
		wantIdx int
	}{
		{"row selects", 5, 5, 0, herdClickSelect, 2},
		{"header selects", 5, 8, 2, herdClickSelect, 5},
		{"divider ignored", 5, 7, 2, herdClickIgnore, 4},
		{"subheader ignored", 5, 4, 2, herdClickIgnore, 1},
		{"chrome ignored", 5, 0, 1, herdClickIgnore, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := mouseTestModel(t, 120, 40)
			m.selectLine(tt.sel)
			idx, act := m.clickDecision(tt.x, tt.y)
			if act != tt.want || idx != tt.wantIdx {
				t.Errorf("clickDecision = (%d,%v), want (%d,%v)", idx, act, tt.wantIdx, tt.want)
			}
		})
	}
}

func TestClickHeaderSelectsWithoutJump(t *testing.T) {
	m := mouseTestModel(t, 120, 40)
	next, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 5, Y: 8})
	got := next.(HerdModel)
	if !got.selIsHeader || got.selSession != "s2" || got.selWindowID != "" {
		t.Errorf("selection = %q/%q/%v, want header of s2", got.selSession, got.selWindowID, got.selIsHeader)
	}
	if got.jumpErr != "" {
		t.Errorf("unexpected jump attempt: %q", got.jumpErr)
	}
}

func TestClickRowSelectsWithoutOpening(t *testing.T) {
	for _, reclick := range []bool{false, true} {
		m := mouseTestModel(t, 120, 40)
		m.selectLine(0)
		msg := tea.MouseClickMsg{Button: tea.MouseLeft, X: 5, Y: 6}
		next, cmd := m.Update(msg)
		if reclick {
			next, cmd = next.(HerdModel).Update(msg)
		}
		got := next.(HerdModel)
		if cursorOf(got) != 3 || got.selWindowID != "@2" || got.selSession != "s1" || got.selIsHeader {
			t.Errorf("selection = cursor %d %q/%q header=%v, want row b (@2)",
				cursorOf(got), got.selSession, got.selWindowID, got.selIsHeader)
		}
		if got.jumpErr != "" {
			t.Errorf("unexpected jump attempt: %q", got.jumpErr)
		}
		if !reclick && cmd == nil {
			t.Error("first click: expected live viewer cmd")
		}
		if reclick && cmd != nil {
			t.Error("reclick on the same row must not re-fire the viewer")
		}
	}
}

func TestClickMiddleButtonIgnored(t *testing.T) {
	m := mouseTestModel(t, 120, 40)
	before := cursorOf(m)
	next, cmd := m.Update(tea.MouseClickMsg{Button: tea.MouseMiddle, X: 5, Y: 7})
	if cmd != nil || cursorOf(next) != before {
		t.Error("middle click should be ignored")
	}
}

// buttonTestModel is mouseTestModel with s1 launchable (dirs set) and s2 not launchable.
func buttonTestModel(t *testing.T) HerdModel {
	t.Helper()
	m := mouseTestModel(t, 120, 40)
	m.groups[0].ProjectDir = "/p/one"
	m.groups[0].RuntimeDir = "/p/one/.agentctl"
	m.groups[1].ProjectDir = ""
	m.groups[1].RuntimeDir = "/p/two/.agentctl"
	m.rebuildLines()
	m.resolveSelection()
	return m
}

func TestButtonDecision(t *testing.T) {
	m := buttonTestModel(t)
	w := m.listColWidth()
	newX := w - herdSidebarRightGutter - 2 // inside "+ new"
	closeX := w - herdSidebarRightGutter - 1
	// y: 3 header(s1), 4 dispatched label, 5 row a, 6 row b, 7 divider, 8 header(s2), 9 dispatched label, 10 row c
	tests := []struct {
		name       string
		x, y       int
		button     tea.MouseButton
		wantIdx    int
		wantAction herdClickAction
	}{
		{"header new button", newX, 3, tea.MouseLeft, 0, herdClickNew},
		{"header elsewhere selects", 5, 3, tea.MouseLeft, 0, herdClickSelect},
		{"stale header has no button", newX, 8, tea.MouseLeft, 5, herdClickSelect},
		{"row close button", closeX, 5, tea.MouseLeft, 2, herdClickClose},
		{"row elsewhere selects", 5, 5, tea.MouseLeft, 2, herdClickSelect},
		{"stale row still closable", closeX, 10, tea.MouseLeft, 7, herdClickClose},
		{"row new column is not a button", newX - 4, 5, tea.MouseLeft, 2, herdClickSelect},
		{"right click row", 5, 5, tea.MouseRight, 2, herdClickMenu},
		{"right click row on close col", closeX, 5, tea.MouseRight, 2, herdClickMenu},
		{"left click subheader", closeX, 4, tea.MouseLeft, 1, herdClickIgnore},
		{"right click subheader", 5, 4, tea.MouseRight, 1, herdClickIgnore},
		{"right click header", 5, 3, tea.MouseRight, 0, herdClickMenu},
		{"right click divider", 5, 7, tea.MouseRight, 4, herdClickIgnore},
		{"middle click", 5, 5, tea.MouseMiddle, -1, herdClickIgnore},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx, action := m.buttonDecision(tt.x, tt.y, tt.button)
			if idx != tt.wantIdx || action != tt.wantAction {
				t.Errorf("buttonDecision(%d,%d) = (%d,%v), want (%d,%v)", tt.x, tt.y, idx, action, tt.wantIdx, tt.wantAction)
			}
		})
	}
}

func TestRenderedButtonsLineUpWithHitColumns(t *testing.T) {
	m := buttonTestModel(t)
	w := m.listColWidth()
	hdr := ansi.Strip(m.renderLine(0, w))
	if !strings.HasSuffix(strings.TrimRight(hdr, " "), herdNewLabel) {
		t.Errorf("header %q should end with %q", hdr, herdNewLabel)
	}
	if x := len([]rune(hdr[:strings.Index(hdr, herdNewLabel)])); !m.onNewButton(x) || !m.onNewButton(x+len(herdNewLabel)-1) {
		t.Errorf("new button at col %d not hit-testable", x)
	}
	row := ansi.Strip(m.renderLine(2, w))
	cols := []rune(row)
	var closeCol = -1
	for i, r := range cols {
		if string(r) == herdCloseLabel {
			closeCol = i
		}
	}
	if closeCol < 0 || !m.onCloseButton(closeCol) {
		t.Errorf("close glyph at col %d in %q not hit-testable", closeCol, row)
	}
	if lipgloss.Width(m.renderLine(2, w)) > w {
		t.Error("row wider than list column")
	}
}

func TestHandleClickActions(t *testing.T) {
	m := buttonTestModel(t)
	w := m.listColWidth()
	closeX := w - herdSidebarRightGutter - 1

	t.Run("new button arms the selector without launching", func(t *testing.T) {
		next, cmd := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: w - herdSidebarRightGutter - 2, Y: 3})
		got := next.(HerdModel)
		if cmd != nil || got.newSession != "s1" || got.infoMsg != "" {
			t.Errorf("expected armed selector and no launch, got cmd=%v armed=%q info=%q", cmd != nil, got.newSession, got.infoMsg)
		}
	})
	t.Run("close click arms, second click confirms", func(t *testing.T) {
		next, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: closeX, Y: 5})
		got := next.(HerdModel)
		if got.confirmWindow != "@1" || got.confirmSession != "s1" {
			t.Fatalf("armed = %q/%q", got.confirmSession, got.confirmWindow)
		}
		if !strings.Contains(ansi.Strip(got.renderLine(2, w)), "close a? y/n") {
			t.Errorf("row should show confirm prompt: %q", ansi.Strip(got.renderLine(2, w)))
		}
		after, cmd := got.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: closeX, Y: 5})
		if cmd == nil || after.(HerdModel).closeArmed() {
			t.Error("second click on armed X should confirm and disarm")
		}
	})
	t.Run("clicking another row cancels", func(t *testing.T) {
		next, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: closeX, Y: 5})
		next, _ = next.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 5, Y: 6})
		if next.(HerdModel).closeArmed() {
			t.Error("selection moving away should cancel")
		}
	})
	t.Run("right click opens menu, click item", func(t *testing.T) {
		next, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseRight, X: 5, Y: 5})
		got := next.(HerdModel)
		if !got.menuOpen {
			t.Fatal("menu should open")
		}
		text, spans := menuItemSpans(got.herdMenuItems(got.menuLine))
		if !strings.Contains(text, "[close]") || !strings.Contains(text, "[home]") || !strings.Contains(text, "[focus]") || !strings.Contains(text, "[new]") {
			t.Errorf("menu text %q", text)
		}
		// click [close]
		after, _ := got.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: spans[1][0], Y: got.footerMessageY()})
		a := after.(HerdModel)
		if a.menuOpen || !a.closeArmed() {
			t.Errorf("close item should arm confirm and close menu (menu=%v armed=%v)", a.menuOpen, a.closeArmed())
		}
	})
	t.Run("menu footer is on the rendered message line", func(t *testing.T) {
		next, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseRight, X: 5, Y: 5})
		lines := strings.Split(ansi.Strip(next.(HerdModel).render()), "\n")
		y := next.(HerdModel).footerMessageY()
		if y >= len(lines) || !strings.Contains(lines[y], "[close]") {
			t.Errorf("line %d = %q, want menu", y, lines[min(y, len(lines)-1)])
		}
	})
}

func cursorOf(m tea.Model) int {
	h := m.(HerdModel)
	return h.splitPane.Cursor()
}

// armedHeaderModel returns buttonTestModel with the selector armed on header 0.
func armedHeaderModel(t *testing.T) HerdModel {
	t.Helper()
	m := buttonTestModel(t)
	m.selectLine(0)
	m.armNew(0)
	if !m.newArmed() {
		t.Fatal("selector did not arm")
	}
	return m
}

func TestSelectorHitColumns(t *testing.T) {
	m := armedHeaderModel(t)
	shell, agent := m.selectorSpans()
	end := m.listColWidth() - herdSidebarRightGutter
	if agent[1] != end || agent[1]-agent[0] != len(herdAgentLabel) || shell[1]-shell[0] != len(herdShellLabel) {
		t.Fatalf("spans shell=%v agent=%v end=%d", shell, agent, end)
	}
	tests := []struct {
		name string
		x    int
		want herdClickAction
	}{
		{"left of shell", shell[0] - 2, herdClickSelect},
		{"slack left of shell", shell[0] - 1, herdClickNewShell},
		{"shell first col", shell[0], herdClickNewShell},
		{"shell last col", shell[1] - 1, herdClickNewShell},
		{"space after shell", shell[1], herdClickNewShell},
		{"pipe", shell[1] + 1, herdClickSelect},
		{"space before agent", agent[0] - 1, herdClickNewAgent},
		{"agent first col", agent[0], herdClickNewAgent},
		{"agent last col", agent[1] - 1, herdClickNewAgent},
		{"slack right of agent", agent[1], herdClickNewAgent},
		{"past slack", agent[1] + 1, herdClickSelect},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, got := m.buttonDecision(tt.x, 3, tea.MouseLeft); got != tt.want {
				t.Errorf("x=%d: action %v, want %v", tt.x, got, tt.want)
			}
		})
	}
}

func TestSelectorClicks(t *testing.T) {
	m := armedHeaderModel(t)
	shell, agent := m.selectorSpans()
	click := func(m HerdModel, x, y int) (HerdModel, bool) {
		next, cmd := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
		return next.(HerdModel), cmd != nil
	}
	t.Run("shell", func(t *testing.T) {
		got, cmd := click(m, shell[0], 3)
		if !cmd || got.newArmed() || !strings.Contains(got.infoMsg, "opening shell in s1") {
			t.Errorf("cmd=%v armed=%v info=%q", cmd, got.newArmed(), got.infoMsg)
		}
	})
	t.Run("agent", func(t *testing.T) {
		got, cmd := click(m, agent[0], 3)
		if !cmd || got.newArmed() || !strings.Contains(got.infoMsg, "launching agent in s1") {
			t.Errorf("cmd=%v armed=%v info=%q", cmd, got.newArmed(), got.infoMsg)
		}
	})
	t.Run("elsewhere cancels", func(t *testing.T) {
		got, _ := click(m, 5, 6)
		if got.newArmed() {
			t.Error("still armed")
		}
	})
	t.Run("hover each side", func(t *testing.T) {
		if h := m.hoverAt(shell[0], 3); h.reg != herdHoverNewShell {
			t.Errorf("shell hover = %v", h.reg)
		}
		if h := m.hoverAt(agent[0], 3); h.reg != herdHoverNewAgent {
			t.Errorf("agent hover = %v", h.reg)
		}
	})
}

func TestSelectorKeys(t *testing.T) {
	tests := []struct {
		key       string
		wantCmd   bool
		wantInfo  string
		wantArmed bool
	}{
		{"s", true, "opening shell in s1", false},
		{"a", true, "launching agent in s1", false},
		{"enter", true, "launching agent in s1", false},
		{"esc", false, "", false},
		{"n", false, "", false},
		{"z", false, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			m := armedHeaderModel(t)
			code := rune(tt.key[0])
			text := tt.key
			switch tt.key {
			case "esc":
				code, text = tea.KeyEscape, ""
			case "enter":
				code, text = tea.KeyEnter, ""
			}
			next, cmd := m.Update(tea.KeyPressMsg{Code: code, Text: text})
			got := next.(HerdModel)
			if (cmd != nil) != tt.wantCmd || got.newArmed() != tt.wantArmed || !strings.Contains(got.infoMsg, tt.wantInfo) {
				t.Errorf("cmd=%v armed=%v info=%q", cmd != nil, got.newArmed(), got.infoMsg)
			}
		})
	}
}

func TestSelectorArmingAndCancel(t *testing.T) {
	t.Run("n arms from a row", func(t *testing.T) {
		m := buttonTestModel(t)
		m.selectLine(2)
		next, cmd := m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
		got := next.(HerdModel)
		if cmd != nil || got.newSession != "s1" {
			t.Errorf("cmd=%v armed=%q", cmd != nil, got.newSession)
		}
	})
	t.Run("cursor movement cancels", func(t *testing.T) {
		m := armedHeaderModel(t)
		m.moveCursor(1)
		if m.newArmed() {
			t.Error("moving the cursor should cancel")
		}
	})
	t.Run("j key cancels", func(t *testing.T) {
		m := armedHeaderModel(t)
		next, _ := m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
		if next.(HerdModel).newArmed() {
			t.Error("j should cancel")
		}
	})
	t.Run("menu new arms", func(t *testing.T) {
		m := buttonTestModel(t)
		m.openMenu(2)
		m.runMenuKey("n")
		if m.newSession != "s1" {
			t.Errorf("armed = %q", m.newSession)
		}
	})
	t.Run("non-launchable group never arms", func(t *testing.T) {
		m := buttonTestModel(t)
		m.selectLine(5) // s2: no dirs
		m.armNew(5)
		if m.newArmed() {
			t.Error("must not arm")
		}
		if got := m.herdMenuItems(5); len(got) != 0 {
			for _, it := range got {
				if it.key == "n" {
					t.Error("menu offers new for a non-launchable group")
				}
			}
		}
	})
}

func TestSelectorRenderingAndHints(t *testing.T) {
	for _, width := range []int{30, 44, 80, 120} {
		m := armedHeaderModel(t)
		m.termWidth = width
		m.configureSplitPane()
		cw := m.listColWidth()
		out := m.renderLine(0, cw)
		if w := lipgloss.Width(out); w > cw {
			t.Errorf("width %d: header %d wide > %d", width, w, cw)
		}
		plain := strings.TrimRight(ansi.Strip(out), " ")
		if !strings.HasSuffix(plain, herdShellLabel+herdSelSep+herdAgentLabel) {
			t.Errorf("width %d: selector truncated: %q", width, plain)
		}
		shell, _ := m.selectorSpans()
		if shell[0] >= 0 && !strings.HasPrefix(string([]rune(ansi.Strip(out))[shell[0]:]), herdShellLabel) {
			t.Errorf("width %d: shell label not at span col %d: %q", width, shell[0], ansi.Strip(out))
		}
	}
	m := armedHeaderModel(t)
	if f := ansi.Strip(m.renderFooter(80)); !strings.Contains(f, "shell") || !strings.Contains(f, "esc cancel") {
		t.Errorf("footer lacks selector hints: %q", f)
	}
	m.cancelNew()
	if f := ansi.Strip(m.renderFooter(80)); strings.Contains(f, "esc cancel") {
		t.Errorf("footer shows selector hints when disarmed: %q", f)
	}
}
