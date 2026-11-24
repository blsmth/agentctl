package improve

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/audit"
	"github.com/blsmth/agentctl/internal/closedlog"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/sessions"
)

// MaxEvidenceBytes caps the rendered evidence pack.
const MaxEvidenceBytes = 30 * 1024

// maxTopFailing is how many failing commands are listed per session.
const maxTopFailing = 3

// Verdict is one review verdict on a PR (the outcome ledger of PR B).
type Verdict struct {
	PR              int
	Repo            string
	Verdict         string
	Rounds          int
	AuthorSessionID string
}

// VerdictSource returns the review verdicts since a time. It returns nothing
// when no ledger exists.
type VerdictSource interface {
	Verdicts(since time.Time) []Verdict
}

// FindingSource returns session-audit findings (one line each) for a session.
// There is no implementation yet (sessionaudit has not merged); a nil source
// contributes nothing.
type FindingSource interface {
	Findings(sessionID string) []string
}

// Seams; tests replace them. Resolved per call, never at package init.
var (
	verdictSource VerdictSource = ledgerVerdicts{}
	findingSource FindingSource
	closedLoad    = closedlog.Load
	auditDirs     = audit.AllProjectAuditDirs
	auditQuery    = audit.QueryDirs
	listSessions  = func() ([]sessions.Session, error) {
		by, err := sessions.FindSessionsByAgent()
		if err != nil {
			return nil, err
		}
		var all []sessions.Session
		for _, ss := range by {
			all = append(all, ss...)
		}
		return all, nil
	}
	evidenceNow = time.Now
)

// SessionEvidence is everything known about one session.
type SessionEvidence struct {
	ID        string
	Agent     string
	Project   string
	Start     time.Time
	Duration  time.Duration
	Turns     int
	Model     string
	Backend   string
	Source    string // "closedlog" or "transcript"
	Closed    bool   // closedlog knows the window
	Done      bool
	FinalSt   string
	PR        string
	Denied    int
	Failed    int
	TopFailed []string
	CostUSD   float64
	CostKnown bool
	Verdicts  []Verdict
	Findings  []string
}

// Pack is the evidence for one improve run.
type Pack struct {
	Agent    string // "" in fleet mode
	Fleet    bool
	Since    time.Duration
	Sessions []SessionEvidence
}

// Evidence builds the pack. agent selects one agent (ignored when fleet);
// sessionIDs (full IDs or prefixes), when non-empty, restrict it to those
// sessions regardless of since. Improve sessions are never included.
func Evidence(agent string, fleet bool, since time.Duration, sessionIDs []string) (*Pack, error) {
	all, err := listSessions()
	if err != nil {
		return nil, fmt.Errorf("listing sessions: %w", err)
	}
	now := evidenceNow()
	cutoff := now.Add(-since)

	var picked []sessions.Session
	for _, s := range all {
		switch {
		case len(sessionIDs) > 0:
			if !matchesAny(s.ID, sessionIDs) {
				continue
			}
		case !fleet && s.Agent != agent:
			continue
		case s.StartTime.Before(cutoff) && s.EndTime.Before(cutoff):
			continue
		}
		if sessions.IsImproveSession(s.LogFile) {
			continue
		}
		picked = append(picked, s)
	}

	closedBy := map[string]closedlog.Record{}
	if recs, err := closedLoad(cutoffOrZero(sessionIDs, cutoff)); err == nil {
		for _, r := range recs {
			if r.SessionID != "" {
				closedBy[r.SessionID] = r
			}
		}
	}
	verdicts := map[string][]Verdict{}
	for _, v := range verdictSource.Verdicts(cutoffOrZero(sessionIDs, cutoff)) {
		if v.AuthorSessionID != "" {
			verdicts[v.AuthorSessionID] = append(verdicts[v.AuthorSessionID], v)
		}
	}
	dirs, _ := auditDirs()
	backends := config.Load().Backends

	p := &Pack{Agent: agent, Fleet: fleet, Since: since}
	for _, s := range picked {
		p.Sessions = append(p.Sessions, buildSession(s, closedBy, verdicts, dirs, backends))
	}
	sort.SliceStable(p.Sessions, func(i, j int) bool {
		a, b := p.Sessions[i], p.Sessions[j]
		if sa, sb := a.Denied+a.Failed, b.Denied+b.Failed; sa != sb {
			return sa > sb
		}
		return a.Start.After(b.Start)
	})
	return p, nil
}

func cutoffOrZero(ids []string, cutoff time.Time) time.Time {
	if len(ids) > 0 {
		return time.Time{}
	}
	return cutoff
}

func matchesAny(id string, prefixes []string) bool {
	for _, p := range prefixes {
		if p != "" && strings.HasPrefix(id, p) {
			return true
		}
	}
	return false
}

func buildSession(s sessions.Session, closedBy map[string]closedlog.Record, verdicts map[string][]Verdict, dirs []string, backends map[string]config.BackendConfig) SessionEvidence {
	ev := SessionEvidence{
		ID: s.ID, Agent: s.Agent, Project: sessions.NormalizeProjectName(s.Project),
		Start: s.StartTime, Duration: s.Duration(), Source: "transcript",
		Verdicts: verdicts[s.ID],
	}
	if sum, err := sessions.ParseSummary(s); err == nil {
		ev.Turns = sum.TurnCount
		ev.Model = sum.ModelName
		c := sessions.CostOfSummary(sum, sessions.BillingFor(s, sum.ModelName, backends))
		ev.CostUSD, ev.CostKnown = c.USD, c.Known
	}
	if r, ok := closedBy[s.ID]; ok {
		ev.Source, ev.Closed = "closedlog", true
		ev.Done, ev.FinalSt = r.Done, r.FinalState()
		ev.Backend = r.Backend
		if r.Model != "" {
			ev.Model = r.Model
		}
		if r.TurnCount > 0 {
			ev.Turns = r.TurnCount
		}
		switch {
		case r.PRURL != "":
			ev.PR = r.PRURL
		case r.PRNumber > 0:
			ev.PR = fmt.Sprintf("#%d", r.PRNumber)
		}
	}
	if len(dirs) > 0 {
		recs, _ := auditQuery(dirs, audit.Filter{Session: s.ID})
		ev.Denied, ev.Failed, ev.TopFailed = failureStats(recs)
	}
	if findingSource != nil {
		ev.Findings = findingSource.Findings(s.ID)
	}
	return ev
}

// failureStats counts denied and failed tool calls and returns the most
// frequent failing commands.
func failureStats(recs []audit.Record) (denied, failed int, top []string) {
	counts := map[string]int{}
	for _, r := range recs {
		switch r.Event {
		case audit.EventToolDenied:
			denied++
		case audit.EventToolFailure:
			failed++
		default:
			continue
		}
		key := r.Tool
		if r.Summary != "" {
			key += ": " + oneLine(r.Summary, 100)
		}
		counts[key]++
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys {
		if len(top) == maxTopFailing {
			break
		}
		top = append(top, fmt.Sprintf("%s (x%d)", k, counts[k]))
	}
	return denied, failed, top
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		s = s[:n] + "..."
	}
	return s
}

// Render formats the pack, at most max bytes (0 = MaxEvidenceBytes), and says
// how many sessions were cut. Sessions are ordered most problematic first, so
// the cut ones are the least notable.
func (p *Pack) Render(max int) string {
	if max <= 0 {
		max = MaxEvidenceBytes
	}
	scope := "agent " + p.Agent
	if p.Fleet {
		scope = "every agent"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "EVIDENCE (%s, last %s, %d session(s)):\n", scope, shortDur(p.Since), len(p.Sessions))
	if len(p.Sessions) == 0 {
		b.WriteString("  (no sessions found)\n")
		return b.String()
	}
	shown := 0
	for _, s := range p.Sessions {
		block := s.render(p.Fleet)
		// Reserve room for the truncation notice.
		if b.Len()+len(block)+120 > max {
			break
		}
		b.WriteString(block)
		shown++
	}
	if cut := len(p.Sessions) - shown; cut > 0 {
		fmt.Fprintf(&b, "\n[%d of %d session(s) cut to stay under %d KB; ordered by denials/failures, so the least notable were dropped]\n",
			cut, len(p.Sessions), max/1024)
	}
	return b.String()
}

func shortDur(d time.Duration) string {
	if d >= 48*time.Hour && d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	}
	return d.Round(time.Minute).String()
}

func (s SessionEvidence) render(fleet bool) string {
	var b strings.Builder
	id := s.ID
	if len(id) > 8 {
		id = id[:8]
	}
	fmt.Fprintf(&b, "\nSession %s", id)
	if fleet {
		fmt.Fprintf(&b, " [%s]", s.Agent)
	}
	fmt.Fprintf(&b, " %s, %s, %d turn(s)", s.Start.Local().Format("2006-01-02 15:04"), s.Duration.Round(time.Minute), s.Turns)
	if s.Project != "" {
		b.WriteString(", project " + s.Project)
	}
	b.WriteString("\n")
	b.WriteString("  outcome: ")
	if s.Closed {
		b.WriteString(s.FinalSt)
		if s.PR != "" {
			b.WriteString(", PR " + s.PR)
		}
	} else {
		b.WriteString("unknown (no closed-window record; from transcript)")
	}
	if s.Model != "" {
		b.WriteString("; model " + s.Model)
	}
	if s.Backend != "" {
		b.WriteString("; backend " + s.Backend)
	}
	b.WriteString("\n")
	if s.CostKnown {
		fmt.Fprintf(&b, "  cost: $%.2f\n", s.CostUSD)
	} else {
		b.WriteString("  cost: unknown\n")
	}
	if s.Denied+s.Failed > 0 {
		fmt.Fprintf(&b, "  tools: %d denied, %d failed\n", s.Denied, s.Failed)
		for _, t := range s.TopFailed {
			b.WriteString("    - " + t + "\n")
		}
	}
	for _, v := range s.Verdicts {
		fmt.Fprintf(&b, "  verdict: PR #%d %s", v.PR, v.Verdict)
		if v.Rounds > 0 {
			fmt.Fprintf(&b, " (round %d)", v.Rounds)
		}
		b.WriteString("\n")
	}
	for _, f := range s.Findings {
		b.WriteString("  finding: " + oneLine(f, 300) + "\n")
	}
	return b.String()
}

// ledgerVerdicts reads <stateDir>/verdicts/verdicts-*.ndjson. A missing
// directory or file yields nothing.
type ledgerVerdicts struct{}

type verdictLine struct {
	TS              time.Time `json:"ts"`
	PR              int       `json:"pr"`
	Repo            string    `json:"repo"`
	Verdict         string    `json:"verdict"`
	Rounds          int       `json:"rounds"`
	AuthorSessionID string    `json:"author_session_id"`
}

func (ledgerVerdicts) Verdicts(since time.Time) []Verdict {
	state, err := config.StateDir()
	if err != nil {
		return nil
	}
	files, _ := filepath.Glob(filepath.Join(state, "verdicts", "verdicts-*.ndjson"))
	var out []Verdict
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for sc.Scan() {
			var l verdictLine
			if json.Unmarshal(sc.Bytes(), &l) != nil || l.Verdict == "" {
				continue
			}
			if !since.IsZero() && l.TS.Before(since) {
				continue
			}
			out = append(out, Verdict{PR: l.PR, Repo: l.Repo, Verdict: l.Verdict, Rounds: l.Rounds, AuthorSessionID: l.AuthorSessionID})
		}
		fh.Close()
	}
	return out
}
