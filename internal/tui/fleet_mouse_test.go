package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/fleet"
)

// mouseTestModel builds a loaded hub with lines:
// 0 header(s1), 1 dispatched label, 2 row a, 3 row b, 4 divider, 5 header(s2),
// 6 dispatched label, 7 row c.
func mouseTestModel(t *testing.T, width, height int) FleetModel {
	t.Helper()
	InitStyles()
	row := func(name, win, sess string) fleet.Row {
		r := testRow(name, sess, "idle", "")
		r.WindowID = win
		r.Dispatched = true // parentless dispatched rows sit under a "dispatched (N)" label
		return r
	}
	m := NewFleetModel()
	m.termWidth, m.termHeight = width, height
	m.configureSplitPane()
	m.loaded = true
	m.groups = []fleet.Group{
		{Session: "s1", Rows: []fleet.Row{row("a", "@1", "s1"), row("b", "@2", "s1")}},
		{Session: "s2", Rows: []fleet.Row{row("c", "@3", "s2")}},
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
		{"subheader", 40, -1, 5, 3, 1, true},
		{"first row", 40, -1, 5, 4, 2, true},
		{"row b", 40, -1, 5, 6, 3, true}, // the selected row a has a chip line
		{"divider", 40, -1, 5, 7, 4, true},
		{"header", 40, -1, 5, 8, 5, true},
		{"row c", 40, -1, 5, 10, 7, true},
		{"title", 40, -1, 5, 0, -1, false},
		{"blank row above list", 40, -1, 5, 1, -1, false},
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
		want    fleetClickAction
		wantIdx int
	}{
		{"row selects", 5, 4, 0, fleetClickSelect, 2},
		{"header selects", 1, 8, 2, fleetClickSelect, 5},
		{"header name folds", 5, 8, 2, fleetClickFold, 5},
		{"divider ignored", 5, 7, 2, fleetClickIgnore, 4},
		{"subheader ignored", 5, 3, 2, fleetClickIgnore, 1},
		{"chrome ignored", 5, 0, 1, fleetClickIgnore, -1},
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
	next, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 1, Y: 8})
	got := next.(FleetModel)
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
		msg := tea.MouseClickMsg{Button: tea.MouseLeft, X: 5, Y: 5}
		next, cmd := m.Update(msg)
		if reclick {
			next, cmd = next.(FleetModel).Update(msg)
		}
		got := next.(FleetModel)
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
	next, cmd := m.Update(tea.MouseClickMsg{Button: tea.MouseMiddle, X: 5, Y: 5})
	if cmd != nil || cursorOf(next) != before {
		t.Error("middle click should be ignored")
	}
}

// buttonTestModel is mouseTestModel with s1 launchable (dirs set) and s2 not launchable.
func buttonTestModel(t *testing.T) FleetModel {
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
	newX := w - fleetSidebarRightGutter - 2 // inside "+ new"
	closeX := w - fleetSidebarRightGutter - 1
	// y: 2 header(s1), 3 dispatched label, 4 row a (selected: 5 is its agent chip), 6 row b, 7 divider, 8 header(s2), 9 dispatched label, 10 row c
	tests := []struct {
		name       string
		x, y       int
		button     tea.MouseButton
		wantIdx    int
		wantAction fleetClickAction
	}{
		{"header new button", newX, 2, tea.MouseLeft, 0, fleetClickNew},
		{"header elsewhere selects", 1, 2, tea.MouseLeft, 0, fleetClickSelect},
		{"header gap before counts selects", newX - 30, 2, tea.MouseLeft, 0, fleetClickSelect},
		{"header fold marker folds", fleetHeaderNameCol, 2, tea.MouseLeft, 0, fleetClickFold},
		{"header space before name folds", fleetHeaderNameCol + 1, 2, tea.MouseLeft, 0, fleetClickFold},
		{"stale header has no new button (count slot instead)", newX, 8, tea.MouseLeft, 5, fleetClickCount},
		{"row close button", closeX, 4, tea.MouseLeft, 2, fleetClickClose},
		{"row elsewhere selects", 5, 4, tea.MouseLeft, 2, fleetClickSelect},
		{"stale row still closable", closeX, 10, tea.MouseLeft, 7, fleetClickClose},
		{"row new column is not a button", newX - 4, 4, tea.MouseLeft, 2, fleetClickSelect},
		{"right click row", 5, 4, tea.MouseRight, 2, fleetClickMenu},
		{"right click row on close col", closeX, 4, tea.MouseRight, 2, fleetClickMenu},
		{"left click subheader", closeX, 3, tea.MouseLeft, 1, fleetClickIgnore},
		{"right click subheader", 5, 3, tea.MouseRight, 1, fleetClickIgnore},
		{"right click header", 5, 2, tea.MouseRight, 0, fleetClickMenu},
		{"right click divider", 5, 7, tea.MouseRight, 4, fleetClickIgnore},
		{"middle click", 5, 4, tea.MouseMiddle, -1, fleetClickIgnore},
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
	// "+ new" is drawn only on the selected or hovered header.
	if hdr := ansi.Strip(m.renderLine(0, w)); strings.Contains(hdr, fleetNewLabel) {
		t.Errorf("unselected, unhovered header %q should not draw %q", hdr, fleetNewLabel)
	}
	m.hover = fleetHover{line: 0, reg: fleetHoverHeader}
	hdr := ansi.Strip(m.renderLine(0, w))
	if !strings.HasSuffix(strings.TrimRight(hdr, " "), fleetNewLabel) {
		t.Errorf("hovered header %q should end with %q", hdr, fleetNewLabel)
	}
	g := m.lines[0].group
	if x := len([]rune(hdr[:strings.Index(hdr, fleetNewLabel)])); !m.onNewButton(g, x) || !m.onNewButton(g, x+len(fleetNewLabel)-1) {
		t.Errorf("new button at col %d not hit-testable", x)
	}
	row := ansi.Strip(m.renderLine(2, w))
	cols := []rune(row)
	var closeCol = -1
	for i, r := range cols {
		if string(r) == fleetCloseLabel {
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
	closeX := w - fleetSidebarRightGutter - 1

	t.Run("new button arms the selector without launching", func(t *testing.T) {
		next, cmd := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: w - fleetSidebarRightGutter - 2, Y: 2})
		got := next.(FleetModel)
		if cmd != nil || got.newSession != "s1" || got.infoMsg != "" {
			t.Errorf("expected armed selector and no launch, got cmd=%v armed=%q info=%q", cmd != nil, got.newSession, got.infoMsg)
		}
	})
	t.Run("close click arms, second click confirms", func(t *testing.T) {
		next, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: closeX, Y: 4})
		got := next.(FleetModel)
		if got.confirmWindow != "@1" || got.confirmSession != "s1" {
			t.Fatalf("armed = %q/%q", got.confirmSession, got.confirmWindow)
		}
		if !strings.Contains(ansi.Strip(got.renderLine(2, w)), "close a? y/n") {
			t.Errorf("row should show confirm prompt: %q", ansi.Strip(got.renderLine(2, w)))
		}
		after, cmd := got.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: closeX, Y: 4})
		if cmd == nil || after.(FleetModel).closeArmed() {
			t.Error("second click on armed X should confirm and disarm")
		}
	})
	t.Run("clicking another row cancels", func(t *testing.T) {
		next, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: closeX, Y: 4})
		next, _ = next.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 5, Y: 6})
		if next.(FleetModel).closeArmed() {
			t.Error("selection moving away should cancel")
		}
	})
	t.Run("right click opens menu, click item", func(t *testing.T) {
		next, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseRight, X: 5, Y: 4})
		got := next.(FleetModel)
		if !got.menuOpen {
			t.Fatal("menu should open")
		}
		text, spans := menuItemSpans(got.fleetMenuItems(got.menuLine))
		if !strings.Contains(text, "[close]") || !strings.Contains(text, "[home]") || !strings.Contains(text, "[focus]") || !strings.Contains(text, "[new]") {
			t.Errorf("menu text %q", text)
		}
		// click [close]
		after, _ := got.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: spans[1][0], Y: got.footerMessageY()})
		a := after.(FleetModel)
		if a.menuOpen || !a.closeArmed() {
			t.Errorf("close item should arm confirm and close menu (menu=%v armed=%v)", a.menuOpen, a.closeArmed())
		}
	})
	t.Run("menu footer is on the rendered message line", func(t *testing.T) {
		next, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseRight, X: 5, Y: 4})
		lines := strings.Split(ansi.Strip(next.(FleetModel).render()), "\n")
		y := next.(FleetModel).footerMessageY()
		if y >= len(lines) || !strings.Contains(lines[y], "[close]") {
			t.Errorf("line %d = %q, want menu", y, lines[min(y, len(lines)-1)])
		}
	})
}

func cursorOf(m tea.Model) int {
	h := m.(FleetModel)
	return h.splitPane.Cursor()
}

// armedHeaderModel returns buttonTestModel with the selector armed on header 0.
func armedHeaderModel(t *testing.T) FleetModel {
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
	shell, agent, cron := m.selectorSpans(m.groups[0])
	end := m.listColWidth() - fleetSidebarRightGutter
	if agent[1] != end || cron[1]-cron[0] != len(fleetCronLabel) || agent[1]-agent[0] != len(fleetAgentLabel) || shell[1]-shell[0] != len(fleetShellLabel) {
		t.Fatalf("spans shell=%v agent=%v cron=%v end=%d", shell, agent, cron, end)
	}
	tests := []struct {
		name string
		x    int
		want fleetClickAction
	}{
		{"left of shell", shell[0] - 2, fleetClickSelect},
		{"slack left of shell", shell[0] - 1, fleetClickNewShell},
		{"shell first col", shell[0], fleetClickNewShell},
		{"shell last col", shell[1] - 1, fleetClickNewShell},
		{"space after shell", shell[1], fleetClickNewShell},
		{"pipe", shell[1] + 1, fleetClickSelect},
		{"space before cron", cron[0] - 1, fleetClickNewCron},
		{"cron first col", cron[0], fleetClickNewCron},
		{"cron last col", cron[1] - 1, fleetClickNewCron},
		{"space after cron", cron[1], fleetClickNewCron},
		{"second pipe", cron[1] + 1, fleetClickSelect},
		{"space before agent", agent[0] - 1, fleetClickNewAgent},
		{"agent first col", agent[0], fleetClickNewAgent},
		{"agent last col", agent[1] - 1, fleetClickNewAgent},
		{"slack right of agent", agent[1], fleetClickNewAgent},
		{"past slack", agent[1] + 1, fleetClickSelect},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, got := m.buttonDecision(tt.x, 2, tea.MouseLeft); got != tt.want {
				t.Errorf("x=%d: action %v, want %v", tt.x, got, tt.want)
			}
		})
	}
}

func TestSelectorClicks(t *testing.T) {
	m := armedHeaderModel(t)
	shell, agent, cron := m.selectorSpans(m.groups[0])
	click := func(m FleetModel, x, y int) (FleetModel, bool) {
		next, cmd := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
		return next.(FleetModel), cmd != nil
	}
	t.Run("shell", func(t *testing.T) {
		got, cmd := click(m, shell[0], 2)
		if !cmd || got.newArmed() || !strings.Contains(got.infoMsg, "opening shell in s1") {
			t.Errorf("cmd=%v armed=%v info=%q", cmd, got.newArmed(), got.infoMsg)
		}
	})
	t.Run("agent", func(t *testing.T) {
		got, cmd := click(m, agent[0], 2)
		if !cmd || got.newArmed() || !strings.Contains(got.infoMsg, "launching agent in s1") {
			t.Errorf("cmd=%v armed=%v info=%q", cmd, got.newArmed(), got.infoMsg)
		}
	})
	t.Run("cron", func(t *testing.T) {
		got, cmd := click(m, cron[0], 2)
		if got.newArmed() || !got.schedFormOpen() || got.schedForm.mode != schedFormNew {
			t.Fatalf("armed=%v formOpen=%v", got.newArmed(), got.schedFormOpen())
		}
		if got.schedForm.dirs[got.schedForm.dirIdx] != "/p/one" {
			t.Errorf("form dir = %v / %d, want /p/one", got.schedForm.dirs, got.schedForm.dirIdx)
		}
		if !cmd {
			t.Error("opening the form before the jobs loaded must load them")
		}
	})
	t.Run("elsewhere cancels", func(t *testing.T) {
		got, _ := click(m, 5, 5)
		if got.newArmed() {
			t.Error("still armed")
		}
	})
	t.Run("hover each side", func(t *testing.T) {
		if h := m.hoverAt(shell[0], 2); h.reg != fleetHoverNewShell {
			t.Errorf("shell hover = %v", h.reg)
		}
		if h := m.hoverAt(agent[0], 2); h.reg != fleetHoverNewAgent {
			t.Errorf("agent hover = %v", h.reg)
		}
		if h := m.hoverAt(cron[0], 2); h.reg != fleetHoverNewCron {
			t.Errorf("cron hover = %v", h.reg)
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
			got := next.(FleetModel)
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
		got := next.(FleetModel)
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
		if next.(FleetModel).newArmed() {
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
		if got := m.fleetMenuItems(5); len(got) != 0 {
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
		if !strings.HasSuffix(plain, fleetShellLabel+fleetSelSep+fleetCronLabel+fleetSelSep+fleetAgentLabel) {
			t.Errorf("width %d: selector truncated: %q", width, plain)
		}
		shell, _, _ := m.selectorSpans(m.groups[0])
		if shell[0] >= 0 && !strings.HasPrefix(string([]rune(ansi.Strip(out))[shell[0]:]), fleetShellLabel) {
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

func TestSelectorTargetsArmedSessionAfterReload(t *testing.T) {
	setup := func(t *testing.T) FleetModel {
		m := armedHeaderModel(t) // armed on s1 at line 0
		// Reload: s2 now sorts first, so it sits at the armed index.
		for i := range m.groups {
			m.groups[i].ProjectDir, m.groups[i].RuntimeDir = "/p", "/r"
		}
		m.groups[0], m.groups[1] = m.groups[1], m.groups[0]
		m.rebuildLines()
		if m.lines[m.newLine].group.Session != "s2" {
			t.Fatalf("setup: line %d is %q", m.newLine, m.lines[m.newLine].group.Session)
		}
		return m
	}
	t.Run("key targets armed session or cancels", func(t *testing.T) {
		m := setup(t)
		cmd, _ := m.handleNewKey("s")
		if m.newArmed() {
			t.Error("still armed")
		}
		if cmd != nil && !strings.Contains(m.infoMsg, "s1") {
			t.Errorf("shell targeted wrong group: %q", m.infoMsg)
		}
	})
	t.Run("reload cancels", func(t *testing.T) {
		m := setup(t)
		m.validateNew()
		if m.newArmed() {
			t.Error("selector should cancel when its group moved off the armed line")
		}
	})
	t.Run("vanished group cancels", func(t *testing.T) {
		m := armedHeaderModel(t)
		m.groups = m.groups[1:]
		m.rebuildLines()
		cmd, _ := m.handleNewKey("s")
		if cmd != nil || m.newArmed() {
			t.Error("must cancel without launching")
		}
	})
}

// formDir is the project directory the open schedule form targets.
func formDir(t *testing.T, m FleetModel) string {
	t.Helper()
	if !m.schedFormOpen() || m.schedForm.mode != schedFormNew {
		t.Fatalf("schedule form open=%v, want create mode", m.schedFormOpen())
	}
	return m.schedForm.dirs[m.schedForm.dirIdx]
}

func TestNewCronKeyAndMenu(t *testing.T) {
	press := func(m FleetModel, key string) FleetModel {
		next, _ := m.Update(tea.KeyPressMsg{Code: rune(key[0]), Text: key})
		return next.(FleetModel)
	}
	t.Run("key on header", func(t *testing.T) {
		m := buttonTestModel(t)
		m.selectLine(0)
		if got := formDir(t, press(m, fleetCronKey)); got != "/p/one" {
			t.Errorf("dir = %q", got)
		}
	})
	t.Run("key from armed selector", func(t *testing.T) {
		for _, k := range []string{fleetCronKey, "c"} {
			got := press(armedHeaderModel(t), k)
			if got.newArmed() || formDir(t, got) != "/p/one" {
				t.Errorf("key %q: armed=%v", k, got.newArmed())
			}
		}
	})
	t.Run("menu item", func(t *testing.T) {
		m := buttonTestModel(t)
		m.selectLine(2)
		found := false
		for _, it := range m.fleetMenuItems(2) {
			found = found || (it.label == "new cron" && it.key == fleetCronKey)
		}
		if !found {
			t.Fatalf("no new cron item: %v", m.fleetMenuItems(2))
		}
		m.openMenu(2)
		_ = m.runMenuKey(fleetCronKey)
		if got := formDir(t, m); got != "/p/one" {
			t.Errorf("dir = %q", got)
		}
	})
	t.Run("not offered without a project dir", func(t *testing.T) {
		m := buttonTestModel(t)
		for i, l := range m.lines {
			if l.kind == fleetLineHeader && l.group.Session == m.groups[1].Session {
				for _, it := range m.fleetMenuItems(i) {
					if it.key == fleetCronKey {
						t.Errorf("menu offers cron for a group that cannot launch")
					}
				}
				m.selectLine(i)
				if got := press(m, fleetCronKey); got.schedFormOpen() {
					t.Error("key opened a form for a group that cannot launch")
				}
			}
		}
	})
}

func TestSelectorCronNarrow(t *testing.T) {
	for w := 20; w <= 120; w += 2 {
		m := armedHeaderModel(t)
		m.termWidth = w
		m.configureSplitPane()
		cw := m.listColWidth()
		out := ansi.Strip(m.renderLine(0, cw))
		shell, agent, cron := m.selectorSpans(m.groups[0])
		rs := []rune(out)
		at := func(sp [2]int) string {
			if sp[0] < 0 || sp[1] > len(rs) {
				return "<out of range>"
			}
			return string(rs[sp[0]:sp[1]])
		}
		if got := at(shell); got != fleetShellLabel {
			t.Errorf("w=%d shell span %v = %q in %q", w, shell, got, out)
		}
		if got := at(agent); got != fleetAgentLabel {
			t.Errorf("w=%d agent span %v = %q", w, agent, got)
		}
		hasCron := strings.Contains(strings.TrimRight(out, " "), fleetCronLabel)
		if hasCron != (cron[1] > 0) {
			t.Errorf("w=%d cron drawn=%v span=%v: %q", w, hasCron, cron, out)
		}
		if cron[1] > 0 && at(cron) != fleetCronLabel {
			t.Errorf("w=%d cron span %v = %q", w, cron, at(cron))
		}
	}
	wide := armedHeaderModel(t)
	if _, _, cron := wide.selectorSpans(wide.groups[0]); cron[1] == 0 {
		t.Error("cron missing at the default width")
	}
}

func TestSelectorAgentIsDefaultStyle(t *testing.T) {
	m := armedHeaderModel(t)
	cw := m.listColWidth()
	g := m.groups[0]
	tests := []struct {
		name  string
		hover fleetHoverRegion
		want  string // label rendered with the emphasis style
	}{
		{"unhovered", fleetHoverNone, fleetAgentLabel},
		{"header hover", fleetHoverHeader, fleetAgentLabel},
		{"shell hover", fleetHoverNewShell, fleetShellLabel},
		{"cron hover", fleetHoverNewCron, fleetCronLabel},
		{"agent hover", fleetHoverNewAgent, fleetAgentLabel},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := m.renderGroupHeaderHover(g, true, tt.hover, cw)
			for _, l := range []string{fleetShellLabel, fleetCronLabel, fleetAgentLabel} {
				emph := fleetNewHoverStyle.Render(l)
				if got := strings.Contains(out, emph); got != (l == tt.want) {
					t.Errorf("%q emphasised=%v, want %v", l, got, l == tt.want)
				}
			}
		})
	}
}
