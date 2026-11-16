package cmd

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func pipeWith(t *testing.T, content string) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(content); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func TestReadAPIKey(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		fromStdin bool
		want      string
		wantErr   string
	}{
		{"stdin line", "sk-abc\nignored\n", true, "sk-abc", ""},
		{"stdin trimmed, no newline", "  sk-abc  ", true, "sk-abc", ""},
		{"stdin empty", "\n", true, "", "no key entered"},
		{"pipe without --stdin", "sk-abc\n", false, "", "--stdin"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var errOut bytes.Buffer
			got, err := readAPIKey(pipeWith(t, tt.in), tt.fromStdin, "prompt: ", &errOut)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}
