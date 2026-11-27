package fleet

import (
	"time"

	"github.com/blsmth/agentctl/internal/windowstate"
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
	// TagLogs); PRNumber comes from the same source as PRURL, never mixed. PendingTool is only set while the
	// window is blocked, which is when it names the tool the dialog asks about.
	PRURL        string `json:"pr_url,omitempty"`
	Issue        int    `json:"issue,omitempty"`
	BlockedSince string `json:"blocked_since,omitempty"`
	StateSince   string `json:"state_since,omitempty"`
	PendingTool  string `json:"pending_tool,omitempty"`
	// PendingToolDetail is what the pending tool is asked to do (the Bash
	// command, the file path), set with PendingTool; push bodies use it.
	PendingToolDetail string `json:"pending_tool_detail,omitempty"`
	PRNumber          int    `json:"pr_number,omitempty"`

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
	// Role is the role label of a sub-agent window ("build", "fix", "review", ...,
	// shorttitle.Role); omitted for windows no agent dispatched.
	Role string `json:"role,omitempty"`

	// Model is the model the window is running (the log's latest model id), as
	// in the window detail; omitted until known.
	Model string `json:"model,omitempty"`

	// TurnDone marks a bare window that finished a turn and is unseen: the
	// display-only ✓ (never Done: sweep and completion ignore it). Omitted
	// when false.
	TurnDone bool `json:"turn_done,omitempty"`

	// Attention is why the window needs the human (the ! glyph): "question",
	// "errors" or "message"; AttentionDetail the one-line explanation. Both
	// omitted unless the row shows ! (it is not working or blocked).
	Attention       string `json:"attention,omitempty"`
	AttentionDetail string `json:"attention_detail,omitempty"`

	// Label is the human-facing name (fleet.Label; serve makes it unique within
	// a group); window_id stays the machine key. Set by `status --json` (and
	// serve). Row handles come from the fleet's own index, the handle route
	// from LoadHandleIndex, so a short handle can rarely be ambiguous there.
	Label string `json:"label,omitempty"`

	// Harness is the agent program the window runs ("pi"); omitted for
	// Claude Code. The phone offers slash commands only on Claude windows.
	Harness string `json:"harness,omitempty"`

	// Handle is the session's short ID (shortest unique prefix of its session
	// ID, at least 4 characters). Set by `status --json` (and serve); omitted
	// until the window has a session ID.
	Handle string `json:"handle,omitempty"`
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
		PRNumber:     r.PRNumber,
		Detached:     m.Detached(),
		Model:        r.ModelName,
		TurnDone:     r.TurnDone,
		Label:        RowDisplayName(r),
		Harness:      r.Harness,
	}
	if NeedsAttention(r.AgentState) {
		w.Attention, w.AttentionDetail = r.Attention.Reason, r.Attention.Detail
	}
	if r.Recap.Text != "" {
		w.Recap, w.RecapSource = r.Recap.Text, r.Recap.Source.String()
		w.Title = r.Recap.Title
		w.Role = r.Recap.Role
	}
	if m.ScheduledBy != "" {
		w.ScheduledBy = m.ScheduledBy
		if !m.ScheduledAt.IsZero() {
			w.ScheduledAt = m.ScheduledAt.UTC().Format(time.RFC3339)
		}
	}
	if m.hasPR() {
		// Windowstate's pair wins whole; rows not run through TagMeta
		// still get it.
		w.PRNumber, w.PRURL = m.PRNumber, m.PRURL
	}
	if !m.StateSince.IsZero() {
		w.StateSince = m.StateSince.UTC().Format(time.RFC3339)
	}
	if r.State == "blocked" {
		w.PendingTool = m.PendingTool
		w.PendingToolDetail = m.PendingToolDetail
		if m.PendingTool == windowstate.PendingTrust {
			w.Activity = TrustActivity(m.PendingToolDetail)
		}
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
