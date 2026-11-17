package serve

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/herd"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/panestate"
	"github.com/blsmth/agentctl/internal/serve/fleetwatch"
	"github.com/blsmth/agentctl/internal/tmux"
)

// FleetSource produces the fleet (see fleetwatch.Source).
type FleetSource = fleetwatch.Source

// PaneSource captures a window's pane, with ANSI escapes.
type PaneSource interface {
	Capture(session, windowID string, lines int) (string, error)
}

// AgentLister lists the agents a dispatch can target.
type AgentLister interface {
	Agents(projectDirs []string) ([]AgentInfo, error)
}

// AgentInfo is one entry of GET /v1/agents.
type AgentInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Scope       string `json:"scope"` // "global" | "project"
}

type tmuxPanes struct{}

func (tmuxPanes) Capture(session, windowID string, lines int) (string, error) {
	return tmux.CapturePaneANSI(session, windowID, lines)
}

type discoveredAgents struct{}

func (discoveredAgents) Agents(projectDirs []string) ([]AgentInfo, error) {
	cfg := config.Load()
	found, err := agent.DiscoverAll(cfg, agent.ProjectDirs(cfg, projectDirs...))
	if err != nil {
		return nil, err
	}
	out := make([]AgentInfo, 0, len(found))
	for _, a := range found {
		scope := "global"
		switch a.Scope {
		case agent.ScopeTemplate:
			continue // not installed, cannot be dispatched
		case agent.ScopeLocal:
			scope = "project"
		}
		out = append(out, AgentInfo{Name: a.Name, Description: a.Description, Scope: scope})
	}
	return out, nil
}

// registerRead registers the read API and the event stream.
func (s *Server) registerRead(rt *Router) {
	rt.Feature("fleet")
	rt.Device("GET /v1/fleet", s.handleFleet)
	rt.Device("GET /v1/sessions/{session}/windows/{window}", s.handleWindow)
	rt.Device("GET /v1/sessions/{session}/windows/{window}/pane", s.handlePane)
	rt.Device("GET /v1/agents", s.handleAgents)
	rt.Device("GET /v1/projects", s.handleProjects)
	rt.Feature("events")
	rt.Device("GET /v1/events", s.handleEvents)
}

type fleetBody struct {
	Generated string             `json:"generated"`
	Groups    []fleetwatch.Group `json:"groups"`
}

func (s *Server) fleetBody(groups []fleetwatch.Group) fleetBody {
	if groups == nil {
		groups = []fleetwatch.Group{}
	}
	return fleetBody{Generated: s.now().UTC().Format(time.RFC3339), Groups: groups}
}

func (s *Server) loadFleet(w http.ResponseWriter) ([]fleetwatch.Group, bool) {
	groups, err := s.fleet.Fleet()
	if err != nil {
		logging.Warn("serve: loading fleet: %v", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "loading fleet")
		return nil, false
	}
	return groups, true
}

func (s *Server) handleFleet(w http.ResponseWriter, _ *http.Request) {
	groups, ok := s.loadFleet(w)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.fleetBody(groups))
}

// normalizeWindowID accepts "@523" and "523".
func normalizeWindowID(id string) string {
	if id != "" && !strings.HasPrefix(id, "@") {
		return "@" + id
	}
	return id
}

// windowDetail is the "detail" object of the window endpoint.
type windowDetail struct {
	Summary           string  `json:"summary,omitempty"`
	LastActivity      string  `json:"last_activity,omitempty"`
	PendingTool       string  `json:"pending_tool,omitempty"`
	PendingToolDetail string  `json:"pending_tool_detail,omitempty"`
	BlockedSince      string  `json:"blocked_since,omitempty"`
	Model             string  `json:"model,omitempty"`
	CostUSD           float64 `json:"cost_usd,omitempty"`
	Title             string  `json:"title,omitempty"`
}

type windowBody struct {
	herd.Window
	Detail windowDetail `json:"detail"`
}

func detailFor(row herd.Row, m herd.Meta) windowDetail {
	d := windowDetail{
		Summary:           row.LastText,
		PendingTool:       m.PendingTool,
		PendingToolDetail: m.PendingToolDetail,
		Model:             row.ModelName,
		CostUSD:           m.CostUSD,
		Title:             m.TicketTitle,
	}
	if !m.UpdatedAt.IsZero() {
		d.LastActivity = m.UpdatedAt.UTC().Format(time.RFC3339)
	}
	if row.State == "blocked" && !m.BlockedSince.IsZero() {
		d.BlockedSince = m.BlockedSince.UTC().Format(time.RFC3339)
	}
	return d
}

// lookup resolves the {session} and {window} path values.
func (s *Server) lookup(w http.ResponseWriter, r *http.Request) (fleetwatch.Group, int, bool) {
	groups, ok := s.loadFleet(w)
	if !ok {
		return fleetwatch.Group{}, 0, false
	}
	session, id := r.PathValue("session"), normalizeWindowID(r.PathValue("window"))
	g, i, found := fleetwatch.Find(groups, session, id)
	if !found {
		writeError(w, http.StatusNotFound, codeNotFound, fmt.Sprintf("no window %s in session %s", id, session))
		return fleetwatch.Group{}, 0, false
	}
	return g, i, true
}

func (s *Server) handleWindow(w http.ResponseWriter, r *http.Request) {
	g, i, ok := s.lookup(w, r)
	if !ok {
		return
	}
	win := g.Windows[i]
	body := windowBody{Window: win}
	if i < len(g.Rows) {
		body.Detail = detailFor(g.Rows[i], g.Meta[win.WindowID])
	}
	writeJSON(w, http.StatusOK, body)
}

const (
	defaultPaneLines = 200
	maxPaneLines     = 2000
)

type paneBody struct {
	Text    string            `json:"text"`
	Verdict string            `json:"verdict"`
	Dialog  *panestate.Dialog `json:"dialog"`
}

// apiVerdict maps a pane verdict to the API vocabulary.
func apiVerdict(v herd.PaneVerdict) string {
	switch v {
	case herd.PaneAwaitingInput:
		return "awaiting_input"
	case herd.PaneBusy:
		return "working"
	case herd.PaneIdle:
		return "idle"
	}
	return "unknown"
}

// buildPaneBody classifies captured pane text (with ANSI) and parses its
// dialog. Shared with PR4's answer guard, which must re-run exactly this.
func buildPaneBody(text string) paneBody {
	plain := panestate.StripANSI(text)
	verdict := herd.ClassifyPane(plain)
	body := paneBody{Text: text, Verdict: apiVerdict(verdict)}
	if verdict == herd.PaneAwaitingInput {
		if d, ok := panestate.ParseDialog(plain); ok {
			body.Dialog = &d
		}
	}
	return body
}

func (s *Server) handlePane(w http.ResponseWriter, r *http.Request) {
	g, i, ok := s.lookup(w, r)
	if !ok {
		return
	}
	lines := defaultPaneLines
	if v := r.URL.Query().Get("lines"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, codeBadRequest, "lines must be a positive integer")
			return
		}
		lines = min(n, maxPaneLines)
	}
	text, err := s.panes.Capture(g.Session, g.Windows[i].WindowID, lines)
	if err != nil {
		logging.Warn("serve: capturing pane: %v", err)
		writeError(w, http.StatusBadGateway, codePaneUnavailable, "pane could not be captured")
		return
	}
	writeJSON(w, http.StatusOK, buildPaneBody(text))
}

func (s *Server) handleAgents(w http.ResponseWriter, _ *http.Request) {
	groups, ok := s.loadFleet(w)
	if !ok {
		return
	}
	var dirs []string
	for _, g := range groups {
		if g.Dir != "" {
			dirs = append(dirs, g.Dir)
		}
	}
	agents, err := s.agents.Agents(dirs)
	if err != nil {
		logging.Warn("serve: discovering agents: %v", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "discovering agents")
		return
	}
	if agents == nil {
		agents = []AgentInfo{}
	}
	sort.SliceStable(agents, func(i, j int) bool { return agents[i].Name < agents[j].Name })
	writeJSON(w, http.StatusOK, struct {
		Agents []AgentInfo `json:"agents"`
	}{agents})
}

// projectInfo is one entry of GET /v1/projects.
type projectInfo struct {
	Name    string `json:"name"`
	Dir     string `json:"dir"`
	Session string `json:"session"`
}

func (s *Server) handleProjects(w http.ResponseWriter, _ *http.Request) {
	groups, ok := s.loadFleet(w)
	if !ok {
		return
	}
	projects := make([]projectInfo, 0, len(groups))
	for _, g := range groups {
		if g.Dir == "" {
			continue
		}
		projects = append(projects, projectInfo{Name: g.Project, Dir: g.Dir, Session: g.Session})
	}
	writeJSON(w, http.StatusOK, struct {
		Projects []projectInfo `json:"projects"`
	}{projects})
}

// handleEvents streams fleet changes as Server-Sent Events.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	// The server's WriteTimeout would cut the stream; lift it for this request.
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		logging.Debug("serve: lifting write deadline: %v", err)
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	snapshot, sub := s.bus.Subscribe()
	defer sub.Close()

	send := func(event string, v any) bool {
		data, err := json.Marshal(v)
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !send("fleet", s.fleetBody(snapshot)) {
		return
	}

	ping := time.NewTicker(s.pingEvery)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.done:
			return
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil || rc.Flush() != nil {
				return
			}
		case ev := <-sub.C:
			ok := true
			switch ev.Kind {
			case fleetwatch.KindFleet:
				ok = send("fleet", s.fleetBody(ev.Fleet))
			case fleetwatch.KindWindow:
				ok = send("window", ev.Window)
			case fleetwatch.KindGone:
				ok = send("gone", struct {
					Session  string `json:"session"`
					WindowID string `json:"window_id"`
				}{ev.Session, ev.WindowID})
			}
			if !ok {
				return
			}
		}
	}
}
