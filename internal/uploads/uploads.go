// Package uploads stores images a phone attaches to a reply, in the project
// runtime dir where the agent can read them: <runtimeDir>/uploads/<window>/.
// Files are 0600 in 0700 directories and named by random IDs; the client's
// file name is never used.
package uploads

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/safefs"
)

// MaxAge is how long an upload is kept before Prune removes it.
const MaxAge = 7 * 24 * time.Hour

const dirName = "uploads"

var (
	windowRE = regexp.MustCompile(`^@?[0-9]{1,9}$`)
	// IDRE matches an ID Save returns: 128 random bits and a known extension.
	IDRE = regexp.MustCompile(`^[0-9a-f]{32}\.(png|jpg|gif|webp)$`)
)

// ErrUnsupported is returned for content that is not png, jpeg, gif or webp.
var ErrUnsupported = errors.New("not a png, jpeg, gif or webp image")

// ExtFor sniffs data and returns its file extension, or ErrUnsupported. The
// declared type and file name are never consulted.
func ExtFor(data []byte) (string, error) {
	switch http.DetectContentType(data) {
	case "image/png":
		return ".png", nil
	case "image/jpeg":
		return ".jpg", nil
	case "image/gif":
		return ".gif", nil
	case "image/webp":
		return ".webp", nil
	}
	return "", ErrUnsupported
}

// Dir is the upload directory of a window.
func Dir(runtimeDir, windowID string) string {
	return filepath.Join(runtimeDir, dirName, strings.TrimPrefix(windowID, "@"))
}

// Save stores data (already sniffed to extension ext) and returns its ID.
func Save(runtimeDir, windowID, ext string, data []byte) (string, error) {
	if runtimeDir == "" || !windowRE.MatchString(windowID) {
		return "", fmt.Errorf("uploads: bad target %q %q", runtimeDir, windowID)
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating name: %w", err)
	}
	id := hex.EncodeToString(b[:]) + ext
	if !IDRE.MatchString(id) {
		return "", fmt.Errorf("uploads: bad extension %q", ext)
	}
	if err := safefs.WriteFile(runtimeDir, filepath.Join(Dir(runtimeDir, windowID), id), data, 0o600); err != nil {
		return "", fmt.Errorf("writing upload: %w", err)
	}
	return id, nil
}

// Path returns the absolute path of upload id of a window, which must exist.
func Path(runtimeDir, windowID, id string) (string, error) {
	if !IDRE.MatchString(id) || runtimeDir == "" || !windowRE.MatchString(windowID) {
		return "", fmt.Errorf("invalid image %q", id)
	}
	p := filepath.Join(Dir(runtimeDir, windowID), id)
	if !filepath.IsAbs(p) {
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", err
		}
		p = abs
	}
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return "", fmt.Errorf("image %q not found", id)
	}
	return p, nil
}

// Prune removes uploads last modified more than maxAge before now, and the
// window directories that become empty. It returns the files removed.
func Prune(runtimeDir string, now time.Time, maxAge time.Duration) (int, error) {
	root := filepath.Join(runtimeDir, dirName)
	dirs, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	removed := 0
	var firstErr error
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		dir := filepath.Join(root, d.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			firstErr = errors.Join(firstErr, err)
			continue
		}
		left := len(files)
		for _, f := range files {
			fi, err := f.Info()
			if err != nil || !f.Type().IsRegular() || now.Sub(fi.ModTime()) <= maxAge {
				continue
			}
			if err := os.Remove(filepath.Join(dir, f.Name())); err != nil {
				firstErr = errors.Join(firstErr, err)
				continue
			}
			removed++
			left--
		}
		if left == 0 {
			_ = os.Remove(dir)
		}
	}
	return removed, firstErr
}
