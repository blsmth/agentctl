package detach

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/selfpath"
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

func TestStartSelf_RefusesUnderTest(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	t.Setenv("AGENTCTL_MARKER", marker)
	if err := StartSelf([]string{"-test.run=NONE"}, nil); !errors.Is(err, selfpath.ErrTestBinary) {
		t.Fatalf("StartSelf err = %v, want ErrTestBinary", err)
	}
	if err := StartSelfPinned([]string{"-test.run=NONE"}, t.TempDir()); !errors.Is(err, selfpath.ErrTestBinary) {
		t.Fatalf("StartSelfPinned err = %v, want ErrTestBinary", err)
	}
}

func TestStart_RefusesOwnBinaryUnderTest(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Skip("no executable path")
	}
	if err := Start(self, []string{"-test.run=NONE"}, nil); !errors.Is(err, selfpath.ErrTestBinary) {
		t.Fatalf("Start(self) err = %v, want ErrTestBinary", err)
	}
}

func TestStart_DepthGuard(t *testing.T) {
	tests := []struct {
		name     string
		env      string
		set      bool
		wantErr  bool
		wantNext string
	}{
		{"unset", "", false, false, "1"},
		{"zero", "0", true, false, "1"},
		{"two", "2", true, false, "3"},
		{"three", "3", true, true, ""},
		{"garbage", "banana", true, false, "1"},
		{"negative", "-4", true, false, "1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.set {
				t.Setenv(DepthEnv, tt.env)
			} else {
				t.Setenv(DepthEnv, "")
				os.Unsetenv(DepthEnv)
			}
			out := filepath.Join(t.TempDir(), "depth")
			err := Start("/bin/sh", []string{"-c", `printf %s "$` + DepthEnv + `" > ` + out}, nil)
			if tt.wantErr {
				if !errors.Is(err, ErrDepth) {
					t.Fatalf("err = %v, want ErrDepth", err)
				}
				time.Sleep(100 * time.Millisecond)
				if _, err := os.Stat(out); err == nil {
					t.Fatal("child ran despite depth guard")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				if b, err := os.ReadFile(out); err == nil && len(b) > 0 {
					if string(b) != tt.wantNext {
						t.Fatalf("child depth = %q, want %q", b, tt.wantNext)
					}
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
			t.Fatal("child did not report depth")
		})
	}
}
