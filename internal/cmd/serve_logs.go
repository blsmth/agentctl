package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/blsmth/agentctl/internal/serve"
)

// ServeLogsOptions configures `agentctl serve logs`.
type ServeLogsOptions struct {
	// Lines is how many trailing lines to print (default 50 when <= 0).
	Lines int
	// Audit reads audit.jsonl instead of serve.log.
	Audit bool
	// Follow keeps printing lines as they are appended until ctx ends.
	Follow bool
}

// serveLogsPoll is how often --follow checks the file for new data.
var serveLogsPoll = 500 * time.Millisecond

// RunServeLogs prints the tail of serve.log (or audit.jsonl). It is the
// read-only way for agents to see serve's logs: the Bash validator blocks
// direct access to the serve state dir.
func RunServeLogs(ctx context.Context, w io.Writer, opts ServeLogsOptions) error {
	dir, err := serve.Dir()
	if err != nil {
		return err
	}
	return printServeLogs(ctx, w, dir, opts)
}

func printServeLogs(ctx context.Context, w io.Writer, dir string, opts ServeLogsOptions) error {
	name := serve.LogFile
	if opts.Audit {
		name = serve.AuditFile
	}
	n := opts.Lines
	if n <= 0 {
		n = 50
	}
	path := filepath.Join(dir, name)
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		if !opts.Follow {
			return fmt.Errorf("%s does not exist yet", path)
		}
	} else if err != nil {
		return err
	}
	var offset int64
	if f != nil {
		tail, end, err := tailLines(f, n)
		_ = f.Close()
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}
		if _, err := w.Write(tail); err != nil {
			return err
		}
		offset = end
	}
	if !opts.Follow {
		return nil
	}
	return followFile(ctx, w, path, offset)
}

// tailLines returns the last n lines of f and the file size they end at.
func tailLines(f *os.File, n int) ([]byte, int64, error) {
	fi, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	size := fi.Size()
	const chunk = 64 << 10
	var buf []byte
	pos := size
	for pos > 0 && bytes.Count(buf, []byte{'\n'}) <= n {
		step := int64(chunk)
		if pos < step {
			step = pos
		}
		pos -= step
		b := make([]byte, step)
		if _, err := f.ReadAt(b, pos); err != nil && !errors.Is(err, io.EOF) {
			return nil, 0, err
		}
		buf = append(b, buf...)
	}
	lines := bytes.SplitAfter(buf, []byte{'\n'})
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	if pos > 0 && len(lines) > 0 {
		lines = lines[1:] // starts mid-line
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return bytes.Join(lines, nil), size, nil
}

// followFile prints data appended to path after offset until ctx ends. A
// file that shrank (log rotation) is read again from the start.
func followFile(ctx context.Context, w io.Writer, path string, offset int64) error {
	t := time.NewTicker(serveLogsPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		fi, err := os.Stat(path)
		if err != nil {
			continue
		}
		if fi.Size() < offset {
			offset = 0
		}
		if fi.Size() == offset {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		if _, err := f.Seek(offset, io.SeekStart); err == nil {
			written, cerr := io.Copy(w, f)
			offset += written
			if cerr != nil {
				_ = f.Close()
				return cerr
			}
		}
		_ = f.Close()
	}
}
