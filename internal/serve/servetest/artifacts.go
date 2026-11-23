package servetest

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

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
