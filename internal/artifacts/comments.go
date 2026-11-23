package artifacts

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// Comment limits.
const (
	// MaxComments is how many comments an artifact keeps; the oldest are
	// dropped.
	MaxComments = 200
	// MaxCommentRunes bounds one comment's text (characters, not bytes).
	MaxCommentRunes = 4000
)

// Comment statuses.
const (
	// CommentDelivered: typed into the author's idle window.
	CommentDelivered = "delivered"
	// CommentQueued: typed into the author's working window (Claude Code
	// queues it until the turn ends).
	CommentQueued = "queued"
	// CommentUndelivered: kept in the store only (window gone, other
	// account, or delivery failed).
	CommentUndelivered = "undelivered"
)

// Comment is feedback from the viewer on one revision.
type Comment struct {
	ID          string    `json:"id"`
	Rev         int       `json:"rev"`
	At          time.Time `json:"at"`
	Device      string    `json:"device,omitempty"`
	Text        string    `json:"text"`
	DeliveredAt time.Time `json:"delivered_at,omitzero"`
	Status      string    `json:"status"`
}

// AddComment appends a comment to the artifact (status undelivered until
// MarkComment says otherwise) and returns it. The text must be non-empty and
// at most MaxCommentRunes; rev must be a stored revision.
func (s *Store) AddComment(id string, rev int, device, text string) (Comment, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Comment{}, fmt.Errorf("comment text is empty")
	}
	if utf8.RuneCountInString(text) > MaxCommentRunes {
		return Comment{}, fmt.Errorf("comment exceeds %d characters", MaxCommentRunes)
	}
	var c Comment
	err := s.modify(id, func(m *Meta) error {
		if !m.HasRev(rev) {
			return fmt.Errorf("%w: %s has no revision %d", ErrNotFound, id, rev)
		}
		c = Comment{ID: NewID(), Rev: rev, At: s.now(), Device: device, Text: text, Status: CommentUndelivered}
		m.Comments = append(m.Comments, c)
		if over := len(m.Comments) - MaxComments; over > 0 {
			m.Comments = append([]Comment(nil), m.Comments[over:]...)
		}
		return nil
	})
	return c, err
}

// MarkComment records the delivery outcome of a comment. A missing comment
// (dropped by the cap, or the artifact removed) is not an error worth
// surfacing to callers beyond ErrNotFound.
func (s *Store) MarkComment(id, commentID, status string) error {
	return s.modify(id, func(m *Meta) error {
		for i := range m.Comments {
			if m.Comments[i].ID != commentID {
				continue
			}
			m.Comments[i].Status = status
			if status == CommentDelivered || status == CommentQueued {
				m.Comments[i].DeliveredAt = s.now()
			}
			return nil
		}
		return fmt.Errorf("%w: comment %s", ErrNotFound, commentID)
	})
}

// modify reads, changes and rewrites meta.json under the store lock. It does
// not touch Updated: comments do not extend an artifact's retention.
func (s *Store) modify(id string, fn func(*Meta) error) error {
	if !ValidID(id) {
		return fmt.Errorf("%w: %q is not an artifact id", ErrNotFound, id)
	}
	root, err := s.root(true)
	if err != nil {
		return err
	}
	unlock, err := s.lock(root)
	if err != nil {
		return err
	}
	defer unlock()
	m, err := readMeta(root, id)
	if err != nil {
		return err
	}
	if err := fn(&m); err != nil {
		return err
	}
	if err := writeMeta(filepath.Join(root, id), m); err != nil {
		return err
	}
	touchChanged(root)
	return nil
}
