package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"golang.org/x/term"

	"github.com/blsmth/agentctl/internal/audit"
	"github.com/blsmth/agentctl/internal/auditnarrate"
	"github.com/blsmth/agentctl/internal/claudehome"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/logstream"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/race"
	"github.com/blsmth/agentctl/internal/sessionaudit"
	"github.com/blsmth/agentctl/internal/sessions"
	"github.com/blsmth/agentctl/internal/theme"
	"github.com/blsmth/agentctl/internal/transcript"
)

// AuditReportOptions are the flags of `agentctl audit report`.
type AuditReportOptions struct {
	Target   string // @N, session ID, or transcript path; "" = this window
	All      bool
	Since    string // with All: how far back
	Agent    string // with All: exact agent name
	Project  string // with All: project basename
	Limit    int    // with All: max sessions
	Severity string // hide findings below this
	JSON     bool
	Narrate  bool // add an LLM verdict (nous backend); failures are a one-line note
}

// Seams for tests.
var (
	reportFindSessions = sessions.FindRecentSessions
	reportAuditDirs    = defaultReportAuditDirs
	reportCost         = defaultReportCost
	reportHome         = os.UserHomeDir
	reportColor        = func(w io.Writer) bool {
		f, ok := w.(*os.File)
		return ok && term.IsTerminal(int(f.Fd()))
	}
	reportLoc                   = func() *time.Location { return time.Local }
	reportAllowedDirs           = claudeManagedDirs
	reportNarrator              = auditnarrate.New
	reportNoteErr     io.Writer = os.Stderr
)

// claudeManagedDirs are the dirs in every Claude account that Claude Code
// itself has agents write to: agent memory and plans.
func claudeManagedDirs() []string {
	var out []string
	for _, a := range claudehome.All() {
		out = append(out, filepath.Join(a.Dir, "agent-memory"), filepath.Join(a.Dir, "plans"))
	}
	return out
}

// errNoEntries marks a transcript with no parseable line.
var errNoEntries = errors.New("no parseable transcript entries")

// RunAuditReport prints the audit report of one session, or with All a
// leaderboard over many.
func RunAuditReport(w io.Writer, opts AuditReportOptions) error {
	sev, ok := sessionaudit.ParseSeverity(opts.Severity)
	if !ok {
		return fmt.Errorf("--severity must be high, med or low, not %q", opts.Severity)
	}
	if opts.All {
		if opts.Target != "" {
			return errors.New("--all takes no target")
		}
		return runAuditReportAll(w, opts, sev)
	}
	path, err := resolveRaceLog(opts.Target)
	if err != nil {
		return err
	}
	rep, err := auditTranscript(path, sessions.Session{})
	if err != nil && !errors.Is(err, errNoEntries) {
		return err
	}
	// Narrate before filtering: the verdict covers every finding.
	var note string
	if opts.Narrate {
		note = narrateOne(rep)
	}
	filterFindings(rep, sev)
	if opts.JSON {
		writeNote(reportNoteErr, note)
		return writeReportJSON(w, rep)
	}
	renderAuditReport(w, rep, sev, newReportPalette(reportColor(w)))
	renderNarrative(w, rep.Narrative, note)
	return nil
}

// maxNarrated caps how many sessions one --all run narrates.
const maxNarrated = 20

// narrateOne sets r.Narrative when r has findings and returns a one-line note
// when it could not (or "" when it did or had nothing to say).
func narrateOne(r *sessionaudit.Report) string {
	if !auditnarrate.HasFindings(r) {
		return ""
	}
	text, err := reportNarrator().Session(context.Background(), r)
	if err != nil {
		return narrateNote(err)
	}
	r.Narrative = text
	return ""
}

func narrateNote(err error) string {
	return "Narrative unavailable: " + strings.ReplaceAll(err.Error(), "\n", " ")
}

func writeNote(w io.Writer, note string) {
	if note != "" {
		fmt.Fprintln(w, note)
	}
}

func renderNarrative(w io.Writer, text, note string) {
	if text != "" {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Verdict")
		fmt.Fprintln(w, "  "+text)
	}
	if note != "" {
		fmt.Fprintln(w)
		fmt.Fprintln(w, note)
	}
}

// narrateAll narrates each report with findings (at most maxNarrated, the
// rest are logged as skipped) and then the aggregate. It stops at the first
// failure: the remaining calls would fail the same way (no credits, no key).
// The returned note is "" when everything went through.
func narrateAll(reports []*sessionaudit.Report, agg *sessionaudit.Aggregate) string {
	nar := reportNarrator()
	ctx := context.Background()
	done, skipped := 0, 0
	for _, r := range reports {
		if !auditnarrate.HasFindings(r) {
			continue
		}
		if done >= maxNarrated {
			skipped++
			continue
		}
		text, err := nar.Session(ctx, r)
		if err != nil {
			return narrateNote(err)
		}
		r.Narrative = text
		done++
	}
	text, err := nar.Aggregate(ctx, reports, *agg)
	if err != nil {
		return narrateNote(err)
	}
	agg.Narrative = text
	if skipped > 0 {
		logging.Log("audit report --narrate: narrated %d sessions, skipped %d over the cap of %d", done, skipped, maxNarrated)
		return fmt.Sprintf("Narrated %d sessions; skipped %d over the per-run cap of %d.", done, skipped, maxNarrated)
	}
	return ""
}

// auditTranscript reads path (a session still being written is
// snapshotted as it stands) and audits it. s carries what the session scan
// already knows (project, harness); it may be zero.
func auditTranscript(path string, s sessions.Session) (*sessionaudit.Report, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening transcript: %w", err)
	}
	entries, err := logstream.ReadAll(f)
	f.Close() //nolint:errcheck
	if err != nil {
		return nil, fmt.Errorf("reading transcript %s: %w", path, err)
	}
	parsed := 0
	for _, e := range entries {
		if e.Raw != nil {
			parsed++
		}
	}

	p := sessionaudit.DefaultParams()
	if home, err := reportHome(); err == nil {
		p.Home = home
	}
	p.AllowedDirs = reportAllowedDirs()
	id, cwd := transcriptIdentity(entries, path)
	if s.ID == "" {
		s.ID = id
	}
	if s.Harness == "" && isPiTranscript(entries) {
		s.Harness = transcript.HarnessPi
	}
	p.Cost = func(u []sessionaudit.TurnUsage) (float64, bool) { return reportCost(s, u) }

	var recs []audit.Record
	if s.ID != "" {
		recs, _ = audit.QueryDirs(reportAuditDirs(cwd), audit.Filter{Session: s.ID})
	}
	rep := sessionaudit.Analyze(entries, recs, p)
	if rep.Session.ID == "" {
		rep.Session.ID = s.ID
	}
	if rep.Session.Project == "" && s.Project != "" {
		rep.Session.Project = filepath.Base(s.Project)
	}
	if rep.Session.Agent == "" {
		rep.Session.Agent = s.Agent
	}
	rep.Session.Title = raceTitle(path)
	rep.Session.Source = path
	rep.Session.Account = accountOf(path, rep.Session.Harness)
	if parsed == 0 && len(entries) > 0 {
		return rep, errNoEntries
	}
	return rep, nil
}

// transcriptIdentity is the session ID (from the entries, else the file
// name) and the first recorded cwd.
func transcriptIdentity(entries []logstream.Entry, path string) (id, cwd string) {
	for _, e := range entries {
		if e.Raw == nil {
			continue
		}
		if id == "" {
			id, _ = e.Raw["sessionId"].(string)
		}
		if cwd == "" {
			cwd, _ = e.Raw["cwd"].(string)
		}
		if id != "" && cwd != "" {
			break
		}
	}
	if id == "" {
		id = transcript.SessionIDFromLog(filepath.Base(path))
	}
	return id, cwd
}

func isPiTranscript(entries []logstream.Entry) bool {
	for _, e := range entries {
		if strings.HasPrefix(e.Type, "pi:") {
			return true
		}
	}
	return false
}

// defaultReportAuditDirs lists the audit dirs to search for a session's
// records: every registered project's (current project first), plus the one
// belonging to the transcript's own cwd (the repo above a worktree).
func defaultReportAuditDirs(cwd string) []string {
	dirs, err := audit.AllProjectAuditDirs()
	if err != nil {
		dirs = nil
		if d := paths.AuditDir(); d != "" {
			dirs = append(dirs, d)
		}
	}
	if cwd != "" {
		root := strings.TrimRight(cwd, "/")
		if i := strings.Index(root, "/.worktrees/"); i > 0 {
			root = root[:i]
		}
		own := filepath.Join(root, ".agentctl", "audit")
		seen := false
		for _, d := range dirs {
			seen = seen || d == own
		}
		if !seen {
			dirs = append(dirs, own)
		}
	}
	return dirs
}

// defaultReportCost prices the requests through sessions.CostOfSummary, so
// an unknown model is an unknown cost.
func defaultReportCost(s sessions.Session, u []sessionaudit.TurnUsage) (float64, bool) {
	sum := &sessions.Summary{Session: s}
	for i, t := range u {
		if t.Model != "" && t.Model != "<synthetic>" {
			sum.ModelName = t.Model
		}
		sum.TurnMetrics = append(sum.TurnMetrics, sessions.TurnMetric{
			Turn: i + 1, InputTokens: t.Input, OutputTokens: t.Output,
			CacheRead: t.CacheRead, CacheCreate: t.CacheCreate, Timestamp: t.Timestamp, Model: t.Model,
		})
	}
	c := sessions.CostOfSummary(sum, sessions.BillingFor(s, sum.ModelName, config.Load().Backends))
	return c.USD, c.Known
}

// accountOf names the Claude account whose projects dir holds path
// ("default" for the unnamed default dir), or "pi"; "" when unknown.
func accountOf(path, harness string) string {
	if harness == transcript.HarnessPi {
		return "pi"
	}
	projDir := filepath.Dir(filepath.Dir(path))
	if filepath.Base(projDir) != "projects" {
		return ""
	}
	dir := filepath.Dir(projDir)
	for _, a := range claudehome.All() {
		if a.Dir == dir {
			if a.Name == "" {
				return "default"
			}
			return a.Name
		}
	}
	if name := claudehome.AccountForDir(dir); name != "" {
		return name
	}
	return ""
}

// filterFindings drops findings below sev. Scores already count them.
func filterFindings(r *sessionaudit.Report, sev sessionaudit.Severity) {
	if r == nil || sev == sessionaudit.Low {
		return
	}
	kept := r.Findings[:0]
	for _, f := range r.Findings {
		if f.Severity.Rank() >= sev.Rank() {
			kept = append(kept, f)
		}
	}
	r.Findings = kept
}

func writeReportJSON(w io.Writer, v any) error {
	b, err := sessionaudit.MarshalJSON(v)
	if err != nil {
		return fmt.Errorf("encoding report: %w", err)
	}
	_, err = fmt.Fprintf(w, "%s\n", b)
	return err
}

// ─── --all ───────────────────────────────────────────────────────────────────

func runAuditReportAll(w io.Writer, opts AuditReportOptions, sev sessionaudit.Severity) error {
	since := 7 * 24 * time.Hour
	if opts.Since != "" {
		d, err := sessions.ParseSince(opts.Since)
		if err != nil {
			return err
		}
		since = d
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 20
	}
	found, err := reportFindSessions(0, since)
	if err != nil {
		return fmt.Errorf("listing sessions: %w", err)
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].StartTime.After(found[j].StartTime) })

	var reports []*sessionaudit.Report
	skipped := 0
	for _, s := range found {
		if len(reports) >= limit {
			break
		}
		if opts.Agent != "" && s.Agent != opts.Agent {
			continue
		}
		if opts.Project != "" && !mayBeProject(s, opts.Project) {
			continue
		}
		// One transcript at a time: its entries are dropped before the next.
		rep, err := auditTranscript(s.LogFile, s)
		if err != nil {
			skipped++
			continue
		}
		if opts.Project != "" && rep.Session.Project != opts.Project {
			continue
		}
		reports = append(reports, rep)
	}
	agg := sessionaudit.Summarize(reports, skipped)
	var note string
	if opts.Narrate && len(reports) > 0 {
		note = narrateAll(reports, &agg)
	}
	for _, r := range reports {
		filterFindings(r, sev)
	}
	if opts.JSON {
		writeNote(reportNoteErr, note)
		if reports == nil {
			reports = []*sessionaudit.Report{}
		}
		return writeReportJSON(w, sessionaudit.Multi{Schema: sessionaudit.SchemaVersion, Reports: reports, Aggregate: agg})
	}
	renderLeaderboard(w, reports, agg, sev, newReportPalette(reportColor(w)))
	for _, r := range reports {
		if r.Narrative != "" {
			fmt.Fprintln(w)
			fmt.Fprintf(w, "%s (%s)\n  %s\n", "Verdict "+shortID(r.Session.ID), r.Session.Agent, r.Narrative)
		}
	}
	renderNarrative(w, agg.Narrative, note)
	return nil
}

// mayBeProject is a cheap pre-filter on the scanned project path (whose
// decoding from the projects dir name is lossy); the exact check is on the
// report's cwd-derived project.
func mayBeProject(s sessions.Session, project string) bool {
	enc := strings.NewReplacer("/", "-", ".", "-", "_", "-").Replace
	return strings.Contains(enc(s.Project), enc(project)) || strings.Contains(s.LogFile, enc(project))
}

// ─── Rendering ───────────────────────────────────────────────────────────────

// reportPalette colors the report on a terminal and is a no-op otherwise.
type reportPalette struct {
	on bool
}

func newReportPalette(on bool) reportPalette { return reportPalette{on: on} }

func (p reportPalette) style(color string, bold bool, s string) string {
	if !p.on || s == "" {
		return s
	}
	st := lipgloss.NewStyle().Foreground(lipgloss.Color(color))
	if bold {
		st = st.Bold(true)
	}
	return st.Render(s)
}

func (p reportPalette) header(s string) string { return p.style(theme.Current().Accent, true, s) }
func (p reportPalette) muted(s string) string  { return p.style(theme.Current().Muted, false, s) }

func (p reportPalette) grade(g, s string) string {
	c := theme.Current()
	switch g {
	case "A", "B":
		return p.style(c.Green, true, s)
	case "C":
		return p.style(c.Yellow, true, s)
	}
	return p.style(c.Red, true, s)
}

func (p reportPalette) severity(sev sessionaudit.Severity, s string) string {
	c := theme.Current()
	switch sev {
	case sessionaudit.High:
		return p.style(c.Red, false, s)
	case sessionaudit.Med:
		return p.style(c.Yellow, false, s)
	}
	return p.style(c.Muted, false, s)
}

// padRight pads s to n runes (no cut).
func padRight(s string, n int) string {
	if k := utf8.RuneCountInString(s); k < n {
		return s + strings.Repeat(" ", n-k)
	}
	return s
}

// cutRunes cuts s to n runes with an ellipsis.
func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

func renderAuditReport(w io.Writer, r *sessionaudit.Report, sev sessionaudit.Severity, pal reportPalette) {
	loc := reportLoc()
	s := r.Session
	head := []string{s.Title}
	for _, v := range []string{s.Agent, s.Project, s.Model} {
		if v != "" {
			head = append(head, v)
		}
	}
	if s.Account != "" {
		head = append(head, "account "+s.Account)
	}
	fmt.Fprintln(w, pal.header(strings.Join(head, " · ")))

	var when []string
	if s.Start != nil {
		start, end := s.Start.In(loc), s.End.In(loc)
		span := start.Format("2006-01-02 15:04") + " → "
		if start.YearDay() == end.YearDay() && start.Year() == end.Year() {
			span += end.Format("15:04")
		} else {
			span += end.Format("2006-01-02 15:04")
		}
		when = append(when, span+" ("+r.Metrics.Wall+")")
	}
	when = append(when, "cost "+formatUSD(r.Metrics.CostUSD))
	fmt.Fprintln(w, pal.muted(strings.Join(when, " · ")))
	if r.Audit.Merged {
		fmt.Fprintln(w, pal.muted(fmt.Sprintf("Audit trail merged: %d records, %d matched to steps.", r.Audit.Records, r.Audit.Matched)))
	} else {
		fmt.Fprintln(w, pal.muted("No audit trail for this session: denials and failures are inferred from tool results."))
	}
	fmt.Fprintln(w)

	for _, sc := range []struct {
		name string
		s    sessionaudit.Score
	}{{"Safety", r.Safety}, {"Efficiency", r.Efficiency}} {
		fmt.Fprintf(w, "%s %s  %s\n", padRight(sc.name, 11),
			pal.grade(sc.s.Grade, fmt.Sprintf("%s %3d", sc.s.Grade, sc.s.Score)),
			pal.muted(countNoun(sc.s.Findings, "finding")))
	}

	if r.Metrics.Steps == 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "No agent steps yet.")
	}
	for _, cat := range []sessionaudit.Category{sessionaudit.Safety, sessionaudit.Efficiency} {
		var fs []sessionaudit.Finding
		for _, f := range r.Findings {
			if f.Category == cat {
				fs = append(fs, f)
			}
		}
		if len(fs) == 0 {
			continue
		}
		fmt.Fprintln(w)
		fmt.Fprintln(w, pal.header(strings.ToUpper(string(cat[:1]))+string(cat[1:])))
		for _, f := range fs {
			fmt.Fprintln(w, findingLine(f, loc, pal))
			if f.RequestedByPrompt != "" {
				fmt.Fprintln(w, "        "+pal.muted("requested: \""+f.RequestedByPrompt+"\""))
			}
		}
	}
	if hidden := r.Safety.Findings + r.Efficiency.Findings - len(r.Findings); hidden > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, pal.muted(fmt.Sprintf("%s below %s hidden.", countNoun(hidden, "finding"), sev)))
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, pal.header("Metrics"))
	for _, l := range metricLines(r.Metrics) {
		fmt.Fprintln(w, "  "+l)
	}
}

// findingLine is `#step  time  rule  evidence  — why`.
func findingLine(f sessionaudit.Finding, loc *time.Location, pal reportPalette) string {
	step := "  -"
	if f.Step > 0 {
		step = fmt.Sprintf("#%d", f.Step)
	}
	at := "        "
	if f.Time != nil {
		at = f.Time.In(loc).Format("15:04:05")
	}
	return fmt.Sprintf("  %s  %s  %s  %s  %s",
		padRight(step, 4), pal.muted(at),
		pal.severity(f.Severity, padRight(f.Rule, 17)+" "+padRight(string(f.Severity), 4)),
		f.Evidence, pal.muted("— "+f.Why+requestedTag(f)))
}

// requestedTag marks a finding the human asked for.
func requestedTag(f sessionaudit.Finding) string {
	if f.RequestedByPrompt == "" {
		return ""
	}
	return " [requested]"
}

func metricLines(m sessionaudit.Metrics) []string {
	by := make([]race.ToolCount, 0, len(m.ToolCallsBy))
	for k, v := range m.ToolCallsBy {
		by = append(by, race.ToolCount{Tool: k, Count: v})
	}
	sort.Slice(by, func(i, j int) bool {
		if by[i].Count != by[j].Count {
			return by[i].Count > by[j].Count
		}
		return by[i].Tool < by[j].Tool
	})
	var parts []string
	for _, t := range by {
		parts = append(parts, fmt.Sprintf("%s %d", t.Tool, t.Count))
	}
	calls := fmt.Sprintf("tool calls %d", m.ToolCalls)
	if len(parts) > 0 {
		calls += " (" + strings.Join(parts, ", ") + ")"
	}
	perEdit := ""
	if m.CostPerEditUSD != nil {
		perEdit = fmt.Sprintf(" · %s per edit", sessions.FormatCost(*m.CostPerEditUSD))
	}
	wall := m.Wall
	if wall == "" {
		wall = "-"
	}
	wasted := ""
	if m.WastedTokensEst > 0 {
		wasted = fmt.Sprintf(" (~%s tokens)", sessions.FormatTokensShort(m.WastedTokensEst))
	}
	return []string{
		fmt.Sprintf("steps %d · %s · turns %d", m.Steps, calls, m.Turns),
		fmt.Sprintf("wall %s · agent active %s", wall, m.AgentActive),
		fmt.Sprintf("tokens in %s · out %s · cache read %s · cache write %s",
			sessions.FormatTokensShort(m.Tokens.Input), sessions.FormatTokensShort(m.Tokens.Output),
			sessions.FormatTokensShort(m.Tokens.CacheRead), sessions.FormatTokensShort(m.Tokens.CacheCreate)),
		fmt.Sprintf("cost %s · %d edits%s", formatUSD(m.CostUSD), m.Edits, perEdit),
		fmt.Sprintf("tool errors %d (%.1f%%) · denied %d · repeat reads %d%s",
			m.ToolErrors, m.ErrorRate*100, m.Denied, m.RepeatReads, wasted),
		fmt.Sprintf("sub-agent entries %d (not graded) · unparsed Bash %d", m.SidechainEntries, m.UnparsedBash),
	}
}

func formatUSD(v *float64) string {
	if v == nil {
		return "unknown"
	}
	return sessions.FormatCost(*v)
}

func countNoun(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// Leaderboard column widths.
const (
	lbSession = 10
	lbAgent   = 16
	lbProject = 14
	lbWhen    = 12
	lbSteps   = 5
	lbGrade   = 6
	lbCost    = 8
	lbTop     = 60
)

func renderLeaderboard(w io.Writer, reports []*sessionaudit.Report, agg sessionaudit.Aggregate, sev sessionaudit.Severity, pal reportPalette) {
	if len(reports) == 0 {
		fmt.Fprintln(w, "No sessions matched.")
		skippedFooter(w, agg.Skipped, pal)
		return
	}
	loc := reportLoc()
	row := func(cells ...string) { fmt.Fprintln(w, strings.TrimRight(strings.Join(cells, "  "), " ")) }
	row(pal.header(padRight("SESSION", lbSession)), pal.header(padRight("AGENT", lbAgent)),
		pal.header(padRight("PROJECT", lbProject)), pal.header(padRight("WHEN", lbWhen)),
		pal.header(fmt.Sprintf("%*s", lbSteps, "STEPS")), pal.header(padRight("SAFETY", lbGrade)),
		pal.header(padRight("EFF", lbGrade)), pal.header(fmt.Sprintf("%*s", lbCost, "COST")), pal.header("TOP FINDING"))
	for _, r := range reports {
		id := r.Session.ID
		if len(id) > 8 {
			id = id[:8]
		}
		when := ""
		if r.Session.Start != nil {
			when = r.Session.Start.In(loc).Format("01-02 15:04")
		}
		top := ""
		if f := r.TopFinding(); f != nil {
			top = f.Title + ": " + f.Evidence
		}
		cost := "?"
		if r.Metrics.CostUSD != nil {
			cost = sessions.FormatCost(*r.Metrics.CostUSD)
		}
		row(padRight(cutRunes(id, lbSession), lbSession),
			padRight(cutRunes(r.Session.Agent, lbAgent), lbAgent),
			padRight(cutRunes(r.Session.Project, lbProject), lbProject),
			padRight(when, lbWhen),
			fmt.Sprintf("%*d", lbSteps, r.Metrics.Steps),
			pal.grade(r.Safety.Grade, padRight(fmt.Sprintf("%s %d", r.Safety.Grade, r.Safety.Score), lbGrade)),
			pal.grade(r.Efficiency.Grade, padRight(fmt.Sprintf("%s %d", r.Efficiency.Grade, r.Efficiency.Score), lbGrade)),
			fmt.Sprintf("%*s", lbCost, cost),
			cutRunes(top, lbTop))
	}

	var shown []sessionaudit.RuleCount
	for _, rc := range agg.Rules {
		if rc.Severity.Rank() >= sev.Rank() {
			shown = append(shown, rc)
		}
	}
	if len(shown) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, pal.header("Most frequent findings"))
		for _, rc := range shown {
			fmt.Fprintf(w, "  %s  %s  %s\n",
				pal.severity(rc.Severity, padRight(rc.Title, 36)),
				padRight(fmt.Sprintf("×%d across %s", rc.Count, countNoun(rc.Sessions, "session")), 24),
				pal.muted(fmt.Sprintf("%s, %s", rc.Category, rc.Severity)))
		}
	}
	skippedFooter(w, agg.Skipped, pal)
}

func skippedFooter(w io.Writer, n int, pal reportPalette) {
	if n == 0 {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, pal.muted(fmt.Sprintf("Skipped %s that could not be read.", countNoun(n, "transcript"))))
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
