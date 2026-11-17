package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/serve"
)

func writeLines(t *testing.T, path string, from, to int) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	for i := from; i <= to; i++ {
		fmt.Fprintf(f, "line %d\n", i)
	}
}

func TestServeLogsTail(t *testing.T) {
	tests := []struct {
		name  string
		total int
		n     int
		audit bool
		want  string
	}{
		{"last 3", 10, 3, false, "line 8\nline 9\nline 10\n"},
		{"fewer than n", 2, 5, false, "line 1\nline 2\n"},
		{"default n", 60, 0, false, "line 11\n"}, // first of the last 50
		{"audit", 4, 2, true, "line 3\nline 4\n"},
		{"many chunks", 20000, 2, false, "line 19999\nline 20000\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			name := serve.LogFile
			if tc.audit {
				name = serve.AuditFile
				writeLines(t, filepath.Join(dir, serve.LogFile), 100, 101) // must not be read
			}
			writeLines(t, filepath.Join(dir, name), 1, tc.total)
			var out bytes.Buffer
			if err := printServeLogs(context.Background(), &out, dir, ServeLogsOptions{Lines: tc.n, Audit: tc.audit}); err != nil {
				t.Fatal(err)
			}
			got := out.String()
			if tc.name == "default n" {
				if !strings.HasPrefix(got, tc.want) || strings.Count(got, "\n") != 50 {
					t.Fatalf("got %d lines starting %q", strings.Count(got, "\n"), got[:20])
				}
				return
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestServeLogsMissing(t *testing.T) {
	if err := printServeLogs(context.Background(), &bytes.Buffer{}, t.TempDir(), ServeLogsOptions{}); err == nil {
		t.Fatal("missing log should be an error")
	}
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestServeLogsFollow(t *testing.T) {
	orig := serveLogsPoll
	serveLogsPoll = 5 * time.Millisecond
	t.Cleanup(func() { serveLogsPoll = orig })
	dir := t.TempDir()
	path := filepath.Join(dir, serve.LogFile)
	writeLines(t, path, 1, 3)
	ctx, cancel := context.WithCancel(context.Background())
	out := &syncBuf{}
	done := make(chan error, 1)
	go func() { done <- printServeLogs(ctx, out, dir, ServeLogsOptions{Lines: 1, Follow: true}) }()
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(out.String(), "line 3") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	writeLines(t, path, 4, 5)
	for !strings.Contains(out.String(), "line 5") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "line 3\nline 4\nline 5\n" {
		t.Fatalf("got %q", got)
	}
}
