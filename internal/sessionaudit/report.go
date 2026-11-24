// Package sessionaudit grades one past agent session for safety and
// efficiency. It reads the session's transcript (as logstream entries, any
// harness) and, when there are any, the hook audit records for the same
// session, which are authoritative for denials and failures. Deterministic,
// table-driven rules (rules.go) turn the steps into findings; scores start
// at 100 per category and lose points per finding by severity.
//
// The package is pure: no tmux, no network, no spawns, no file reads. The
// steps come from internal/race, so step numbers match `agentctl race`.
package sessionaudit

import (
	"bytes"
	"encoding/json"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/blsmth/agentctl/internal/audit"
	"github.com/blsmth/agentctl/internal/logstream"
	"github.com/blsmth/agentctl/internal/race"
)

// SchemaVersion is the `schema` of the JSON report.
const SchemaVersion = 1

// Category groups rules into the two scores.
type Category string

// Categories.
const (
	Safety     Category = "safety"
	Efficiency Category = "efficiency"
)

// Severity is how much a finding costs.
type Severity string

// Severities.
const (
	High Severity = "high"
	Med  Severity = "med"
	Low  Severity = "low"
)

// Rank orders severities: high 3, med 2, low 1, unknown 0.
func (s Severity) Rank() int {
	switch s {
	case High:
		return 3
	case Med:
		return 2
	case Low:
		return 1
	}
	return 0
}

// ParseSeverity accepts high, med (or medium) and low.
func ParseSeverity(s string) (Severity, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "high":
		return High, true
	case "med", "medium":
		return Med, true
	case "low", "":
		return Low, true
	}
	return "", false
}

// weights are every scoring knob in one place.
var weights = struct {
	High, Med, Low float64
	// EfficiencyRefSteps is the session length at which efficiency findings
	// cost their full weight; a longer session pays ref/steps of it.
	EfficiencyRefSteps int
	// Grade floors: A ≥ 90, B ≥ 80, C ≥ 70, D ≥ 60, else F.
	Grades []gradeFloor
}{
	High: 25, Med: 10, Low: 3,
	EfficiencyRefSteps: 50,
	Grades:             []gradeFloor{{90, "A"}, {80, "B"}, {70, "C"}, {60, "D"}},
}

type gradeFloor struct {
	min   int
	grade string
}

func weight(s Severity) float64 {
	switch s {
	case High:
		return weights.High
	case Med:
		return weights.Med
	case Low:
		return weights.Low
	}
	return 0
}

// Grade is the letter for a score.
func Grade(score int) string {
	for _, g := range weights.Grades {
		if score >= g.min {
			return g.grade
		}
	}
	return "F"
}

// Params tune the analysis.
type Params struct {
	Race    race.Params   // step building; MaxGap caps charged agent time
	IdleGap time.Duration // a gap inside a turn at least this long is a finding
	Home    string        // expands ~ and $HOME; "" = unknown (treated as outside)
	Cwd     string        // fallback when the transcript records none
	// TempDirs are prefixes that never count as outside the repo.
	TempDirs []string
	// AllowedDirs are dirs the harness itself has agents write to (Claude
	// Code's agent-memory and plans dirs); writes there are not "outside".
	AllowedDirs []string
	// Cost prices the session's requests; nil = cost unknown.
	Cost func([]TurnUsage) (usd float64, known bool)
}

// DefaultParams are the defaults of `agentctl audit report`.
func DefaultParams() Params {
	return Params{
		Race:     race.DefaultParams(),
		IdleGap:  5 * time.Minute,
		TempDirs: []string{"/tmp", "/private/tmp", "/var/folders", "/private/var/folders", "$TMPDIR"},
	}
}

// TurnUsage is one API request's tokens, for pricing.
type TurnUsage struct {
	Model                                 string
	Input, Output, CacheRead, CacheCreate int
	Timestamp                             time.Time
}

// Report is the audit of one session: the payload of `audit report --json`.
type Report struct {
	Schema     int        `json:"schema"`
	Session    Session    `json:"session"`
	Safety     Score      `json:"safety"`
	Efficiency Score      `json:"efficiency"`
	Findings   []Finding  `json:"findings"`
	Metrics    Metrics    `json:"metrics"`
	Audit      AuditTrail `json:"audit"`
	// Narrative is the LLM verdict of `audit report --narrate`; absent otherwise.
	Narrative string `json:"narrative,omitempty"`
}

// Session identifies what was audited. Title, Source and Account are filled
// by the caller, which knows where the transcript came from.
type Session struct {
	ID      string     `json:"id,omitempty"`
	Title   string     `json:"title,omitempty"`
	Source  string     `json:"source,omitempty"`
	Agent   string     `json:"agent,omitempty"`
	Project string     `json:"project,omitempty"`
	Cwd     string     `json:"cwd,omitempty"`
	Model   string     `json:"model,omitempty"`
	Account string     `json:"account,omitempty"`
	Harness string     `json:"harness"`
	Start   *time.Time `json:"start,omitempty"`
	End     *time.Time `json:"end,omitempty"`
}

// Score is one category's result.
type Score struct {
	Score    int    `json:"score"`
	Grade    string `json:"grade"`
	Findings int    `json:"findings"`
}

// Finding is one rule hit.
type Finding struct {
	Rule     string     `json:"rule"`
	Title    string     `json:"title"`
	Category Category   `json:"category"`
	Severity Severity   `json:"severity"`
	Step     int        `json:"step"` // 1-based race step; 0 = the whole session
	Time     *time.Time `json:"time,omitempty"`
	Tool     string     `json:"tool,omitempty"`
	Evidence string     `json:"evidence"`
	Why      string     `json:"why"`
}

// Tokens are the session's token totals, deduplicated by message.
type Tokens struct {
	Input       int `json:"input"`
	Output      int `json:"output"`
	CacheRead   int `json:"cache_read"`
	CacheCreate int `json:"cache_create"`
}

// Metrics are report-only numbers; they are not scored.
type Metrics struct {
	Steps            int            `json:"steps"`
	ToolCalls        int            `json:"tool_calls"`
	ToolCallsBy      map[string]int `json:"tool_calls_by_name"`
	Turns            int            `json:"turns"`
	WallSeconds      float64        `json:"wall_seconds"`
	Wall             string         `json:"wall"`
	AgentSeconds     float64        `json:"agent_active_seconds"`
	AgentActive      string         `json:"agent_active"`
	Tokens           Tokens         `json:"tokens"`
	CostUSD          *float64       `json:"cost_usd"` // null = unknown (an unpriced model)
	Edits            int            `json:"edits"`
	CostPerEditUSD   *float64       `json:"cost_per_edit_usd"`
	ToolErrors       int            `json:"tool_errors"`
	ErrorRate        float64        `json:"error_rate"`
	Denied           int            `json:"denied"`
	RepeatReads      int            `json:"repeat_reads"`
	WastedTokensEst  int            `json:"wasted_tokens_est"`
	SidechainEntries int            `json:"sidechain_entries"`
	UnparsedBash     int            `json:"unparsed_bash"`
}

// AuditTrail says whether hook audit records were merged.
type AuditTrail struct {
	Merged  bool `json:"merged"`
	Records int  `json:"records"`
	Matched int  `json:"matched"` // records tied to a transcript step
}

// step is a tool step with what the rules need.
type step struct {
	n int // 1-based race step number
	race.Step
	bash     *parsedBash
	denied   bool
	deniedBy string // rule, human, or "" when unknown
	inferred bool   // denial inferred from the result text, not the audit trail
	failed   bool   // the tool errored (not a denial)
	matched  bool   // an audit record covers this step
}

func (s *step) str(key string) string { v, _ := s.Input[key].(string); return v }

// command is a Bash step's command.
func (s *step) command() string { return s.str("command") }

// relPath shows p relative to the step's cwd when it is under it.
func (s *step) relPath(p string) string {
	if s.Cwd != "" && strings.HasPrefix(p, s.Cwd+"/") {
		return strings.TrimPrefix(p, s.Cwd+"/")
	}
	return p
}

// filePath is the file an edit or read tool names.
func (s *step) filePath() string {
	if p := s.str("file_path"); p != "" {
		return p
	}
	return s.str("notebook_path")
}

// analysis is what the rules run over.
type analysis struct {
	p       Params
	steps   []*step
	pi      bool
	cwd     string
	modes   []modeChange
	orphans []audit.Record // denied records with no transcript step
	metrics *Metrics
}

type modeChange struct {
	from, to string
	at       time.Time
}

// Analyze audits a transcript. recs are the session's hook audit records
// (any schema version; nil when the trail is absent).
func Analyze(entries []logstream.Entry, recs []audit.Record, p Params) *Report {
	r := &Report{Schema: SchemaVersion, Findings: []Finding{}}
	a := &analysis{p: p, metrics: &r.Metrics}
	rc := race.Build(entries, p.Race)

	r.Session = sessionInfo(entries, p)
	a.cwd = r.Session.Cwd
	a.pi = r.Session.Harness == "pi"
	a.modes = modeChanges(entries)

	for i, s := range rc.Steps {
		if s.Kind != race.KindTool {
			continue
		}
		st := &step{n: i + 1, Step: s}
		if st.Cwd == "" {
			st.Cwd = a.cwd
		}
		if s.Tool == "Bash" {
			st.bash = parseBash(st.command())
		}
		a.steps = append(a.steps, st)
	}
	r.Audit = a.merge(recs)
	a.inferDenials()
	fillMetrics(r, a, rc, entries)

	for _, rule := range rules {
		for _, h := range rule.check(a) {
			f := Finding{
				Rule: rule.ID, Title: rule.Title, Category: rule.Category, Severity: rule.Severity,
				Evidence: snippet(h.evidence), Why: h.why,
			}
			if f.Why == "" {
				f.Why = rule.Why
			}
			if h.step != nil {
				f.Step = h.step.n
				f.Tool = h.step.Tool
				if !h.step.At.IsZero() {
					t := h.step.At
					f.Time = &t
				}
			}
			if !h.at.IsZero() {
				t := h.at
				f.Time = &t
			}
			r.Findings = append(r.Findings, f)
		}
	}
	SortFindings(r.Findings)
	r.Safety, r.Efficiency = score(r.Findings, len(a.steps))
	return r
}

// SortFindings orders findings: safety first, then by severity, then step.
func SortFindings(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if a.Category != b.Category {
			return a.Category == Safety
		}
		if a.Severity.Rank() != b.Severity.Rank() {
			return a.Severity.Rank() > b.Severity.Rank()
		}
		if a.Step != b.Step {
			return a.Step < b.Step
		}
		return a.Rule < b.Rule
	})
}

// score totals the penalties per category. Efficiency penalties are scaled
// down for sessions longer than weights.EfficiencyRefSteps.
func score(fs []Finding, steps int) (safety, eff Score) {
	var sp, ep float64
	for _, f := range fs {
		switch f.Category {
		case Safety:
			sp += weight(f.Severity)
			safety.Findings++
		case Efficiency:
			ep += weight(f.Severity)
			eff.Findings++
		}
	}
	if steps > weights.EfficiencyRefSteps {
		ep *= float64(weights.EfficiencyRefSteps) / float64(steps)
	}
	safety.Score = clampScore(100 - sp)
	eff.Score = clampScore(100 - ep)
	safety.Grade = Grade(safety.Score)
	eff.Grade = Grade(eff.Score)
	return safety, eff
}

func clampScore(v float64) int {
	return int(math.Max(0, math.Min(100, math.Round(v))))
}

// sessionInfo reads identity from the transcript's own fields.
func sessionInfo(entries []logstream.Entry, p Params) Session {
	s := Session{Harness: "claude"}
	var first, last time.Time
	for _, e := range entries {
		if e.Raw == nil {
			continue
		}
		if strings.HasPrefix(e.Type, "pi:") {
			s.Harness = "pi"
		}
		if s.ID == "" {
			s.ID, _ = e.Raw["sessionId"].(string)
		}
		if s.Agent == "" {
			s.Agent, _ = e.Raw["agentSetting"].(string)
		}
		side, _ := e.Raw["isSidechain"].(bool)
		if c, _ := e.Raw["cwd"].(string); c != "" && s.Cwd == "" && !side {
			s.Cwd = strings.TrimRight(c, "/")
		}
		if e.Type == "assistant" && !side && e.Model != "" && e.Model != "<synthetic>" {
			s.Model = e.Model
		}
		if !e.Timestamp.IsZero() {
			if first.IsZero() {
				first = e.Timestamp
			}
			last = e.Timestamp
		}
	}
	if s.Cwd == "" {
		s.Cwd = strings.TrimRight(p.Cwd, "/")
	}
	if s.Cwd != "" {
		s.Project = projectName(s.Cwd)
	}
	if !first.IsZero() {
		s.Start, s.End = &first, &last
	}
	return s
}

// projectName is the project a cwd belongs to: the repo above a
// .worktrees/<slug> worktree, else the cwd's own name.
func projectName(cwd string) string {
	if i := strings.Index(cwd, "/.worktrees/"); i > 0 {
		return filepath.Base(cwd[:i])
	}
	return filepath.Base(cwd)
}

// modeChanges lists the permission-mode changes recorded on entries.
func modeChanges(entries []logstream.Entry) []modeChange {
	var out []modeChange
	last := ""
	for _, e := range entries {
		if e.Raw == nil {
			continue
		}
		if side, _ := e.Raw["isSidechain"].(bool); side {
			continue
		}
		m, _ := e.Raw["permissionMode"].(string)
		if m == "" {
			continue
		}
		if last != "" && m != last {
			out = append(out, modeChange{from: last, to: m, at: e.Timestamp})
		}
		last = m
	}
	return out
}

// merge ties audit records to steps by tool_use_id. Denials and failures
// from the trail override what the transcript text suggests.
func (a *analysis) merge(recs []audit.Record) AuditTrail {
	t := AuditTrail{Merged: len(recs) > 0, Records: len(recs)}
	byID := map[string]*step{}
	for _, s := range a.steps {
		if s.ID != "" {
			byID[s.ID] = s
		}
	}
	for _, rec := range recs {
		s := byID[rec.ToolUseID]
		if s == nil || s.matched {
			s = a.matchByInput(rec)
		}
		if s == nil {
			if rec.Event == audit.EventToolDenied {
				a.orphans = append(a.orphans, rec)
			}
			continue
		}
		t.Matched++
		switch rec.Event {
		case audit.EventToolDenied:
			s.denied, s.deniedBy, s.failed = true, rec.DeniedBy, false
		case audit.EventToolFailure:
			if !s.denied {
				s.failed = true
			}
		case audit.EventTool:
			if rec.OK && !s.denied {
				s.failed = false
			}
		}
		s.inferred = false
		s.matched = true
	}
	return t
}

// matchWindow is how far apart a record and its call may be when they are
// matched by input rather than tool_use_id.
const matchWindow = 5 * time.Second

// matchByInput finds the step a record with no (or an unknown) tool_use_id
// belongs to: the first unmatched step of the same tool with the same
// command (Bash) or file path, called within matchWindow of the record. The
// PreToolUse deny writers record no tool_use_id.
func (a *analysis) matchByInput(rec audit.Record) *step {
	ts, err := time.Parse(time.RFC3339Nano, rec.Timestamp)
	if err != nil || len(rec.Input) == 0 {
		return nil
	}
	var in map[string]any
	if json.Unmarshal(rec.Input, &in) != nil {
		return nil
	}
	key := "file_path"
	if rec.Tool == "Bash" {
		key = "command"
	}
	want, _ := in[key].(string)
	if want == "" {
		return nil
	}
	for _, s := range a.steps {
		if s.matched || s.Tool != rec.Tool || s.str(key) != want || s.At.IsZero() {
			continue
		}
		if d := ts.Sub(s.At); d <= matchWindow && d >= -matchWindow {
			return s
		}
	}
	return nil
}

// inferDenials reads denials and failures from the result text of every
// step the audit trail did not cover.
func (a *analysis) inferDenials() {
	for _, s := range a.steps {
		if s.matched || !s.IsError {
			continue
		}
		if by, ok := deniedByText(s.ResultHead); ok {
			s.denied, s.deniedBy, s.inferred = true, by, true
			continue
		}
		s.failed = true
	}
}

// deniedByText recognizes Claude Code's refusal texts: a person rejecting
// the call, or a hook or permission rule blocking it.
func deniedByText(text string) (by string, ok bool) {
	l := strings.ToLower(text)
	switch {
	case strings.Contains(l, "doesn't want to proceed"), strings.Contains(l, "tool use was rejected"),
		strings.Contains(l, "user rejected"):
		return "human", true
	case strings.Contains(l, "hook error"), strings.Contains(l, "blocked by"),
		strings.Contains(l, "permission to use"), strings.Contains(l, "denied by"),
		strings.HasPrefix(l, "denied"):
		return "rule", true
	}
	return "", false
}

// fillMetrics computes the report-only numbers.
func fillMetrics(r *Report, a *analysis, rc *race.Race, entries []logstream.Entry) {
	m := &r.Metrics
	m.Steps = len(rc.Steps)
	m.ToolCallsBy = map[string]int{}
	for k, v := range rc.ToolCalls {
		m.ToolCallsBy[k] = v
		m.ToolCalls += v
	}
	if r.Session.Start != nil {
		wall := r.Session.End.Sub(*r.Session.Start)
		m.WallSeconds = round1(wall.Seconds())
		m.Wall = race.Friendly(wall)
	}
	m.AgentSeconds = round1(rc.AgentTotal.Seconds())
	m.AgentActive = race.Friendly(rc.AgentTotal)

	seen := map[string]bool{}
	var usage []TurnUsage
	for _, e := range entries {
		if e.Raw == nil {
			continue
		}
		if side, _ := e.Raw["isSidechain"].(bool); side {
			m.SidechainEntries++
		}
		if race.IsPrompt(e) {
			if meta, _ := e.Raw["isMeta"].(bool); !meta {
				m.Turns++
			}
		}
		if e.Type != "assistant" || e.Usage == nil {
			continue
		}
		if msg, _ := e.Raw["message"].(map[string]any); msg != nil {
			if id, _ := msg["id"].(string); id != "" {
				if seen[id] {
					continue
				}
				seen[id] = true
			}
		}
		u := e.Usage
		m.Tokens.Input += u.InputTokens
		m.Tokens.Output += u.OutputTokens
		m.Tokens.CacheRead += u.CacheReadTokens
		m.Tokens.CacheCreate += u.CacheCreateTokens
		usage = append(usage, TurnUsage{
			Model: e.Model, Input: u.InputTokens, Output: u.OutputTokens,
			CacheRead: u.CacheReadTokens, CacheCreate: u.CacheCreateTokens, Timestamp: e.Timestamp,
		})
	}

	for _, s := range a.steps {
		switch {
		case s.denied:
			m.Denied++
		case s.failed:
			m.ToolErrors++
		}
		if editTools[s.Tool] && !s.denied && !s.failed {
			m.Edits++
		}
		if s.bash != nil && s.bash.unparsed {
			m.UnparsedBash++
		}
	}
	if m.ToolCalls > 0 {
		m.ErrorRate = math.Round(float64(m.ToolErrors)/float64(m.ToolCalls)*1000) / 1000
	}
	if a.p.Cost != nil {
		if usd, ok := a.p.Cost(usage); ok {
			m.CostUSD = &usd
			if m.Edits > 0 {
				per := usd / float64(m.Edits)
				m.CostPerEditUSD = &per
			}
		}
	}
}

// editTools change files.
var editTools = map[string]bool{"Edit": true, "MultiEdit": true, "Write": true, "NotebookEdit": true}

// snippetMax caps evidence text.
const snippetMax = 120

// snippet collapses whitespace and cuts s to snippetMax runes.
func snippet(s string) string {
	s = strings.Join(strings.Fields(strings.ReplaceAll(s, dynamic, "…")), " ")
	if utf8.RuneCountInString(s) <= snippetMax {
		return s
	}
	rs := []rune(s)
	return string(rs[:snippetMax-1]) + "…"
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }

// JSON returns the report indented, for golden files and --json.
func (r *Report) JSON() ([]byte, error) { return MarshalJSON(r) }

// MarshalJSON indents v without HTML escaping, so evidence stays readable.
func MarshalJSON(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// TopFinding is the report's worst finding, or nil.
func (r *Report) TopFinding() *Finding {
	if len(r.Findings) == 0 {
		return nil
	}
	best := 0
	for i, f := range r.Findings {
		if f.Severity.Rank() > r.Findings[best].Severity.Rank() {
			best = i
		}
	}
	return &r.Findings[best]
}
