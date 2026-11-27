package serve

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/dispatch"
	"github.com/blsmth/agentctl/internal/fleet"
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

// PaneSizer is optionally implemented by a PaneSource to report the window's
// size in cells, which the phone needs to render the pane exactly.
type PaneSizer interface {
	Size(windowID string) (cols, rows int, err error)
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
	// Project and ProjectDir name the project whose .claude/agents defines a
	// project-scoped agent (base name and absolute dir); empty for globals.
	Project    string `json:"project,omitempty"`
	ProjectDir string `json:"project_dir,omitempty"`
	// Orchestrator is true when the resolved role is orchestrator.
	Orchestrator bool `json:"orchestrator,omitempty"`
}

type tmuxPanes struct{}

func (tmuxPanes) Capture(session, windowID string, lines int) (string, error) {
	return tmux.CapturePaneANSI(session, windowID, lines)
}

func (tmuxPanes) Size(windowID string) (int, int, error) {
	return tmux.WindowSize(windowID)
}

type discoveredAgents struct{}

func (discoveredAgents) Agents(projectDirs []string) ([]AgentInfo, error) {
	globalsMu.RLock() // config.Load reads state withSession rebinds
	defer globalsMu.RUnlock()
	cfg := config.Load()
	found, err := agent.DiscoverAll(cfg, agent.ProjectDirs(cfg, projectDirs...))
	if err != nil {
		return nil, err
	}
	out := make([]AgentInfo, 0, len(found))
	for _, a := range found {
		scope, project, projectDir := "global", "", ""
		switch a.Scope {
		case agent.ScopeTemplate:
			continue // not installed, cannot be dispatched
		case agent.ScopeLocal:
			scope = "project"
			if a.ProjectDir != "" {
				project, projectDir = agent.ProjectLabel(a.ProjectDir), a.ProjectDir
			}
		}
		out = append(out, AgentInfo{Name: a.Name, Description: a.Description, Scope: scope, Project: project, ProjectDir: projectDir, Orchestrator: a.Role == agent.RoleOrchestrator})
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
	rt.Feature("launch-options")
	rt.Device("GET /v1/launch-options", s.handleLaunchOptions)
	rt.Device("GET /v1/templates", s.handleTemplates)
	rt.Feature("host")
	rt.Device("GET /v1/host", s.handleHost)
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
	for i := range groups {
		if groups[i].Shells == nil {
			groups = slices.Clone(groups)
			for j := i; j < len(groups); j++ {
				if groups[j].Shells == nil {
					groups[j].Shells = []fleetwatch.Shell{}
				}
			}
			break
		}
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
	LastActivityText  string  `json:"last_activity_text,omitempty"`
	LastActivity      string  `json:"last_activity,omitempty"`
	PendingTool       string  `json:"pending_tool,omitempty"`
	PendingToolDetail string  `json:"pending_tool_detail,omitempty"`
	BlockedSince      string  `json:"blocked_since,omitempty"`
	Model             string  `json:"model,omitempty"`
	CostUSD           float64 `json:"cost_usd,omitempty"`
	Title             string  `json:"title,omitempty"`
	// Prompt is the full initial prompt (detail route only, never the list).
	Prompt string `json:"prompt,omitempty"`
	// Task is the one-line task recorded at dispatch (ad-hoc and review windows).
	Task string `json:"task,omitempty"`
}

type windowBody struct {
	fleet.Window
	Detail windowDetail `json:"detail"`
}

func detailFor(row fleet.Row, m fleet.Meta) windowDetail {
	d := windowDetail{
		Summary:           row.LastText,
		LastActivityText:  row.LastText,
		PendingTool:       m.PendingTool,
		PendingToolDetail: m.PendingToolDetail,
		Model:             row.ModelName,
		CostUSD:           m.CostUSD,
		Title:             m.TicketTitle,
		Prompt:            m.Prompt,
		Task:              m.Task,
	}
	if row.Recap.Text != "" {
		d.Summary = row.Recap.Text
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
		m := g.Meta[win.WindowID]
		m.Prompt = s.promptFor(g.RuntimeDir, m)
		body.Detail = detailFor(g.Rows[i], m)
	}
	writeJSON(w, http.StatusOK, body)
}

const (
	defaultPaneLines = 200
	maxPaneLines     = 2000
	// historyPaneLines is what lines=all captures: the explicit "load earlier"
	// request. 20000 lines of ANSI text is a few MB at most; the live poll
	// never asks for it.
	historyPaneLines = 20000
)

type paneBody struct {
	Text    string            `json:"text"`
	Verdict string            `json:"verdict"`
	Dialog  *panestate.Dialog `json:"dialog"`
	// Cols and Rows are the window size (0 when unknown). Additive.
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

// apiVerdict maps a pane verdict to the API vocabulary.
func apiVerdict(v fleet.PaneVerdict) string {
	switch v {
	case fleet.PaneAwaitingInput:
		return "awaiting_input"
	case fleet.PaneBusy:
		return "working"
	case fleet.PaneIdle:
		return "idle"
	}
	return "unknown"
}

// buildPaneBody classifies captured pane text (with ANSI) and parses its
// dialog. Shared with PR4's answer guard, which must re-run exactly this.
func buildPaneBody(text string) paneBody { return buildPaneBodyFor("", text) }

// buildPaneBodyFor is buildPaneBody with the window's harness reader: a
// harness agentctl cannot read (pi) yields verdict "unknown" and no dialog.
func buildPaneBodyFor(harnessName, text string) paneBody {
	plain := panestate.StripANSI(text)
	verdict := fleet.ClassifyPaneFor(harnessName, plain)
	body := paneBody{Text: text, Verdict: apiVerdict(verdict)}
	if verdict == fleet.PaneAwaitingInput {
		// The trust dialog is answered by its own keys (trust, exit). A
		// numbered dialog whose options are the trust rows (the older layout
		// left on screen after Claude Code exited) is never offered: number
		// keys would go to a shell. A trust dialog quoted in scrollback does
		// not hide a live dialog below it.
		if d, ok := panestate.ParseTrustDialog(plain); ok {
			body.Dialog = &d
		} else if d, ok := panestate.ParseDialog(plain); ok && !isTrustOptions(d) {
			body.Dialog = &d
		}
	}
	return body
}

// isTrustOptions reports whether a numbered dialog's options are the
// folder-trust dialog's rows.
func isTrustOptions(d panestate.Dialog) bool {
	for _, o := range d.Options {
		if o.Label == "Yes, I trust this folder" || o.Label == "No, exit" {
			return true
		}
	}
	return false
}

func (s *Server) handlePane(w http.ResponseWriter, r *http.Request) {
	g, i, ok := s.lookup(w, r)
	if !ok {
		return
	}
	lines := defaultPaneLines
	if v := r.URL.Query().Get("lines"); v == "all" {
		lines = historyPaneLines
	} else if v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, codeBadRequest, "lines must be a positive integer")
			return
		}
		lines = min(n, maxPaneLines)
	}
	s.touchPhoneView(r, g.Windows[i].WindowID)
	text, err := s.panes.Capture(g.Session, g.Windows[i].WindowID, lines)
	if err != nil {
		logging.Warn("serve: capturing pane: %v", err)
		writeError(w, http.StatusBadGateway, codePaneUnavailable, "pane could not be captured")
		return
	}
	body := buildPaneBodyFor(harnessOf(g, i), text)
	if sz, ok := s.panes.(PaneSizer); ok {
		if c, r, err := sz.Size(g.Windows[i].WindowID); err == nil {
			body.Cols, body.Rows = c, r
		} else {
			logging.Debug("serve: window size: %v", err)
		}
	}
	writeJSON(w, http.StatusOK, body)
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
	// DefaultAgent is the agent a launch in this project would default to
	// (project default_agent, else the global one); "" when none is usable.
	DefaultAgent string `json:"default_agent"`
}

// projectDefaultAgent resolves the default agent of a project the way a
// launch there would (dispatch.ResolveDefaultAgent). It binds the project's
// session via withSession, since config.Load reads state that rebinds. Any
// failure yields "": a bad default must never fail the request.
func projectDefaultAgent(session, runtimeDir, dir string) string {
	var name string
	err := withSession(session, runtimeDir, func() error {
		name = dispatch.ResolveDefaultAgent(config.Load(), dir, dispatch.DefaultAgentDiscovery{}, func(msg string) {
			logging.Debug("serve: projects: %s", msg)
		})
		return nil
	})
	if err != nil {
		logging.Debug("serve: project default agent for %s: %v", dir, err)
		return ""
	}
	return name
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
		projects = append(projects, projectInfo{
			Name: g.Project, Dir: g.Dir, Session: g.Session,
			DefaultAgent: projectDefaultAgent(g.Session, g.RuntimeDir, g.Dir),
		})
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
	approvalC, pending := s.approvals.subscribe()
	defer s.approvals.unsubscribe(approvalC)

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
	for _, v := range approvalsOf(pending, s.now()) {
		if !send("approval", v) {
			return
		}
	}

	ping := time.NewTicker(s.pingEvery)
	defer ping.Stop()
	phoneOff := s.phone.turnedOff()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.done:
			return
		case <-phoneOff:
			// The app's reconnect then sees 503 phone_off.
			return
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil || rc.Flush() != nil {
				return
			}
		case ev := <-approvalC:
			if !sendApprovalEvent(send, ev) {
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
			case fleetwatch.KindArtifact:
				ok = send("artifact", struct {
					ID     string `json:"id"`
					Action string `json:"action"`
				}{ev.Artifact.ID, ev.Artifact.Action})
			}
			if !ok {
				return
			}
		}
	}
}

// harnessOf is window i's recorded harness name ("" = claude).
func harnessOf(g fleetwatch.Group, i int) string {
	if i < len(g.Rows) {
		return g.Rows[i].Harness
	}
	return ""
}
