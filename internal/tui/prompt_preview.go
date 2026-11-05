package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/glamour"

	"github.com/blsmth/agentctl/internal/prompt"
	"github.com/blsmth/agentctl/internal/theme"
)

// PromptPreviewComponent renders a resolved agent prompt with glamour markdown
// rendering, a token breakdown bar, section provenance separators, and a
// scrollable viewport.
//
// It is NOT a standalone tea.Model — the parent model owns a
// PromptPreviewComponent value, calls Update/View, and delegates relevant
// key messages to it. Content is supplied via SetContent() and dimensions via
// SetSize().
type PromptPreviewComponent struct {
	width  int
	height int

	content  *prompt.ResolvedPrompt
	rawMode  bool // true = show raw .md source; false = glamour-rendered
	viewport viewport.Model

	// cached rendered strings so we only re-render on content/size change
	renderedExpanded string
	renderedRaw      string
	dirty            bool // true when a re-render is needed
}

// SetSize updates the terminal dimensions. Call this whenever the terminal
// resizes; it updates the viewport dimensions and marks the component dirty
// so the next View() call re-renders with the new width.
func (p *PromptPreviewComponent) SetSize(w, h int) {
	if p.width == w && p.height == h {
		return
	}
	p.width = w
	p.height = h
	p.viewport.SetWidth(w)
	// Reserve 2 lines for the token bar above the viewport.
	vpH := h - 2
	if vpH < 1 {
		vpH = 1
	}
	p.viewport.SetHeight(vpH)
	p.dirty = true
}

// SetContent replaces the displayed ResolvedPrompt. Pass nil to clear.
func (p *PromptPreviewComponent) SetContent(rp *prompt.ResolvedPrompt) {
	p.content = rp
	p.renderedExpanded = ""
	p.renderedRaw = ""
	p.dirty = true
	p.viewport.GotoTop()
}

// Update handles key presses for the preview component. Returns true when the
// message was consumed.
func (p *PromptPreviewComponent) Update(msg tea.KeyPressMsg) bool {
	switch msg.String() {
	case "r":
		p.rawMode = !p.rawMode
		p.refreshViewport()
		return true
	case "pgup":
		p.viewport.PageUp()
		return true
	case "pgdown":
		p.viewport.PageDown()
		return true
	case "home":
		p.viewport.GotoTop()
		return true
	case "end":
		p.viewport.GotoBottom()
		return true
	case "up", "k":
		p.viewport.ScrollUp(1)
		return true
	case "down", "j":
		p.viewport.ScrollDown(1)
		return true
	}
	return false
}

// View renders the token bar and the scrollable viewport.
func (p *PromptPreviewComponent) View() string {
	if p.dirty {
		p.refreshViewport()
	}

	var b strings.Builder
	b.WriteString(p.renderTokenBar())
	b.WriteString("\n")
	b.WriteString(p.renderModeHint())
	b.WriteString("\n")
	b.WriteString(p.viewport.View())
	return b.String()
}

// ── internal helpers ──────────────────────────────────────────────────────────

// refreshViewport re-renders the glamour/raw content and loads it into the
// viewport. Called lazily from View() when dirty==true.
func (p *PromptPreviewComponent) refreshViewport() {
	p.dirty = false
	if p.content == nil {
		p.viewport.SetContent("")
		return
	}
	if p.rawMode {
		if p.renderedRaw == "" {
			p.renderedRaw = p.buildRaw()
		}
		p.viewport.SetContent(p.renderedRaw)
	} else {
		if p.renderedExpanded == "" {
			p.renderedExpanded = p.buildExpanded()
		}
		p.viewport.SetContent(p.renderedExpanded)
	}
}

// renderTokenBar renders the context-usage bar:
//
//	[||||||||          ] 18% of context · ~12,400 tokens
func (p *PromptPreviewComponent) renderTokenBar() string {
	t := theme.Current()
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted))
	accentStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(t.Accent))

	if p.content == nil {
		return dimStyle.Render("  (no content)")
	}

	td := p.content.Tokens
	pct := td.ContextPct
	if pct > 100 {
		pct = 100
	}

	const barWidth = 20
	filled := int(pct / 100 * barWidth)
	if filled > barWidth {
		filled = barWidth
	}
	bar := "[" + strings.Repeat("|", filled) + strings.Repeat(" ", barWidth-filled) + "]"

	label := fmt.Sprintf("%.0f%% of context · ~%s tokens", pct, formatTokenCount(td.Total))
	return "  " + accentStyle.Render(bar) + "  " + dimStyle.Render(label)
}

// renderModeHint shows which mode is active and how to toggle.
func (p *PromptPreviewComponent) renderModeHint() string {
	t := theme.Current()
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted))
	keyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(t.Accent))

	mode := "expanded"
	if p.rawMode {
		mode = "raw source"
	}
	return "  " + dimStyle.Render(mode) + "  " + keyStyle.Render("r") + dimStyle.Render(" toggle")
}

// buildExpanded returns the glamour-rendered expanded prompt with section
// provenance separators inserted between include boundaries.
func (p *PromptPreviewComponent) buildExpanded() string {
	if p.content == nil {
		return ""
	}

	t := theme.Current()
	sepStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(t.Muted))

	wrapWidth := p.width - 4
	if wrapWidth < 40 {
		wrapWidth = 40
	}

	var sb strings.Builder
	var prevSource string

	for _, sec := range p.content.Sections {
		// Emit a provenance separator when the source file changes.
		if sec.Source != prevSource && prevSource != "" {
			rel := relPath(sec.Source)
			sep := fmt.Sprintf("── from %s ──", rel)
			sb.WriteString(sepStyle.Render(sep))
			sb.WriteString("\n\n")
		}
		prevSource = sec.Source

		rendered := glamourRender(sec.Content, wrapWidth)
		sb.WriteString(rendered)
	}

	return sb.String()
}

// buildRaw returns the raw source with @-include lines highlighted.
func (p *PromptPreviewComponent) buildRaw() string {
	if p.content == nil {
		return ""
	}

	t := theme.Current()
	atStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(t.Accent)).Bold(true)
	textStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(t.Text))

	var sb strings.Builder
	for _, line := range strings.Split(p.content.RawContent, "\n") {
		if strings.HasPrefix(line, "@") {
			sb.WriteString(atStyle.Render(line))
		} else {
			sb.WriteString(textStyle.Render(line))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// glamourRender renders markdown content with glamour using the dark theme.
// Falls back to the raw text on error.
func glamourRender(content string, wordWrap int) string {
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(wordWrap),
	)
	if err != nil {
		return content
	}
	out, err := r.Render(content)
	if err != nil {
		return content
	}
	return out
}

// relPath returns a short relative-looking path for display in separators.
// If the path cannot be made relative, it returns the base name.
func relPath(absPath string) string {
	// Try to trim the home directory prefix for readability.
	if home := homeDir(); home != "" {
		if rel, err := filepath.Rel(home, absPath); err == nil {
			return "~/" + rel
		}
	}
	return filepath.Base(absPath)
}

// homeDir returns the user's home directory using the HOME environment variable.
func homeDir() string {
	return os.Getenv("HOME")
}

// formatTokenCount formats a token count with thousands separators.
func formatTokenCount(n int) string {
	s := fmt.Sprintf("%d", n)
	if n < 1000 {
		return s
	}
	// Insert commas.
	result := make([]byte, 0, len(s)+len(s)/3)
	rem := len(s) % 3
	if rem == 0 {
		rem = 3
	}
	result = append(result, s[:rem]...)
	for i := rem; i < len(s); i += 3 {
		result = append(result, ',')
		result = append(result, s[i:i+3]...)
	}
	return string(result)
}
