package detach

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStartOutlivesCaller(t *testing.T) {
	out := filepath.Join(t.TempDir(), "ran")
	if err := Start("/bin/sh", []string{"-c", "touch " + out}, nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(out); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("detached child did not run")
}

func TestStartMissingExe(t *testing.T) {
	if err := Start("/nonexistent/agentctl", nil, nil); err == nil {
		t.Fatal("want error")
	}
}
