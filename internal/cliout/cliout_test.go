package cliout

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/blsmth/agentctl/internal/testenv"
)

func TestShouldStrip(t *testing.T) {
	if ShouldStrip(true) {
		t.Error("a terminal must keep its stdout untouched")
	}
	if !ShouldStrip(false) {
		t.Error("a pipe must be stripped")
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
	restore() // idempotent
	w.Close()

	got, _ := io.ReadAll(out)
	if string(got) != "red\n" {
		t.Fatalf("got %q, want %q", got, "red\n")
	}
}

// TestMain lets the test binary double as a helper process for the Exit test.
func TestMain(m *testing.M) {
	cleanup := testenv.Setup()
	if os.Getenv("CLIOUT_EXIT_HELPER") == "1" {
		Plain()
		os.Stdout.WriteString("\x1b[31mbefore exit\x1b[0m\n")
		Exit(3)
	}
	code := m.Run()
	cleanup()
	os.Exit(code)
}

func TestExitFlushesOutput(t *testing.T) {
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "CLIOUT_EXIT_HELPER=1")
	var out bytes.Buffer
	cmd.Stdout = &out // a pipe, not a TTY
	err := cmd.Run()
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 3 {
		t.Fatalf("want exit code 3, got %v", err)
	}
	if out.String() != "before exit\n" {
		t.Fatalf("output lost or unstripped: %q", out.String())
	}
}

// Regression: a read boundary inside an escape sequence used to corrupt the
// strip writer and drop the rest of the output (a status table printed only
// its header when piped).
func TestCopyLinesSurvivesSplitReads(t *testing.T) {
	in := "\x1b[1;38;2;88;91;112mSESSION\x1b[m  WIN\n\x1b[38;2;88;91;112m────\x1b[m\n\x1b]8;;agentctl://window/@1\x1b\\row\x1b]8;;\x1b\\ tail-no-newline"
	var out bytes.Buffer
	copyLines(StripWriter(&out), iotest.OneByteReader(strings.NewReader(in)))
	want := "SESSION  WIN\n────\nrow tail-no-newline"
	if out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}
