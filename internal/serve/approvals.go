package serve

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/remoteapproval"
)

// Remote approvals ("away mode"): an approval hook opens an approval over the
// control socket and waits on that connection; the phone answers through
// POST /v1/approvals/{id}. Pending approvals live only in memory, each bound
// to its connection: when the connection drops, the approval is gone. No
// control-socket op can approve anything; the only path to a decision is a
// paired device with allow_write. See docs/design/mobile-web-app.md, "Remote
// approvals".

const (
	// maxPendingApprovals bounds memory held by approval connections.
	maxPendingApprovals = 32
	// maxApprovalWait clamps a request's deadline.
	maxApprovalWait = 15 * time.Minute
	// approvalGrace is how long past its deadline serve keeps an approval
	// whose hook has not hung up, before giving up on it.
	approvalGrace = 10 * time.Second
	// resolvedTTL is how long a resolved approval's ID is remembered, so a
	// late answer gets 409 instead of 404.
	resolvedTTL = 15 * time.Minute
	// maxHistory and historyTTL bound the resolved-approval history that lets
	// a stale push link say what happened.
	maxHistory = 50
	historyTTL = 30 * time.Minute
	// decisionWriteTimeout bounds writing the decision to the hook.
	decisionWriteTimeout = 2 * time.Second

	codeApprovalChanged = "approval_changed"
	codeApprovalGone    = "approval_gone"

	// Outcomes recorded in audit lines and approval_resolved events.
	outcomeAnswered = "answered"
	outcomeExpired  = "expired"
	outcomeGone     = "gone"
	outcomeStopping = "server_stopping"
)

// approvalView is one pending approval as GET /v1/approvals and the SSE
// "approval" event show it: everything the human needs to decide.
type approvalView struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	AskingAgent string `json:"asking_agent"`
	Session     string `json:"session"`
	Project     string `json:"project"`
	WindowID    string `json:"window_id"`
	TargetAgent string `json:"target_agent"`
	Issue       int    `json:"issue"`
	Prompt      string `json:"prompt"`
	Command     string `json:"command"`
	// Details is the dispatch's resolved where/how (empty for other kinds);
	// covered by PayloadSHA256 like every other shown field.
	Details          remoteapproval.Details `json:"details"`
	Opened           string                 `json:"opened"`
	Deadline         string                 `json:"deadline"`
	SecondsRemaining int                    `json:"seconds_remaining"`
	PayloadSHA256    string                 `json:"payload_sha256"`
}

// Resolved outcomes shown to the phone.
const (
	resolvedApproved  = "approved"
	resolvedDenied    = "denied"
	resolvedAnswered  = "answered" // answered where serve could not see the decision (laptop)
	resolvedTimedOut  = "timed_out"
	resolvedCancelled = "cancelled"
	sourceLaptop      = "laptop"
)

// resolvedApproval is one history entry. It deliberately holds no prompt or
// command text: the summary fields are enough to say what happened.
type resolvedApproval struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	Kind        string `json:"kind"`
	AskingAgent string `json:"asking_agent"`
	TargetAgent string `json:"target_agent"`
	// TargetProject is the project a dispatch went to (Details.Project).
	TargetProject string `json:"target_project,omitempty"`
	Session       string `json:"session"`
	Project       string `json:"project"`
	WindowID      string `json:"window_id"`
	Opened        string `json:"opened"`
	ResolvedAt    string `json:"resolved_at"`
	Outcome       string `json:"outcome"`
	Source        string `json:"source,omitempty"`

	at time.Time
}

// newResolved builds the history entry for p. outcome is the connection
// outcome (answered, expired, a cancel reason, ...); decision and device
// describe a phone answer.
func newResolved(p *pendingApproval, outcome, decision, device string, now time.Time) resolvedApproval {
	r := resolvedApproval{
		ID: p.req.ID, Status: "resolved", Kind: p.req.Kind, AskingAgent: p.req.AskingAgent,
		TargetAgent: p.req.TargetAgent, TargetProject: p.req.Details.Project, Session: p.req.Session, Project: p.project, WindowID: p.req.WindowID,
		Opened: p.opened.UTC().Format(time.RFC3339), ResolvedAt: now.UTC().Format(time.RFC3339), at: now,
	}
	switch outcome {
	case outcomeAnswered:
		r.Outcome, r.Source = resolvedDenied, device
		if decision == remoteapproval.Allow {
			r.Outcome = resolvedApproved
		}
	case remoteapproval.ReasonLaptop:
		r.Outcome, r.Source = resolvedAnswered, sourceLaptop
	case outcomeExpired, remoteapproval.ReasonTimeout:
		r.Outcome = resolvedTimedOut
	default:
		r.Outcome = resolvedCancelled
	}
	return r
}

// approvalResolved is the SSE "approval_resolved" event.
type approvalResolved struct {
	ID       string `json:"id"`
	Session  string `json:"session"`
	WindowID string `json:"window_id"`
	Outcome  string `json:"outcome"`
	Decision string `json:"decision,omitempty"`
}

// approvalEvent is one message to SSE subscribers.
type approvalEvent struct {
	opened   *approvalView
	resolved *approvalResolved
}

// approvalAnswer is a phone's decision handed to the approval's connection.
type approvalAnswer struct {
	decision string
	device   string
	// done receives the result of writing the decision to the hook.
	done chan error
}

type pendingApproval struct {
	req      remoteapproval.Request
	hash     string
	project  string
	opened   time.Time
	deadline time.Time
	// claimed is set (under the store lock) by the first valid answer.
	claimed bool
	answer  chan approvalAnswer
	// closed is closed when the approval's connection is done.
	closed chan struct{}
}

func (p *pendingApproval) view(now time.Time) approvalView {
	secs := int(p.deadline.Sub(now).Seconds())
	if secs < 0 {
		secs = 0
	}
	return approvalView{
		ID: p.req.ID, Kind: p.req.Kind, AskingAgent: p.req.AskingAgent, Session: p.req.Session,
		Project: p.project, WindowID: p.req.WindowID, TargetAgent: p.req.TargetAgent, Issue: p.req.Issue,
		Prompt: p.req.Prompt, Command: p.req.Command, Details: p.req.Details,
		Opened:           p.opened.UTC().Format(time.RFC3339),
		Deadline:         p.deadline.UTC().Format(time.RFC3339),
		SecondsRemaining: secs,
		PayloadSHA256:    p.hash,
	}
}

// approvalStore holds pending approvals and the SSE subscribers.
type approvalStore struct {
	mu       sync.Mutex
	pending  map[string]*pendingApproval
	resolved map[string]time.Time
	history  []resolvedApproval // oldest first
	subs     map[chan approvalEvent]struct{}
}

func newApprovalStore() *approvalStore {
	return &approvalStore{
		pending:  make(map[string]*pendingApproval),
		resolved: make(map[string]time.Time),
		subs:     make(map[chan approvalEvent]struct{}),
	}
}

var (
	errApprovalDuplicate = errors.New("duplicate approval id")
	errApprovalTooMany   = errors.New("too many pending approvals")
)

func (st *approvalStore) add(p *pendingApproval, now time.Time) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	for id, at := range st.resolved {
		if now.Sub(at) > resolvedTTL {
			delete(st.resolved, id)
		}
	}
	if _, dup := st.pending[p.req.ID]; dup {
		return errApprovalDuplicate
	}
	if _, dup := st.resolved[p.req.ID]; dup {
		return errApprovalDuplicate
	}
	if len(st.pending) >= maxPendingApprovals {
		return errApprovalTooMany
	}
	st.pending[p.req.ID] = p
	return nil
}

func (st *approvalStore) remove(r resolvedApproval) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.pending, r.ID)
	st.resolved[r.ID] = r.at
	st.history = append(st.history, r)
	st.pruneHistory(r.at)
}

// pruneHistory drops expired entries and keeps the newest maxHistory.
// Callers hold st.mu.
func (st *approvalStore) pruneHistory(now time.Time) {
	st.history = slices.DeleteFunc(st.history, func(r resolvedApproval) bool {
		return now.Sub(r.at) > historyTTL
	})
	if n := len(st.history) - maxHistory; n > 0 {
		st.history = slices.Delete(st.history, 0, n)
	}
}

// recent returns unexpired resolved approvals, newest first.
func (st *approvalStore) recent(now time.Time) []resolvedApproval {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.pruneHistory(now)
	out := make([]resolvedApproval, 0, len(st.history))
	for i := len(st.history) - 1; i >= 0; i-- {
		out = append(out, st.history[i])
	}
	return out
}

// lookup returns the pending approval or the resolved history entry for id.
func (st *approvalStore) lookup(id string, now time.Time) (*pendingApproval, *resolvedApproval) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if p, ok := st.pending[id]; ok {
		return p, nil
	}
	st.pruneHistory(now)
	for i := range st.history {
		if st.history[i].ID == id {
			r := st.history[i]
			return nil, &r
		}
	}
	return nil, nil
}

// list returns pending approvals, oldest first.
func (st *approvalStore) list() []*pendingApproval {
	st.mu.Lock()
	defer st.mu.Unlock()
	out := make([]*pendingApproval, 0, len(st.pending))
	for _, p := range st.pending {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].opened.Equal(out[j].opened) {
			return out[i].opened.Before(out[j].opened)
		}
		return out[i].req.ID < out[j].req.ID
	})
	return out
}

// claimResult is why claim refused an answer.
type claimResult int

const (
	claimOK claimResult = iota
	claimUnknown
	claimNotPending
	claimHashMismatch
)

// claim reserves approval id for one answer whose hash must equal what the
// phone was shown. Only the first valid claim succeeds.
func (st *approvalStore) claim(id, hash string) (*pendingApproval, claimResult) {
	st.mu.Lock()
	defer st.mu.Unlock()
	p, ok := st.pending[id]
	if !ok {
		if _, was := st.resolved[id]; was {
			return nil, claimNotPending
		}
		return nil, claimUnknown
	}
	if p.claimed {
		return p, claimNotPending
	}
	if hash != p.hash {
		return p, claimHashMismatch
	}
	p.claimed = true
	return p, claimOK
}

func (st *approvalStore) subscribe() (chan approvalEvent, []*pendingApproval) {
	c := make(chan approvalEvent, 16)
	st.mu.Lock()
	st.subs[c] = struct{}{}
	st.mu.Unlock()
	return c, st.list()
}

func (st *approvalStore) unsubscribe(c chan approvalEvent) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.subs, c)
}

// publish never blocks: a subscriber that fell behind misses the event and
// catches up from GET /v1/approvals (the web app refetches on reconnect).
func (st *approvalStore) publish(ev approvalEvent) {
	st.mu.Lock()
	defer st.mu.Unlock()
	for c := range st.subs {
		select {
		case c <- ev:
		default:
		}
	}
}

// Ping reports who is listening on the control socket, for the hook's guard.
func (s *Server) Ping() remoteapproval.PingInfo {
	return remoteapproval.PingInfo{PID: os.Getpid(), Build: s.opts.Build}
}

// ServeApproval runs one approval connection until it is answered, cancelled,
// dropped, expired, or the server stops. It owns conn.
func (s *Server) ServeApproval(conn net.Conn, br *bufio.Reader, req remoteapproval.Request) {
	s.ctlMu.Lock()
	if s.ctlStopped {
		s.ctlMu.Unlock()
		_ = conn.Close()
		return
	}
	s.ctlWG.Add(1)
	s.ctlMu.Unlock()
	defer s.ctlWG.Done() // runs last: Close returns only after the audit write
	defer func() { _ = conn.Close() }()
	refuse := func(msg string) {
		_ = conn.SetWriteDeadline(time.Now().Add(decisionWriteTimeout))
		_ = json.NewEncoder(conn).Encode(remoteapproval.Decision{Error: msg})
	}
	if err := req.Validate(); err != nil {
		refuse(err.Error())
		return
	}
	now := s.now()
	deadline := req.Deadline
	if !deadline.After(now) {
		refuse("deadline already passed")
		return
	}
	if max := now.Add(maxApprovalWait); deadline.After(max) {
		deadline = max
	}
	p := &pendingApproval{
		req:      req,
		hash:     remoteapproval.PayloadHash(req),
		project:  s.projectOf(req.Session),
		opened:   now,
		deadline: deadline,
		answer:   make(chan approvalAnswer, 1),
		closed:   make(chan struct{}),
	}
	if err := s.approvals.add(p, now); err != nil {
		refuse(err.Error())
		return
	}
	defer close(p.closed)
	_ = conn.SetDeadline(time.Time{})

	s.auditApproval(p, "approval_requested", "pending", "", "")
	view := p.view(now)
	s.approvals.publish(approvalEvent{opened: &view})
	s.pushApproval(p)
	logging.Log("serve: approval %s opened (%s by %s in %s %s)", req.ID, req.Kind, req.AskingAgent, req.Session, req.WindowID)

	// The hook writes at most a cancel line; EOF means it gave up or exited.
	ended := make(chan string, 1)
	go func() { ended <- readApprovalEnd(br, req.ID) }()

	timer := time.NewTimer(deadline.Sub(now) + approvalGrace)
	defer timer.Stop()

	outcome, decision, device := "", "", ""
	select {
	case a := <-p.answer:
		_ = conn.SetWriteDeadline(time.Now().Add(decisionWriteTimeout))
		err := json.NewEncoder(conn).Encode(remoteapproval.Decision{Decision: a.decision, Device: a.device, PayloadSHA256: p.hash})
		a.done <- err
		if err != nil {
			outcome = outcomeGone
		} else {
			outcome, decision, device = outcomeAnswered, a.decision, a.device
		}
	case reason := <-ended:
		outcome = reason
	case <-timer.C:
		outcome = outcomeExpired
	case <-s.done:
		outcome = outcomeStopping
	}
	resolvedAt := s.now()
	s.approvals.remove(newResolved(p, outcome, decision, device, resolvedAt))
	s.approvals.publish(approvalEvent{resolved: &approvalResolved{
		ID: req.ID, Session: req.Session, WindowID: req.WindowID, Outcome: outcome, Decision: decision,
	}})
	if outcome != outcomeAnswered {
		// Answered approvals were audited by the POST handler.
		s.auditApproval(p, "approval_resolved", outcome, "", "")
	}
	logging.Log("serve: approval %s resolved: %s %s %s", req.ID, outcome, decision, device)
}

// readApprovalEnd reads the approval connection until the hook cancels or
// hangs up, and returns the outcome to record.
func readApprovalEnd(br *bufio.Reader, id string) string {
	for {
		line, err := remoteapproval.ReadLine(br)
		if err != nil {
			return outcomeGone
		}
		var c remoteapproval.Cancel
		if json.Unmarshal(line, &c) != nil || c.Op != remoteapproval.OpCancel || c.ID != id {
			continue
		}
		switch c.Reason {
		case remoteapproval.ReasonLaptop, remoteapproval.ReasonTimeout, remoteapproval.ReasonCanceled:
			return c.Reason
		}
		return remoteapproval.ReasonCanceled
	}
}

// projectOf names the project of a session from the bus snapshot.
func (s *Server) projectOf(session string) string {
	for _, g := range s.bus.Snapshot() {
		if g.Session == session && g.Project != "" {
			return g.Project
		}
	}
	return strings.TrimPrefix(session, "agentctl-")
}

// pushApproval notifies subscribed devices. The push names who asks and what
// kind of action, never the prompt or command. Muted projects are respected;
// quiet hours are not, since an unanswered approval blocks work.
func (s *Server) pushApproval(p *pendingApproval) {
	if s.pushQ == nil || !s.opts.Config.Push.ApprovalsEnabled() {
		return
	}
	if slices.Contains(s.opts.Config.Push.MutedProjects, p.project) {
		return
	}
	asking := p.req.AskingAgent
	if asking == "" {
		asking = "An agent"
	}
	target := p.req.TargetAgent
	if target == "" {
		target = "an agent"
	}
	body := asking + " wants to run a command"
	switch p.req.Kind {
	case remoteapproval.KindDispatch:
		body = fmt.Sprintf("%s wants to dispatch %s", asking, target)
		if proj := p.req.Details.Project; proj != "" {
			body += " to " + proj
		}
	case remoteapproval.KindCron:
		body = fmt.Sprintf("%s wants to schedule %s", asking, target)
	}
	ttl := int(p.deadline.Sub(p.opened).Seconds())
	if ttl < 1 {
		ttl = 1
	}
	s.pushQ.enqueue(Message{
		Title:  "Approval needed",
		Body:   body,
		URL:    approvalURL(p.req.ID),
		Tag:    "approval:" + p.req.ID,
		Urgent: true,
		Kind:   "approval",
		TTL:    ttl,
	})
}

// approvalURL is the app route for an approval.
func approvalURL(id string) string { return "/#/approvals/" + encodeURIComponent(id) }

// auditApproval records an approval lifecycle line that no HTTP request
// caused (requested, resolved elsewhere).
func (s *Server) auditApproval(p *pendingApproval, action, result, deviceID, deviceName string) {
	s.recordAudit(auditEntry{
		Time:          s.now().UTC().Format(time.RFC3339Nano),
		DeviceID:      deviceID,
		DeviceName:    deviceName,
		Action:        action,
		Session:       clip(p.req.Session),
		Window:        clip(p.req.WindowID),
		Result:        result,
		Agent:         clip(p.req.TargetAgent),
		Project:       clip(p.project),
		ApprovalID:    p.req.ID,
		ApprovalKind:  p.req.Kind,
		AskingAgent:   clip(p.req.AskingAgent),
		PayloadSHA256: p.hash,
	})
}

// registerApprovals registers the approval API.
func (s *Server) registerApprovals(rt *Router) {
	rt.Feature("approvals")
	rt.Device("GET /v1/approvals", s.handleApprovals)
	rt.Device("GET /v1/approvals/{id}", s.handleApprovalGet)
	rt.Write("POST /v1/approvals/{id}", writeRoute(s, "approval", s.handleApprovalAnswer))
}

func (s *Server) handleApprovals(w http.ResponseWriter, _ *http.Request) {
	now := s.now()
	views := []approvalView{}
	for _, p := range s.approvals.list() {
		views = append(views, p.view(now))
	}
	writeJSON(w, http.StatusOK, struct {
		Approvals []approvalView     `json:"approvals"`
		Recent    []resolvedApproval `json:"recent"`
	}{views, s.approvals.recent(now)})
}

// handleApprovalGet returns one approval: the pending view, or the resolved
// summary while it is still in history. Unknown and expired ids are 404.
func (s *Server) handleApprovalGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	now := s.now()
	if remoteapproval.ValidID(id) {
		p, res := s.approvals.lookup(id, now)
		switch {
		case p != nil:
			writeJSON(w, http.StatusOK, struct {
				approvalView
				Status string `json:"status"`
			}{p.view(now), "pending"})
			return
		case res != nil:
			writeJSON(w, http.StatusOK, res)
			return
		}
	}
	writeError(w, http.StatusNotFound, codeNotFound, "no such approval")
}

type approvalAnswerBody struct {
	Decision      string `json:"decision"`
	PayloadSHA256 string `json:"payload_sha256"`
}

func (s *Server) handleApprovalAnswer(r *http.Request, in approvalAnswerBody, a *auditEntry) outcome {
	id := r.PathValue("id")
	a.ApprovalID = clip(id)
	a.Decision = clip(in.Decision)
	a.PayloadSHA256 = clip(in.PayloadSHA256)
	if in.Decision != remoteapproval.Allow && in.Decision != remoteapproval.Deny {
		return failure(http.StatusBadRequest, codeBadRequest, `decision must be "allow" or "deny"`)
	}
	if !remoteapproval.ValidID(id) {
		return failure(http.StatusNotFound, codeNotFound, "no such approval")
	}
	p, res := s.approvals.claim(id, in.PayloadSHA256)
	if p != nil {
		a.Session, a.Window = clip(p.req.Session), clip(p.req.WindowID)
		a.Agent, a.Project = clip(p.req.TargetAgent), clip(p.project)
		a.ApprovalKind, a.AskingAgent = p.req.Kind, clip(p.req.AskingAgent)
	}
	switch res {
	case claimUnknown:
		return failure(http.StatusNotFound, codeNotFound, "no such approval")
	case claimNotPending:
		return failure(http.StatusConflict, codeAlreadyAnswered, "the approval is no longer pending")
	case claimHashMismatch:
		return failure(http.StatusConflict, codeApprovalChanged, "the approval changed since it was shown")
	}
	d, _ := DeviceFrom(r.Context())
	s.auditAttempt(*a)
	ans := approvalAnswer{decision: in.Decision, device: d.Name, done: make(chan error, 1)}
	p.answer <- ans // buffered; only the single claimant sends
	var err error
	select {
	case err = <-ans.done:
	case <-p.closed:
		// The connection ended; the decision may still have been written
		// just before.
		select {
		case err = <-ans.done:
		default:
			err = errors.New("approval ended before the answer was delivered")
		}
	}
	if err != nil {
		logging.Warn("serve: approval %s: %v", id, err)
		return failure(http.StatusConflict, codeApprovalGone, "the approval ended before the answer reached the agent")
	}
	return success(struct {
		OK       bool   `json:"ok"`
		Decision string `json:"decision"`
	}{true, in.Decision})
}

// sendApprovalEvent writes the approval SSE events for ev.
func sendApprovalEvent(send func(event string, v any) bool, ev approvalEvent) bool {
	switch {
	case ev.opened != nil:
		return send("approval", ev.opened)
	case ev.resolved != nil:
		return send("approval_resolved", ev.resolved)
	}
	return true
}

// approvalsOf returns the approval views of pending approvals, for the SSE
// connect snapshot.
func approvalsOf(ps []*pendingApproval, now time.Time) []approvalView {
	out := make([]approvalView, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.view(now))
	}
	return out
}
