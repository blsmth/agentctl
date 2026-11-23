package serve

import (
	"fmt"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/blsmth/agentctl/internal/artifacts"
	"github.com/blsmth/agentctl/internal/logging"
)

// Feedback from the viewer to the agent that made the artifact: the comment
// is always stored in the artifact's meta, and typed into the author's window
// the way reply (idle) or queue (working) would when that window is still
// there. Delivery never fails the request: a comment that could not be typed
// is kept as undelivered and the response says why.

// Delivery outcomes in the response.
const (
	deliveryDelivered = "delivered"
	deliveryQueued    = "queued"
	deliverySaved     = "saved"
)

type artifactCommentIn struct {
	Text string `json:"text"`
	// Rev is the revision the comment applies to; 0 means the latest.
	Rev int `json:"rev"`
}

type artifactCommentOut struct {
	Comment artifacts.Comment `json:"comment"`
	// Delivery is "delivered", "queued" or "saved".
	Delivery string `json:"delivery"`
	// Reason says why a comment was only saved.
	Reason string `json:"reason,omitempty"`
}

// commentMessage is the text typed into the author's window.
func commentMessage(m artifacts.Meta, rev int, text string) string {
	title := strings.Join(strings.FieldsFunc(m.Title, unicode.IsControl), " ")
	title = strings.Join(strings.Fields(title), " ")
	return fmt.Sprintf("Feedback on artifact %q (%s, rev %d) from Brendan: %s\nUpdate it with: agentctl artifact update %s <file>",
		title, m.ID, rev, strings.TrimSpace(text), m.ID)
}

func (s *Server) handleArtifactComment(r *http.Request, in artifactCommentIn, a *auditEntry) outcome {
	id := r.PathValue("id")
	a.Artifact = clip(id)
	if utf8.RuneCountInString(in.Text) > artifacts.MaxCommentRunes {
		return failure(http.StatusRequestEntityTooLarge, codeTooLarge, fmt.Sprintf("comment exceeds %d characters", artifacts.MaxCommentRunes))
	}
	if out, ok := checkText("text", in.Text); !ok {
		return out
	}
	m, err := s.artifacts.Get(id)
	if err != nil {
		status, code, msg := artifactErr(err)
		return failure(status, code, msg)
	}
	rev := in.Rev
	if rev == 0 {
		rev = m.Latest()
	}
	if !m.HasRev(rev) {
		return failure(http.StatusBadRequest, codeBadRequest, fmt.Sprintf("no revision %d", rev))
	}
	a.Project = clip(m.Project)
	a.Agent = clip(m.Agent)
	s.auditAttempt(*a)
	d, _ := DeviceFrom(r.Context())
	c, err := s.artifacts.AddComment(id, rev, d.Name, in.Text)
	if err != nil {
		status, code, msg := artifactErr(err)
		return failure(status, code, msg)
	}
	s.artifactWatch.Kick()

	status, reason := s.deliverComment(m, rev, in.Text, a)
	res := artifactCommentOut{Comment: c, Delivery: deliverySaved, Reason: reason}
	switch status {
	case artifacts.CommentDelivered:
		res.Delivery = deliveryDelivered
	case artifacts.CommentQueued:
		res.Delivery = deliveryQueued
	}
	if status != artifacts.CommentUndelivered {
		if err := s.artifacts.MarkComment(id, c.ID, status); err != nil {
			logging.Warn("serve: marking comment %s of %s: %v", c.ID, id, err)
		}
		res.Comment.Status = status
		s.artifactWatch.Kick()
	}
	return success(res)
}

// deliverComment types the comment into the author window when it is still
// the one that made the artifact and accepts input. It returns the comment's
// status and, when undelivered, the reason.
func (s *Server) deliverComment(m artifacts.Meta, rev int, text string, a *auditEntry) (string, string) {
	if m.Session == "" || m.WindowID == "" {
		return artifacts.CommentUndelivered, "no agent window is recorded for this artifact"
	}
	rw, _, ok := s.resolveWindowAt(m.Session, m.WindowID)
	if !ok {
		return artifacts.CommentUndelivered, "the agent's window is closed"
	}
	if m.Account != "" && rw.group.Rows != nil && rw.index < len(rw.group.Rows) && rw.group.Rows[rw.index].Account != m.Account {
		return artifacts.CommentUndelivered, "the window now belongs to another account"
	}
	queue := rw.window.State == "working"
	da := *a
	out := s.deliverText(m.Session, m.WindowID, replyIn{Text: commentMessage(m, rev, text)}, &da, queue)
	if out.status >= 400 {
		switch out.code {
		case codeNotFound:
			return artifacts.CommentUndelivered, "the agent's window is closed"
		case codeNotIdle:
			return artifacts.CommentUndelivered, "the agent is busy or waiting at a prompt: " + out.msg
		}
		return artifacts.CommentUndelivered, "the agent could not be reached: " + out.msg
	}
	if queue {
		return artifacts.CommentQueued, ""
	}
	return artifacts.CommentDelivered, ""
}
