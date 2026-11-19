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

	// Detached: dispatched with no recorded dispatcher (phone, cron); shown as a
	// plain session.
	Detached bool `json:"detached,omitempty"`

	// ScheduledBy names the `agentctl cron` job that launched the window and
	// ScheduledAt (RFC 3339) when it fired; both omitted for other windows.
	ScheduledBy string `json:"scheduled_by,omitempty"`
	ScheduledAt string `json:"scheduled_at,omitempty"`

	// Title is a short generated title for Recap (2-5 words), for list rows;
	// omitted until one is cached. Recap stays the full text.
	Title string `json:"title,omitempty"`
	// Kind is the work-kind tag of Title ("go", "review", ...); omitted when
	// there is none.
	Kind string `json:"kind,omitempty"`

	// Model is the model the window is running (the log's latest model id), as
	// in the window detail; omitted until known.
	Model string `json:"model,omitempty"`
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
		Detached:     m.Detached(),
		Model:        r.ModelName,
	}
	if r.Recap.Text != "" {
		w.Recap, w.RecapSource = r.Recap.Text, r.Recap.Source.String()
		w.Title = r.Recap.Title
		w.Kind = r.Recap.Kind
	}
	if m.ScheduledBy != "" {
		w.ScheduledBy = m.ScheduledBy
		if !m.ScheduledAt.IsZero() {
			w.ScheduledAt = m.ScheduledAt.UTC().Format(time.RFC3339)
		}
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
