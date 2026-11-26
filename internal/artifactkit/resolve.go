package artifactkit

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	// MaxImageBytes is the largest single image file Resolve inlines.
	MaxImageBytes = 8 << 20
	// MaxImagesTotal is the most image bytes (before base64) one spec may inline.
	MaxImagesTotal = 20 << 20
)

// imageTypes are the content types an image block accepts.
var imageTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// ResolveImages reads every local `src` of an image block and rewrites it to a
// base64 data: URI, so the page stays one self-contained file. Relative paths
// resolve against baseDir and "~/" against the home directory; https:// URLs
// and data: URIs are left as they are. maxTotal caps the bytes read across all
// images (0 means MaxImagesTotal). data must already pass Validate; the spec is
// returned unchanged when it has no local images.
func ResolveImages(data []byte, baseDir string, maxTotal int64) ([]byte, error) {
	if maxTotal <= 0 || maxTotal > MaxImagesTotal {
		maxTotal = MaxImagesTotal
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var spec map[string]any
	if err := dec.Decode(&spec); err != nil {
		return nil, Errors{{"spec", "not valid JSON: " + err.Error()}}
	}
	r := &resolver{base: baseDir, max: maxTotal}
	blocks, _ := spec["blocks"].([]any)
	for i, b := range blocks {
		m, _ := b.(map[string]any)
		if m == nil || m["type"] != "image" {
			continue
		}
		p := "blocks[" + strconv.Itoa(i) + "]"
		r.inline(m, p)
		imgs, _ := m["images"].([]any)
		for j, im := range imgs {
			if mm, ok := im.(map[string]any); ok {
				r.inline(mm, idx(p+".images", j))
			}
		}
	}
	if len(r.errs) > 0 {
		return nil, r.errs
	}
	if !r.changed {
		return data, nil
	}
	return json.Marshal(spec)
}

type resolver struct {
	base    string
	max     int64
	total   int64
	changed bool
	errs    Errors
}

// inline rewrites m["src"] when it is a local path.
func (r *resolver) inline(m map[string]any, path string) {
	src, _ := m["src"].(string)
	if src == "" || strings.HasPrefix(src, "https://") || strings.HasPrefix(src, "data:") {
		return
	}
	p := path + ".src"
	uri, err := r.load(src)
	if err != nil {
		r.errs = append(r.errs, Error{p, err.Error()})
		return
	}
	m["src"] = uri
	r.changed = true
}

func (r *resolver) load(src string) (string, error) {
	file := src
	if rest, ok := strings.CutPrefix(src, "~/"); ok {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolving ~ in %s: %w", src, err)
		}
		file = filepath.Join(home, rest)
	} else if !filepath.IsAbs(file) {
		file = filepath.Join(r.base, file)
	}
	f, err := os.Open(file)
	if err != nil {
		return "", fmt.Errorf("reading image %s: %w", file, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("reading image %s: %w", file, err)
	}
	if fi.IsDir() {
		return "", fmt.Errorf("%s is a directory, not an image", file)
	}
	if fi.Size() > MaxImageBytes {
		return "", fmt.Errorf("%s is %s, over the %s per-image limit", file, human(fi.Size()), human(MaxImageBytes))
	}
	if r.total+fi.Size() > r.max {
		return "", fmt.Errorf("%s (%s) would bring the spec's images to %s, over the %s total limit", file, human(fi.Size()), human(r.total+fi.Size()), human(r.max))
	}
	raw, err := io.ReadAll(io.LimitReader(f, MaxImageBytes+1))
	if err != nil {
		return "", fmt.Errorf("reading image %s: %w", file, err)
	}
	if len(raw) > MaxImageBytes {
		return "", fmt.Errorf("%s is over the %s per-image limit", file, human(MaxImageBytes))
	}
	ct := sniffImage(raw)
	if !imageTypes[ct] {
		return "", fmt.Errorf("%s is not supported (png, jpeg, gif, webp): %s", ct, file)
	}
	r.total += int64(len(raw))
	return "data:" + ct + ";base64," + base64.StdEncoding.EncodeToString(raw), nil
}

// sniffImage returns the content type of raw from its bytes. SVG is not known
// to http.DetectContentType, so it is recognised by its root element to give
// the clear "image/svg+xml" error.
func sniffImage(raw []byte) string {
	ct := http.DetectContentType(raw)
	if i := strings.Index(ct, ";"); i >= 0 {
		ct = ct[:i]
	}
	if strings.HasPrefix(ct, "text/") {
		head := raw
		if len(head) > 2048 {
			head = head[:2048]
		}
		if bytes.Contains(bytes.ToLower(head), []byte("<svg")) {
			return "image/svg+xml"
		}
	}
	return ct
}

func human(n int64) string {
	if n >= 1<<20 {
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
}
