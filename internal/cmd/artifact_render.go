package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/blsmth/agentctl/internal/artifactkit"
	"github.com/blsmth/agentctl/internal/artifacts"
)

// ArtifactRenderOptions are the flags of `agentctl artifact render`.
type ArtifactRenderOptions struct {
	Path, Title, Description, Update, Note, Out string
	NoNotify, Pin, JSON                         bool
}

// RunArtifactRender validates a kit spec, builds its page and publishes it.
func RunArtifactRender(w io.Writer, o ArtifactRenderOptions) error {
	return defaultArtifactEnv.render(w, o)
}

// render builds the HTML for a spec file and hands it to the same add/update
// code path as `artifact add` / `artifact update`. An invalid spec is never
// published; with Out the page is only written to a file.
func (e artifactEnv) render(w io.Writer, o ArtifactRenderOptions) error {
	switch {
	case o.Out != "" && o.Update != "":
		return errors.New("--out only writes the file; it cannot be combined with --update")
	case o.Note != "" && o.Update == "":
		return errors.New("--note describes a revision; it needs --update <id>")
	case o.Update != "" && (o.Pin || o.NoNotify || o.Description != ""):
		return errors.New("--pin, --no-notify and --description apply to a new artifact; they cannot be combined with --update")
	}
	data, err := e.readSpec(o.Path)
	if err != nil {
		return err
	}
	page, err := artifactkit.Render(data)
	if err != nil {
		return err
	}
	title := o.Title
	if title == "" {
		title = page.Title
	}
	if o.Out != "" {
		return writeRendered(w, o.Out, page.HTML, title, o.JSON)
	}
	if o.Update != "" {
		cur, err := e.store().Get(o.Update)
		if err != nil {
			return err
		}
		if cur.Kind != artifacts.KindHTML {
			return fmt.Errorf("artifact %s is a %s artifact; render publishes html", cur.ID, cur.Kind)
		}
	}
	pub := e
	pub.stdin = bytes.NewReader(page.HTML)
	if o.Update != "" {
		return pub.update(w, ArtifactUpdateOptions{ID: o.Update, Path: "-", Note: o.Note, Title: title, JSON: o.JSON})
	}
	return pub.add(w, ArtifactAddOptions{
		Path: "-", Title: title, Description: o.Description, Kind: artifacts.KindHTML, Entry: "index.html",
		Pin: o.Pin, NoNotify: o.NoNotify, JSON: o.JSON,
	})
}

func (e artifactEnv) readSpec(p string) ([]byte, error) {
	switch p {
	case "":
		return nil, errors.New("a spec .json file (or - for stdin) is required")
	case "-":
		data, err := io.ReadAll(e.stdin)
		if err != nil {
			return nil, fmt.Errorf("reading spec from stdin: %w", err)
		}
		return data, nil
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("reading spec: %w", err)
	}
	return data, nil
}

func writeRendered(w io.Writer, out string, html []byte, title string, jsonOut bool) error {
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}
	if err := os.WriteFile(out, html, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", out, err)
	}
	if jsonOut {
		return writeJSON(w, map[string]any{"out": out, "title": title, "bytes": len(html), "published": false})
	}
	_, err := fmt.Fprintf(w, "Wrote %q to %s (not published)\n", title, out)
	return err
}
