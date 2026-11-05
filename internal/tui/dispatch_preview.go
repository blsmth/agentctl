package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/highlight"
	"github.com/blsmth/agentctl/internal/theme"
)

// dispatchPreviewContentWidth is the DEFAULT usable content width inside the
// popup box, used whenever a caller doesn't supply WithContentWidth (e.g.
// existing tests and any future caller that doesn't care). Callers that want
// the popup to grow to fit a larger terminal (internal/hooks/dispatch_preview.go)
// pass a client-derived value via WithContentWidth instead. Chosen to fit
// comfortably in a standard terminal and accommodate typical prompt text
// without excessive wrapping.
const dispatchPreviewContentWidth = 88

// dpLabelWidth is the fixed column width for metadata row labels ("Requested
// by", "Dispatching", "Issue", "Prompt") so their values line up. The longest
// label is "Requested by" (12 chars); +1 leaves a one-space gutter.
const dpLabelWidth = 13

// dpMaxVPHeight is the DEFAULT cap on viewport height, used whenever a caller
// doesn't supply WithMaxViewportHeight. It caps the viewport height so the
// fully expanded popup never exceeds the fixed 40-row popup
// internal/hooks/dispatch_preview.go asks tmux display-popup for by default.
// See the arithmetic on PopupSize(): total expanded height = vpHeight + 16,
// so 22 + 16 = 38, leaving 2 rows of headroom.
const dpMaxVPHeight = 22

// PreviewKindDispatch and PreviewKindBash mirror
// internal/hooks.PreviewKindDispatch / PreviewKindBash. This package cannot
// import internal/hooks (hooks already imports tui to build the model), so
// the string values are duplicated here rather than shared -- keep them in
// sync if either side changes.
const (
	PreviewKindDispatch = "dispatch"
	PreviewKindBash     = "bash"
)

// dispatchPreviewOpts holds the resolved, optional sizing inputs for a
// DispatchPreviewModel. Zero values mean "use the historical fixed default"
// -- see dispatchPreviewContentWidth / dpMaxVPHeight above.
type dispatchPreviewOpts struct {
	contentWidth int
	maxVPHeight  int
	kind         string
}

// DispatchPreviewOption configures optional sizing inputs to NewDispatchPreview
// and DispatchPreviewMaxPopupSize. Using functional options (rather than
// widening the constructor's positional argument list) means every existing
// 6-argument call site -- including every test in dispatch_preview_test.go --
// keeps compiling unchanged and keeps the historical 88/22 behavior.
type DispatchPreviewOption func(*dispatchPreviewOpts)

// WithContentWidth overrides the popup's usable content width. Values <= 0
// are ignored (keeps the default). Intended for a width derived from the
// attached tmux client's dimensions -- see internal/hooks/popup_clamp.go.
func WithContentWidth(w int) DispatchPreviewOption {
	return func(o *dispatchPreviewOpts) {
		if w > 0 {
			o.contentWidth = w
		}
	}
}

// WithMaxViewportHeight overrides the cap applied to the prompt viewport's
// height. Values <= 0 are ignored (keeps the default). This is the SAME
// value that must be passed to DispatchPreviewMaxPopupSize when sizing the
// tmux popup up front -- see that function's doc comment.
func WithMaxViewportHeight(h int) DispatchPreviewOption {
	return func(o *dispatchPreviewOpts) {
		if h > 0 {
			o.maxVPHeight = h
		}
	}
}

// WithKind selects which popup layout to render -- PreviewKindDispatch (the
// default) shows the full dispatch metadata block (target agent, issue,
// prompt key); PreviewKindBash renders just the command itself, since that
// metadata doesn't apply to a plain Bash command gated behind bash_preview.
// An empty or unrecognized value is treated as PreviewKindDispatch.
func WithKind(kind string) DispatchPreviewOption {
	return func(o *dispatchPreviewOpts) {
		if kind != "" {
			o.kind = kind
		}
	}
}

func resolveDispatchPreviewOpts(opts []DispatchPreviewOption) dispatchPreviewOpts {
	o := dispatchPreviewOpts{
		contentWidth: dispatchPreviewContentWidth,
		maxVPHeight:  dpMaxVPHeight,
		kind:         PreviewKindDispatch,
	}
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// dispatchPreview-specific styles (initialised lazily on first use so that
// theme.Current() has been populated by the time they're needed).
var (
	dpBoxStyle      lipgloss.Style
	dpTitleStyle    lipgloss.Style
	dpHintStyle     lipgloss.Style
	dpBodyStyle     lipgloss.Style
	dpToggleStyle   lipgloss.Style
	dpDimStyle      lipgloss.Style
	dpLabelStyle    lipgloss.Style
	dpSubtleStyle   lipgloss.Style
	dpTargetStyle   lipgloss.Style
	dpSkyStyle      lipgloss.Style
	dpKeyAllowStyle lipgloss.Style
	dpKeyDenyStyle  lipgloss.Style
)

func initDispatchPreviewStyles() {
	t := theme.Current()

	// Width() is deliberately NOT set here -- content width now varies per
	// model instance (see DispatchPreviewModel.contentWidth), so it's applied
	// at render time in View() via .Width(m.effectiveContentWidth()+n).
	dpBoxStyle = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(t.Accent)).
		Padding(1, 2)

	dpTitleStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.Bright)).
		Background(lipgloss.Color(t.Accent)).
		Padding(0, 2).
		Align(lipgloss.Center)

	dpHintStyle = lipgloss.NewStyle().
		Align(lipgloss.Center).
		Foreground(lipgloss.Color(t.Muted))

	dpBodyStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Text))

	dpToggleStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Accent)).
		Bold(true)

	dpDimStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Muted))

	dpLabelStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Muted)).
		Width(dpLabelWidth)

	dpSubtleStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Subtle))

	dpTargetStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Accent)).
		Bold(true)

	dpSkyStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Sky))

	dpKeyAllowStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Green)).
		Bold(true)

	dpKeyDenyStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Red)).
		Bold(true)
}

// DispatchPreviewModel is a standalone Bubble Tea model for the dispatch-preview
// popup. It shows the identity of the dispatching agent and target agent, an
// aligned metadata block, and a collapsible, content-aware-rendered prompt
// viewport.
type DispatchPreviewModel struct {
	agentName   string
	targetAgent string
	issue       int    // 0 for ad-hoc prompt dispatches
	prompt      string // --prompt flag value, or the RESOLVED content of --prompt-key
	promptKey   string // --prompt-key flag value (empty unless dispatching via a context key)
	command     string // full original bash command (shown in collapsible section)
	expanded    bool   // whether the prompt viewport is visible
	viewport    viewport.Model
	decision    string // "allow" or "deny"
	vpHeight    int

	// contentWidth is the usable content width inside the popup box. Zero
	// means "use dispatchPreviewContentWidth" -- see effectiveContentWidth().
	// Set via WithContentWidth at construction time, normally from the
	// attached tmux client's dimensions (internal/hooks/popup_clamp.go).
	contentWidth int

	rawMode         bool   // true = show rawContent in the viewport instead of renderedContent
	rawContent      string // the literal prompt/command/placeholder text
	renderedContent string // markdown/syntax-highlighted (or plain, for the placeholder) form

	// kind selects the popup layout -- PreviewKindDispatch (default, zero
	// value) or PreviewKindBash. See WithKind.
	kind string
}

// isBash reports whether this model is previewing a plain Bash command
// (PreviewKindBash) rather than a full "agentctl dispatch" invocation. A
// zero-value model (e.g. one built directly in a test without going through
// NewDispatchPreview) defaults to the dispatch layout, matching
// resolveDispatchPreviewOpts's default.
func (m DispatchPreviewModel) isBash() bool {
	return m.kind == PreviewKindBash
}

// NewDispatchPreview creates a DispatchPreviewModel.
//   - agentName: the agent making the dispatch request (displayed in title)
//   - targetAgent: the agent that will be dispatched
//   - issue: GitHub issue number (0 for ad-hoc)
//   - prompt: --prompt flag value, or the resolved content of --prompt-key
//     (may be empty for issue-based dispatches)
//   - promptKey: --prompt-key flag value, for provenance display only (empty
//     unless dispatching via a context key)
//   - command: the full original bash command (shown when expanded)
//
// opts are optional sizing overrides -- see WithContentWidth and
// WithMaxViewportHeight. With no opts, behavior is unchanged from before
// responsive sizing was added.
func NewDispatchPreview(agentName, targetAgent string, issue int, prompt, promptKey, command string, opts ...DispatchPreviewOption) DispatchPreviewModel {
	initDispatchPreviewStyles()

	o := resolveDispatchPreviewOpts(opts)

	if agentName == "" {
		agentName = "agent"
	}
	if targetAgent == "" {
		if issue > 0 {
			targetAgent = "(auto-detect from labels)"
		} else {
			targetAgent = "agent"
		}
	}

	// The collapsible section shows the prompt if available, or the command.
	rawContent := prompt
	if rawContent == "" {
		rawContent = command
	}

	var renderedContent string
	switch {
	case rawContent == "":
		// Neither a prompt nor a command: leave it plain, dimmed.
		rawContent = "(no additional prompt)"
		renderedContent = dpDimStyle.Render(rawContent)
	case prompt != "":
		// Prose/markdown prompt — render with glamour.
		renderedContent = glamourRender(rawContent, o.contentWidth)
		if strings.TrimSpace(renderedContent) == "" {
			renderedContent = rawContent
		}
	default:
		// Raw shell command — syntax-highlight it. Glamour would mangle a
		// shell command since it isn't markdown.
		h, err := highlight.Highlight(rawContent, "cmd.sh")
		if err != nil || h == "" {
			renderedContent = rawContent
		} else {
			renderedContent = h
		}
	}

	// Calculate viewport height from the RENDERED content — glamour/highlight
	// inflate line count (blank lines, indentation) relative to the raw
	// source, so the raw line count alone would undersize the viewport.
	lines := strings.Count(renderedContent, "\n") + 1
	vpHeight := lines
	if vpHeight > o.maxVPHeight {
		vpHeight = o.maxVPHeight
	}
	if vpHeight < 3 {
		vpHeight = 3
	}

	vp := viewport.New()
	vp.SetWidth(o.contentWidth)
	vp.SetHeight(vpHeight)
	vp.SetContent(renderedContent)

	return DispatchPreviewModel{
		agentName:       agentName,
		targetAgent:     targetAgent,
		issue:           issue,
		prompt:          prompt,
		promptKey:       promptKey,
		command:         command,
		viewport:        vp,
		vpHeight:        vpHeight,
		contentWidth:    o.contentWidth,
		rawContent:      rawContent,
		renderedContent: renderedContent,
		kind:            o.kind,
	}
}

// effectiveContentWidth returns the model's content width, falling back to
// dispatchPreviewContentWidth for a zero-value model (e.g. one constructed
// directly in a test without going through NewDispatchPreview).
func (m DispatchPreviewModel) effectiveContentWidth() int {
	if m.contentWidth <= 0 {
		return dispatchPreviewContentWidth
	}
	return m.contentWidth
}

// PopupSize returns the tmux display-popup dimensions for this model.
// Uses the expanded size so the popup can accommodate the viewport without
// clipping when the user toggles it open.
//
// Chrome line-count arithmetic (keep this in sync with View() — if the
// layout there changes, update both):
//
//	collapsed content lines = title(1) + blank(1) + metadata(3) + blank(1) + toggle(1) = 7
//	expanded adds           = blank(1) + viewport(vpHeight) + blank(1)               = vpHeight+2
//	box border(2) + box padding(2)                                                   = 4
//	tmux border(2)                                                                   = 2
//	hint bar row (rendered outside the box)                                         = 1
//	total = 7 + (vpHeight+2) + 4 + 2 + 1 = vpHeight + 16
func (m DispatchPreviewModel) PopupSize() PopupSize {
	// Width: content + border(2) + padding(4) + tmux border(2) = content + 8.
	w := m.effectiveContentWidth() + 8
	h := m.vpHeight + 16
	return PopupSize{Width: w, Height: h}
}

// DispatchPreviewMaxPopupSize returns the popup dimensions sized for the
// worst case: the viewport filled to its max height. The permission-request
// hook (internal/hooks/dispatch_preview.go) must size the tmux popup BEFORE
// the "dispatch-preview" subprocess ever builds the real model — tmux fixes
// a popup's dimensions at launch time, so the hook cannot know the actual
// rendered line count in advance. Deriving the hook's fixed size from this
// same PopupSize() formula means the two can never drift apart: any change
// to the chrome above flows straight through to the hook automatically.
//
// With no opts, this reproduces the historical fixed 88/22-derived size.
// The hook passes WithContentWidth/WithMaxViewportHeight resolved from the
// attached tmux client's dimensions (see internal/hooks/popup_clamp.go) so
// the popup can grow as well as shrink -- but the arithmetic performed here,
// in PopupSize(), stays the single source of truth either way: the hook only
// ever supplies INPUTS to this formula, never a competing size computation.
func DispatchPreviewMaxPopupSize(opts ...DispatchPreviewOption) PopupSize {
	o := resolveDispatchPreviewOpts(opts)
	return DispatchPreviewModel{vpHeight: o.maxVPHeight, contentWidth: o.contentWidth}.PopupSize()
}

// Decision returns the user's decision: "allow" or "deny".
// Empty string means the model hasn't quit yet.
func (m DispatchPreviewModel) Decision() string { return m.decision }

func (m DispatchPreviewModel) Init() tea.Cmd { return nil }

func (m DispatchPreviewModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "y":
			m.decision = "allow"
			return m, tea.Quit
		case "n", "esc", "q":
			m.decision = "deny"
			return m, tea.Quit
		case "ctrl+c":
			m.decision = "deny"
			return m, tea.Quit
		case "v", " ", "space", "enter":
			m.expanded = !m.expanded
		case "r":
			m.rawMode = !m.rawMode
			m.viewport.SetContent(m.currentViewportContent())
		case "up", "k":
			if m.expanded {
				m.viewport.ScrollUp(1)
			}
		case "down", "j":
			if m.expanded {
				m.viewport.ScrollDown(1)
			}
		case "pgup":
			if m.expanded {
				m.viewport.PageUp()
			}
		case "pgdown":
			if m.expanded {
				m.viewport.PageDown()
			}
		}
	case tea.MouseWheelMsg:
		if m.expanded {
			switch msg.Button {
			case tea.MouseWheelUp:
				m.viewport.ScrollUp(3)
			case tea.MouseWheelDown:
				m.viewport.ScrollDown(3)
			}
		}
	}
	return m, nil
}

// currentViewportContent returns whichever of rawContent/renderedContent
// should currently be shown in the viewport, based on rawMode.
func (m DispatchPreviewModel) currentViewportContent() string {
	if m.rawMode {
		return m.rawContent
	}
	return m.renderedContent
}

func (m DispatchPreviewModel) View() tea.View {
	var b strings.Builder

	// Width is applied per-render (not baked into the package-level styles)
	// because it now varies per model instance -- see contentWidth's doc
	// comment.
	width := m.effectiveContentWidth()

	// Title bar — "{agentName} wants to dispatch {targetAgent}" for a
	// dispatch preview, or "{agentName} wants to run a command" for a plain
	// Bash preview (there is no target agent to name).
	title := fmt.Sprintf("%s wants to dispatch %s", m.agentName, m.targetAgent)
	if m.isBash() {
		title = fmt.Sprintf("%s wants to run a command", m.agentName)
	}
	b.WriteString(dpTitleStyle.Width(width).Render(title))
	b.WriteString("\n\n")

	// Metadata block: who / what / issue-or-prompt, aligned label/value rows.
	for _, line := range m.metadataLines() {
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("\n")

	// Toggle line, with a dim line-count suffix.
	toggleIcon := "▶"
	if m.expanded {
		toggleIcon = "▼"
	}
	toggleLabel := "View prompt"
	if m.isBash() {
		toggleLabel = "View command"
	}
	lineCount := strings.Count(m.rawContent, "\n") + 1
	b.WriteString(dpToggleStyle.Render(toggleIcon + " " + toggleLabel))
	b.WriteString(" ")
	b.WriteString(dpDimStyle.Render(fmt.Sprintf("(%d lines)", lineCount)))
	b.WriteString("\n")

	// Expanded viewport.
	if m.expanded {
		b.WriteString("\n")
		b.WriteString(m.viewport.View())
		b.WriteString("\n")
	}

	// Render the box, then place the hint bar below it (outside the border).
	rendered := dpBoxStyle.Width(width + 6).Render(b.String())

	hint := m.hintLine()

	var out strings.Builder
	out.WriteString(rendered)
	out.WriteString("\n")
	out.WriteString(dpHintStyle.Width(width).Render(hint))

	v := tea.NewView(out.String())
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// metadataLines returns the aligned label/value rows shown above the toggle
// line: who requested the dispatch, who is being dispatched, and either the
// issue number, a truncated prompt preview, or an ad-hoc placeholder.
//
// For a plain Bash preview (PreviewKindBash) the target-agent / issue /
// prompt-key rows are meaningless -- there is no target agent, no issue, no
// prompt -- so they're omitted entirely rather than rendered blank. Only
// "Requested by" survives, since that's still meaningful: which agent wants
// to run the command.
func (m DispatchPreviewModel) metadataLines() []string {
	if m.isBash() {
		return []string{
			dpLabelStyle.Render("Requested by") + dpSubtleStyle.Render(m.agentName),
		}
	}

	lines := []string{
		dpLabelStyle.Render("Requested by") + dpSubtleStyle.Render(m.agentName),
		dpLabelStyle.Render("Dispatching") + dpTargetStyle.Render(m.targetAgent),
	}

	switch {
	case m.issue > 0:
		lines = append(lines, dpLabelStyle.Render("Issue")+dpSkyStyle.Render(m.summaryLine()))
	case m.promptKey != "":
		// The key name is an identifier, like the issue number — style it the
		// same way rather than as free-form body text.
		lines = append(lines, dpLabelStyle.Render("Prompt key")+dpSkyStyle.Render(m.summaryLine()))
	case m.prompt != "":
		lines = append(lines, dpLabelStyle.Render("Prompt")+dpBodyStyle.Render(m.summaryLine()))
	default:
		lines = append(lines, dpLabelStyle.Render("Prompt")+dpDimStyle.Render(m.summaryLine()))
	}
	return lines
}

// hintLine builds the popup's hint bar, with semantic colors on the allow/deny
// keys. The raw/rendered toggle hint is only surfaced when the viewport is
// expanded — it's meaningless while collapsed.
func (m DispatchPreviewModel) hintLine() string {
	hintParts := []string{"y", "allow", "n", "deny", "v", "toggle prompt"}
	keyStyles := []lipgloss.Style{dpKeyAllowStyle, dpKeyDenyStyle, menuHintKeyStyle}

	if m.expanded {
		hintParts = append(hintParts, "↕", "scroll")
		keyStyles = append(keyStyles, menuHintKeyStyle)

		rawLabel := "raw"
		if m.rawMode {
			rawLabel = "rendered"
		}
		hintParts = append(hintParts, "r", rawLabel)
		keyStyles = append(keyStyles, menuHintKeyStyle)
	}

	return renderHintLineStyled(keyStyles, hintParts...)
}

// summaryLine returns the value shown in the metadata block's third row
// (the issue number, the prompt key name, a truncated prompt preview, or the
// ad-hoc placeholder), independent of the label/color used to render it.
// Kept small and testable.
func (m DispatchPreviewModel) summaryLine() string {
	switch {
	case m.issue > 0:
		return fmt.Sprintf("#%d", m.issue)
	case m.promptKey != "":
		// Show the key name, not a preview of the resolved content — the full
		// content is already in the viewport below.
		return m.promptKey
	case m.prompt != "":
		return truncatePromptPreview(m.prompt)
	default:
		return "(ad-hoc dispatch)"
	}
}

// truncatePromptPreview truncates a prompt to 80 characters for the one-line
// metadata preview, appending an ellipsis when truncation occurs.
func truncatePromptPreview(p string) string {
	if len(p) > 80 {
		return p[:79] + "…"
	}
	return p
}
