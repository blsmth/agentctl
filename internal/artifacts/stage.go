package artifacts

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"
)

// Source is what to publish: a file or directory at Path, or the bytes of
// Stdin saved under the entry name.
type Source struct {
	Path  string
	Stdin io.Reader
}

// staged is a validated, copied revision waiting in the staging area.
type staged struct {
	dir   string // staging dir; content is in dir/content
	entry string // slash-separated, relative to the content root
	kind  string
	title string // inferred
	size  int64
}

func (st *staged) content() string { return filepath.Join(st.dir, "content") }

// defaultEntry is the stdin entry name for a kind given without --entry.
var defaultEntry = map[string]string{
	KindHTML: "index.html", KindMarkdown: "report.md", KindText: "output.txt",
}

// stage validates src against the limits and copies it into a fresh dir under
// <root>/.tmp. Nothing is visible in the store until the caller renames it.
// kind and entry are the user's overrides; prevEntry is the artifact's
// current entry (an update from stdin reuses it).
func stage(root string, src Source, kind, entry, prevEntry string) (*staged, error) {
	if kind != "" && !validKind(kind) {
		return nil, fmt.Errorf("unknown kind %q (want html, markdown, image or text)", kind)
	}
	if entry != "" {
		c, err := cleanRel(entry)
		if err != nil {
			return nil, fmt.Errorf("--entry: %w", err)
		}
		entry = c
	}
	tmp := filepath.Join(root, ".tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return nil, fmt.Errorf("creating staging dir: %w", err)
	}
	dir := filepath.Join(tmp, "stage-"+NewID())
	if err := os.MkdirAll(filepath.Join(dir, "content"), 0o700); err != nil {
		return nil, fmt.Errorf("creating staging dir: %w", err)
	}
	st := &staged{dir: dir}
	fail := func(err error) (*staged, error) {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	srcName, err := st.copyIn(src, kind, entry, prevEntry)
	if err != nil {
		return fail(err)
	}
	if err := st.resolve(kind, entry, srcName); err != nil {
		return fail(err)
	}
	return st, nil
}

// copyIn fills dir/content and returns the display name of the source (the
// title fallback) and, via st.entry, a preselected entry for single files.
func (st *staged) copyIn(src Source, kind, entry, prevEntry string) (string, error) {
	if src.Stdin != nil {
		name := entry
		if name == "" {
			name = prevEntry
		}
		if name == "" {
			name = defaultEntry[kind]
		}
		if name == "" {
			return "", errors.New("reading from stdin needs --entry <name.ext> (e.g. --entry report.html) or --kind html|markdown|text")
		}
		data, err := io.ReadAll(io.LimitReader(src.Stdin, MaxRevBytes+1))
		if err != nil {
			return "", fmt.Errorf("reading stdin: %w", err)
		}
		if int64(len(data)) > MaxRevBytes {
			return "", fmt.Errorf("stdin exceeds the %s per-revision limit", humanBytes(MaxRevBytes))
		}
		if len(data) == 0 {
			return "", errors.New("stdin is empty")
		}
		dst := filepath.Join(st.content(), filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return "", err
		}
		if err := os.WriteFile(dst, data, 0o600); err != nil {
			return "", err
		}
		st.size = int64(len(data))
		st.entry = name
		return path.Base(name), nil
	}

	p := filepath.Clean(src.Path)
	info, err := os.Lstat(p)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", src.Path, err)
	}
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		return "", fmt.Errorf("%s is a symlink; symlinks are not followed (pass the real path)", src.Path)
	case info.Mode().IsRegular():
		if info.Size() > MaxRevBytes {
			return "", fmt.Errorf("%s is %s, over the %s per-revision limit", src.Path, humanBytes(info.Size()), humanBytes(MaxRevBytes))
		}
		name := filepath.Base(p)
		if err := copyFile(p, filepath.Join(st.content(), name), info.Size()); err != nil {
			return "", err
		}
		st.size = info.Size()
		st.entry = name
		return name, nil
	case info.IsDir():
		return filepath.Base(p), st.copyDir(p)
	}
	return "", fmt.Errorf("%s is not a regular file or directory", src.Path)
}

// copyDir scans src first (limits, symlinks, special files) and only then
// copies, so a rejected tree costs no I/O beyond the walk, and the walk stops
// at the file limit.
func (st *staged) copyDir(src string) error {
	type item struct {
		rel  string
		size int64
	}
	var files []item
	var total int64
	errTooMany := errors.New("too many files")
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			return fmt.Errorf("%s contains a symlink (%s); symlinks are not followed", src, rel)
		case d.IsDir():
			return nil
		case !d.Type().IsRegular():
			return fmt.Errorf("%s contains a special file (%s)", src, rel)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if len(files) >= MaxFiles {
			return errTooMany
		}
		total += info.Size()
		if total > MaxRevBytes {
			return fmt.Errorf("%s is over the %s per-revision limit (stopped at %s)", src, humanBytes(MaxRevBytes), rel)
		}
		files = append(files, item{rel, info.Size()})
		return nil
	})
	if errors.Is(err, errTooMany) {
		return fmt.Errorf("%s has more than %d files (the per-artifact limit is %d); publish the built output, not the whole tree", src, MaxFiles, MaxFiles)
	}
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("%s has no files", src)
	}
	for _, f := range files {
		if err := copyFile(filepath.Join(src, f.rel), filepath.Join(st.content(), f.rel), f.size); err != nil {
			return err
		}
	}
	st.size = total
	return nil
}

// copyFile copies src to dst (0600) without following a symlink at src, and
// never writes more than max bytes.
func copyFile(src, dst string, max int64) error {
	in, err := os.OpenFile(src, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("reading %s: %w", src, err)
	}
	defer in.Close() //nolint:errcheck
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	// +1 so a file that grew while copying is noticed.
	n, cerr := io.Copy(out, io.LimitReader(in, max+1))
	if err := errors.Join(cerr, out.Close()); err != nil {
		return fmt.Errorf("copying %s: %w", src, err)
	}
	if n > max {
		return fmt.Errorf("%s changed while being copied", src)
	}
	return nil
}

// resolve picks the entry file and kind and infers the title.
func (st *staged) resolve(kind, entry, srcName string) error {
	if entry != "" {
		st.entry = entry
	}
	if st.entry == "" {
		e, err := pickEntry(st.content())
		if err != nil {
			return err
		}
		st.entry = e
	}
	entryPath := filepath.Join(st.content(), filepath.FromSlash(st.entry))
	if info, err := os.Lstat(entryPath); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("entry %q is not a file in the artifact", st.entry)
	}
	if kind == "" {
		kind = kindForExt(st.entry)
	}
	if kind == "" {
		// Unknown extension: text if it reads as text, else refuse.
		if !looksLikeText(entryPath) {
			return fmt.Errorf("%s has an unknown extension and is not valid UTF-8 text; pass --kind or use a known type (.html .md .png .jpg .gif .webp .svg .txt .log .json .csv)", st.entry)
		}
		kind = KindText
	}
	if (kind == KindText || kind == KindMarkdown) && !looksLikeText(entryPath) {
		return fmt.Errorf("%s is not valid UTF-8 text, so it cannot be published as %s", st.entry, kind)
	}
	st.kind = kind
	st.title = inferTitle(kind, entryPath, srcName)
	return nil
}

// pickEntry chooses the file to open for a directory: index.html, else the
// only file.
func pickEntry(content string) (string, error) {
	if info, err := os.Lstat(filepath.Join(content, "index.html")); err == nil && info.Mode().IsRegular() {
		return "index.html", nil
	}
	var all []string
	_ = filepath.WalkDir(content, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			rel, _ := filepath.Rel(content, p)
			all = append(all, filepath.ToSlash(rel))
		}
		return nil
	})
	if len(all) == 1 {
		return all[0], nil
	}
	return "", errors.New("directory has no index.html; pass --entry <file> to say which file to open")
}

// cleanRel validates a relative slash path: no "..", not absolute.
func cleanRel(p string) (string, error) {
	p = strings.ReplaceAll(p, "\\", "/")
	if p == "" || strings.HasPrefix(p, "/") || strings.ContainsRune(p, 0) {
		return "", fmt.Errorf("%q must be a relative path inside the artifact", p)
	}
	c := path.Clean(p)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return "", fmt.Errorf("%q must stay inside the artifact (no ..)", p)
	}
	return c, nil
}

// looksLikeText reports whether the file is valid UTF-8 with no NUL byte.
func looksLikeText(p string) bool {
	data, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	return utf8.Valid(data) && !strings.ContainsRune(string(data), 0)
}
