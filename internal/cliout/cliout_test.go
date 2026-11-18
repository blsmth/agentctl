package cliout

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func TestShouldStrip(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	tests := []struct {
		name  string
		tty   bool
		env   map[string]string
		strip bool
	}{
		{"tty keeps color", true, nil, false},
		{"pipe strips", false, nil, true},
		{"NO_COLOR on tty strips", true, map[string]string{"NO_COLOR": "1"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShouldStrip(tt.tty, env(tt.env)); got != tt.strip {
				t.Fatalf("ShouldStrip = %v, want %v", got, tt.strip)
			}
		})
	}
}

func TestStripWriter(t *testing.T) {
	var buf bytes.Buffer
	in := "\x1b[1;38;2;88;110;117mSESSION\x1b[m  plain\n"
	if _, err := io.WriteString(StripWriter(&buf), in); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "SESSION  plain\n" || strings.ContainsRune(got, 0x1b) {
		t.Fatalf("got %q", got)
	}
}

func TestPlainStripsStdoutWhenNotTTY(t *testing.T) {
	realStdout := os.Stdout
	out, w, err := os.Pipe() // stands in for a non-TTY stdout
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = realStdout })

	restore := Plain()
	os.Stdout.WriteString("\x1b[31mred\x1b[0m\n")
	restore()
	w.Close()

	got, _ := io.ReadAll(out)
	if string(got) != "red\n" {
		t.Fatalf("got %q, want %q", got, "red\n")
	}
}
