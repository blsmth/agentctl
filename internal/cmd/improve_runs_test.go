package cmd

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestRunImproveRunsEmpty(t *testing.T) {
	var out bytes.Buffer
	if err := RunImproveRuns(&out, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "No improve runs.") {
		t.Fatalf("out = %q", out.String())
	}
}
