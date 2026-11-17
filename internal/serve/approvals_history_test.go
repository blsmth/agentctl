package serve

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/remoteapproval"
)

func (e *approvalEnv) get(t *testing.T, id string) (int, map[string]any) {
	t.Helper()
	w := do(e.Handler(), http.MethodGet, "/v1/approvals/"+id, withCookie(e.tok))
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w.Code, m
}

func (e *approvalEnv) waitResolved(t *testing.T, id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		code, m := e.get(t, id)
		if code == http.StatusOK && m["status"] == "resolved" {
			return m
		}
		if time.Now().After(deadline) {
			t.Fatalf("approval %s never resolved: %d %v", id, code, m)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestApprovalGetPendingResolvedUnknown(t *testing.T) {
	e := newApprovalEnv(t, true, nil)
	req := e.request(t)
	a := e.open(t, req)
	e.waitPending(t, 1)

	code, m := e.get(t, req.ID)
	if code != http.StatusOK || m["status"] != "pending" || m["payload_sha256"] == "" || m["command"] == nil {
		t.Fatalf("pending: %d %v", code, m)
	}

	if w := e.answer(req.ID, "allow", remoteapproval.PayloadHash(req)); w.Code != http.StatusOK {
		t.Fatalf("answer: %d %s", w.Code, w.Body)
	}
	if _, err := waitDecision(t, a); err != nil {
		t.Fatal(err)
	}
	m = e.waitResolved(t, req.ID)
	if m["outcome"] != resolvedApproved || m["source"] == "" || m["resolved_at"] == "" || m["asking_agent"] != "orch" {
		t.Fatalf("resolved: %v", m)
	}
	for _, k := range []string{"prompt", "command", "payload_sha256"} {
		if _, ok := m[k]; ok {
			t.Errorf("resolved entry leaks %q", k)
		}
	}
	if w := e.answer(req.ID, "allow", remoteapproval.PayloadHash(req)); w.Code != http.StatusConflict {
		t.Fatalf("POST on resolved: %d", w.Code)
	}

	id2, _ := remoteapproval.NewID()
	if code, _ := e.get(t, id2); code != http.StatusNotFound {
		t.Fatalf("unknown id: %d", code)
	}
	if code, _ := e.get(t, "bad%21id"); code != http.StatusNotFound {
		t.Fatalf("invalid id: %d", code)
	}
}

func TestApprovalGetExpiredAndOutcomes(t *testing.T) {
	e := newApprovalEnv(t, true, nil)
	req := e.request(t)
	a := e.open(t, req)
	e.waitPending(t, 1)
	a.Cancel(remoteapproval.ReasonLaptop)
	m := e.waitResolved(t, req.ID)
	if m["outcome"] != resolvedAnswered || m["source"] != sourceLaptop {
		t.Fatalf("laptop: %v", m)
	}
	e.clock.Advance(historyTTL + time.Minute)
	if code, _ := e.get(t, req.ID); code != http.StatusNotFound {
		t.Fatalf("expired history: %d", code)
	}
}

func TestResolvedOutcomeMapping(t *testing.T) {
	now := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	p := &pendingApproval{req: remoteapproval.Request{ID: "x"}, opened: now}
	tests := []struct {
		outcome, decision, device, want, source string
	}{
		{outcomeAnswered, remoteapproval.Allow, "phone", resolvedApproved, "phone"},
		{outcomeAnswered, remoteapproval.Deny, "phone", resolvedDenied, "phone"},
		{remoteapproval.ReasonLaptop, "", "", resolvedAnswered, sourceLaptop},
		{outcomeExpired, "", "", resolvedTimedOut, ""},
		{remoteapproval.ReasonTimeout, "", "", resolvedTimedOut, ""},
		{outcomeGone, "", "", resolvedCancelled, ""},
		{remoteapproval.ReasonCanceled, "", "", resolvedCancelled, ""},
	}
	for _, tt := range tests {
		t.Run(tt.outcome+"/"+tt.decision, func(t *testing.T) {
			r := newResolved(p, tt.outcome, tt.decision, tt.device, now)
			if r.Outcome != tt.want || r.Source != tt.source {
				t.Fatalf("got %q/%q, want %q/%q", r.Outcome, r.Source, tt.want, tt.source)
			}
		})
	}
}

func TestApprovalHistoryBounds(t *testing.T) {
	st := newApprovalStore()
	base := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	for i := 0; i < maxHistory+10; i++ {
		id := strings.Repeat("a", 3) + string(rune('A'+i%26)) + time.Duration(i).String()
		st.remove(resolvedApproval{ID: id, at: base.Add(time.Duration(i) * time.Second)})
	}
	got := st.recent(base.Add(time.Minute))
	if len(got) != maxHistory {
		t.Fatalf("len = %d, want %d", len(got), maxHistory)
	}
	if !got[0].at.After(got[len(got)-1].at) {
		t.Fatal("recent not newest first")
	}
	// Entries older than the TTL are dropped.
	if got := st.recent(base.Add(historyTTL + time.Hour)); len(got) != 0 {
		t.Fatalf("after TTL: %d entries", len(got))
	}
}
