package artifacts

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// ErrBadPath is returned (wrapped) by Open for a path that is not a plain
// relative path inside the revision: "..", absolute, empty segments,
// backslashes, NUL, or a symlink anywhere on the way.
var ErrBadPath = errors.New("invalid artifact path")

// Content is one opened file of a stored revision. Close it.
type Content struct {
	*os.File
	Meta Meta
	// Rev is the resolved revision number (never 0).
	Rev int
	// Rel is the slash path of the file inside the revision.
	Rel  string
	Info fs.FileInfo
}

// Open opens file rel of revision rev of artifact id for reading. rev <= 0
// means the latest; an empty rel means the entry file; a directory means its
// index.html. The file must be a regular file reached without following a
// symlink, inside the revision dir. A missing artifact, revision or file is
// ErrNotFound; a path that tries to leave the revision is ErrBadPath.
func (s *Store) Open(id string, rev int, rel string) (*Content, error) {
	m, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	if rev <= 0 {
		rev = m.Latest()
	}
	if !m.HasRev(rev) {
		return nil, fmt.Errorf("%w: %s has no revision %d", ErrNotFound, id, rev)
	}
	if rel == "" {
		rel = m.Entry
	}
	clean, err := checkRel(rel)
	if err != nil {
		return nil, err
	}
	root, err := s.root(false)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(root, id, "rev", strconv.Itoa(rev))
	f, info, clean, err := openInDir(dir, clean)
	if err != nil {
		return nil, fmt.Errorf("%s rev %d: %w", id, rev, err)
	}
	return &Content{File: f, Meta: m, Rev: rev, Rel: clean, Info: info}, nil
}

// checkRel accepts only a plain relative slash path: no "." or ".." segment,
// no empty segment (so no leading "/" or "//"), no backslash, no NUL. Unlike
// cleanRel it never rewrites the path, so "a/../b" is refused rather than
// resolved. A trailing "/" (a directory URL) is allowed.
func checkRel(rel string) (string, error) {
	p := strings.TrimSuffix(rel, "/")
	if p == "" || strings.ContainsAny(p, "\\\x00") {
		return "", fmt.Errorf("%w: %q", ErrBadPath, rel)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("%w: %q", ErrBadPath, rel)
		}
	}
	return p, nil
}

// openInDir opens rel under dir through an os.Root (so nothing outside dir
// can be reached), refusing a symlink at any component and opening the final
// file with O_NOFOLLOW. A directory resolves to its index.html.
func openInDir(dir, rel string) (*os.File, fs.FileInfo, string, error) {
	r, err := os.OpenRoot(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, "", fmt.Errorf("%w: revision content is missing", ErrNotFound)
	}
	if err != nil {
		return nil, nil, "", err
	}
	defer r.Close() //nolint:errcheck

	info, err := lstatPath(r, rel)
	if err != nil {
		return nil, nil, "", err
	}
	if info.IsDir() {
		rel = path.Join(rel, "index.html")
		if info, err = lstatPath(r, rel); err != nil {
			return nil, nil, "", err
		}
	}
	if !info.Mode().IsRegular() {
		return nil, nil, "", fmt.Errorf("%w: %s is not a file", ErrNotFound, rel)
	}
	f, err := r.OpenFile(rel, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, "", fmt.Errorf("%w: %s", ErrNotFound, rel)
		}
		return nil, nil, "", fmt.Errorf("%w: %s: %v", ErrBadPath, rel, err)
	}
	got, err := f.Stat()
	if err != nil || !got.Mode().IsRegular() || !os.SameFile(info, got) {
		_ = f.Close()
		return nil, nil, "", fmt.Errorf("%w: %s changed while opening", ErrBadPath, rel)
	}
	return f, got, rel, nil
}

// lstatPath Lstats every component of rel under r, refusing symlinks and
// non-directories on the way.
func lstatPath(r *os.Root, rel string) (fs.FileInfo, error) {
	parts := strings.Split(rel, "/")
	var info fs.FileInfo
	for i := range parts {
		p := strings.Join(parts[:i+1], "/")
		fi, err := r.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, rel)
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrBadPath, rel, err)
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return nil, fmt.Errorf("%w: %s is a symlink", ErrBadPath, p)
		}
		if i < len(parts)-1 && !fi.IsDir() {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, rel)
		}
		info = fi
	}
	return info, nil
}
