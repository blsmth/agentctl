package cmd

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/backendauth"
	"github.com/blsmth/agentctl/internal/config"
)

func TestPrintCreditsHealth(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	backends := map[string]config.BackendConfig{
		"nous":   {Kind: config.BackendKindNous},
		"clear":  {Kind: config.BackendKindNous},
		"ollama": {BaseURL: "http://x"},
	}
	var buf bytes.Buffer
	printCreditsHealth(&buf, backends, time.Now())
	if buf.Len() != 0 {
		t.Fatalf("clear state printed %q", buf.String())
	}
	backendauth.MarkNoCredits("nous")
	printCreditsHealth(&buf, backends, time.Now())
	out := buf.String()
	for _, s := range []string{"Backend nous: warn", "out of credits since just now", "backend status --verify"} {
		if !strings.Contains(out, s) {
			t.Errorf("output %q missing %q", out, s)
		}
	}
	if strings.Contains(out, "clear") || strings.Contains(out, "ollama") {
		t.Errorf("unexpected backends in %q", out)
	}
}
