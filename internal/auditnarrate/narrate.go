// Package auditnarrate turns an audit report (internal/sessionaudit) into a
// short written verdict through the configured Nous backend. Only findings
// (their evidence snippets, with secrets redacted) and metrics are sent, never raw tool output or file contents, and the
// prompt is capped at about 6KB. Verdicts are cached per session under
// <stateDir>/audit/narratives/, keyed by a hash of what was sent, so a
// changed report is narrated again and an unchanged one costs nothing.
//
// Every failure is non-fatal for the caller: it prints the report and a note.
package auditnarrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/backendauth"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/llm"
	"github.com/blsmth/agentctl/internal/llmusage"
	"github.com/blsmth/agentctl/internal/sessionaudit"
)

// Feature is the llmusage feature the calls are ledgered under.
const Feature = "audit"

// MaxPromptBytes caps the whole prompt.
const MaxPromptBytes = 6 << 10

// maxPayloadBytes caps the JSON inside the prompt, leaving room for the
// instructions.
const maxPayloadBytes = MaxPromptBytes - 800

// ErrDisabled means no nous backend is configured (or titles are off).
var ErrDisabled = errors.New("no nous backend configured")

// Narrator generates and caches narratives. Every dependency is a seam so
// tests never touch the network, keychain or real state dir.
type Narrator struct {
	Settings     func() config.TitleSettings
	KeyAvailable func(config.TitleSettings) error
	NewCompleter func(config.TitleSettings) (llm.Completer, error)
	NoCredits    func(name string, now time.Time) (backendauth.CreditState, bool)
	StateDir     func() (string, error)
	Now          func() time.Time
}

// New is a Narrator wired to the real backend, key store and state dir.
func New() *Narrator {
	return &Narrator{
		Settings:     func() config.TitleSettings { return config.Load().EffectiveTitles() },
		KeyAvailable: func(s config.TitleSettings) error { return backendauth.Available(s.BackendName, s.Backend) },
		NewCompleter: func(s config.TitleSettings) (llm.Completer, error) {
			return llm.NewNousClient(s.BackendName, s.Backend, s.Model)
		},
		NoCredits: backendauth.NoCreditsFresh,
		StateDir:  config.StateDir,
		Now:       time.Now,
	}
}

// HasFindings reports whether r has anything to narrate. Clean sessions are
// skipped.
func HasFindings(r *sessionaudit.Report) bool {
	return r != nil && r.Safety.Findings+r.Efficiency.Findings > 0
}

// Session narrates one report.
func (n *Narrator) Session(ctx context.Context, r *sessionaudit.Report) (string, error) {
	payload := sessionPayload(r)
	return n.narrate(ctx, "session", r.Session.ID, r.Session.Project, payload)
}

// Aggregate narrates the whole run.
func (n *Narrator) Aggregate(ctx context.Context, reports []*sessionaudit.Report, agg sessionaudit.Aggregate) (string, error) {
	return n.narrate(ctx, "aggregate", "", "", aggregatePayload(reports, agg))
}

func (n *Narrator) narrate(ctx context.Context, kind, id, project string, payload []byte) (string, error) {
	prompt := buildPrompt(kind, payload)
	sum := sha256.Sum256([]byte(prompt))
	hash := hex.EncodeToString(sum[:])

	if text, ok := n.cached(id, hash); ok {
		return text, nil
	}
	s := n.Settings()
	if !s.Enabled {
		return "", ErrDisabled
	}
	now := n.Now()
	// Out of credits: no request until the marker goes stale. A :free model
	// cannot 402.
	if _, out := n.NoCredits(s.BackendName, now); out && !strings.HasSuffix(s.Model, ":free") {
		return "", fmt.Errorf("backend %s: %w", s.BackendName, backendauth.ErrNoCredits)
	}
	if err := n.KeyAvailable(s); err != nil {
		return "", fmt.Errorf("no key for backend %s: %w", s.BackendName, err)
	}
	comp, err := n.NewCompleter(s)
	if err != nil {
		return "", err
	}
	ctx = llmusage.WithMeta(ctx, llmusage.Meta{Feature: Feature, Project: project})
	out, err := comp.Complete(ctx, prompt)
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(out)
	if text == "" {
		return "", errors.New("empty completion")
	}
	n.store(id, hash, s.Model, text)
	return text, nil
}

// ─── Prompt ──────────────────────────────────────────────────────────────────

const instructions = `You review an AI coding agent's session from an automated audit report (JSON below). ` +
	`Reply with a verdict of 3 to 5 plain sentences, no headings or lists: what went well, what was risky, ` +
	`and one concrete habit the agent (or whoever configures it) should change. ` +
	`Use only the data given; do not invent details.`

func buildPrompt(kind string, payload []byte) string {
	scope := "one session"
	if kind == "aggregate" {
		scope = "many sessions, summarized"
	}
	return instructions + "\n\nScope: " + scope + ".\n\n" + string(payload)
}

type sessionOut struct {
	ID      string `json:"id,omitempty"`
	Agent   string `json:"agent,omitempty"`
	Project string `json:"project,omitempty"`
	Model   string `json:"model,omitempty"`
	Harness string `json:"harness,omitempty"`
}

type findingOut struct {
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Step     int    `json:"step,omitempty"`
	Tool     string `json:"tool,omitempty"`
	Evidence string `json:"evidence"`
	Why      string `json:"why"`
}

type sessionDoc struct {
	Session         sessionOut           `json:"session"`
	Safety          sessionaudit.Score   `json:"safety"`
	Efficiency      sessionaudit.Score   `json:"efficiency"`
	Findings        []findingOut         `json:"findings"`
	FindingsOmitted int                  `json:"findings_omitted,omitempty"`
	Metrics         sessionaudit.Metrics `json:"metrics"`
}

// sessionPayload is the report minus paths and titles, with findings sorted
// worst first and dropped from the tail until it fits the cap.
func sessionPayload(r *sessionaudit.Report) []byte {
	fs := append([]sessionaudit.Finding(nil), r.Findings...)
	sort.SliceStable(fs, func(i, j int) bool { return fs[i].Severity.Rank() > fs[j].Severity.Rank() })
	all := make([]findingOut, len(fs))
	for i, f := range fs {
		all[i] = findingOut{Rule: f.Rule, Severity: string(f.Severity), Step: f.Step, Tool: f.Tool, Evidence: Redact(f.Evidence), Why: Redact(f.Why)}
	}
	return fit(len(all), func(k int) []byte {
		doc := sessionDoc{
			Session: sessionOut{ID: r.Session.ID, Agent: r.Session.Agent, Project: r.Session.Project, Model: r.Session.Model, Harness: r.Session.Harness},
			Safety:  r.Safety, Efficiency: r.Efficiency, Metrics: r.Metrics,
			Findings: all[:k], FindingsOmitted: len(all) - k,
		}
		b, _ := json.Marshal(doc)
		return b
	})
}

type aggSession struct {
	Agent      string `json:"agent,omitempty"`
	Project    string `json:"project,omitempty"`
	Steps      int    `json:"steps"`
	Safety     string `json:"safety"`
	Efficiency string `json:"efficiency"`
	Findings   int    `json:"findings"`
}

type aggDoc struct {
	Sessions      int                      `json:"sessions"`
	Skipped       int                      `json:"skipped_unreadable"`
	Rules         []sessionaudit.RuleCount `json:"rules"`
	Detail        []aggSession             `json:"session_summaries"`
	DetailOmitted int                      `json:"session_summaries_omitted,omitempty"`
}

func aggregatePayload(reports []*sessionaudit.Report, agg sessionaudit.Aggregate) []byte {
	detail := make([]aggSession, len(reports))
	for i, r := range reports {
		detail[i] = aggSession{
			Agent: r.Session.Agent, Project: r.Session.Project, Steps: r.Metrics.Steps,
			Safety:     fmt.Sprintf("%s %d", r.Safety.Grade, r.Safety.Score),
			Efficiency: fmt.Sprintf("%s %d", r.Efficiency.Grade, r.Efficiency.Score),
			Findings:   r.Safety.Findings + r.Efficiency.Findings,
		}
	}
	rules := agg.Rules
	if len(rules) > 15 {
		rules = rules[:15]
	}
	return fit(len(detail), func(k int) []byte {
		b, _ := json.Marshal(aggDoc{Sessions: agg.Sessions, Skipped: agg.Skipped, Rules: rules, Detail: detail[:k], DetailOmitted: len(detail) - k})
		return b
	})
}

// fit renders with as many of the n items as stay under the cap.
func fit(n int, render func(k int) []byte) []byte {
	for k := n; k > 0; k-- {
		if b := render(k); len(b) <= maxPayloadBytes {
			return b
		}
	}
	b := render(0)
	if len(b) > maxPayloadBytes {
		b = b[:maxPayloadBytes]
	}
	return b
}

// MaxAge is how long a cached narrative is kept.
const MaxAge = 90 * 24 * time.Hour

// Prune removes cached narratives not written for maxAge. It returns the
// number removed; a missing dir is not an error.
func Prune(now time.Time, maxAge time.Duration) (int, error) {
	root, err := config.StateDir()
	if err != nil {
		return 0, err
	}
	dir := filepath.Join(root, "audit", "narratives")
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	n := 0
	for _, e := range ents {
		info, err := e.Info()
		if err != nil || e.IsDir() || now.Sub(info.ModTime()) < maxAge {
			continue
		}
		if os.Remove(filepath.Join(dir, e.Name())) == nil {
			n++
		}
	}
	return n, nil
}

// ─── Cache ───────────────────────────────────────────────────────────────────

type cacheEntry struct {
	SessionID string    `json:"session_id,omitempty"`
	Hash      string    `json:"hash"`
	Model     string    `json:"model,omitempty"`
	Text      string    `json:"text"`
	TS        time.Time `json:"ts"`
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// cachePath is <stateDir>/audit/narratives/<session id>.json; a run with no
// session id (the aggregate) is keyed by its hash.
func (n *Narrator) cachePath(id, hash string) (string, error) {
	dir, err := n.StateDir()
	if err != nil {
		return "", err
	}
	name := unsafeName.ReplaceAllString(id, "_")
	if name == "" || strings.Trim(name, ".") == "" {
		name = "aggregate-" + hash[:16]
	}
	return filepath.Join(dir, "audit", "narratives", name+".json"), nil
}

func (n *Narrator) cached(id, hash string) (string, bool) {
	p, err := n.cachePath(id, hash)
	if err != nil {
		return "", false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	var e cacheEntry
	if json.Unmarshal(b, &e) != nil || e.Hash != hash || e.Text == "" {
		return "", false
	}
	return e.Text, true
}

// store is best effort: a failed write only costs a re-narration later.
func (n *Narrator) store(id, hash, model, text string) {
	p, err := n.cachePath(id, hash)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	b, err := json.Marshal(cacheEntry{SessionID: id, Hash: hash, Model: model, Text: text, TS: n.Now()})
	if err != nil {
		return
	}
	tmp := p + ".tmp"
	if os.WriteFile(tmp, b, 0o600) != nil {
		return
	}
	if os.Rename(tmp, p) != nil {
		_ = os.Remove(tmp)
	}
}
