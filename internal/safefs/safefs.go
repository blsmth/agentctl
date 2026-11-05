// Package safefs writes files beneath a directory that may be partly
// controlled by someone else -- most importantly <repo>/.agentctl and a
// worktree's .claude/, both of which a cloned repository can pre-populate
// with tracked content, including symlinks.
//
// Every helper here refuses to traverse or replace a symlink anywhere from
// the caller-supplied root down to the target, so a repo that commits
// `.agentctl/settings/x.json -> ../../../../.zshenv` cannot redirect an
// agentctl write into the user's home directory.
//
// The root itself is Lstat-checked too (it must be a real directory, or
// absent so it can be created), but the components ABOVE the root are not:
// those are the user's own path to the project (e.g. macOS's /var ->
// /private/var), which agentctl does not get to second-guess.
package safefs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// ErrSymlink is returned (wrapped) when a path component or target is a
// symlink.
var ErrSymlink = errors.New("refusing to follow symlink")

// MkdirAll creates dir and any missing parents between root and dir with
// perm, refusing if root, dir, or any component in between exists as a
// symlink or as a non-directory. dir must be root or lie beneath it. A
// missing root is created with os.MkdirAll (its own ancestors are the user's
// path to the project and are not checked).
func MkdirAll(root, dir string, perm os.FileMode) error {
	root = filepath.Clean(root)
	dir = filepath.Clean(dir)
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("safefs: %s is not under %s", dir, root)
	}

	if err := ensureRealDir(root, perm, true); err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	cur := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		if err := ensureRealDir(cur, perm, false); err != nil {
			return err
		}
	}
	return nil
}

// ensureRealDir makes sure path is a real directory, creating it when
// missing. isRoot permits creating missing ancestors.
func ensureRealDir(path string, perm os.FileMode, isRoot bool) error {
	fi, err := os.Lstat(path)
	switch {
	case err == nil:
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("safefs: %s: %w", path, ErrSymlink)
		}
		if !fi.IsDir() {
			return fmt.Errorf("safefs: %s exists and is not a directory", path)
		}
		return nil
	case errors.Is(err, os.ErrNotExist):
		mk := os.Mkdir
		if isRoot {
			mk = os.MkdirAll
		}
		if err := mk(path, perm); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("safefs: mkdir %s: %w", path, err)
		}
		// Re-check: a concurrent creator may have planted a symlink between
		// our Lstat and Mkdir (Mkdir itself never follows one, but EEXIST
		// would have hidden it).
		fi, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("safefs: %s: %w", path, err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("safefs: %s: %w", path, ErrSymlink)
		}
		if !fi.IsDir() {
			return fmt.Errorf("safefs: %s exists and is not a directory", path)
		}
		return nil
	default:
		return fmt.Errorf("safefs: %s: %w", path, err)
	}
}

// checkTarget refuses a target path that exists as anything other than a
// regular file.
func checkTarget(path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("safefs: %s: %w", path, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("safefs: %s: %w", path, ErrSymlink)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("safefs: %s exists and is not a regular file", path)
	}
	return nil
}

// WriteFile atomically writes data to path (a temp file in the same
// directory, then rename) with mode perm. Missing directories between root
// and path's parent are created 0700. It refuses when any directory from
// root down to path's parent is a symlink, or when path itself exists as a
// symlink or non-regular file.
//
// Because the final step is a rename, an existing file's mode is replaced
// by perm, which is how callers tighten files written by older versions.
func WriteFile(root, path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := MkdirAll(root, dir, 0o700); err != nil {
		return err
	}
	if err := checkTarget(path); err != nil {
		return err
	}

	return replace(path, data, perm)
}

// ReplaceFile atomically writes data to path (temp file + rename) with mode
// perm, creating path's directory if needed, and refuses when path exists as
// a symlink or non-regular file. Unlike WriteFile it does not check the
// directory chain: use it for files in the user's own directories (e.g. the
// global config dir, which dotfile managers commonly symlink), where only a
// predictable temp name or a symlinked target would be a hazard.
func ReplaceFile(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("safefs: mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := checkTarget(path); err != nil {
		return err
	}
	return replace(path, data, perm)
}

// replace writes data to a fresh, unpredictably named temp file next to
// path and renames it over path.
func replace(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp") // O_EXCL: never follows a link
	if err != nil {
		return fmt.Errorf("safefs: create temp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(perm); err != nil {
		return fmt.Errorf("safefs: chmod %s: %w", tmpName, err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("safefs: write %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("safefs: close %s: %w", tmpName, err)
	}
	// rename(2) replaces a directory entry and never follows a symlink at
	// the destination, so even a link planted after checkTarget is replaced
	// rather than written through.
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("safefs: rename to %s: %w", path, err)
	}
	ok = true
	return nil
}

// OpenNoFollow opens path with flag|O_NOFOLLOW (and mode perm when
// creating), after checking that every directory from root down to path's
// parent is a real directory (creating missing ones 0700). A symlink at path
// itself makes the open fail with ELOOP, reported as ErrSymlink.
//
// Use it for lock files and append-only files, which cannot be written via
// temp+rename.
func OpenNoFollow(root, path string, flag int, perm os.FileMode) (*os.File, error) {
	if err := MkdirAll(root, filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, flag|syscall.O_NOFOLLOW, perm)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("safefs: %s: %w", path, ErrSymlink)
		}
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("safefs: %s is not a regular file", path)
	}
	return f, nil
}

// PrivateTempDir returns a per-user directory under os.TempDir()
// (agentctl-<uid>), created 0700. It refuses an existing path that is a
// symlink, is not a directory, or is owned by another user, so another local
// user cannot pre-create it to capture agentctl's state. An existing
// directory with looser permissions is tightened to 0700.
func PrivateTempDir() (string, error) {
	uid := os.Getuid()
	dir := filepath.Join(os.TempDir(), "agentctl-"+strconv.Itoa(uid))
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("safefs: mkdir %s: %w", dir, err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return "", fmt.Errorf("safefs: %s: %w", dir, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("safefs: %s: %w", dir, ErrSymlink)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("safefs: %s is not a directory", dir)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != uid {
		return "", fmt.Errorf("safefs: %s is owned by uid %d, not %d", dir, st.Uid, uid)
	}
	if fi.Mode().Perm() != 0o700 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return "", fmt.Errorf("safefs: chmod %s: %w", dir, err)
		}
	}
	return dir, nil
}

// CheckDir verifies, without creating anything, that root and every
// directory from root down to dir exist and are real directories (not
// symlinks). A missing component is reported wrapping os.ErrNotExist so
// readers can treat it as "nothing there yet". Use it before reading,
// listing, or deleting inside a directory a repository could have replaced
// with a symlink (deleting through one would remove files elsewhere).
func CheckDir(root, dir string) error {
	root = filepath.Clean(root)
	dir = filepath.Clean(dir)
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("safefs: %s is not under %s", dir, root)
	}
	cur := root
	parts := []string{"."}
	if rel != "." {
		parts = append(parts, strings.Split(rel, string(filepath.Separator))...)
	}
	for _, part := range parts {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if err != nil {
			return fmt.Errorf("safefs: %s: %w", cur, err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("safefs: %s: %w", cur, ErrSymlink)
		}
		if !fi.IsDir() {
			return fmt.Errorf("safefs: %s exists and is not a directory", cur)
		}
	}
	return nil
}

// IsSymlink reports whether path exists and is a symlink.
func IsSymlink(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}

// ChmodDir sets dir's mode through a descriptor opened with O_NOFOLLOW, so
// a symlink at dir is refused (ErrSymlink) instead of chmodding its target.
func ChmodDir(dir string, perm os.FileMode) error {
	f, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY, 0)
	if err != nil {
		// O_NOFOLLOW|O_DIRECTORY on a symlink yields ELOOP on Linux and
		// ENOTDIR on macOS.
		if errors.Is(err, syscall.ELOOP) || IsSymlink(dir) {
			return fmt.Errorf("safefs: %s: %w", dir, ErrSymlink)
		}
		return fmt.Errorf("safefs: chmod %s: %w", dir, err)
	}
	defer f.Close() //nolint:errcheck
	return f.Chmod(perm)
}
