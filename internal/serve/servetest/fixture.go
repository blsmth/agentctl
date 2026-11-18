package servetest

import (
	"cmp"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/serve/fleetwatch"
	"github.com/blsmth/agentctl/internal/state"
)

// Pane texts the fakes serve. They are real Claude Code screens as far as
// panestate's classifier is concerned, so the server's own pane checks (the
// answer hash, the close and reply guards) run unchanged.
const (
	IdlePane    = "──── a ──\n❯\n──────\n"
	WorkingPane = "· Twisting… (10m 5s · ↓ 3k tokens)\n❯\n  esc to interrupt\n"
	DialogPane  = "\x1b[2m● Bash(go test ./...)\x1b[0m\n" +
		"╭────────────────────────────────────────────────╮\n" +
		"│ Bash command                                   │\n" +
		"│   go test ./...                                │\n" +
		"│ Do you want to proceed?                        │\n" +
		"│ \x1b[36m❯ 1. Yes\x1b[0m                                       │\n" +
		"│   2. No, and tell Claude what to do differently│\n" +
		"╰────────────────────────────────────────────────╯\n"
)

// paneKinds maps Window.Pane names to pane text.
var paneKinds = map[string]string{
	"idle":    IdlePane,
	"working": WorkingPane,
	"dialog":  DialogPane,
}

// Window is one fixture window. It is the JSON the control API accepts, and
// deliberately flat: the harness derives the fleet row, windowstate metadata
// and served window from it, so they can never disagree.
type Window struct {
	ID          string `json:"id"`
	Index       int    `json:"index"`
	Agent       string `json:"agent"`
	State       string `json:"state"` // idle | working | blocked
	Done        bool   `json:"done,omitempty"`
	Mode        string `json:"mode,omitempty"` // "" or bare = interactive; dispatch, adhoc = dispatched
	Branch      string `json:"branch,omitempty"`
	Parent      string `json:"parent,omitempty"` // dispatching window ID in the same session
	Activity    string `json:"activity,omitempty"`
	// Recap and RecapSource (summary, task, title, activity) fill the row's
	// recap line as fleet.TagLogs would.
	Recap       string `json:"recap,omitempty"`
	RecapSource string `json:"recap_source,omitempty"`
	ContextPct  int    `json:"context_pct,omitempty"`
	PendingTool string `json:"pending_tool,omitempty"`
	// Title is the task title (windowstate ticket_title), Issue the GitHub
	// issue number, Model the model name, CostUSD the session cost and PRURL
	// the window's PR: the header fields of the window view.
	Title   string  `json:"title,omitempty"`
	Issue   int     `json:"issue,omitempty"`
	Model   string  `json:"model,omitempty"`
	CostUSD float64 `json:"cost_usd,omitempty"`
	PRURL   string  `json:"pr_url,omitempty"`
	// Pane names the captured screen: idle, working or dialog. Empty picks
	// one from State. PaneText, when set, is used verbatim instead.
	Pane     string `json:"pane,omitempty"`
	PaneText string `json:"pane_text,omitempty"`
	// ScheduledBy marks a window launched by `agentctl cron` job of that
	// name; it fired 15 minutes before the fixture's now.
	ScheduledBy string `json:"scheduled_by,omitempty"`
}

var recapSources = map[string]fleet.RecapSource{
	"summary": fleet.RecapSummary, "task": fleet.RecapTask,
	"title": fleet.RecapTitle, "activity": fleet.RecapActivity,
}

// Group is one fixture session.
type Group struct {
	// Key is the group's saved-order key; empty means Dir, then Session.
	Key     string   `json:"key,omitempty"`
	Session string   `json:"session"`
	Project string   `json:"project"`
	Dir     string   `json:"dir"`
	Windows []Window `json:"windows"`
}

// paneText is the screen a window shows.
func (w Window) paneText() string {
	if w.PaneText != "" {
		return w.PaneText
	}
	if t, ok := paneKinds[w.Pane]; ok {
		return t
	}
	switch w.State {
	case "working":
		return WorkingPane
	case "blocked":
		return DialogPane
	}
	return IdlePane
}

// HubSession is the hub's session name (tmux.HubSessionName): its windows
// are never closable.
const HubSession = "agentctl-hub"

// DefaultFixture is the fleet the harness starts with and returns to on
// reset: every row state, a three-level parent/child tree, two projects and
// the hub.
func DefaultFixture() []Group {
	return []Group{
		{
			Session: "agentctl-shop", Project: "shop", Dir: "/fixture/shop",
			Windows: []Window{
				{ID: "@11", Index: 1, Agent: "orchestrator", State: "idle", Activity: "waiting for review"},
				{ID: "@12", Index: 2, Agent: "go-engineer", State: "idle", Done: true, Mode: "dispatch", Parent: "@11",
					Branch: "agent/go-engineer/issue-7", Activity: "opened PR #8", ContextPct: 42,
					Title: "Add a checkout retry", Issue: 7,
					Recap: "#7 Add a checkout retry", RecapSource: "task", Model: "claude-opus-4-1", CostUSD: 1.84,
					PRURL: "https://github.com/example/shop/pull/8"},
				{ID: "@13", Index: 3, Agent: "test-writer", State: "working", Mode: "dispatch", Parent: "@11", Activity: "go test ./...",
					Branch: "agent/test-writer/issue-9", ContextPct: 9, Title: "Cover the cart service", Issue: 9,
					Model: "claude-sonnet-4-5", CostUSD: 0.98},
				{ID: "@14", Index: 4, Agent: "reviewer", State: "blocked", Mode: "dispatch", Parent: "@12",
					PendingTool: "Bash", Activity: "go test ./...", Branch: "agent/reviewer/adhoc-3f2a", Model: "claude-haiku-4-5"},
				{ID: "@15", Index: 5, Agent: "docs", State: "idle", Activity: "updated README"},
			},
		},
		{
			Session: "agentctl-blog", Project: "blog", Dir: "/fixture/blog",
			Windows: []Window{
				{ID: "@31", Index: 1, Agent: "writer", State: "idle", Activity: "draft ready"},
			},
		},
		{
			Session: HubSession, Project: "hub",
			Windows: []Window{
				{ID: "@41", Index: 1, Agent: "hub", State: "idle", Activity: "hub session"},
			},
		},
	}
}

// Fixtures are the named fleets the control API can switch to.
var Fixtures = map[string]func() []Group{
	"default": DefaultFixture,
	"empty":   func() []Group { return nil },
}

// validate rejects a fixture the fakes could not serve consistently.
func validate(groups []Group) error {
	seen := map[string]bool{}
	for _, g := range groups {
		if g.Session == "" || strings.ContainsAny(g.Session, ":.") {
			return fmt.Errorf("invalid session %q", g.Session)
		}
		for _, w := range g.Windows {
			if !strings.HasPrefix(w.ID, "@") || len(w.ID) < 2 {
				return fmt.Errorf("invalid window id %q", w.ID)
			}
			if seen[w.ID] {
				// Pane IDs derive from window IDs, which tmux keeps
				// unique per server; so must a fixture.
				return fmt.Errorf("duplicate window id %q", w.ID)
			}
			seen[w.ID] = true
			switch w.State {
			case "idle", "working", "blocked":
			default:
				return fmt.Errorf("window %s: invalid state %q", w.ID, w.State)
			}
		}
	}
	return nil
}

// build converts fixture groups into what fleetwatch serves. root is the
// harness state dir; fake runtime dirs live under it and are never created.
func build(groups []Group, root string, now time.Time) []fleetwatch.Group {
	out := make([]fleetwatch.Group, 0, len(groups))
	for _, g := range groups {
		fg := fleetwatch.Group{
			Session: g.Session, Key: cmp.Or(g.Key, g.Dir, g.Session), Project: g.Project, Dir: g.Dir,
			RuntimeDir: filepath.Join(root, "fixture", g.Session, ".agentctl"),
			Meta:       map[string]fleet.Meta{},
		}
		for _, w := range g.Windows {
			row, meta := rowFor(g, w, now)
			fg.Rows = append(fg.Rows, row)
			fg.Meta[w.ID] = meta
			fg.Windows = append(fg.Windows, fleet.ToWindow(row, meta))
		}
		fg.Rollup = rollup(fg.Windows)
		out = append(out, fg)
	}
	return out
}

func rowFor(g Group, w Window, now time.Time) (fleet.Row, fleet.Meta) {
	row := fleet.Row{Session: g.Session, Project: g.Project, AgentState: state.AgentState{
		Name: w.Agent, WindowID: w.ID, WindowIndex: w.Index, State: w.State, Done: w.Done,
		ContextPct: w.ContextPct, LastText: w.Activity, MainPane: paneID(w.ID),
		ModelName: w.Model, Ticket: w.Issue,
	}}
	if w.Recap != "" {
		row.Recap = fleet.RecapLine{Text: w.Recap, Source: recapSources[w.RecapSource]}
	}
	mode := w.Mode
	if mode == "" {
		mode = "bare"
	}
	meta := fleet.Meta{
		WindowID: w.ID, Mode: mode, Branch: w.Branch, MainPane: paneID(w.ID),
		UpdatedAt: now.Add(-2 * time.Minute), StateSince: now.Add(-3 * time.Minute),
		TicketTitle: w.Title, CostUSD: w.CostUSD, PRURL: w.PRURL,
	}
	if w.Issue > 0 {
		meta.Ticket = fmt.Sprint(w.Issue)
	}
	if w.ScheduledBy != "" {
		row.ScheduledBy, row.ScheduledAt = w.ScheduledBy, now.Add(-15*time.Minute)
		meta.ScheduledBy, meta.ScheduledAt = row.ScheduledBy, row.ScheduledAt
	}
	if w.Parent != "" {
		meta.SpawnedBy, meta.SpawnedBySession = w.Parent, g.Session
	}
	if w.State == "blocked" {
		meta.PendingTool, meta.PendingToolDetail = w.PendingTool, w.Activity
		meta.BlockedSince = now.Add(-90 * time.Second)
	}
	return row, meta
}

// paneID is the fake main pane of a window: "@12" -> "%12".
func paneID(windowID string) string { return "%" + strings.TrimPrefix(windowID, "@") }

// rollup is the most urgent status in ws (fleetwatch.Status precedence).
func rollup(ws []fleet.Window) string {
	rank := map[string]int{"blocked": 0, "working": 1, "done": 2, "idle": 3}
	best := "idle"
	for _, w := range ws {
		if s := fleetwatch.Status(w); rank[s] < rank[best] {
			best = s
		}
	}
	return best
}
