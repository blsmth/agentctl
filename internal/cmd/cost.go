package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/dispatch"
	"github.com/blsmth/agentctl/internal/sessions"
)

// CostOptions are the flags of `agentctl cost`.
type CostOptions struct {
	Session string // optional session ID (prefix): per-model breakdown for one session
	Since   string // "7d", "36h", "2w"; default 7d
	By      string // "", agent, model, day, project
	Metered bool   // metered sessions only
	JSON    bool
}

// costRow is one priced session.
type costRow struct {
	Start     time.Time
	SessionID string
	Project   string
	Agent     string
	Model     string // display label of the session's last model
	Turns     int
	Cost      sessions.SessionCost
}

// costTotals accumulates the money columns. Sessions whose cost is unknown are
// counted in Excluded and contribute nothing to the dollar figures.
type costTotals struct {
	Sessions  int
	Turns     int
	CacheRead int
	Output    int
	Metered   float64 // real spend
	Sub       float64 // API-equivalent value of subscription sessions
	Excluded  int     // sessions (or model slices) with no known price
}

// Total is the sum of metered and subscription dollars (free adds nothing).
func (t costTotals) Total() float64 { return t.Metered + t.Sub }

func (t *costTotals) addRow(r costRow) {
	t.Sessions++
	t.Turns += r.Turns
	t.CacheRead += r.Cost.Usage.CacheRead
	t.Output += r.Cost.Usage.Output
	t.addCost(r.Cost.Billing, r.Cost.USD, r.Cost.Known)
}

func (t *costTotals) addCost(b sessions.Billing, usd float64, known bool) {
	if !known {
		t.Excluded++
		return
	}
	switch b {
	case sessions.BillingMetered:
		t.Metered += usd
	case sessions.BillingSub:
		t.Sub += usd
	}
}

// costGroup is one line of a --by report.
type costGroup struct {
	Key string
	costTotals
}

// parseSince parses "7d", "2w", "36h" (and anything time.ParseDuration takes).
func parseSince(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 7 * 24 * time.Hour, nil
	}
	if n := len(s); n > 1 {
		if v, err := strconv.ParseFloat(s[:n-1], 64); err == nil && v >= 0 {
			switch s[n-1] {
			case 'd':
				return time.Duration(v * float64(24*time.Hour)), nil
			case 'w':
				return time.Duration(v * float64(7*24*time.Hour)), nil
			}
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid --since %q (want e.g. 7d, 2w, 36h)", s)
	}
	return d, nil
}

// groupCosts aggregates rows by agent, model, day or project. Model grouping
// attributes each session's per-model slices, so a session that switched
// models contributes to both.
func groupCosts(rows []costRow, by string) ([]costGroup, error) {
	idx := map[string]*costGroup{}
	get := func(k string) *costGroup {
		g, ok := idx[k]
		if !ok {
			g = &costGroup{Key: k}
			idx[k] = g
		}
		return g
	}
	for _, r := range rows {
		switch by {
		case "agent":
			get(r.Agent).addRow(r)
		case "project":
			get(r.Project).addRow(r)
		case "day":
			get(r.Start.In(time.Local).Format("2006-01-02")).addRow(r)
		case "model":
			g := map[string]bool{}
			for _, mc := range r.Cost.ByModel {
				k := cliAbbreviateModel(mc.Model)
				if k == "" {
					k = "(none)"
				}
				grp := get(k)
				if !g[k] {
					grp.Sessions++
					g[k] = true
				}
				grp.Turns += mc.Turns
				grp.CacheRead += mc.Usage.CacheRead
				grp.Output += mc.Usage.Output
				grp.addCost(r.Cost.Billing, mc.USD, mc.Known)
			}
		default:
			return nil, fmt.Errorf("invalid --by %q (want agent, model, day or project)", by)
		}
	}
	out := make([]costGroup, 0, len(idx))
	for _, g := range idx {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		if by == "day" {
			return out[i].Key < out[j].Key
		}
		if out[i].Total() != out[j].Total() {
			return out[i].Total() > out[j].Total()
		}
		return out[i].Key < out[j].Key
	})
	return out, nil
}

func sumCosts(rows []costRow) costTotals {
	var t costTotals
	for _, r := range rows {
		t.addRow(r)
	}
	return t
}

// pricedRow turns a parsed session into a costRow.
func pricedRow(s sessions.Session, sum *sessions.Summary, backends map[string]config.BackendConfig) costRow {
	billing := sessions.BillingFor(s, sum.ModelName, backends)
	return costRow{
		Start:     s.StartTime,
		SessionID: s.ID,
		Project:   sessions.NormalizeProjectName(s.Project),
		Agent:     s.Agent,
		Model:     cliAbbreviateModel(sum.ModelName),
		Turns:     sum.TurnCount,
		Cost:      sessions.CostOfSummary(sum, billing),
	}
}

func loadCostRows(since time.Duration, meteredOnly bool) ([]costRow, error) {
	recent, err := sessions.FindRecentSessions(0, since)
	if err != nil {
		return nil, fmt.Errorf("loading sessions: %w", err)
	}
	backends := config.Load().Backends
	type parsed struct {
		s   sessions.Session
		sum *sessions.Summary
	}
	var all []parsed
	for _, s := range recent {
		sum, err := sessions.ParseSummary(s)
		if err != nil || sum.TurnCount == 0 {
			continue
		}
		all = append(all, parsed{s, sum})
	}
	build := func() []costRow {
		var rows []costRow
		for _, p := range all {
			r := pricedRow(p.s, p.sum, backends)
			if meteredOnly && r.Cost.Billing != sessions.BillingMetered {
				continue
			}
			rows = append(rows, r)
		}
		return rows
	}
	rows := build()
	// Unpriced sessions and a nous backend configured: the cached catalog may
	// be missing or predate pricing. Refresh it once (explicit command, so a
	// network call is fine) and re-price.
	if anyUnknown(rows) || meteredOnly {
		if err := dispatch.RefreshNousPricing(backends); err != nil {
			fmt.Fprintf(os.Stderr, "note: could not refresh Nous pricing: %v\n", err)
		}
		rows = build()
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Start.Before(rows[j].Start) })
	return rows, nil
}

func anyUnknown(rows []costRow) bool {
	for _, r := range rows {
		if !r.Cost.Known {
			return true
		}
	}
	return false
}

// RunCost implements `agentctl cost`.
func RunCost(o CostOptions) error {
	since, err := parseSince(o.Since)
	if err != nil {
		return err
	}
	if o.Session != "" {
		return printSessionCost(o)
	}
	if o.By != "" {
		if _, err := groupCosts(nil, o.By); err != nil {
			return err
		}
	}
	rows, err := loadCostRows(since, o.Metered)
	if err != nil {
		return err
	}
	totals := sumCosts(rows)
	var groups []costGroup
	if o.By != "" {
		groups, _ = groupCosts(rows, o.By)
	}
	if o.JSON {
		return printCostJSON(rows, groups, totals, o)
	}
	printCostTable(rows, groups, totals, o)
	return nil
}

// ─── rendering ────────────────────────────────────────────────────────────────

func fmtCount(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return strconv.Itoa(n)
}

func costCell(usd float64, known bool) string {
	if !known {
		return "?"
	}
	return fmt.Sprintf("$%.2f", usd)
}

func costTable(headers ...string) *table.Table {
	return table.New().
		Border(lipgloss.RoundedBorder()).
		BorderStyle(cliMuted()).
		Headers(headers...).
		StyleFunc(func(row, _ int) lipgloss.Style {
			if row == table.HeaderRow {
				return cliHeaderStyle()
			}
			return lipgloss.NewStyle()
		})
}

func printCostTable(rows []costRow, groups []costGroup, totals costTotals, o CostOptions) {
	if len(rows) == 0 {
		fmt.Println(cliMuted().Render("No sessions in range."))
		return
	}
	if o.By != "" {
		t := costTable(strings.ToUpper(o.By), "SESSIONS", "TURNS", "CACHE READ", "OUTPUT", "METERED", "SUB", "EXCLUDED")
		for _, g := range groups {
			t.Row(g.Key, strconv.Itoa(g.Sessions), strconv.Itoa(g.Turns), fmtCount(g.CacheRead), fmtCount(g.Output),
				fmt.Sprintf("$%.2f", g.Metered), fmt.Sprintf("$%.2f", g.Sub), excludedCell(g.Excluded))
		}
		t.Row("TOTAL", strconv.Itoa(totals.Sessions), strconv.Itoa(totals.Turns), fmtCount(totals.CacheRead), fmtCount(totals.Output),
			fmt.Sprintf("$%.2f", totals.Metered), fmt.Sprintf("$%.2f", totals.Sub), excludedCell(totals.Excluded))
		fmt.Println(t.Render())
		printCostFooter(totals)
		return
	}
	t := costTable("DATE", "PROJECT", "AGENT", "MODEL", "BILLING", "TURNS", "CACHE READ", "OUTPUT", "COST")
	for _, r := range rows {
		t.Row(r.Start.In(time.Local).Format("01-02 15:04"), r.Project, r.Agent, r.Model, r.Cost.Billing.String(),
			strconv.Itoa(r.Turns), fmtCount(r.Cost.Usage.CacheRead), fmtCount(r.Cost.Usage.Output), costCell(r.Cost.USD, r.Cost.Known))
	}
	t.Row("TOTAL", "", "", "", "", strconv.Itoa(totals.Turns), fmtCount(totals.CacheRead), fmtCount(totals.Output),
		costCell(totals.Total(), true))
	fmt.Println(t.Render())
	printCostFooter(totals)
}

func excludedCell(n int) string {
	if n == 0 {
		return "-"
	}
	return strconv.Itoa(n)
}

func printCostFooter(t costTotals) {
	fmt.Printf("%s  $%.2f    %s  $%.2f (API-equivalent)\n",
		cliAccent().Render("metered:"), t.Metered, cliMuted().Render("sub:"), t.Sub)
	if t.Excluded > 0 {
		fmt.Println(cliRed().Render(fmt.Sprintf("%d session(s) with unknown pricing (shown as ?) are excluded from totals", t.Excluded)))
	}
}

// ─── single session ───────────────────────────────────────────────────────────

func findSessionByPrefix(id string) (*sessions.Session, error) {
	recent, err := sessions.FindRecentSessions(0, 100*365*24*time.Hour)
	if err != nil {
		return nil, fmt.Errorf("loading sessions: %w", err)
	}
	var match *sessions.Session
	for i := range recent {
		if strings.HasPrefix(recent[i].ID, id) {
			if match != nil {
				return nil, fmt.Errorf("session id prefix %q is ambiguous", id)
			}
			s := recent[i]
			match = &s
		}
	}
	if match == nil {
		return nil, fmt.Errorf("session %q not found", id)
	}
	return match, nil
}

type sessionCostJSON struct {
	ID      string          `json:"id"`
	Agent   string          `json:"agent"`
	Project string          `json:"project"`
	Billing string          `json:"billing"`
	Turns   int             `json:"turns"`
	CostUSD *float64        `json:"cost_usd"`
	Models  []modelCostJSON `json:"models"`
}

type modelCostJSON struct {
	Model       string   `json:"model"`
	Turns       int      `json:"turns"`
	Input       int      `json:"input_tokens"`
	Output      int      `json:"output_tokens"`
	CacheRead   int      `json:"cache_read_tokens"`
	CacheCreate int      `json:"cache_create_tokens"`
	CostUSD     *float64 `json:"cost_usd"`
}

func ptrIf(usd float64, known bool) *float64 {
	if !known {
		return nil
	}
	return &usd
}

func printSessionCost(o CostOptions) error {
	s, err := findSessionByPrefix(o.Session)
	if err != nil {
		return err
	}
	sum, err := sessions.ParseSummary(*s)
	if err != nil {
		return fmt.Errorf("parsing session: %w", err)
	}
	r := pricedRow(*s, sum, config.Load().Backends)
	if o.JSON {
		out := sessionCostJSON{ID: s.ID, Agent: s.Agent, Project: r.Project, Billing: r.Cost.Billing.String(),
			Turns: r.Turns, CostUSD: ptrIf(r.Cost.USD, r.Cost.Known)}
		for _, mc := range r.Cost.ByModel {
			out.Models = append(out.Models, modelCostJSON{Model: mc.Model, Turns: mc.Turns, Input: mc.Usage.Input,
				Output: mc.Usage.Output, CacheRead: mc.Usage.CacheRead, CacheCreate: mc.Usage.CacheCreate,
				CostUSD: ptrIf(mc.USD, mc.Known)})
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	fmt.Printf("%s  %s · %s · %s\n\n", cliAccent().Render("Session "+s.ID), r.Agent, r.Project, r.Cost.Billing.String())
	t := costTable("MODEL", "TURNS", "INPUT", "CACHE WRITE", "CACHE READ", "OUTPUT", "COST")
	for _, mc := range r.Cost.ByModel {
		name := mc.Model
		if name == "" {
			name = "(unknown)"
		}
		t.Row(name, strconv.Itoa(mc.Turns), fmtCount(mc.Usage.Input), fmtCount(mc.Usage.CacheCreate),
			fmtCount(mc.Usage.CacheRead), fmtCount(mc.Usage.Output), costCell(mc.USD, mc.Known))
	}
	t.Row("TOTAL", strconv.Itoa(r.Turns), fmtCount(r.Cost.Usage.Input), fmtCount(r.Cost.Usage.CacheCreate),
		fmtCount(r.Cost.Usage.CacheRead), fmtCount(r.Cost.Usage.Output), costCell(r.Cost.USD, r.Cost.Known))
	fmt.Println(t.Render())
	if !r.Cost.Known {
		fmt.Println(cliRed().Render("Some models have no known price (?); add one under models.overrides in config.json."))
	}
	return nil
}

// ─── JSON ─────────────────────────────────────────────────────────────────────

type costRowJSON struct {
	Date      string   `json:"date"`
	ID        string   `json:"id"`
	Project   string   `json:"project"`
	Agent     string   `json:"agent"`
	Model     string   `json:"model"`
	Billing   string   `json:"billing"`
	Turns     int      `json:"turns"`
	CacheRead int      `json:"cache_read_tokens"`
	Output    int      `json:"output_tokens"`
	CostUSD   *float64 `json:"cost_usd"` // null = unknown
}

type costGroupJSON struct {
	Key       string  `json:"key"`
	Sessions  int     `json:"sessions"`
	Turns     int     `json:"turns"`
	CacheRead int     `json:"cache_read_tokens"`
	Output    int     `json:"output_tokens"`
	MeteredUS float64 `json:"metered_usd"`
	SubUSD    float64 `json:"sub_usd"`
	Excluded  int     `json:"excluded"`
}

type costTotalsJSON struct {
	Sessions int     `json:"sessions"`
	Metered  float64 `json:"metered_usd"`
	Sub      float64 `json:"sub_usd"`
	Total    float64 `json:"total_usd"`
	Excluded int     `json:"excluded_sessions"`
}

func printCostJSON(rows []costRow, groups []costGroup, totals costTotals, o CostOptions) error {
	out := struct {
		Since    string          `json:"since"`
		By       string          `json:"by,omitempty"`
		Sessions []costRowJSON   `json:"sessions,omitempty"`
		Groups   []costGroupJSON `json:"groups,omitempty"`
		Totals   costTotalsJSON  `json:"totals"`
	}{Since: o.Since, By: o.By}
	if out.Since == "" {
		out.Since = "7d"
	}
	if o.By == "" {
		out.Sessions = []costRowJSON{}
		for _, r := range rows {
			out.Sessions = append(out.Sessions, costRowJSON{
				Date: r.Start.Format(time.RFC3339), ID: r.SessionID, Project: r.Project, Agent: r.Agent,
				Model: r.Model, Billing: r.Cost.Billing.String(), Turns: r.Turns,
				CacheRead: r.Cost.Usage.CacheRead, Output: r.Cost.Usage.Output,
				CostUSD: ptrIf(r.Cost.USD, r.Cost.Known),
			})
		}
	} else {
		out.Groups = []costGroupJSON{}
		for _, g := range groups {
			out.Groups = append(out.Groups, costGroupJSON{Key: g.Key, Sessions: g.Sessions, Turns: g.Turns,
				CacheRead: g.CacheRead, Output: g.Output, MeteredUS: g.Metered, SubUSD: g.Sub, Excluded: g.Excluded})
		}
	}
	out.Totals = costTotalsJSON{Sessions: totals.Sessions, Metered: totals.Metered, Sub: totals.Sub,
		Total: totals.Total(), Excluded: totals.Excluded}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
