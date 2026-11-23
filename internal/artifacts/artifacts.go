// Package artifacts is the machine-wide store of agent-published artifacts:
// reports, mockups, charts and other files an agent wants to show the human,
// served later by `agentctl serve` and managed by `agentctl artifact`.
//
// Layout under <config.StateDir()>/artifacts (dirs 0700, files 0600):
//
//	<id>/meta.json
//	<id>/rev/<n>/...     content of revision n
//	.lock                flock serializing every writer
//	.tmp/                staging; a crash leaves only swept leftovers here
//
// The CLI is the only writer. Content is copied (never symlinked), staged in
// .tmp and renamed into place, so a crash never leaves a half artifact.
// The state dir is resolved lazily, never at package init.
package artifacts

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/blsmth/agentctl/internal/config"
)

// Limits. Variables so tests can lower them.
var (
	// MaxRevBytes is the most content one revision may hold.
	MaxRevBytes int64 = 25 << 20
	// MaxFiles is the most files one revision may hold.
	MaxFiles = 500
	// MaxStoreBytes caps the content of the whole store.
	MaxStoreBytes int64 = 1 << 30
	// MaxRevs is how many revisions an artifact keeps; the oldest is dropped.
	MaxRevs = 20
)

// DefaultRetention is how long an unpinned artifact survives without an
// update before housekeeping prunes it.
const DefaultRetention = 30 * 24 * time.Hour

// staleTmp is the age after which a leftover staging dir is swept.
const staleTmp = time.Hour

// Kinds.
const (
	KindHTML     = "html"
	KindMarkdown = "markdown"
	KindImage    = "image"
	KindText     = "text"
)

// ErrNotFound is returned (wrapped) when an artifact id does not exist.
var ErrNotFound = errors.New("artifact not found")

var idRe = regexp.MustCompile(`^[a-z2-7]{10}$`)

// ValidID reports whether id has the shape NewID produces. Every method that
// takes an id checks it first, so an id can never be a path.
func ValidID(id string) bool { return idRe.MatchString(id) }

// Revision is one stored version of an artifact.
type Revision struct {
	N       int       `json:"n"`
	Created time.Time `json:"created"`
	Size    int64     `json:"size"`
	Note    string    `json:"note,omitempty"`
}

// Meta is meta.json.
type Meta struct {
	ID          string     `json:"id"`
	Slug        string     `json:"slug,omitempty"`
	Title       string     `json:"title"`
	Description string     `json:"description,omitempty"`
	Kind        string     `json:"kind"`
	Entry       string     `json:"entry"`
	Created     time.Time  `json:"created"`
	Updated     time.Time  `json:"updated"`
	Revs        []Revision `json:"revs"`
	Pinned      bool       `json:"pinned,omitempty"`
	// NoNotify is `artifact add --no-notify`: serve sends no push for it.
	NoNotify   bool   `json:"no_notify,omitempty"`
	ProjectDir string `json:"project_dir,omitempty"`
	Project    string `json:"project,omitempty"`
	Agent      string `json:"agent,omitempty"`
	WindowID   string `json:"window_id,omitempty"`
	Session    string `json:"session,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	Branch     string `json:"branch,omitempty"`
	// Account is the author window's account, so feedback is never typed into
	// a window of another account that reused its window ID.
	Account string `json:"account,omitempty"`
	// Comments is feedback sent from the viewer (see comments.go).
	Comments []Comment `json:"comments,omitempty"`
}

// Latest returns the newest revision number (0 when there are none).
func (m Meta) Latest() int {
	if len(m.Revs) == 0 {
		return 0
	}
	return m.Revs[len(m.Revs)-1].N
}

// HasRev reports whether revision n is stored.
func (m Meta) HasRev(n int) bool {
	for _, r := range m.Revs {
		if r.N == n {
			return true
		}
	}
	return false
}

// Origin is where an artifact came from, filled by the caller from the
// calling window.
type Origin struct {
	ProjectDir, Project, Agent, WindowID, Session, SessionID, Branch, Account string
}

// Store is the artifact store. Use New; tests build one with Dir pointing at
// a temp dir.
type Store struct {
	// Dir resolves the store root (<StateDir>/artifacts), lazily.
	Dir func() (string, error)
	// Now is the clock.
	Now func() time.Time
}

// New returns the store under the agentctl state dir.
func New() *Store {
	return &Store{Dir: defaultDir, Now: time.Now}
}

func defaultDir() (string, error) {
	sd, err := config.StateDir()
	if err != nil {
		return "", fmt.Errorf("resolving state dir: %w", err)
	}
	return filepath.Join(sd, "artifacts"), nil
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// root returns the store root, creating it (0700) when create is set.
func (s *Store) root(create bool) (string, error) {
	dir, err := s.Dir()
	if err != nil {
		return "", err
	}
	if create {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", fmt.Errorf("creating artifact store: %w", err)
		}
	}
	return dir, nil
}

// lock takes the store's exclusive flock.
func (s *Store) lock(root string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(root, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening artifact lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("locking artifact store: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// NewID returns a random 10-character base32 (lowercase) id.
func NewID() string {
	var b [7]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("artifacts: crypto/rand: %v", err))
	}
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])
	return strings.ToLower(enc)[:10]
}

// Get returns one artifact's metadata.
func (s *Store) Get(id string) (Meta, error) {
	if !ValidID(id) {
		return Meta{}, fmt.Errorf("%w: %q is not an artifact id", ErrNotFound, id)
	}
	root, err := s.root(false)
	if err != nil {
		return Meta{}, err
	}
	return readMeta(root, id)
}

func readMeta(root, id string) (Meta, error) {
	data, err := os.ReadFile(filepath.Join(root, id, "meta.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return Meta{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return Meta{}, fmt.Errorf("reading artifact %s: %w", id, err)
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return Meta{}, fmt.Errorf("parsing artifact %s: %w", id, err)
	}
	return m, nil
}

// writeMeta replaces <dir>/meta.json atomically.
func writeMeta(dir string, m Meta) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".meta-*")
	if err != nil {
		return fmt.Errorf("writing artifact meta: %w", err)
	}
	name := f.Name()
	_, werr := f.Write(append(data, '\n'))
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("writing artifact meta: %w", err)
	}
	if err := os.Chmod(name, 0o600); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, filepath.Join(dir, "meta.json")); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("writing artifact meta: %w", err)
	}
	return nil
}

// Filter narrows List. Zero values match everything.
type Filter struct {
	Project  string // display name or project dir
	Agent    string
	WindowID string
	Since    time.Time // updated at or after
}

func (f Filter) match(m Meta) bool {
	if f.Project != "" && f.Project != m.Project && f.Project != m.ProjectDir {
		return false
	}
	if f.Agent != "" && f.Agent != m.Agent {
		return false
	}
	if f.WindowID != "" && f.WindowID != m.WindowID {
		return false
	}
	return f.Since.IsZero() || !m.Updated.Before(f.Since)
}

// List returns the matching artifacts, most recently updated first. Entries
// with unreadable metadata are skipped.
func (s *Store) List(f Filter) ([]Meta, error) {
	root, err := s.root(false)
	if err != nil {
		return nil, err
	}
	all, err := listAll(root)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, m := range all {
		if f.match(m) {
			out = append(out, m)
		}
	}
	return out, nil
}

func listAll(root string) ([]Meta, error) {
	ents, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("listing artifacts: %w", err)
	}
	var out []Meta
	for _, e := range ents {
		if !e.IsDir() || !ValidID(e.Name()) {
			continue
		}
		m, err := readMeta(root, e.Name())
		if err != nil {
			continue
		}
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out, nil
}

// storeBytes sums the content size of every revision.
func storeBytes(root string) int64 {
	all, _ := listAll(root)
	var n int64
	for _, m := range all {
		for _, r := range m.Revs {
			n += r.Size
		}
	}
	return n
}

// checkCapacity refuses a write of add bytes that would take the store over
// MaxStoreBytes once freed bytes (revisions the write drops) are released.
func checkCapacity(root string, add, freed int64) error {
	if used := storeBytes(root); used-freed+add > MaxStoreBytes {
		return fmt.Errorf("artifact store is full (%s used of %s): free space with `agentctl artifact rm <id>` or `agentctl artifact prune`",
			humanBytes(used), humanBytes(MaxStoreBytes))
	}
	return nil
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// RevDir returns the directory holding revision n of artifact id; the
// revision must exist. n <= 0 means the latest.
func (s *Store) RevDir(id string, n int) (string, error) {
	m, err := s.Get(id)
	if err != nil {
		return "", err
	}
	if n <= 0 {
		n = m.Latest()
	}
	if !m.HasRev(n) {
		return "", fmt.Errorf("%w: %s has no revision %d", ErrNotFound, id, n)
	}
	root, err := s.root(false)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, id, "rev", fmt.Sprint(n)), nil
}

// Pin sets or clears the pinned flag.
func (s *Store) Pin(id string, pinned bool) (Meta, error) {
	if !ValidID(id) {
		return Meta{}, fmt.Errorf("%w: %q is not an artifact id", ErrNotFound, id)
	}
	root, err := s.root(true)
	if err != nil {
		return Meta{}, err
	}
	unlock, err := s.lock(root)
	if err != nil {
		return Meta{}, err
	}
	defer unlock()
	m, err := readMeta(root, id)
	if err != nil {
		return Meta{}, err
	}
	m.Pinned = pinned
	if err := writeMeta(filepath.Join(root, id), m); err != nil {
		return Meta{}, err
	}
	touchChanged(root)
	return m, nil
}

// Remove deletes an artifact and all its revisions.
func (s *Store) Remove(id string) (Meta, error) {
	if !ValidID(id) {
		return Meta{}, fmt.Errorf("%w: %q is not an artifact id", ErrNotFound, id)
	}
	root, err := s.root(true)
	if err != nil {
		return Meta{}, err
	}
	unlock, err := s.lock(root)
	if err != nil {
		return Meta{}, err
	}
	defer unlock()
	m, err := readMeta(root, id)
	if err != nil {
		return Meta{}, err
	}
	if err := removeDir(root, id); err != nil {
		return m, err
	}
	touchChanged(root)
	return m, nil
}

// removeDir moves <root>/<id> out of the namespace first (an atomic rename),
// then deletes it, so a reader never sees a half-deleted artifact.
func removeDir(root, id string) error {
	tmp := filepath.Join(root, ".tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return err
	}
	dst := filepath.Join(tmp, "rm-"+NewID())
	if err := os.Rename(filepath.Join(root, id), dst); err != nil {
		return fmt.Errorf("removing artifact %s: %w", id, err)
	}
	return os.RemoveAll(dst)
}

// Prune deletes unpinned artifacts last updated more than olderThan ago and
// returns them (with dryRun, only returns them). It also sweeps stale staging
// dirs.
func (s *Store) Prune(olderThan time.Duration, dryRun bool) ([]Meta, error) {
	if olderThan <= 0 {
		return nil, errors.New("prune: older-than must be positive")
	}
	root, err := s.root(false)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	unlock, err := s.lock(root)
	if err != nil {
		return nil, err
	}
	defer unlock()
	all, err := listAll(root)
	if err != nil {
		return nil, err
	}
	cutoff := s.now().Add(-olderThan)
	var gone []Meta
	for _, m := range all {
		if m.Pinned || !m.Updated.Before(cutoff) {
			continue
		}
		gone = append(gone, m)
		if dryRun {
			continue
		}
		if err := removeDir(root, m.ID); err != nil {
			touchChanged(root)
			return gone, err
		}
	}
	if !dryRun {
		s.sweepTmp(root)
		if len(gone) > 0 {
			touchChanged(root)
		}
	}
	return gone, nil
}

// sweepTmp deletes staging leftovers older than staleTmp. Callers hold the
// lock; live stagers are young.
func (s *Store) sweepTmp(root string) {
	tmp := filepath.Join(root, ".tmp")
	ents, err := os.ReadDir(tmp)
	if err != nil {
		return
	}
	for _, e := range ents {
		info, err := e.Info()
		if err != nil || s.now().Sub(info.ModTime()) < staleTmp {
			continue
		}
		_ = os.RemoveAll(filepath.Join(tmp, e.Name()))
	}
}

// changesDirName is the directory every writer touches after a change, so a
// watcher (serve) needs one fsnotify watch however many artifacts there are.
const changesDirName = ".changes"

// ChangesDir returns <store root>/.changes, creating it (and the root) 0700.
// Every successful Add, Update, Pin, Remove and Prune rewrites a file in it;
// watch it and rescan with List to see what changed.
func (s *Store) ChangesDir() (string, error) {
	root, err := s.root(true)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, changesDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating artifact change dir: %w", err)
	}
	return dir, nil
}

// touchChanged records that the store changed. Best effort: a watcher also
// polls.
func touchChanged(root string) {
	dir := filepath.Join(root, changesDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, "stamp"), []byte(time.Now().UTC().Format(time.RFC3339Nano)+"\n"), 0o600)
}
