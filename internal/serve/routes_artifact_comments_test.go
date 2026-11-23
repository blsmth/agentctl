package serve

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/artifacts"
)

func (ae *artifactEnv) commentArtifact(t *testing.T, o artifacts.Origin) artifacts.Meta {
	t.Helper()
	return ae.add(t, map[string]string{"r.md": "# Report\n"}, artifacts.AddOptions{Title: "My \"Report\"", Origin: o})
}

var authorOrigin = artifacts.Origin{Session: "agentctl-proj", WindowID: "@523", Agent: "go-engineer", Project: "proj"}

func decodeComment(t *testing.T, body []byte) artifactCommentOut {
	t.Helper()
	var out artifactCommentOut
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("%s: %v", body, err)
	}
	return out
}

func TestArtifactCommentDelivery(t *testing.T) {
	tests := []struct {
		name       string
		state      string
		pane       string
		origin     func(artifacts.Origin) artifacts.Origin
		delivery   string
		status     string
		injected   bool
		reasonPart string
	}{
		{"idle delivers", "idle", idlePane, nil, deliveryDelivered, artifacts.CommentDelivered, true, ""},
		{"working queues", "working", workingPane, nil, deliveryQueued, artifacts.CommentQueued, true, ""},
		{"blocked saves", "blocked", dialogPane, nil, deliverySaved, artifacts.CommentUndelivered, false, "busy"},
		{"gone window saves", "idle", idlePane, func(o artifacts.Origin) artifacts.Origin { o.WindowID = "@999"; return o }, deliverySaved, artifacts.CommentUndelivered, false, "closed"},
		{"other session saves", "idle", idlePane, func(o artifacts.Origin) artifacts.Origin { o.Session = "agentctl-other"; return o }, deliverySaved, artifacts.CommentUndelivered, false, "closed"},
		{"other account saves", "idle", idlePane, func(o artifacts.Origin) artifacts.Origin { o.Account = "work"; return o }, deliverySaved, artifacts.CommentUndelivered, false, "account"},
		{"recycled window id saves", "idle", idlePane, func(o artifacts.Origin) artifacts.Origin { o.SessionID = "old-session"; return o }, deliverySaved, artifacts.CommentUndelivered, false, "different session"},
		{"same session id delivers", "idle", idlePane, func(o artifacts.Origin) artifacts.Origin { o.SessionID = "cur-session"; return o }, deliveryDelivered, artifacts.CommentDelivered, true, ""},
		{"no window recorded saves", "idle", idlePane, func(o artifacts.Origin) artifacts.Origin { o.WindowID, o.Session = "", ""; return o }, deliverySaved, artifacts.CommentUndelivered, false, "no agent window"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ae := newArtifactEnv(t, true)
			g := writeGroup(tt.state, false, "go-engineer", "dispatch")
			g.Rows[0].SessionID = "cur-session"
			ae.fleet.set(g)
			ae.panes.set(tt.pane)
			o := authorOrigin
			if tt.origin != nil {
				o = tt.origin(o)
			}
			m := ae.commentArtifact(t, o)
			w := ae.send("POST", "/v1/artifacts/"+m.ID+"/comments", `{"text":"make it blue","rev":1}`)
			if w.Code != http.StatusOK {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			out := decodeComment(t, w.Body.Bytes())
			if out.Delivery != tt.delivery || out.Comment.Status != tt.status {
				t.Errorf("delivery %q status %q, want %q %q (%s)", out.Delivery, out.Comment.Status, tt.delivery, tt.status, out.Reason)
			}
			if !strings.Contains(out.Reason, tt.reasonPart) {
				t.Errorf("reason %q", out.Reason)
			}
			if got := len(ae.inj.got) == 1; got != tt.injected {
				t.Fatalf("injected %q", ae.inj.got)
			}
			if tt.injected {
				want := `Feedback on artifact "My \"Report\"" (` + m.ID + `, rev 1) from Brendan: make it blue` + "\n" +
					"Update it with: agentctl artifact update " + m.ID + " <file>"
				if ae.inj.got[0] != want {
					t.Errorf("text = %q, want %q", ae.inj.got[0], want)
				}
			}
			got, _ := ae.store.Get(m.ID)
			if len(got.Comments) != 1 || got.Comments[0].Status != tt.status || got.Comments[0].Text != "make it blue" || got.Comments[0].Device != "phone" {
				t.Errorf("stored = %+v", got.Comments)
			}
			if tt.injected != !got.Comments[0].DeliveredAt.IsZero() {
				t.Errorf("delivered_at = %v", got.Comments[0].DeliveredAt)
			}
		})
	}
}

func TestArtifactCommentValidation(t *testing.T) {
	ae := newArtifactEnv(t, true)
	ae.fleet.set(writeGroup("idle", false, "go-engineer", "dispatch"))
	ae.panes.set(idlePane)
	m := ae.commentArtifact(t, authorOrigin)
	path := "/v1/artifacts/" + m.ID + "/comments"
	tests := []struct {
		name, path, body string
		status           int
	}{
		{"empty", path, `{"text":"  ","rev":1}`, 400},
		{"control chars", path, `{"text":"a\u0007b","rev":1}`, 400},
		{"too long", path, `{"text":"` + strings.Repeat("x", artifacts.MaxCommentRunes+1) + `","rev":1}`, 413},
		{"bad rev", path, `{"text":"x","rev":7}`, 400},
		{"unknown field", path, `{"text":"x","extra":1}`, 400},
		{"no artifact", "/v1/artifacts/aaaaaaaaaa/comments", `{"text":"x"}`, 404},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if w := ae.send("POST", tt.path, tt.body); w.Code != tt.status {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
		})
	}
	if len(ae.inj.got) != 0 {
		t.Errorf("injected %q", ae.inj.got)
	}
	// rev omitted means the latest.
	if w := ae.send("POST", path, `{"text":"fine"}`); w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	} else if c := decodeComment(t, w.Body.Bytes()).Comment; c.Rev != 1 {
		t.Errorf("rev = %d", c.Rev)
	}
}

func TestArtifactCommentGetAndList(t *testing.T) {
	ae := newArtifactEnv(t, true)
	ae.fleet.set(writeGroup("idle", false, "go-engineer", "dispatch"))
	ae.panes.set(idlePane)
	m := ae.commentArtifact(t, authorOrigin)
	if w := ae.send("POST", "/v1/artifacts/"+m.ID+"/comments", `{"text":"one","rev":1}`); w.Code != 200 {
		t.Fatal(w.Body)
	}
	var one artifactView
	w := ae.send("GET", "/v1/artifacts/"+m.ID, "")
	if err := json.Unmarshal(w.Body.Bytes(), &one); err != nil || len(one.Comments) != 1 || one.CommentCount != 1 {
		t.Fatalf("get: %v %s", err, w.Body)
	}
	var list struct{ Artifacts []artifactView }
	w = ae.send("GET", "/v1/artifacts", "")
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list.Artifacts) != 1 {
		t.Fatalf("list: %v %s", err, w.Body)
	}
	if got := list.Artifacts[0]; len(got.Comments) != 0 || got.CommentCount != 1 {
		t.Errorf("list entry comments=%d count=%d", len(got.Comments), got.CommentCount)
	}
}

func TestArtifactCommentAuditAndRateLimit(t *testing.T) {
	ae := newArtifactEnv(t, true)
	ae.fleet.set(writeGroup("idle", false, "go-engineer", "dispatch"))
	ae.panes.set(idlePane)
	m := ae.commentArtifact(t, authorOrigin)
	if w := ae.send("POST", "/v1/artifacts/"+m.ID+"/comments", `{"text":"x","rev":1}`); w.Code != 200 {
		t.Fatal(w.Body)
	}
	var sawFinal bool
	for _, l := range ae.auditLines(t) {
		if l.Action == "artifact-comment" && l.Artifact == m.ID && l.Result == "ok" {
			sawFinal = true
		}
	}
	if !sawFinal {
		t.Errorf("audit = %+v", ae.auditLines(t))
	}
	limited := false
	for i := 0; i < 200 && !limited; i++ {
		w := ae.send("POST", "/v1/artifacts/"+m.ID+"/comments", `{"text":"x","rev":1}`)
		limited = w.Code == http.StatusTooManyRequests
	}
	if !limited {
		t.Error("never rate limited")
	}
}
