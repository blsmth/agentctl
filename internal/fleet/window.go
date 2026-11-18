package fleet

import (
	"time"
)

// Window is the machine-readable shape of one agent window. It is the single
// conversion from Row (+ Meta) used by `agentctl status --json` and by
// `agentctl serve`, so the two cannot drift. Existing field names are a
// compatibility contract; add new fields at the end, with omitempty.
type Window struct {
	Session      string `json:"session"`
	Project      string `json:"project"`
	WindowID     string `json:"window_id"`
	WindowIndex  int    `json:"window_index"`
	Agent        string `json:"agent"`
	State        string `json:"state"`
	Done         bool   `json:"done"`
	Mode         string `json:"mode,omitempty"`
	Backend      string `json:"backend,omitempty"`
	ContextPct   int    `json:"context_pct,omitempty"`
	Ticket       string `json:"ticket,omitempty"`
	Branch       string `json:"branch,omitempty"`
	WorktreePath string `json:"worktree_path,omitempty"`
	Parent       string `json:"parent,omitempty"`
	Activity     string `json:"activity,omitempty"`
	MailPending  int    `json:"mail_pending,omitempty"`

	// Additive fields (serve). PRURL is the PR recorded by dispatch completion
	// (windowstate), else the newest pr-link record in the session log (see
	// TagLogs); PRNumber is only known from the former. PendingTool is only set while the
	// window is blocked, which is when it names the tool the dialog asks about.
	PRURL        string `json:"pr_url,omitempty"`
	Issue        int    `json:"issue,omitempty"`
	BlockedSince string `json:"blocked_since,omitempty"`
	StateSince   string `json:"state_since,omitempty"`
	PendingTool  string `json:"pending_tool,omitempty"`
	PRNumber     int    `json:"pr_number,omitempty"`

	// Recap is the row's one-line "what is this agent working on" (see Recap)
	// and RecapSource names where it came from: summary, task, title or activity.
	Recap       string `json:"recap,omitempty"`
	RecapSource string `json:"recap_source,omitempty"`
}

// ToWindow converts a row and its windowstate metadata to a Window.
func ToWindow(r Row, m Meta) Window {
	w := Window{
		Session:      r.Session,
		Project:      r.Project,
		WindowID:     r.WindowID,
		WindowIndex:  r.WindowIndex,
		Agent:        r.Name,
		State:        r.State,
		Done:         r.Done,
		Mode:         m.Mode,
		Backend:      m.Backend,
		ContextPct:   r.ContextPct,
		Ticket:       m.Ticket,
		Branch:       m.Branch,
		WorktreePath: r.WorktreePath,
		Parent:       m.Parent(),
		Activity:     ActivityLine(r),
		MailPending:  r.MailboxPending,
		Issue:        r.Ticket,
		PRURL:        r.PRURL,
		PRNumber:     m.PRNumber,
	}
	if r.Recap.Text != "" {
		w.Recap, w.RecapSource = r.Recap.Text, r.Recap.Source.String()
	}
	if m.PRURL != "" {
		w.PRURL = m.PRURL
	}
	if !m.StateSince.IsZero() {
		w.StateSince = m.StateSince.UTC().Format(time.RFC3339)
	}
	if r.State == "blocked" {
		w.PendingTool = m.PendingTool
		if !m.BlockedSince.IsZero() {
			w.BlockedSince = m.BlockedSince.UTC().Format(time.RFC3339)
			w.StateSince = w.BlockedSince
		}
	}
	return w
}

// Windows converts every row of a group.
func (g Group) Windows(meta map[string]Meta) []Window {
	out := make([]Window, 0, len(g.Rows))
	for _, r := range g.Rows {
		out = append(out, ToWindow(r, meta[r.WindowID]))
	}
	return out
}
