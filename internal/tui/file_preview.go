package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/highlight"
	"github.com/blsmth/agentctl/internal/theme"
)

// filePreviewContentWidth is the DEFAULT usable content width inside the
// popup box, used whenever a caller doesn't supply WithFileContentWidth.
// ModalLarge = 108 cols content. tmux popup border = 2, lipgloss border = 2,
// padding = 4 → popup -w = 108 + 2 + 2 + 4 = 116.
const filePreviewContentWidth = 108

// filePreviewMaxVPHeight is the DEFAULT cap on the viewport, used whenever a
// caller doesn't supply WithFileMaxViewportHeight. It caps the viewport at
// this many lines regardless of how long the previewed file is. Shared by
// NewFilePreview (to size the real viewport) and FilePreviewMaxPopupSize (to
// size the tmux popup for the worst case before the real model exists) so
// the two can never drift apart.
const filePreviewMaxVPHeight = 32

// filePreviewOpts holds the resolved, optional sizing inputs for a
// FilePreviewModel. Zero values mean "use the historical fixed default" --
// see filePreviewContentWidth / filePreviewMaxVPHeight above.
type filePreviewOpts struct {
	contentWidth int
	maxVPHeight  int
}

// FilePreviewOption configures optional sizing inputs to NewFilePreview and
// FilePreviewMaxPopupSize. Functional options (rather than widening the
// constructor's positional argument list) keep every existing call site
// compiling unchanged with the historical 108/32 behavior.
type FilePreviewOption func(*filePreviewOpts)

// WithFileContentWidth overrides the popup's usable content width. Values
// <= 0 are ignored (keeps the default). Intended for a width derived from
// the attached tmux client's dimensions -- see internal/hooks/popup_clamp.go.
func WithFileContentWidth(w int) FilePreviewOption {
	return func(o *filePreviewOpts) {
		if w > 0 {
			o.contentWidth = w
		}
	}
}

// WithFileMaxViewportHeight overrides the cap applied to the file viewport's
// height. Values <= 0 are ignored (keeps the default). This is the SAME
// value that must be passed to FilePreviewMaxPopupSize when sizing the tmux
// popup up front -- see that function's doc comment.
func WithFileMaxViewportHeight(h int) FilePreviewOption {
	return func(o *filePreviewOpts) {
		if h > 0 {
			o.maxVPHeight = h
		}
	}
}

func resolveFilePreviewOpts(opts []FilePreviewOption) filePreviewOpts {
	o := filePreviewOpts{
		contentWidth: filePreviewContentWidth,
		maxVPHeight:  filePreviewMaxVPHeight,
	}
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// filePreview-specific styles (initialised lazily on first use so that
// theme.Current() has been populated by the time they're needed).
var (
	filePreviewBoxStyle   lipgloss.Style
	filePreviewTitleStyle lipgloss.Style
	filePreviewHintStyle  lipgloss.Style
)

func initFilePreviewStyles() {
	t := theme.Current()

	// Width() is deliberately NOT set here -- content width now varies per
	// model instance (see FilePreviewModel.width), so it's applied at render
	// time in View() via .Width(m.effectiveWidth()+n).
	filePreviewBoxStyle = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(t.Accent)).
		Padding(1, 2)

	filePreviewTitleStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.Bright)).
		Background(lipgloss.Color(t.Accent)).
		Padding(0, 2).
		Align(lipgloss.Center)

	filePreviewHintStyle = lipgloss.NewStyle().
		Align(lipgloss.Center).
		Foreground(lipgloss.Color(t.Muted))
}

// FilePreviewModel is a standalone Bubble Tea model for the file-preview popup.
// It displays a syntax-highlighted file with a scrollable viewport and
// allow/deny decision keys.
type FilePreviewModel struct {
	filePath    string
	agentName   string
	content     string
	highlighted string // ANSI-highlighted content from highlight package
	viewport    viewport.Model
	decision    string // "allow" or "deny" — set on keypress, triggers tea.Quit
	width       int
	height      int
	isBinary    bool
}

// NewFilePreview creates a FilePreviewModel for the given file path and content.
// agentName is displayed in the title bar ("agentName wants to write to filePath").
// Syntax highlighting is applied immediately; falls back to plain content on error.
//
// opts are optional sizing overrides -- see WithFileContentWidth and
// WithFileMaxViewportHeight. With no opts, behavior is unchanged from before
// responsive sizing was added.
func NewFilePreview(filePath, content, agentName string, opts ...FilePreviewOption) FilePreviewModel {
	initFilePreviewStyles()

	o := resolveFilePreviewOpts(opts)

	if agentName == "" {
		agentName = "Agent"
	}

	isBin := highlight.IsBinary(content)

	var highlighted string
	if isBin {
		highlighted = fmt.Sprintf("(binary file — %d bytes)", len(content))
	} else if content == "" {
		highlighted = "(empty file)"
	} else {
		h, err := highlight.Highlight(content, filepath.Base(filePath))
		if err != nil || h == "" {
			highlighted = content
		} else {
			highlighted = h
		}
	}

	// Calculate viewport height: cap at o.maxVPHeight lines, minus chrome
	// (title=1, blank=1, border+padding~=4, hint=2) ≈ 8 lines of chrome.
	lines := strings.Count(highlighted, "\n") + 1
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
	vp.SetContent(highlighted)

	return FilePreviewModel{
		filePath:    filePath,
		agentName:   agentName,
		content:     content,
		highlighted: highlighted,
		viewport:    vp,
		width:       o.contentWidth,
		height:      vpHeight,
		isBinary:    isBin,
	}
}

// effectiveWidth returns the model's content width, falling back to
// filePreviewContentWidth for a zero-value model (e.g. one constructed
// directly in a test without going through NewFilePreview).
func (m FilePreviewModel) effectiveWidth() int {
	if m.width <= 0 {
		return filePreviewContentWidth
	}
	return m.width
}

// PopupSize returns the tmux display-popup dimensions for this model.
func (m FilePreviewModel) PopupSize() PopupSize {
	// Width: content + border(2) + padding(4) + tmux border(2) = content + 8.
	w := m.effectiveWidth() + 8
	// Height: title(1) + blank(1) + viewport + blank(1) +
	//         hint(1) + box border(2) + box padding(2) + tmux border(2) = viewport + 10.
	h := m.height + 12
	return PopupSize{Width: w, Height: h}
}

// FilePreviewMaxPopupSize returns the popup dimensions sized for the worst
// case: the viewport capped at filePreviewMaxVPHeight. The permission-request
// hook (internal/hooks/preview.go) must size the tmux popup BEFORE the
// "file-preview" subprocess ever builds the real model -- tmux fixes a
// popup's dimensions at launch time, so the hook cannot know the actual
// rendered line count (which depends on the file being written) in advance.
// Deriving the hook's fixed size from this same PopupSize() formula means the
// two can never drift apart: any change to the chrome above flows straight
// through to the hook automatically. Mirrors DispatchPreviewMaxPopupSize.
//
// With no opts, this reproduces the historical fixed 108/32-derived size.
func FilePreviewMaxPopupSize(opts ...FilePreviewOption) PopupSize {
	o := resolveFilePreviewOpts(opts)
	return FilePreviewModel{height: o.maxVPHeight, width: o.contentWidth}.PopupSize()
}

// Decision returns the user's decision: "allow" or "deny".
// Empty string means the model hasn't quit yet.
func (m FilePreviewModel) Decision() string { return m.decision }

func (m FilePreviewModel) Init() tea.Cmd { return nil }

func (m FilePreviewModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		case "up", "k":
			m.viewport.ScrollUp(1)
		case "down", "j":
			m.viewport.ScrollDown(1)
		case "pgup":
			m.viewport.PageUp()
		case "pgdown", " ":
			m.viewport.PageDown()
		}
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			m.viewport.ScrollUp(3)
		case tea.MouseWheelDown:
			m.viewport.ScrollDown(3)
		}
	}
	return m, nil
}

func (m FilePreviewModel) View() tea.View {
	var b strings.Builder

	// Width is applied per-render (not baked into the package-level styles)
	// because it now varies per model instance -- see effectiveWidth's doc
	// comment.
	width := m.effectiveWidth()

	// Title bar — "{agentName} wants to write to {filePath}"
	title := m.agentName + " wants to write to " + m.filePath

	b.WriteString(filePreviewTitleStyle.Width(width).Render(title))
	b.WriteString("\n\n")

	// Viewport with highlighted content
	b.WriteString(m.viewport.View())

	// Render the box, then place the hint bar below it (outside the border).
	rendered := filePreviewBoxStyle.Width(width + 6).Render(b.String())

	hint := renderHintLine("y", "allow", "n", "deny", "↕", "scroll")
	var out strings.Builder
	out.WriteString(rendered)
	out.WriteString("\n")
	out.WriteString(filePreviewHintStyle.Width(width).Render(hint))

	v := tea.NewView(out.String())
	v.MouseMode = tea.MouseModeCellMotion
	return v
}
