package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/sessions"
	"github.com/blsmth/agentctl/internal/theme"
)

// sessionsSearchJSONRow is the flattened structure emitted for
// `sessions search --json`.
type sessionsSearchJSONRow struct {
	SessionID string `json:"session_id"`
	Agent     string `json:"agent"`
	Project   string `json:"project"`
	Timestamp string `json:"timestamp"`
	Role      string `json:"role"`
	Text      string `json:"text"`
	LogFile   string `json:"log_file"`
}

// PrintSessionsSearch runs a content search across session transcripts and
// prints the results as an attributed, highlighted block list or as JSON.
func PrintSessionsSearch(opts sessions.SearchOptions, jsonOut bool) error {
	hits, err := sessions.Search(opts)
	if err != nil {
		return err
	}

	if jsonOut {
		rows := make([]sessionsSearchJSONRow, 0, len(hits))
		for _, h := range hits {
			rows = append(rows, sessionsSearchJSONRow{
				SessionID: h.SessionID,
				Agent:     h.Agent,
				Project:   sessions.NormalizeProjectName(h.Project),
				Timestamp: h.Timestamp.Format(time.RFC3339),
				Role:      h.Role,
				Text:      h.Text,
				LogFile:   h.LogFile,
			})
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}

	muted := cliMuted()
	accent := cliAccent()
	hdr := cliHeaderStyle()
	matchStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.Current().Red))

	if len(hits) == 0 {
		fmt.Println(muted.Render("No matches found."))
		return nil
	}

	sessionSet := make(map[string]bool, len(hits))
	for _, h := range hits {
		sessionSet[h.SessionID] = true

		idShort := h.SessionID
		if len(idShort) > 8 {
			idShort = idShort[:8]
		}

		fmt.Printf("%s  %s  %s  %s  %s\n",
			muted.Render(h.Timestamp.Format("2006-01-02")),
			accent.MaxWidth(12).Width(12).Render(h.Agent),
			muted.MaxWidth(16).Width(16).Render(sessions.NormalizeProjectName(h.Project)),
			muted.Render(idShort),
			hdr.Render(h.Role),
		)

		for _, c := range h.ContextBefore {
			fmt.Printf("    %s\n", muted.Render(c))
		}
		fmt.Printf("  %s\n", highlightMatch(h.Text, h.MatchStart, h.MatchEnd, matchStyle))
		for _, c := range h.ContextAfter {
			fmt.Printf("    %s\n", muted.Render(c))
		}
		fmt.Println()
	}

	fmt.Printf("%d hit%s across %d session%s. Use \"agentctl sessions <id>\" to read further.\n",
		len(hits), plural(len(hits)), len(sessionSet), plural(len(sessionSet)))
	return nil
}

// highlightMatch renders text with the [start, end) byte range styled as the
// match highlight. start/end are expected to be valid UTF-8 rune boundaries,
// which they are by construction (sessions.Hit computes them from a regexp
// or substring match position, both of which land on rune boundaries).
func highlightMatch(text string, start, end int, style lipgloss.Style) string {
	if start < 0 || end > len(text) || start >= end {
		return text
	}
	return text[:start] + style.Render(text[start:end]) + text[end:]
}

// plural returns "s" when n != 1, for simple English pluralization in
// summary lines.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
