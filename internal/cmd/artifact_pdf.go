package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
)

// ArtifactPDFOptions are the flags of `agentctl artifact pdf`.
type ArtifactPDFOptions struct {
	ID    string
	Rev   int
	Out   string
	Force bool
}

// RunArtifactPDF exports an artifact revision as a PDF.
func RunArtifactPDF(w io.Writer, o ArtifactPDFOptions) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return defaultArtifactEnv.exportPDF(ctx, w, o)
}

// exportPDF renders the revision with a headless browser and writes it to the
// output path (default ./<slug>.pdf), or to w for "-". An existing file is
// kept unless Force. Read-only on the store.
func (e artifactEnv) exportPDF(ctx context.Context, w io.Writer, o ArtifactPDFOptions) error {
	m, err := e.revMeta(o.ID, o.Rev)
	if err != nil {
		return err
	}
	out := o.Out
	if out != "-" {
		if out, err = pdfPath(out, m.Slug, m.ID); err != nil {
			return err
		}
		// Fail before starting a browser; the O_EXCL open below closes the race.
		if _, err := os.Lstat(out); err == nil && !o.Force {
			return fmt.Errorf("%s already exists (use --force to overwrite, or --out to write elsewhere)", out)
		}
	}
	var buf bytes.Buffer
	if err := e.pdf(ctx, e.store(), m.ID, o.Rev, &buf); err != nil {
		return fmt.Errorf("exporting %s to PDF: %w", m.ID, err)
	}
	if o.Out == "-" {
		_, err := w.Write(buf.Bytes())
		return err
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if o.Force {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(out, flags, 0o644)
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("%s already exists (use --force to overwrite)", out)
	}
	if err != nil {
		return fmt.Errorf("writing PDF: %w", err)
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		_ = f.Close()
		return fmt.Errorf("writing PDF: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("writing PDF: %w", err)
	}
	fmt.Fprintln(w, out)
	return nil
}

// pdfPath resolves --out: empty means ./<slug>.pdf in the cwd (the first six
// characters of the id when the artifact has no slug), an existing directory
// gets that name inside it, anything else is the file itself.
func pdfPath(out, slug, id string) (string, error) {
	name := slug
	if name == "" {
		name = id[:min(6, len(id))]
	}
	name += ".pdf"
	switch {
	case out == "":
		out = name
	case dirExists(out):
		out = filepath.Join(out, name)
	}
	abs, err := filepath.Abs(out)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", out, err)
	}
	return abs, nil
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
