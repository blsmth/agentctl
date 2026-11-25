package servetest

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/blsmth/agentctl/internal/artifactpdf"
	"github.com/blsmth/agentctl/internal/artifacts"
)

// ArtifactRev is one revision a test seeds: files by relative path (a value
// starting "base64:" is decoded, for images) and the entry to open.
type ArtifactRev struct {
	Files map[string]string `json:"files"`
	Entry string            `json:"entry"`
	Note  string            `json:"note"`
}

// ArtifactSpec is one artifact a test seeds through PUT /__test/artifacts.
// Revisions are published in order (the first with Add, the rest with
// Update); Kind is inferred from the entry when empty.
type ArtifactSpec struct {
	Title       string        `json:"title"`
	Description string        `json:"description"`
	Kind        string        `json:"kind"`
	Pin         bool          `json:"pin"`
	Project     string        `json:"project"`
	ProjectDir  string        `json:"project_dir"`
	Agent       string        `json:"agent"`
	WindowID    string        `json:"window_id"`
	Session     string        `json:"session"`
	Revisions   []ArtifactRev `json:"revisions"`
}

// artifactWorld is the harness's own artifact store, under its state dir.
type artifactWorld struct {
	root  string
	store *artifacts.Store
	mu    sync.Mutex
}

func newArtifactWorld(root string) *artifactWorld {
	dir := filepath.Join(root, "artifacts")
	return &artifactWorld{root: root, store: &artifacts.Store{Dir: func() (string, error) { return dir, nil }}}
}

// set replaces every artifact with specs and returns their ids in order.
func (a *artifactWorld) set(specs []ArtifactSpec) ([]string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.clearLocked(); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(specs))
	for i, sp := range specs {
		if len(sp.Revisions) == 0 {
			return nil, fmt.Errorf("artifact %d (%q): no revisions", i, sp.Title)
		}
		var id string
		for n, rev := range sp.Revisions {
			dir, err := a.stage(i, n, rev)
			if err != nil {
				return nil, err
			}
			if n == 0 {
				m, err := a.store.Add(artifacts.AddOptions{
					Source: artifacts.Source{Path: dir}, Title: sp.Title, Description: sp.Description, Kind: sp.Kind, Entry: rev.Entry, Pin: sp.Pin,
					NoNotify: true,
					Origin:   artifacts.Origin{ProjectDir: sp.ProjectDir, Project: sp.Project, Agent: sp.Agent, WindowID: sp.WindowID, Session: sp.Session},
				})
				if err != nil {
					return nil, fmt.Errorf("adding %q: %w", sp.Title, err)
				}
				id = m.ID
				continue
			}
			if _, err := a.store.Update(id, artifacts.UpdateOptions{Source: artifacts.Source{Path: dir}, Note: rev.Note}); err != nil {
				return nil, fmt.Errorf("updating %q: %w", sp.Title, err)
			}
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (a *artifactWorld) clearLocked() error {
	list, err := a.store.List(artifacts.Filter{})
	if err != nil {
		return err
	}
	for _, m := range list {
		if _, err := a.store.Remove(m.ID); err != nil {
			return err
		}
	}
	return nil
}

func (a *artifactWorld) clear() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.clearLocked()
}

// stage writes a revision's files under a scratch dir and returns it.
func (a *artifactWorld) stage(i, n int, rev ArtifactRev) (string, error) {
	dir := filepath.Join(a.root, "artifact-src", fmt.Sprintf("%d-%d", i, n))
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	for name, body := range rev.Files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if !strings.HasPrefix(p, dir+string(filepath.Separator)) {
			return "", fmt.Errorf("file %q escapes the artifact dir", name)
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return "", err
		}
		data := []byte(body)
		if b, ok := strings.CutPrefix(body, "base64:"); ok {
			var err error
			if data, err = base64.StdEncoding.DecodeString(b); err != nil {
				return "", fmt.Errorf("file %q: %w", name, err)
			}
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func (h *Harness) handleSetArtifacts(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Artifacts []ArtifactSpec `json:"artifacts"`
	}
	if !decode(w, r, &body) {
		return
	}
	ids, err := h.artifacts.set(body.Artifacts)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]string{"ids": ids})
}

// renderPDF stands in for artifactpdf.Render: no browser is launched. It
// writes a tiny twelve-page PDF (tinyPDF) after a short pause so
// the viewer's busy state can be seen. An artifact whose title contains
// "[no browser]" fails as a host without Chrome would.
func (a *artifactWorld) renderPDF(ctx context.Context, store *artifacts.Store, id string, rev int, w io.Writer) error {
	m, err := store.Get(id)
	if err != nil {
		return err
	}
	if strings.Contains(m.Title, "[no browser]") {
		return fmt.Errorf("%w: servetest", artifactpdf.ErrNoBrowser)
	}
	select {
	case <-time.After(300 * time.Millisecond):
	case <-ctx.Done():
		return ctx.Err()
	}
	_, err = w.Write(tinyPDF(12))
	return err
}

// tinyPDF is a real, valid PDF of n 200x280 point pages, so the viewer's
// PDF.js can open and draw it. Page k (from 0) is white with its left half
// filled in the k-th colour of pdfColors: pixel checks can tell the pages
// apart. It uses no fonts, so no font data is needed to draw it.
func tinyPDF(n int) []byte {
	var b strings.Builder
	var offs []int
	obj := func(body string) {
		offs = append(offs, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", len(offs), body)
	}
	b.WriteString("%PDF-1.4\n")
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	kids := make([]string, n)
	for k := range kids {
		kids[k] = fmt.Sprintf("%d 0 R", 3+2*k)
	}
	obj(fmt.Sprintf("<< /Type /Pages /Count %d /Kids [%s] >>", n, strings.Join(kids, " ")))
	for k := 0; k < n; k++ {
		obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 280] /Contents %d 0 R >>", 4+2*k))
		c := pdfColors[k%len(pdfColors)]
		stream := fmt.Sprintf("%s rg 0 0 100 280 re f", c)
		obj(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offs)+1)
	for _, o := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offs)+1, xref)
	return []byte(b.String())
}

// pdfColors are the fill colours (RGB operands) of tinyPDF's pages: red,
// green, blue.
var pdfColors = []string{"1 0 0", "0 1 0", "0 0 1"}
