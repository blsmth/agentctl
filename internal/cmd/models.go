package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/tokens"
)

// RunModels prints the resolved model registry. When resolve is non-empty,
// it instead runs a single model name through tokens.Lookup and prints the
// resolved Model plus which resolution step matched — the tool for answering
// "why does it think opus-5 is 200k".
func RunModels(resolve string) error {
	// Load config so any configured overrides are installed into the
	// tokens registry before we print anything.
	config.Load()

	if resolve != "" {
		printModelResolve(resolve)
		return nil
	}
	printModelsTable()
	return nil
}

// printModelsTable prints the resolved registry (built-ins + config
// overrides) as a table.
//
// This does NOT use text/tabwriter: tabwriter computes column widths from
// raw byte length, which counts ANSI escape sequences as visible width. The
// header cells here are styled (hdr.Render), and only two of the seven data
// columns (MODEL, SOURCE) are styled to match — the other five (FAMILY,
// WINDOW, RESERVE, IN, OUT) are plain, so tabwriter padded every column out
// to the styled header's inflated byte length. printANSITable below
// computes widths with lipgloss.Width, which is ANSI-aware, and pads
// manually instead.
func printModelsTable() {
	hdr := cliHeaderStyle()
	muted := cliMuted()
	accent := cliAccent()

	header := []string{
		hdr.Render("MODEL"),
		hdr.Render("LABEL"),
		hdr.Render("FAMILY"),
		hdr.Render("WINDOW"),
		hdr.Render("RESERVE"),
		hdr.Render("IN"),
		hdr.Render("OUT"),
		hdr.Render("SOURCE"),
	}

	registry := tokens.Registry()
	rows := make([][]string, 0, len(registry))
	for _, m := range registry {
		rows = append(rows, []string{
			accent.Render(m.Key),
			m.Label,
			string(m.Family),
			formatModelTokens(m.ContextWindow),
			formatModelTokens(m.MaxOutput),
			fmt.Sprintf("%.2f", m.Pricing.InputPerMTok),
			fmt.Sprintf("%.2f", m.Pricing.OutputPerMTok),
			muted.Render(m.Source),
		})
	}

	printANSITable(os.Stdout, header, rows)
}

// printANSITable renders a left-aligned table with a header row and data
// rows, computing column widths via lipgloss.Width (which ignores ANSI
// escape sequences) so styled and unstyled cells align correctly. Columns
// are separated by a 2-space gap, matching the tabwriter convention used
// elsewhere in this package.
func printANSITable(w io.Writer, header []string, rows [][]string) {
	n := len(header)
	widths := make([]int, n)
	for i, h := range header {
		widths[i] = lipgloss.Width(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i >= n {
				continue
			}
			if cw := lipgloss.Width(cell); cw > widths[i] {
				widths[i] = cw
			}
		}
	}

	writeRow := func(cells []string) {
		var sb strings.Builder
		for i, cell := range cells {
			sb.WriteString(cell)
			if i < n-1 {
				pad := widths[i] - lipgloss.Width(cell) + 2
				sb.WriteString(strings.Repeat(" ", pad))
			}
		}
		fmt.Fprintln(w, sb.String())
	}

	writeRow(header)
	for _, row := range rows {
		writeRow(row)
	}
}

// printModelResolve prints the fully resolved Model for a single model name,
// along with which resolution step matched.
func printModelResolve(model string) {
	m := tokens.Lookup(model)

	fmt.Printf("model:          %s\n", model)
	fmt.Printf("resolved key:   %s\n", m.Key)
	fmt.Printf("matched via:    %s\n", describeModelSource(m))
	fmt.Printf("family:         %s\n", nonEmptyOr(string(m.Family), "-"))
	fmt.Printf("label:          %s\n", nonEmptyOr(m.Label, "-"))
	fmt.Printf("short:          %s\n", nonEmptyOr(m.Short, "-"))
	fmt.Printf("known:          %v\n", m.Known)
	fmt.Printf("context window: %s (%d tokens)\n", formatModelTokens(m.ContextWindow), m.ContextWindow)
	fmt.Printf("max output:     %s (%d tokens)\n", formatModelTokens(m.MaxOutput), m.MaxOutput)
	fmt.Printf("pricing:        in=$%.2f out=$%.2f cache_read=$%.2f cache_create=$%.2f (per MTok)\n",
		m.Pricing.InputPerMTok, m.Pricing.OutputPerMTok, m.Pricing.CacheReadPerMTok, m.Pricing.CacheCreatePerMTok)
}

// describeModelSource returns a human-readable description of which
// resolution step produced m.
func describeModelSource(m tokens.Model) string {
	switch m.Source {
	case "config":
		return "config override"
	case "built-in":
		return "built-in table"
	case "family":
		return fmt.Sprintf("family fallback (%s)", m.Family)
	default:
		return "unknown (default window applied)"
	}
}

// formatModelTokens formats a token count with k/M suffixes.
func formatModelTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%dk", n/1000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// nonEmptyOr returns s, or fallback when s is empty.
func nonEmptyOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
