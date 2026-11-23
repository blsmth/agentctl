package artifacts

import (
	"errors"
	"strings"
	"testing"
)

func addOne(t *testing.T, s *Store) Meta {
	t.Helper()
	m, err := s.Add(AddOptions{Source: Source{Path: write(t, t.TempDir()+"/r.md", "# R\n")}})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestAddCommentAndMark(t *testing.T) {
	s := newStore(t)
	m := addOne(t, s)
	c, err := s.AddComment(m.ID, 1, "phone", "  looks good  ")
	if err != nil {
		t.Fatal(err)
	}
	if c.Text != "looks good" || c.Status != CommentUndelivered || c.Rev != 1 || c.ID == "" {
		t.Fatalf("comment = %+v", c)
	}
	if err := s.MarkComment(m.ID, c.ID, CommentQueued); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(m.ID)
	if len(got.Comments) != 1 || got.Comments[0].Status != CommentQueued || got.Comments[0].DeliveredAt.IsZero() {
		t.Fatalf("stored = %+v", got.Comments)
	}
	if !got.Updated.Equal(m.Updated) {
		t.Error("a comment must not change Updated")
	}
	if err := s.MarkComment(m.ID, "nope", CommentDelivered); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown comment err = %v", err)
	}
}

func TestAddCommentRejects(t *testing.T) {
	s := newStore(t)
	m := addOne(t, s)
	tests := []struct {
		name string
		id   string
		rev  int
		text string
	}{
		{"empty", m.ID, 1, "  "},
		{"too long", m.ID, 1, strings.Repeat("é", MaxCommentRunes+1)},
		{"bad rev", m.ID, 9, "x"},
		{"bad id", "../x", 1, "x"},
		{"missing", "aaaaaaaaaa", 1, "x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := s.AddComment(tt.id, tt.rev, "d", tt.text); err == nil {
				t.Fatal("want error")
			}
		})
	}
	if _, err := s.AddComment(m.ID, 1, "d", strings.Repeat("é", MaxCommentRunes)); err != nil {
		t.Errorf("exactly the cap: %v", err)
	}
}

func TestCommentCap(t *testing.T) {
	s := newStore(t)
	m := addOne(t, s)
	for i := 0; i < MaxComments+5; i++ {
		if _, err := s.AddComment(m.ID, 1, "d", "c"+strings.Repeat("x", i%3)); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := s.Get(m.ID)
	if len(got.Comments) != MaxComments {
		t.Fatalf("len = %d", len(got.Comments))
	}
}
