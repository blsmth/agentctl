// Package cmd — status.go implements the non-interactive agent status query
// surface: `agentctl status`, `status <window>`, `status peek`, and
// `status whoami`.
//
// The hub (`agentctl hub`) already renders this data interactively. What was
// missing was a way to *ask* — from a script, from another agent, or from a
// terminal that is not the hub — "what is every agent doing right now, and
// which ones need me?" Before this, that meant hand-joining `tmux
// list-windows` against jq over .agentctl/windows/*.json, which is easy to get
// subtly wrong in ways that read as authoritative.
package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/herd"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/winlink"
)

// StatusOptions configures the `agentctl status` listing.
type StatusOptions struct {
	JSON    bool
	Session string // filter to one tmux session (exact, or a project substring)
	State   string // filter to "blocked" | "working" | "idle" | "done"
	Blocked bool   // shorthand for State == "blocked"
	Verify  bool   // capture each pane and flag disagreements with windowstate
}

// statusJSONRow is the machine-readable shape of one agent window.
type statusJSONRow struct {
	Session      string `json:"session"`
	Project      string `json:"project"`
	WindowID     string `json:"window_id"`
	WindowIndex  int    `json:"window_index"`
	Agent        string `json:"agent"`
	State        string `json:"state"`
	Done         bool   `json:"done"`
	Mode         string `json:"mode,omitempty"`
	ContextPct   int    `json:"context_pct,omitempty"`
	Ticket       string `json:"ticket,omitempty"`
	Branch       string `json:"branch,omitempty"`
	WorktreePath string `json:"worktree_path,omitempty"`
	Parent       string `json:"parent,omitempty"`
	Activity     string `json:"activity,omitempty"`
	MailPending  int    `json:"mail_pending,omitempty"`
	Stale        bool   `json:"stale"`
	PaneVerdict  string `json:"pane_verdict,omitempty"`
	PaneDisagree bool   `json:"pane_disagrees,omitempty"`
}

// statusRow pairs a herd row with the windowstate metadata for that window and
// (optionally) a pane verdict.
type statusRow struct {
	row     herd.Row
	meta    herd.Meta
	group   herd.Group
	verdict herd.PaneVerdict
	checked bool
}

// collectStatusRows loads every live session, enriches each row with its
// windowstate metadata, and applies the caller's filters.
func collectStatusRows(opts StatusOptions) ([]statusRow, []herd.Group, error) {
	groups, err := herd.Load()
	if err != nil {
		return nil, nil, fmt.Errorf("loading agent sessions: %w", err)
	}

	wantState := opts.State
	if opts.Blocked {
		wantState = "blocked"
	}

	var rows []statusRow
	for _, g := range groups {
		if opts.Session != "" && !sessionMatches(g, opts.Session) {
			continue
		}
		meta := herd.LoadMeta(g)
		for _, r := range g.Rows {
			if !stateMatches(r, wantState) {
				continue
			}
			sr := statusRow{row: r, meta: meta[r.WindowID], group: g}
			if opts.Verify {
				sr.verdict, sr.checked = verifyPane(g.Session, r.WindowID)
			}
			rows = append(rows, sr)
		}
	}
	return rows, groups, nil
}

// sessionMatches reports whether a group matches a --session filter, accepting
// either the full tmux session name ("agentctl-acme") or the project
// ("acme", "acm").
//
// ⚠️ The substring test runs against the session name with its "agentctl-"
// prefix removed. Every agentctl session is named "agentctl-<project>", so a
// naive strings.Contains(g.Session, want) makes `--session agentctl` match
// every session in existence — a filter that silently returns everything,
// which reads as "no other sessions exist" rather than as a broken filter.
func sessionMatches(g herd.Group, want string) bool {
	if want == "" {
		return true
	}
	if g.Session == want || g.Project == want {
		return true
	}
	bare := strings.TrimPrefix(g.Session, "agentctl-")
	return strings.Contains(bare, want) || strings.Contains(g.Project, want)
}

// stateMatches applies the --state filter. "done" is not a State value but a
// separate boolean, so it is handled explicitly rather than by string compare.
func stateMatches(r herd.Row, want string) bool {
	switch want {
	case "":
		return true
	case "done":
		return r.Done
	default:
		return r.State == want
	}
}

// verifyPane captures a window's pane and classifies it. Returns checked=false
// when the pane could not be read, so callers can distinguish "no verdict"
// from "verdict is unknown".
func verifyPane(session, windowID string) (herd.PaneVerdict, bool) {
	text, err := tmux.CapturePane(session, windowID, 60)
	if err != nil {
		return herd.PaneUnknown, false
	}
	return herd.ClassifyPane(text), true
}

// RunStatus prints the cross-session agent table (or JSON).
func RunStatus(opts StatusOptions) error {
	rows, groups, err := collectStatusRows(opts)
	if err != nil {
		return err
	}

	if opts.JSON {
		return emitStatusJSON(rows)
	}

	if len(rows) == 0 {
		fmt.Println("No agents match.")
		printStaleWarning(groups)
		return nil
	}

	cols := []string{"SESSION", "WIN", "AGENT", "STATE"}
	if opts.Verify {
		cols = append(cols, "PANE")
	}
	cols = append(cols, "CTX", "MODE", "PARENT", "ACTIVITY")

	grid := make([][]cell, 0, len(rows)+1)
	for _, sr := range rows {
		c := []cell{
			{text: sr.group.Project, style: cliMuted()},
			{text: sr.row.WindowID, style: lipgloss.NewStyle(), window: sr.row.WindowID},
			{text: sr.row.Name, style: cliAccent()},
			stateCell(sr.row),
		}
		if opts.Verify {
			c = append(c, paneCell(sr))
		}
		c = append(c,
			ctxCell(sr.row.ContextPct),
			cell{text: dashIfEmpty(sr.meta.Mode), style: cliMuted()},
			cell{text: dashIfEmpty(sr.meta.Parent()), style: cliMuted()},
			cell{text: truncateDisplay(herd.ActivityLine(sr.row), 44), style: cliMuted()},
		)
		grid = append(grid, c)
	}

	printGrid(os.Stdout, cols, grid)
	printStaleWarning(groups)
	printAttentionSummary(rows)
	return nil
}

// cell is one table cell: the plain text plus the style to render it with.
// Keeping the two apart is what makes alignment correct — see printGrid.
type cell struct {
	text  string
	style lipgloss.Style
	// window, when set, is a tmux window ID ("@65") the rendered cell is
	// hyperlinked to (see internal/winlink). The link escapes are added after
	// styling and never count toward the column width, which is measured on
	// text alone.
	window string
}

// linkWindow wraps already-styled text in a window hyperlink when stdout is a
// terminal. A variable so tests can force links on regardless of stdout.
var linkWindow = winlink.Text

// render returns the cell styled and, if it names a window, hyperlinked.
func (c cell) render() string {
	out := c.style.Render(c.text)
	if c.window != "" {
		out = linkWindow(c.window, out)
	}
	return out
}

// printGrid renders a table, padding on *visible* width rather than byte
// length.
//
// text/tabwriter cannot be used with styled cells: it measures each cell in
// bytes, and a lipgloss-rendered cell carries ANSI escape sequences that
// occupy bytes but no columns. The more heavily styled a column is, the
// further its rows drift from the header — which is why the first version of
// this table looked ragged only in the columns that were colored. So widths
// are computed from the plain strings, and padding is appended after styling
// (styling never changes visible width).
func printGrid(out io.Writer, headers []string, grid [][]cell) {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = lipgloss.Width(h)
	}
	for _, row := range grid {
		for i, c := range row {
			if i < len(widths) {
				if w := lipgloss.Width(c.text); w > widths[i] {
					widths[i] = w
				}
			}
		}
	}

	const gap = 2
	hdr := cliHeaderStyle()
	muted := cliMuted()

	var b strings.Builder
	for i, h := range headers {
		b.WriteString(hdr.Render(h))
		if i < len(headers)-1 {
			b.WriteString(strings.Repeat(" ", widths[i]-lipgloss.Width(h)+gap))
		}
	}
	fmt.Fprintln(out, b.String())

	b.Reset()
	for i := range headers {
		b.WriteString(muted.Render(strings.Repeat("─", widths[i])))
		if i < len(headers)-1 {
			b.WriteString(strings.Repeat(" ", gap))
		}
	}
	fmt.Fprintln(out, b.String())

	for _, row := range grid {
		b.Reset()
		for i, c := range row {
			b.WriteString(c.render())
			if i < len(row)-1 && i < len(widths) {
				b.WriteString(strings.Repeat(" ", widths[i]-lipgloss.Width(c.text)+gap))
			}
		}
		fmt.Fprintln(out, b.String())
	}
}

// emitStatusJSON writes the rows as indented JSON.
func emitStatusJSON(rows []statusRow) error {
	out := make([]statusJSONRow, 0, len(rows))
	for _, sr := range rows {
		j := statusJSONRow{
			Session:      sr.group.Session,
			Project:      sr.group.Project,
			WindowID:     sr.row.WindowID,
			WindowIndex:  sr.row.WindowIndex,
			Agent:        sr.row.Name,
			State:        sr.row.State,
			Done:         sr.row.Done,
			Mode:         sr.meta.Mode,
			ContextPct:   sr.row.ContextPct,
			Ticket:       sr.meta.Ticket,
			Branch:       sr.meta.Branch,
			WorktreePath: sr.row.WorktreePath,
			Parent:       sr.meta.Parent(),
			Activity:     herd.ActivityLine(sr.row),
			MailPending:  sr.row.MailboxPending,
			Stale:        sr.group.Stale,
		}
		if sr.checked {
			j.PaneVerdict = sr.verdict.String()
			j.PaneDisagree = !sr.verdict.AgreesWith(sr.row.State)
		}
		out = append(out, j)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// printStaleWarning reports sessions whose state.json is not being refreshed.
//
// This is deliberately loud and unconditional. A wedged or dead monitor does
// not make the data disappear — it makes it *freeze*, and frozen data reads
// exactly like fresh data in a table. Every state and activity string for that
// session is a claim about the past presented in the present tense.
func printStaleWarning(groups []herd.Group) {
	var stale []herd.Group
	for _, g := range groups {
		if g.Stale {
			stale = append(stale, g)
		}
	}
	if len(stale) == 0 {
		return
	}
	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "⚠️  %d session(s) have a stale or missing snapshot — their rows may be frozen:\n", len(stale))
	for _, g := range stale {
		age := "missing"
		if g.SnapshotAge > 0 {
			age = fmtAge(g.SnapshotAge)
		}
		fmt.Fprintf(os.Stderr, "    %s (%s) — state.json %s\n", g.Project, g.Session, age)
	}
	fmt.Fprintln(os.Stderr, "    That session's monitor may be wedged or dead; check `agentctl health`.")
}

// printAttentionSummary prints a one-line tally of what needs a human.
func printAttentionSummary(rows []statusRow) {
	var blocked, done, disagree int
	for _, sr := range rows {
		if sr.row.State == "blocked" {
			blocked++
		}
		if sr.row.Done {
			done++
		}
		if sr.checked && !sr.verdict.AgreesWith(sr.row.State) {
			disagree++
		}
	}
	if blocked == 0 && done == 0 && disagree == 0 {
		return
	}
	var parts []string
	if blocked > 0 {
		parts = append(parts, fmt.Sprintf("%d blocked", blocked))
	}
	if done > 0 {
		parts = append(parts, fmt.Sprintf("%d done", done))
	}
	if disagree > 0 {
		parts = append(parts, fmt.Sprintf("%d pane/state disagreement(s)", disagree))
	}
	fmt.Fprintf(os.Stderr, "\nNeeds attention: %s\n", strings.Join(parts, ", "))
}

// stateCell renders the recorded state. "done" takes precedence over the
// underlying idle state, matching how the monitor names windows.
func stateCell(r herd.Row) cell {
	switch {
	case r.State == "blocked":
		return cell{text: "blocked", style: cliRed()}
	case r.Done:
		return cell{text: "done", style: cliGreen()}
	case r.State == "working":
		return cell{text: "working", style: cliAccent()}
	case r.State == "":
		return cell{text: "-", style: cliMuted()}
	default:
		return cell{text: r.State, style: cliMuted()}
	}
}

// renderState is the string form of stateCell, for the detail view.
func renderState(r herd.Row) string {
	c := stateCell(r)
	return c.style.Render(c.text)
}

// paneCell shows the pane verdict, marked with "≠" when it contradicts the
// recorded state — the disagreement is the whole point of --verify.
func paneCell(sr statusRow) cell {
	if !sr.checked {
		return cell{text: "?", style: cliMuted()}
	}
	s := sr.verdict.String()
	if !sr.verdict.AgreesWith(sr.row.State) {
		return cell{text: "≠" + s, style: cliRed()}
	}
	return cell{text: s, style: cliMuted()}
}

func ctxCell(pct int) cell {
	if pct <= 0 {
		return cell{text: "-", style: cliMuted()}
	}
	s := fmt.Sprintf("%d%%", pct)
	if pct >= 80 {
		return cell{text: s, style: cliRed()}
	}
	return cell{text: s, style: cliMuted()}
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// truncateDisplay shortens to n runes, not n bytes.
//
// The package already has a byte-based truncate (dispatch.go), which is fine
// for the ASCII command strings it was written for but would split a
// multi-byte rune here: activity lines carry box-drawing characters, ellipses,
// and status glyphs straight from pane output.
func truncateDisplay(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

// RunStatusDetail prints everything known about one window.
func RunStatusDetail(window string, jsonOut bool) error {
	rows, _, err := collectStatusRows(StatusOptions{})
	if err != nil {
		return err
	}
	sr, err := findWindow(rows, window)
	if err != nil {
		return err
	}

	verdict, checked := verifyPane(sr.group.Session, sr.row.WindowID)

	if jsonOut {
		j := statusJSONRow{
			Session: sr.group.Session, Project: sr.group.Project,
			WindowID: sr.row.WindowID, WindowIndex: sr.row.WindowIndex,
			Agent: sr.row.Name, State: sr.row.State, Done: sr.row.Done,
			Mode: sr.meta.Mode, ContextPct: sr.row.ContextPct,
			Ticket: sr.meta.Ticket, Branch: sr.meta.Branch,
			WorktreePath: sr.row.WorktreePath, Parent: sr.meta.Parent(),
			Activity: herd.ActivityLine(sr.row), MailPending: sr.row.MailboxPending,
			Stale: sr.group.Stale,
		}
		if checked {
			j.PaneVerdict = verdict.String()
			j.PaneDisagree = !verdict.AgreesWith(sr.row.State)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(j)
	}

	hdr := cliHeaderStyle()
	muted := cliMuted()
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

	line := func(k, v string) {
		if v == "" {
			v = "-"
		}
		fmt.Fprintf(w, "%s\t%s\n", muted.Render(k), v)
	}

	fmt.Fprintf(w, "%s\t%s\n", hdr.Render("WINDOW"), linkWindow(sr.row.WindowID, cliAccent().Render(sr.row.WindowID))+cliAccent().Render("  "+sr.row.Name))
	line("session", sr.group.Session)
	line("project", sr.group.Project)
	line("state", renderState(sr.row))
	if checked {
		v := verdict.String()
		if !verdict.AgreesWith(sr.row.State) {
			v = cliRed().Render(v + "  ← disagrees with recorded state; trust the pane")
		}
		line("pane says", v)
	}
	line("mode", sr.meta.Mode)
	line("ticket", strings.TrimSpace(sr.meta.Ticket+" "+sr.meta.TicketTitle))
	line("branch", sr.meta.Branch)
	line("worktree", firstNonEmpty(sr.row.WorktreePath, sr.meta.WorktreeDir))
	line("work dir", sr.row.WorkDir)
	line("model", cliAbbreviateModel(sr.row.ModelName))
	if sr.row.ContextPct > 0 {
		line("context", fmt.Sprintf("%d%% (%d / %d tokens)", sr.row.ContextPct, sr.row.ContextTokens, sr.row.ContextWindow))
	}
	line("turns", fmt.Sprintf("%d", sr.row.TurnCount))
	if sr.row.MailboxPending > 0 {
		line("mail pending", fmt.Sprintf("%d", sr.row.MailboxPending))
	}

	// Parent attribution, with the trust rule made explicit rather than
	// silently dropping the field.
	if p := sr.meta.Parent(); p != "" {
		parentName := lookupWindowName(rows, p)
		line("dispatched by", strings.TrimSpace(p+"  "+parentName))
	} else if sr.meta.SpawnedBy != "" {
		line("dispatched by", muted.Render(fmt.Sprintf(
			"- (recorded %s, but not trustworthy: bare-mode launch, see herd.Meta.ParentTrusted)", sr.meta.SpawnedBy)))
	}

	line("session id", sr.meta.SessionID)
	line("log", sr.meta.LogPath)
	if !sr.meta.UpdatedAt.IsZero() {
		line("state written", fmtAge(time.Since(sr.meta.UpdatedAt)))
	}
	line("activity", herd.ActivityLine(sr.row))

	if err := w.Flush(); err != nil {
		return err
	}
	if sr.group.Stale {
		printStaleWarning([]herd.Group{sr.group})
	}
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func lookupWindowName(rows []statusRow, windowID string) string {
	for _, sr := range rows {
		if sr.row.WindowID == windowID {
			return sr.row.Name
		}
	}
	return ""
}

// findWindow resolves a user-supplied window reference against the loaded
// rows. Accepts "@275", "275", or an agent/window name. Ambiguous names are an
// error rather than a silent first-match, because acting on the wrong window
// is exactly the failure this command exists to prevent.
func findWindow(rows []statusRow, want string) (statusRow, error) {
	if want == "" {
		return statusRow{}, fmt.Errorf("no window specified")
	}
	id := want
	if !strings.HasPrefix(id, "@") {
		id = "@" + id
	}
	for _, sr := range rows {
		if sr.row.WindowID == id {
			return sr, nil
		}
	}

	var byName []statusRow
	for _, sr := range rows {
		if sr.row.Name == want {
			byName = append(byName, sr)
		}
	}
	switch len(byName) {
	case 1:
		return byName[0], nil
	case 0:
		return statusRow{}, fmt.Errorf("no agent window matches %q (try `agentctl status` to list them)", want)
	default:
		var ids []string
		for _, sr := range byName {
			ids = append(ids, sr.row.WindowID+" in "+sr.group.Project)
		}
		return statusRow{}, fmt.Errorf("%q is ambiguous — matches %s; use a window ID", want, strings.Join(ids, ", "))
	}
}

// RunPeek prints a window's pane contents plus the derived verdict.
func RunPeek(window string, lines int, jsonOut bool) error {
	rows, _, err := collectStatusRows(StatusOptions{})
	if err != nil {
		return err
	}
	sr, err := findWindow(rows, window)
	if err != nil {
		return err
	}
	if lines <= 0 {
		lines = 40
	}

	text, err := tmux.CapturePane(sr.group.Session, sr.row.WindowID, lines)
	if err != nil {
		return fmt.Errorf("capturing pane for %s: %w", sr.row.WindowID, err)
	}
	verdict := herd.ClassifyPane(text)
	prompts := herd.PromptCount(text)

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			WindowID     string `json:"window_id"`
			Agent        string `json:"agent"`
			Session      string `json:"session"`
			Recorded     string `json:"recorded_state"`
			PaneVerdict  string `json:"pane_verdict"`
			Disagrees    bool   `json:"pane_disagrees"`
			PromptCount  int    `json:"prompt_count"`
			StagedInput  bool   `json:"staged_input"`
			PaneContents string `json:"pane_contents"`
		}{
			WindowID: sr.row.WindowID, Agent: sr.row.Name, Session: sr.group.Session,
			Recorded: sr.row.State, PaneVerdict: verdict.String(),
			Disagrees:   !verdict.AgreesWith(sr.row.State),
			PromptCount: prompts, StagedInput: prompts > 1,
			PaneContents: text,
		})
	}

	muted := cliMuted()
	fmt.Printf("%s  %s  (%s)\n", linkWindow(sr.row.WindowID, cliAccent().Render(sr.row.WindowID)), sr.row.Name, sr.group.Project)
	fmt.Printf("%s recorded=%s  pane=%s\n", muted.Render("state:"), dashIfEmpty(sr.row.State), verdict.String())
	if !verdict.AgreesWith(sr.row.State) {
		fmt.Println(cliRed().Render(
			"⚠️  pane and recorded state disagree — the pane is authoritative"))
	}
	if prompts > 1 {
		fmt.Println(cliRed().Render(fmt.Sprintf(
			"⚠️  %d prompt lines — input is staged but unsubmitted (a wake/send-keys delivery did not go through)", prompts)))
	}
	fmt.Println(muted.Render(strings.Repeat("─", 72)))
	fmt.Println(strings.TrimRight(text, "\n"))
	return nil
}

// whoamiSource is one way of answering "which window am I?", with the answer
// it gave and how much that answer can be trusted.
type whoamiSource struct {
	Name     string `json:"source"`
	Value    string `json:"value"`
	WindowID string `json:"window_id"`
	Trusted  bool   `json:"trusted"`
	Note     string `json:"note"`
}

// RunWhoami reports which tmux window the calling process belongs to,
// showing every resolution source side by side.
//
// This prints all sources rather than a single answer because they can
// disagree, and the disagreement is the useful output. A forked or background
// Claude session inherits AGENTCTL_SESSION_ID from the shell that launched it,
// so the "reliable" session-UUID lookup confidently returns the *parent's*
// window. AGENTCTL_WINDOW_ID is worse: it is session-global in tmux and
// last-writer-wins, so it usually names whichever window was created most
// recently. Only TMUX_PANE is set by tmux itself for the calling pane.
func RunWhoami(jsonOut bool) error {
	var sources []whoamiSource

	if pane := os.Getenv("TMUX_PANE"); pane != "" {
		sources = append(sources, whoamiSource{
			Name: "TMUX_PANE", Value: pane,
			WindowID: tmux.WindowIDForPane(pane), Trusted: true,
			Note: "set by tmux for this pane; authoritative when present",
		})
	}
	if sid := os.Getenv("AGENTCTL_SESSION_ID"); sid != "" {
		sources = append(sources, whoamiSource{
			Name: "AGENTCTL_SESSION_ID", Value: sid,
			WindowID: lookupWindowBySession(sid), Trusted: false,
			Note: "inherited by forked/background sessions — may name the PARENT window",
		})
	}
	if wid := os.Getenv("AGENTCTL_WINDOW_ID"); wid != "" {
		sources = append(sources, whoamiSource{
			Name: "AGENTCTL_WINDOW_ID", Value: wid, WindowID: wid, Trusted: false,
			Note: "tmux session-global, last-writer-wins — usually the most recently created window",
		})
	}

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			Resolved  string         `json:"resolved_window_id"`
			Agreement bool           `json:"sources_agree"`
			Sources   []whoamiSource `json:"sources"`
		}{resolveWhoami(sources), whoamiAgree(sources), sources})
	}

	if len(sources) == 0 {
		fmt.Println("Not running inside a tmux pane or an agentctl-managed window.")
		return nil
	}

	resolved := resolveWhoami(sources)
	trusted := hasTrustedAnswer(sources)

	// Only state an identity as fact when a trusted source produced it.
	// Printing "you are: @274" from the session-global env var would be this
	// command reproducing the exact error it exists to catch.
	switch {
	case resolved == "":
		fmt.Printf("%s could not determine\n\n", cliHeaderStyle().Render("you are:"))
	case trusted:
		fmt.Printf("%s %s\n\n", cliHeaderStyle().Render("you are:"), cliAccent().Render(resolved))
	default:
		fmt.Printf("%s %s %s\n\n",
			cliHeaderStyle().Render("you are:"),
			cliRed().Render(resolved+" (UNVERIFIED)"),
			cliMuted().Render("— no trusted source; do not act on this without confirming"))
	}

	grid := make([][]cell, 0, len(sources))
	for _, s := range sources {
		mark, style := "?", cliMuted()
		if s.Trusted {
			mark, style = "✓", cliGreen()
		}
		grid = append(grid, []cell{
			{text: mark + " " + s.Name, style: style},
			{text: dashIfEmpty(s.WindowID), style: cliMuted(), window: s.WindowID},
			{text: s.Note, style: cliMuted()},
		})
	}
	printGrid(os.Stdout, []string{"SOURCE", "SAYS", "NOTE"}, grid)

	if !whoamiAgree(sources) {
		fmt.Fprintln(os.Stderr, cliRed().Render(
			"\n⚠️  sources disagree — at most one of them is right."))
	}
	if !trusted && resolved != "" {
		// Render each line separately: lipgloss pads a multi-line string to
		// the width of its widest line, which turns an embedded "\n" into a
		// block of trailing whitespace.
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, cliRed().Render("⚠️  No trusted source. TMUX_PANE is unset, which happens in a"))
		fmt.Fprintln(os.Stderr, cliRed().Render("    background or forked session. Confirm by pane contents first:"))
		fmt.Fprintln(os.Stderr, cliMuted().Render(fmt.Sprintf("      agentctl status peek %s", resolved)))
		fmt.Fprintln(os.Stderr, cliMuted().Render("    …and look for your own last message in the output."))
	}
	return nil
}

// hasTrustedAnswer reports whether any source that resolved a window ID is one
// tmux itself set for this pane.
func hasTrustedAnswer(sources []whoamiSource) bool {
	for _, s := range sources {
		if s.Trusted && s.WindowID != "" {
			return true
		}
	}
	return false
}

// resolveWhoami picks the best available answer: the first trusted source,
// else the first source that produced any window ID at all.
func resolveWhoami(sources []whoamiSource) string {
	for _, s := range sources {
		if s.Trusted && s.WindowID != "" {
			return s.WindowID
		}
	}
	for _, s := range sources {
		if s.WindowID != "" {
			return s.WindowID
		}
	}
	return ""
}

// whoamiAgree reports whether every source that produced a window ID produced
// the same one.
func whoamiAgree(sources []whoamiSource) bool {
	seen := ""
	for _, s := range sources {
		if s.WindowID == "" {
			continue
		}
		if seen == "" {
			seen = s.WindowID
			continue
		}
		if seen != s.WindowID {
			return false
		}
	}
	return true
}

// lookupWindowBySession finds the window whose windowstate records sessionID,
// across every live session rather than just the caller's project.
func lookupWindowBySession(sessionID string) string {
	groups, err := herd.Load()
	if err != nil {
		return ""
	}
	for _, g := range groups {
		for id, m := range herd.LoadMeta(g) {
			if m.SessionID == sessionID {
				return id
			}
		}
	}
	return ""
}
