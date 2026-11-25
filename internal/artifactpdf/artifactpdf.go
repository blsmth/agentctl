// Package artifactpdf exports a stored artifact revision as a paged US Letter
// PDF. It serves the revision on a throwaway loopback HTTP server, exactly as
// `agentctl serve` would (see internal/artifactpage), and has a headless
// Chrome, Chromium or Edge on the host print it. It needs no running serve
// and no Go dependency: the browser is just exec'd.
//
// Callers: `agentctl artifact pdf` and the serve route behind the viewer's
// PDF button.
package artifactpdf

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/blsmth/agentctl/internal/artifacts"
)

// Timeout bounds one export; the browser is killed when it passes.
const Timeout = 60 * time.Second

// virtualTimeBudgetMS is how long the page may run before printing, so
// Chart.js and other CDN scripts finish loading and drawing. An offline host
// makes those requests fail fast and the page prints as rendered.
const virtualTimeBudgetMS = 15000

// pageTimeoutMS stops a page still loading after this long (a script on a host
// that never answers would otherwise hold the print until Timeout kills the
// browser): the page is printed as rendered so far.
const pageTimeoutMS = 30000

// stderrTail is how much of the browser's stderr an error carries.
const stderrTail = 2048

// runBrowser runs the browser binary with args to completion and returns its
// stderr. It is the exec seam: tests replace it.
var runBrowser = execBrowser

// Render writes the PDF of revision rev (0 = the latest) of artifact id to w.
// It fails before starting anything when the id or revision is unknown or no
// browser is installed, and kills the browser when ctx ends or Timeout passes.
func Render(ctx context.Context, store *artifacts.Store, id string, rev int, w io.Writer) error {
	m, err := store.Get(id)
	if err != nil {
		return err
	}
	if len(m.Revs) == 0 {
		return fmt.Errorf("artifact %s has no revisions", id)
	}
	if rev > 0 && !m.HasRev(rev) {
		return fmt.Errorf("artifact %s has no revision %d (have %d..%d)", id, rev, m.Revs[0].N, m.Latest())
	}
	bin, err := FindBrowser()
	if err != nil {
		return err
	}
	entryURL, stop, err := serveSource(store, m, rev)
	if err != nil {
		return err
	}
	defer stop()

	dir, err := os.MkdirTemp("", "agentctl-pdf-*")
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(dir) //nolint:errcheck
	out := filepath.Join(dir, "out.pdf")

	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	stderr, runErr := runBrowser(ctx, bin, browserArgs(filepath.Join(dir, "profile"), out, entryURL))

	pdf, readErr := os.ReadFile(out)
	if readErr != nil || len(pdf) == 0 {
		return failure(ctx, runErr, stderr)
	}
	// Chrome writes the file, then may take a moment to exit; a file that
	// reached disk is the result even when the process was killed after it.
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) || !bytes.Contains(pdf[max(0, len(pdf)-1024):], []byte("%%EOF")) {
		return fmt.Errorf("browser wrote %d bytes that are not a complete PDF%s", len(pdf), tail(stderr))
	}
	if _, err := w.Write(pdf); err != nil {
		return fmt.Errorf("writing PDF: %w", err)
	}
	return nil
}

// browserArgs is the headless print invocation. The fresh user-data-dir keeps
// it from joining (or locking out) a running browser. Light is forced twice:
// preferredColorScheme=1 is Blink's "light" (0 is dark), so a dark system does
// not darken html artifacts, and the kit and page print CSS pin the light
// palette under @media print.
func browserArgs(profile, out, url string) []string {
	return []string{
		"--headless=new",
		"--disable-gpu",
		"--no-first-run",
		"--no-default-browser-check",
		"--no-pdf-header-footer",
		"--hide-scrollbars",
		"--blink-settings=preferredColorScheme=1",
		"--user-data-dir=" + profile,
		fmt.Sprintf("--virtual-time-budget=%d", virtualTimeBudgetMS),
		fmt.Sprintf("--timeout=%d", pageTimeoutMS),
		"--print-to-pdf=" + out,
		url,
	}
}

// failure explains a run that produced no PDF.
func failure(ctx context.Context, runErr error, stderr string) error {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return fmt.Errorf("browser did not finish within %s%s", Timeout, tail(stderr))
	case ctx.Err() != nil:
		return fmt.Errorf("pdf export canceled: %w", ctx.Err())
	case runErr != nil:
		return fmt.Errorf("browser failed: %w%s", runErr, tail(stderr))
	}
	return fmt.Errorf("browser produced no PDF%s", tail(stderr))
}

// tail formats the end of the browser's stderr for an error message.
func tail(stderr string) string {
	// Chrome logs its updater at VERBOSE level on some hosts: noise, not cause.
	var keep []string
	for _, l := range strings.Split(stderr, "\n") {
		if !strings.Contains(l, ":VERBOSE") {
			keep = append(keep, l)
		}
	}
	s := strings.TrimSpace(strings.Join(keep, "\n"))
	if s == "" {
		return ""
	}
	if len(s) > stderrTail {
		s = "…" + s[len(s)-stderrTail:]
	}
	return ":\n" + s
}

// execBrowser runs bin in its own process group, so a kill on cancel or
// timeout takes its renderer and helper processes with it.
func execBrowser(ctx context.Context, bin string, args []string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 3 * time.Second
	var buf tailBuffer
	cmd.Stderr = &buf
	err := cmd.Run()
	// Sweep helpers that outlived the browser; ESRCH (group gone) is the norm.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	return buf.String(), err
}

// tailBuffer keeps the last stderrTail*2 bytes written to it.
type tailBuffer struct{ b []byte }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if n := len(t.b) - 2*stderrTail; n > 0 {
		t.b = t.b[n:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return string(t.b) }
