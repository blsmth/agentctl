package servetest

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/blsmth/agentctl/internal/closedlog"
	"github.com/blsmth/agentctl/internal/fleet"
)

// ResumedWindow is the window ID every fake resume reports.
const ResumedWindow = "@902"

// ClosedSpec is one closed-ledger record a test seeds with PUT
// /__test/closed. The project dir is created under the harness dir unless
// Gone is set, so the record passes (or, with Gone, fails) the resume check.
type ClosedSpec struct {
	SessionID  string `json:"session_id"`
	Agent      string `json:"agent"`
	Project    string `json:"project"`
	Title      string `json:"title"`
	AgoMinutes int    `json:"ago_minutes"`
	PRNumber   int    `json:"pr_number,omitempty"`
	PRURL      string `json:"pr_url,omitempty"`
	Gone       bool   `json:"gone,omitempty"`
}

// closedWorld is the harness's closed ledger. Nothing here reads the real
// ledger or spawns `agentctl hub-resume`.
type closedWorld struct {
	root string
	w    *world

	mu   sync.Mutex
	recs []closedlog.Record // newest first
}

func (c *closedWorld) set(specs []ClosedSpec) error {
	now := time.Now()
	recs := make([]closedlog.Record, 0, len(specs))
	for i, sp := range specs {
		if sp.Project == "" {
			return fmt.Errorf("closed[%d]: project is required", i)
		}
		dir := filepath.Join(c.root, "closed-projects", sp.Project)
		if sp.Gone {
			dir = filepath.Join(c.root, "closed-projects-gone", sp.Project)
		} else if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		recs = append(recs, closedlog.Record{
			ClosedAt: now.Add(-time.Duration(sp.AgoMinutes) * time.Minute).UTC(), WindowID: fmt.Sprintf("@%d", 800+i),
			Session: "agentctl-" + sp.Project, ProjectDir: dir, WorkDir: dir, Agent: sp.Agent, Instance: sp.Agent,
			SessionID: sp.SessionID, ShortTitle: sp.Title, PRNumber: sp.PRNumber, PRURL: sp.PRURL,
		})
	}
	c.mu.Lock()
	c.recs = recs
	c.mu.Unlock()
	return nil
}

func (c *closedWorld) load(since time.Time) ([]closedlog.Record, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []closedlog.Record
	for _, r := range c.recs {
		if !r.ClosedAt.Before(since) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (c *closedWorld) index() *fleet.Index {
	c.mu.Lock()
	defer c.mu.Unlock()
	return fleet.NewIndex(nil, c.recs)
}

// resume records the resume instead of running hub-resume.
func (c *closedWorld) resume(r closedlog.Record) (string, error) {
	c.w.record(Action{Kind: "resume", Session: r.Session, Agent: r.Agent, Dir: r.ProjectDir, ID: r.SessionID})
	return ResumedWindow + "\n", nil
}

func (h *Harness) handleSetClosed(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Closed []ClosedSpec `json:"closed"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := h.closed.set(in.Closed); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
