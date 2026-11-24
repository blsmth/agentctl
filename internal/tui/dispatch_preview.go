package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/highlight"
	"github.com/blsmth/agentctl/internal/remoteapproval"
	"github.com/blsmth/agentctl/internal/textutil"
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

// PreviewKindDispatch, PreviewKindBash and PreviewKindCron mirror
// internal/hooks.PreviewKindDispatch / PreviewKindBash / PreviewKindCron
// (pinned by internal/hooks/preview_kind_drift_test.go). This package cannot
// import internal/hooks (hooks already imports tui to build the model), so
// the string values are duplicated here rather than shared -- keep them in
// sync if either side changes.
const (
	PreviewKindDispatch = "dispatch"
	PreviewKindBash     = "bash"
	PreviewKindCron     = "cron"
	PreviewKindTemplate = "template"
)

// dispatchPreviewOpts holds the resolved, optional sizing inputs for a
// DispatchPreviewModel. Zero values mean "use the historical fixed default"
// -- see dispatchPreviewContentWidth / dpMaxVPHeight above.
type dispatchPreviewOpts struct {
	contentWidth int
	maxVPHeight  int
	kind         string
	now          func() time.Time
	account      string
	cron         CronPreview
	template     TemplatePreview
	reviewPR     int
	project      string
	details      remoteapproval.Details
}

// CronPreview is the job-specific part of a PreviewKindCron popup.
type CronPreview struct {
	// Action is what the command does: "add", "edit", "resume" or "run --force".
	Action string
	// Job is the job name.
	Job string
	// Schedule is the one-line schedule summary (expression, words, next
	// fire) or the parse error.
	Schedule string
}

// TemplatePreview is the template-specific part of a PreviewKindTemplate
// popup, or (Name only) the template a PreviewKindDispatch prompt came from.
type TemplatePreview struct {
	// Action is "add", "edit" or "rm" (template popups only).
	Action string
	// Name is the template name.
	Name string
	// Path is the template file (template popups only).
	Path string
}

// WithTemplate fills the template rows: the file of a PreviewKindTemplate
// popup, or the template a dispatch prompt was resolved from.
func WithTemplate(t TemplatePreview) DispatchPreviewOption {
	return func(o *dispatchPreviewOpts) { o.template = t }
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

// WithAccount names the Claude account the dispatched agent will run under
// (shown next to the target agent). Empty shows nothing (no accounts
// configured).
func WithAccount(account string) DispatchPreviewOption {
	return func(o *dispatchPreviewOpts) { o.account = account }
}

// WithProject names the project the dispatched agent will run in, shown in
// the title ("X wants to dispatch Y to P"). Empty leaves the title as is.
func WithProject(project string) DispatchPreviewOption {
	return func(o *dispatchPreviewOpts) { o.project = project }
}

// WithDetails supplies the resolved dispatch plan (project and dir, model,
// backend, harness, worktree, permission mode) shown as extra rows under
// "Dispatching". Rows whose values are all empty are skipped, so an older or
// partial Details degrades cleanly. Dispatch popups only.
func WithDetails(d remoteapproval.Details) DispatchPreviewOption {
	return func(o *dispatchPreviewOpts) { o.details = d }
}

// WithReviewPR marks a `dispatch --review-pr` preview: the third metadata
// row shows the PR number instead of an issue or prompt.
func WithReviewPR(n int) DispatchPreviewOption {
	return func(o *dispatchPreviewOpts) { o.reviewPR = n }
}

// WithCron fills the job rows of a PreviewKindCron popup.
func WithCron(c CronPreview) DispatchPreviewOption {
	return func(o *dispatchPreviewOpts) { o.cron = c }
}

// WithClock injects the time source used for the click arming delay. Nil is
// ignored. Intended for tests.
func WithClock(now func() time.Time) DispatchPreviewOption {
	return func(o *dispatchPreviewOpts) {
		if now != nil {
			o.now = now
		}
	}
}

func resolveDispatchPreviewOpts(opts []DispatchPreviewOption) dispatchPreviewOpts {
	o := dispatchPreviewOpts{
		contentWidth: dispatchPreviewContentWidth,
		maxVPHeight:  dpMaxVPHeight,
		kind:         PreviewKindDispatch,
		now:          time.Now,
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
	dpWarnStyle     lipgloss.Style
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

	dpWarnStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.Yellow)).
		Bold(true)
}

// DispatchPreviewModel is a standalone Bubble Tea model for the dispatch-preview
// popup. It shows the identity of the dispatching agent and target agent, an
// aligned metadata block, and a collapsible, content-aware-rendered prompt
// viewport.
type DispatchPreviewModel struct {
	agentName   string
	targetAgent string
	issue       int                    // 0 for ad-hoc prompt dispatches
	reviewPR    int                    // --review-pr number; 0 otherwise
	prompt      string                 // --prompt flag value, or the RESOLVED content of --prompt-key
	promptKey   string                 // --prompt-key flag value (empty unless dispatching via a context key)
	command     string                 // full original bash command (shown in collapsible section)
	account     string                 // resolved Claude account of the dispatched agent; "" = none configured
	project     string                 // target project of the dispatch; "" = unknown
	details     remoteapproval.Details // resolved plan; zero = none (older hook)
	expanded    bool                   // whether the prompt viewport is visible
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
	// value), PreviewKindBash or PreviewKindCron. See WithKind.
	kind string
	// cron holds the job rows for PreviewKindCron.
	cron CronPreview
	// template holds the rows for PreviewKindTemplate, or the template
	// name of a dispatch.
	template TemplatePreview

	// now is the clock for the click arming delay (nil = time.Now);
	// openedAt is when the popup was created; pressed is the target of the
	// last left-button press (dpTargetNone when none).
	now      func() time.Time
	openedAt time.Time
	pressed  dpTarget

	// autoExpand is true when the popup opens expanded by default (dispatch
	// and cron); toggled records that the user flipped the section by hand,
	// after which the window size no longer overrides it.
	autoExpand bool
	toggled    bool
}

// dpClickArmDelay is how long after opening the popup ignores mouse clicks.
// The popup steals focus from whatever the user was doing, so a click meant
// for the pane underneath must not approve or deny.
const dpClickArmDelay = 400 * time.Millisecond

// dpTarget identifies a clickable region of the popup.
type dpTarget int

const (
	dpTargetNone dpTarget = iota
	dpTargetAllow
	dpTargetDeny
	dpTargetToggle
	dpTargetRaw
)

// dpHintSeg is one hint-bar segment: its styled text and the target a click
// on it triggers (dpTargetNone = not clickable).
type dpHintSeg struct {
	text   string
	target dpTarget
}

// isBash reports whether this model is previewing a plain Bash command
// (PreviewKindBash) rather than a full "agentctl dispatch" invocation. A
// zero-value model (e.g. one built directly in a test without going through
// NewDispatchPreview) defaults to the dispatch layout, matching
// resolveDispatchPreviewOpts's default.
func (m DispatchPreviewModel) isBash() bool {
	return m.kind == PreviewKindBash
}

// isCron reports whether this model previews an `agentctl cron` command
// (PreviewKindCron): the job summary and prompt are in the viewport, the
// job name and schedule in the metadata rows.
func (m DispatchPreviewModel) isCron() bool {
	return m.kind == PreviewKindCron
}

// isTemplate reports whether this model previews an `agentctl template
// add|edit|rm` (PreviewKindTemplate).
func (m DispatchPreviewModel) isTemplate() bool {
	return m.kind == PreviewKindTemplate
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
		if o.reviewPR > 0 {
			targetAgent = "code-reviewer"
		} else if issue > 0 {
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

	// A cron popup opens expanded: the job details (dir, harness, worktree,
	// timeout) live in the viewport and are the point of the review.
	// PopupSize already budgets for the expanded layout.
	// Dispatch popups open expanded too, so the full prompt is visible
	// without a keypress.
	expanded := o.kind == PreviewKindCron || o.kind == PreviewKindDispatch || o.kind == PreviewKindTemplate

	return DispatchPreviewModel{
		agentName:       agentName,
		targetAgent:     targetAgent,
		issue:           issue,
		reviewPR:        o.reviewPR,
		prompt:          prompt,
		promptKey:       promptKey,
		command:         command,
		account:         o.account,
		project:         o.project,
		details:         o.details,
		viewport:        vp,
		vpHeight:        vpHeight,
		contentWidth:    o.contentWidth,
		rawContent:      rawContent,
		renderedContent: renderedContent,
		kind:            o.kind,
		cron:            o.cron,
		template:        o.template,
		expanded:        expanded,
		autoExpand:      expanded,
		now:             o.now,
		openedAt:        o.now(),
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
//	collapsed content lines = title(1) + blank(1) + metadata(3 + detail rows) + blank(1) + toggle(1) = 7 + detail rows
//	expanded adds           = blank(1) + viewport(vpHeight)                          = vpHeight+1
//	(one spare row of headroom remains below the box)
//	box border(2) + box padding(2)                                                   = 4
//	tmux border(2)                                                                   = 2
//	hint bar row (rendered outside the box)                                         = 1
//	total = 7 + (vpHeight+1) + 4 + 2 + 1 = vpHeight + 15, budgeted as vpHeight + 16
//
// A dispatch popup adds one row per DispatchDetailRows (0-3: Project, Model,
// Runs) on top of that; the other kinds have none.
func (m DispatchPreviewModel) PopupSize() PopupSize {
	// Width: content + border(2) + padding(4) + tmux border(2) = content + 8.
	w := m.effectiveContentWidth() + 8
	h := m.vpHeight + 16 + m.detailRowCount()
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
	return DispatchPreviewModel{vpHeight: o.maxVPHeight, contentWidth: o.contentWidth, kind: o.kind, details: o.details}.PopupSize()
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
			m.toggled = true
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
	case tea.WindowSizeMsg:
		// The popup may be clamped to a short terminal. Open collapsed when
		// the expanded layout plus the hint bar cannot fit, so the approve
		// and deny hints never fall off screen. A manual toggle wins.
		if !m.toggled && m.autoExpand {
			m.expanded = m.fitsExpanded(msg.Height)
		}
	case tea.MouseClickMsg:
		if msg.Button == tea.MouseLeft {
			m.pressed = m.targetAt(msg.X, msg.Y)
		} else {
			m.pressed = dpTargetNone
		}
	case tea.MouseReleaseMsg:
		pressed := m.pressed
		m.pressed = dpTargetNone
		if msg.Button != tea.MouseLeft || pressed == dpTargetNone || !m.clickArmed() {
			return m, nil
		}
		if m.targetAt(msg.X, msg.Y) != pressed {
			return m, nil
		}
		switch pressed {
		case dpTargetAllow:
			m.decision = "allow"
			return m, tea.Quit
		case dpTargetDeny:
			m.decision = "deny"
			return m, tea.Quit
		case dpTargetToggle:
			m.expanded = !m.expanded
			m.toggled = true
		case dpTargetRaw:
			m.rawMode = !m.rawMode
			m.viewport.SetContent(m.currentViewportContent())
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

// fitsExpanded reports whether the expanded box plus the hint bar row fit in
// height rows. It measures the real View layout, so it follows any change to
// the chrome. A non-positive height (size unknown) fits.
func (m DispatchPreviewModel) fitsExpanded(height int) bool {
	if height <= 0 {
		return true
	}
	m.expanded = true
	return lipgloss.Height(m.renderBox())+1 <= height
}

// currentViewportContent returns whichever of rawContent/renderedContent
// should currently be shown in the viewport, based on rawMode.
func (m DispatchPreviewModel) currentViewportContent() string {
	if m.rawMode {
		return m.rawContent
	}
	return m.renderedContent
}

// renderBox renders the bordered box (everything except the hint bar).
func (m DispatchPreviewModel) renderBox() string {
	var b strings.Builder
	b.WriteString(m.preToggle())
	b.WriteString(m.toggleText())

	// Expanded viewport. No trailing newline after either the toggle or the
	// viewport: a stray one adds a blank row inside the box that PopupSize
	// does not budget for.
	if m.expanded {
		b.WriteString("\n\n")
		b.WriteString(m.viewport.View())
	}
	return dpBoxStyle.Width(m.effectiveContentWidth() + 6).Render(b.String())
}

func (m DispatchPreviewModel) View() tea.View {
	width := m.effectiveContentWidth()
	rendered := m.renderBox()

	hint := m.hintLine()

	var out strings.Builder
	out.WriteString(rendered)
	out.WriteString("\n")
	out.WriteString(dpHintStyle.Width(width).Render(hint))

	v := tea.NewView(out.String())
	// Alt screen so a view taller than the popup can never make the inline
	// renderer scroll and redraw (flicker).
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// preToggle renders everything above the toggle line (title, blank, metadata,
// blank), ending in a newline.
func (m DispatchPreviewModel) preToggle() string {
	var b strings.Builder
	width := m.effectiveContentWidth()

	// Title bar — "{agentName} wants to dispatch {targetAgent}" for a
	// dispatch preview, or "{agentName} wants to run a command" for a plain
	// Bash preview (there is no target agent to name).
	title := fmt.Sprintf("%s wants to dispatch %s", m.agentName, m.targetAgent)
	switch {
	case m.isBash():
		title = fmt.Sprintf("%s wants to run a command", m.agentName)
	case m.isCron():
		title = m.cronTitle()
	case m.isTemplate():
		title = m.templateTitle()
	case m.project != "":
		// One line (inside the title's 2-column padding), so PopupSize's
		// line budget holds.
		title = textutil.Truncate(title+" to "+m.project, width-4, "…")
	}
	b.WriteString(dpTitleStyle.Width(width).Render(title))
	b.WriteString("\n\n")

	// Metadata block: who / what / issue-or-prompt, aligned label/value rows.
	for _, line := range m.metadataLines() {
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}

// toggleText renders the toggle line, with a dim line-count suffix.
func (m DispatchPreviewModel) toggleText() string {
	toggleIcon := "▶"
	if m.expanded {
		toggleIcon = "▼"
	}
	toggleLabel := "View prompt"
	switch {
	case m.isBash():
		toggleLabel = "View command"
	case m.isCron():
		toggleLabel = "View job"
	case m.isTemplate():
		toggleLabel = "View template"
	}
	lineCount := strings.Count(m.rawContent, "\n") + 1
	return dpToggleStyle.Render(toggleIcon+" "+toggleLabel) + " " +
		dpDimStyle.Render(fmt.Sprintf("(%d lines)", lineCount))
}

// clickArmed reports whether the arming delay since opening has elapsed.
func (m DispatchPreviewModel) clickArmed() bool {
	if m.openedAt.IsZero() {
		return true
	}
	now := time.Now
	if m.now != nil {
		now = m.now
	}
	return now().Sub(m.openedAt) >= dpClickArmDelay
}

// targetAt maps a cell coordinate (relative to the popup) to a click target.
// Regions are derived by measuring the same strings View() renders.
func (m DispatchPreviewModel) targetAt(x, y int) dpTarget {
	// Toggle line inside the box.
	toggleY := dpBoxStyle.GetBorderTopSize() + dpBoxStyle.GetPaddingTop() +
		strings.Count(m.preToggle(), "\n")
	if y == toggleY {
		left := dpBoxStyle.GetBorderLeftSize() + dpBoxStyle.GetPaddingLeft()
		if x >= left && x < left+lipgloss.Width(m.toggleText()) {
			return dpTargetToggle
		}
		return dpTargetNone
	}

	// Hint bar below the box.
	box := m.renderBox()
	if y != lipgloss.Height(box) {
		return dpTargetNone
	}
	hint := dpHintStyle.Width(m.effectiveContentWidth()).Render(m.hintLine())
	stripped := ansi.Strip(hint)
	lead := len(stripped) - len(strings.TrimLeft(stripped, " "))
	cx := lead
	sep := lipgloss.Width(hintSeparator())
	for i, seg := range m.hintSegments() {
		if i > 0 {
			cx += sep
		}
		w := lipgloss.Width(seg.text)
		if x >= cx && x < cx+w {
			return seg.target
		}
		cx += w
	}
	return dpTargetNone
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
	if m.isCron() {
		// Exactly three rows, like the dispatch layout, so PopupSize's
		// line budget holds.
		job := m.cron.Job
		if job == "" {
			job = "(unnamed)"
		}
		return []string{
			dpLabelStyle.Render("Requested by") + dpSubtleStyle.Render(m.agentName),
			dpLabelStyle.Render("Job") + dpSkyStyle.Render(job) + dpDimStyle.Render(" · agent ") +
				dpTargetStyle.Render(m.targetAgent) + m.accountSuffix(),
			dpLabelStyle.Render("Schedule") + dpBodyStyle.Render(m.cron.Schedule),
		}
	}

	if m.isTemplate() {
		// Three rows, like the dispatch layout (PopupSize's line budget).
		agent := m.targetAgent
		if agent == "" {
			agent = "(none)"
		}
		return []string{
			dpLabelStyle.Render("Requested by") + dpSubtleStyle.Render(m.agentName),
			dpLabelStyle.Render("Template") + dpSkyStyle.Render(m.template.Name) + dpDimStyle.Render(" · agent ") +
				dpTargetStyle.Render(agent),
			dpLabelStyle.Render("File") + dpBodyStyle.Render(textutil.Truncate(m.template.Path, m.effectiveContentWidth()-dpLabelWidth-2, "…")),
		}
	}

	lines := []string{
		dpLabelStyle.Render("Requested by") + dpSubtleStyle.Render(m.agentName),
		dpLabelStyle.Render("Dispatching") + dpTargetStyle.Render(m.targetAgent) + m.accountSuffix(),
	}
	lines = append(lines, m.detailLines()...)

	switch {
	case m.reviewPR > 0:
		lines = append(lines, dpLabelStyle.Render("Review PR")+dpSkyStyle.Render(m.rowSummary()))
	case m.issue > 0:
		lines = append(lines, dpLabelStyle.Render("Issue")+dpSkyStyle.Render(m.rowSummary()))
	case m.template.Name != "":
		lines = append(lines, dpLabelStyle.Render("Template")+dpSkyStyle.Render(m.template.Name))
	case m.promptKey != "":
		// The key name is an identifier, like the issue number — style it the
		// same way rather than as free-form body text.
		lines = append(lines, dpLabelStyle.Render("Prompt key")+dpSkyStyle.Render(m.rowSummary()))
	case m.prompt != "":
		lines = append(lines, dpLabelStyle.Render("Prompt")+dpBodyStyle.Render(m.rowSummary()))
	default:
		lines = append(lines, dpLabelStyle.Render("Prompt")+dpDimStyle.Render(m.rowSummary()))
	}
	return lines
}

// dpSeg is one styled run of a detail row.
type dpSeg struct {
	text  string
	style lipgloss.Style
}

// detailRows builds the Project / Model / Runs rows of a dispatch popup from
// the resolved plan, mirroring the phone's wording (approvalRows in
// internal/serve/web/logic.js). A row whose values are all empty is skipped.
// Segments are listed most important first: fitSegs cuts from the end.
func (m DispatchPreviewModel) detailRows() []struct {
	label string
	segs  []dpSeg
} {
	if m.isBash() || m.isCron() || m.isTemplate() {
		return nil
	}
	d := m.details
	var rows []struct {
		label string
		segs  []dpSeg
	}
	add := func(label string, segs ...dpSeg) {
		rows = append(rows, struct {
			label string
			segs  []dpSeg
		}{label, segs})
	}

	switch {
	case d.Project != "":
		segs := []dpSeg{{d.Project, dpBodyStyle}}
		if d.OtherProject {
			asking := d.AskingProject
			if asking == "" {
				asking = "the asking project"
			}
			segs = append(segs, dpSeg{"  ⚠ not " + asking, dpWarnStyle})
		}
		if d.Dir != "" {
			segs = append(segs, dpSeg{"  " + d.Dir, dpDimStyle})
		}
		add("Project", segs...)
	case d.DirSource != "":
		// A cwd the command changes first: the project cannot be known.
		add("Project", dpSeg{"unknown", dpWarnStyle}, dpSeg{"  " + d.DirSource, dpDimStyle})
	}

	if d.Model != "" || d.Backend != "" || d.Harness != "" {
		var segs []dpSeg
		if d.Model != "" {
			segs = append(segs, dpSeg{d.Model, dpBodyStyle})
			if d.ModelSource != "" {
				segs = append(segs, dpSeg{" · " + d.ModelSource, dpDimStyle})
			}
		}
		if d.Backend != "" {
			segs = append(segs, dpSeg{" · backend " + d.Backend, dpDimStyle})
		}
		if d.Harness != "" {
			segs = append(segs, dpSeg{" · harness " + d.Harness, dpDimStyle})
		}
		add("Model", segs...)
	}

	if d.Worktree != "" || d.PermissionMode != "" {
		var segs []dpSeg
		if d.Worktree != "" {
			segs = append(segs, dpSeg{d.Worktree, dpBodyStyle})
		}
		if d.PermissionMode != "" {
			sep := ""
			if d.Worktree != "" {
				sep = " · "
			}
			segs = append(segs, dpSeg{sep + d.PermissionMode + " permissions", dpDimStyle})
		}
		add("Runs", segs...)
	}
	return rows
}

// detailRowCount is how many detail rows the popup renders.
func (m DispatchPreviewModel) detailRowCount() int { return len(m.detailRows()) }

// DispatchDetailRows reports how many extra rows (Project, Model, Runs) a
// dispatch popup shows for d. The hook subtracts it from the viewport budget
// so the popup still fits a short client.
func DispatchDetailRows(d remoteapproval.Details) int {
	initDispatchPreviewStyles()
	return DispatchPreviewModel{details: d}.detailRowCount()
}

// detailLines renders the detail rows, each on exactly one line: segments are
// cut to the content width left after the label.
func (m DispatchPreviewModel) detailLines() []string {
	avail := m.effectiveContentWidth() - dpLabelWidth
	var lines []string
	for _, r := range m.detailRows() {
		lines = append(lines, dpLabelStyle.Render(r.label)+fitSegs(r.segs, avail))
	}
	return lines
}

// fitSegs renders segs on one line of at most width cells, truncating (with an
// ellipsis) the first segment that runs out of room and dropping the rest.
func fitSegs(segs []dpSeg, width int) string {
	var b strings.Builder
	left := width
	for _, s := range segs {
		if left <= 0 {
			break
		}
		t := textutil.Truncate(s.text, left, "…")
		b.WriteString(s.style.Render(t))
		left -= utf8.RuneCountInString(t)
	}
	return b.String()
}

// rowSummary is summaryLine cut to the room left after the label, so the
// row stays on one line at any width (PopupSize's line budget).
func (m DispatchPreviewModel) rowSummary() string {
	return textutil.Truncate(m.summaryLine(), m.effectiveContentWidth()-dpLabelWidth, "…")
}

// templateTitle is the title line of a template popup.
func (m DispatchPreviewModel) templateTitle() string {
	switch m.template.Action {
	case "edit":
		return fmt.Sprintf("%s wants to edit prompt template %s", m.agentName, m.template.Name)
	case "rm":
		return fmt.Sprintf("%s wants to remove prompt template %s", m.agentName, m.template.Name)
	}
	return fmt.Sprintf("%s wants to add prompt template %s", m.agentName, m.template.Name)
}

// cronTitle is the title line of a cron popup.
func (m DispatchPreviewModel) cronTitle() string {
	switch m.cron.Action {
	case "resume":
		return fmt.Sprintf("%s wants to resume cron job %s", m.agentName, m.cron.Job)
	case "run --force":
		return fmt.Sprintf("%s wants to force-run cron job %s", m.agentName, m.cron.Job)
	case "edit":
		return fmt.Sprintf("%s wants to edit cron job %s", m.agentName, m.cron.Job)
	}
	return fmt.Sprintf("%s wants to schedule %s", m.agentName, m.targetAgent)
}

// accountSuffix renders the resolved account after the target agent, on the
// same row so the popup's line budget (PopupSize) is unchanged.
func (m DispatchPreviewModel) accountSuffix() string {
	if m.account == "" {
		return ""
	}
	return dpDimStyle.Render(" · account ") + dpSkyStyle.Render(m.account)
}

// hintSegments builds the popup's hint bar segments, with semantic colors on
// the allow/deny keys. The raw/rendered toggle hint is only surfaced when the
// viewport is expanded — it's meaningless while collapsed.
func (m DispatchPreviewModel) hintSegments() []dpHintSeg {
	seg := func(ks lipgloss.Style, key, label string, t dpTarget) dpHintSeg {
		return dpHintSeg{text: ks.Render(key) + " " + menuHintLabelStyle.Render(label), target: t}
	}
	segs := []dpHintSeg{
		seg(dpKeyAllowStyle, "y", "allow", dpTargetAllow),
		seg(dpKeyDenyStyle, "n", "deny", dpTargetDeny),
		seg(menuHintKeyStyle, "v", "toggle prompt", dpTargetToggle),
	}
	if m.expanded {
		segs = append(segs, seg(menuHintKeyStyle, "↕", "scroll", dpTargetNone))
		rawLabel := "raw"
		if m.rawMode {
			rawLabel = "rendered"
		}
		segs = append(segs, seg(menuHintKeyStyle, "r", rawLabel, dpTargetRaw))
	}
	return segs
}

func hintSeparator() string {
	return "  " + menuHintDotStyle.Render("·") + "  "
}

// hintLine joins the hint segments into the hint bar text.
func (m DispatchPreviewModel) hintLine() string {
	var parts []string
	for _, s := range m.hintSegments() {
		parts = append(parts, s.text)
	}
	return strings.Join(parts, hintSeparator())
}

// summaryLine returns the value shown in the metadata block's third row
// (the issue number, the prompt key name, a truncated prompt preview, or the
// ad-hoc placeholder), independent of the label/color used to render it.
// Kept small and testable.
func (m DispatchPreviewModel) summaryLine() string {
	switch {
	case m.reviewPR > 0:
		return fmt.Sprintf("#%d", m.reviewPR)
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

// truncatePromptPreview returns a one-line preview of a prompt: the first
// non-blank line, whitespace collapsed, truncated to 80 runes with an ellipsis.
// The preview must never contain a newline, since it fills a single metadata row.
func truncatePromptPreview(p string) string {
	for _, line := range strings.Split(p, "\n") {
		if f := strings.Join(strings.Fields(line), " "); f != "" {
			return textutil.Truncate(f, 80, "…")
		}
	}
	return ""
}
