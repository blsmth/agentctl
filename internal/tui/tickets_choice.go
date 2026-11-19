package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// tkChoice is one button of a tkChoiceModel. key is its shortcut, which is
// also what a click on the button sends.
type tkChoice struct {
	label   string
	key     string
	primary bool // rendered as the affirmative action
	danger  bool // rendered as a destructive action
}

// tkChoiceModel is a small confirmation dialog with a row of buttons: the
// autopilot turn-on confirmation, the discard-changes prompt and the
// stale-edit prompt. Focus starts on the safe choice (cancel); esc, n and a
// click outside the modal pick cancel.
type tkChoiceModel struct {
	kind    string
	lines   []string
	choices []tkChoice
	focus   int
	cancel  int // index picked by esc / outside click
	chosen  int // -1 while open
	hover   string
}

func (c tkChoiceModel) Init() tea.Cmd { return nil }

func (c tkChoiceModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok || c.chosen >= 0 {
		return c, nil
	}
	switch k.String() {
	case "tab", "right", "l":
		c.focus = (c.focus + 1) % len(c.choices)
	case "shift+tab", "left", "h":
		c.focus = (c.focus + len(c.choices) - 1) % len(c.choices)
	case "enter", "space":
		c.chosen = c.focus
	case "esc":
		c.chosen = c.cancel
	default:
		for i, ch := range c.choices {
			if k.String() == ch.key {
				c.chosen = i
			}
		}
	}
	return c, nil
}

// withHover implements tkHoverable.
func (c tkChoiceModel) withHover(id string) tea.Model {
	c.hover = id
	return c
}

// HintKeys implements ModalContent.
func (c tkChoiceModel) HintKeys() []string {
	hints := []string{"tab", "switch"}
	for i, ch := range c.choices {
		if i != c.cancel && len(ch.key) == 1 {
			hints = append(hints, ch.key, strings.ToLower(ch.label))
		}
	}
	return append(hints, "esc", strings.ToLower(c.choices[c.cancel].label))
}

// choiceZone is the click zone of button i: it sends the button's key.
func (c tkChoiceModel) choiceZone(i int) string {
	return "mkey:" + c.choices[i].key + "@choice"
}

func (c tkChoiceModel) View() tea.View {
	var b strings.Builder
	for _, l := range c.lines {
		b.WriteString(l + "\n")
	}
	b.WriteString("\n")
	var btns []string
	for i, ch := range c.choices {
		st := btnNormalStyle
		if i == c.focus {
			st = btnSelectedStyle
			if ch.danger {
				st = btnDangerStyle
			}
		}
		id := c.choiceZone(i)
		if c.hover == id {
			st = st.Reverse(true)
		}
		btns = append(btns, zoneMark(id, st.Render(ch.label)))
	}
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, strings.Join(btns, "   ")))
	return tea.NewView(b.String())
}

// done reports whether a choice was made.
func (c tkChoiceModel) done() bool { return c.chosen >= 0 }

// picked is the key of the chosen button ("" while open).
func (c tkChoiceModel) picked() string {
	if c.chosen < 0 || c.chosen >= len(c.choices) {
		return ""
	}
	return c.choices[c.chosen].key
}
