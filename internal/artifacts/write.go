package artifacts

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// AddOptions are the inputs of Add.
type AddOptions struct {
	Source
	Title, Description string
	Kind, Entry        string
	Pin                bool
	Origin             Origin
}

// UpdateOptions are the inputs of Update. The kind and entry are re-inferred
// from the new source (stdin reuses the current entry name).
type UpdateOptions struct {
	Source
	Note, Title string
}

// Add publishes a new artifact with revision 1.
func (s *Store) Add(o AddOptions) (Meta, error) {
	root, err := s.root(true)
	if err != nil {
		return Meta{}, err
	}
	st, err := stage(root, o.Source, o.Kind, o.Entry, "")
	if err != nil {
		return Meta{}, err
	}
	defer func() { _ = os.RemoveAll(st.dir) }() // no-op once renamed
	unlock, err := s.lock(root)
	if err != nil {
		return Meta{}, err
	}
	defer unlock()
	if err := checkCapacity(root, st.size); err != nil {
		return Meta{}, err
	}

	id := NewID()
	for {
		if _, err := os.Lstat(filepath.Join(root, id)); os.IsNotExist(err) {
			break
		}
		id = NewID()
	}
	now := s.now()
	title := strings.TrimSpace(o.Title)
	if title == "" {
		title = st.title
	}
	m := Meta{
		ID: id, Slug: slugify(title), Title: title, Description: o.Description,
		Kind: st.kind, Entry: st.entry, Created: now, Updated: now,
		Revs:       []Revision{{N: 1, Created: now, Size: st.size}},
		Pinned:     o.Pin,
		ProjectDir: o.Origin.ProjectDir, Project: o.Origin.Project, Agent: o.Origin.Agent,
		WindowID: o.Origin.WindowID, Session: o.Origin.Session, SessionID: o.Origin.SessionID,
		Branch: o.Origin.Branch,
	}
	revDir := filepath.Join(st.dir, "rev")
	if err := os.MkdirAll(revDir, 0o700); err != nil {
		return Meta{}, err
	}
	if err := os.Rename(st.content(), filepath.Join(revDir, "1")); err != nil {
		return Meta{}, fmt.Errorf("storing artifact: %w", err)
	}
	if err := writeMeta(st.dir, m); err != nil {
		return Meta{}, err
	}
	// The artifact becomes visible only here, complete.
	if err := os.Rename(st.dir, filepath.Join(root, id)); err != nil {
		return Meta{}, fmt.Errorf("storing artifact: %w", err)
	}
	return m, nil
}

// Update adds a revision to an existing artifact, dropping the oldest ones
// beyond MaxRevs.
func (s *Store) Update(id string, o UpdateOptions) (Meta, error) {
	cur, err := s.Get(id)
	if err != nil {
		return Meta{}, err
	}
	root, err := s.root(true)
	if err != nil {
		return Meta{}, err
	}
	st, err := stage(root, o.Source, "", "", cur.Entry)
	if err != nil {
		return Meta{}, err
	}
	defer func() { _ = os.RemoveAll(st.dir) }()
	unlock, err := s.lock(root)
	if err != nil {
		return Meta{}, err
	}
	defer unlock()
	m, err := readMeta(root, id) // re-read: it may have changed or been removed
	if err != nil {
		return Meta{}, err
	}
	if err := checkCapacity(root, st.size); err != nil {
		return Meta{}, err
	}

	adir := filepath.Join(root, id)
	n := m.Latest() + 1
	if orphan := maxRevDir(adir); orphan >= n { // a crash left a rev dir behind
		n = orphan + 1
	}
	now := s.now()
	if err := os.MkdirAll(filepath.Join(adir, "rev"), 0o700); err != nil {
		return Meta{}, err
	}
	if err := os.Rename(st.content(), filepath.Join(adir, "rev", strconv.Itoa(n))); err != nil {
		return Meta{}, fmt.Errorf("storing revision: %w", err)
	}
	m.Revs = append(m.Revs, Revision{N: n, Created: now, Size: st.size, Note: o.Note})
	for len(m.Revs) > MaxRevs {
		m.Revs = m.Revs[1:]
	}
	m.Kind, m.Entry, m.Updated = st.kind, st.entry, now
	if t := strings.TrimSpace(o.Title); t != "" {
		m.Title, m.Slug = t, slugify(t)
	}
	if err := writeMeta(adir, m); err != nil {
		_ = os.RemoveAll(filepath.Join(adir, "rev", strconv.Itoa(n)))
		return Meta{}, err
	}
	dropUnreferenced(adir, m)
	return m, nil
}

// maxRevDir is the highest numeric dir under <adir>/rev (0 when none).
func maxRevDir(adir string) int {
	ents, _ := os.ReadDir(filepath.Join(adir, "rev"))
	max := 0
	for _, e := range ents {
		if n, err := strconv.Atoi(e.Name()); err == nil && n > max {
			max = n
		}
	}
	return max
}

// dropUnreferenced removes rev dirs meta no longer lists (dropped by the cap,
// or left by a crash).
func dropUnreferenced(adir string, m Meta) {
	ents, _ := os.ReadDir(filepath.Join(adir, "rev"))
	for _, e := range ents {
		if n, err := strconv.Atoi(e.Name()); err != nil || !m.HasRev(n) {
			_ = os.RemoveAll(filepath.Join(adir, "rev", e.Name()))
		}
	}
}
