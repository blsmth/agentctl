package tmux

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestNeedsUTF8Locale(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"empty", nil, true},
		{"LANG=C", map[string]string{"LANG": "C"}, true},
		{"LC_ALL=C beats LANG", map[string]string{"LC_ALL": "C", "LANG": "en_US.UTF-8"}, true},
		{"LANG utf8", map[string]string{"LANG": "en_US.UTF-8"}, false},
		{"LC_CTYPE=UTF-8", map[string]string{"LC_CTYPE": "UTF-8"}, false},
		{"lowercase utf8", map[string]string{"LANG": "en_US.utf8"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NeedsUTF8Locale(func(k string) string { return tt.env[k] }); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnsureUTF8Locale(t *testing.T) {
	t.Setenv("LC_ALL", "C")
	t.Setenv("LC_CTYPE", "")
	t.Setenv("LANG", "")
	if !EnsureUTF8Locale() {
		t.Fatal("expected change")
	}
	if os.Getenv("LC_ALL") != "" || os.Getenv("LC_CTYPE") != fallbackLocale {
		t.Errorf("LC_ALL=%q LC_CTYPE=%q", os.Getenv("LC_ALL"), os.Getenv("LC_CTYPE"))
	}
	if EnsureUTF8Locale() {
		t.Error("second call should be a no-op")
	}
}

func TestListWindowsWithoutLocale(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	t.Setenv("TMUX_TMPDIR", t.TempDir())
	t.Setenv("TMUX", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_CTYPE", "")
	t.Setenv("LANG", "C")
	sess := "locale-test"
	start := exec.Command("tmux", "-u", "new-session", "-d", "-s", sess, "-n", "●work", "-c", filepath.Clean(t.TempDir()))
	if out, err := start.CombinedOutput(); err != nil {
		t.Skipf("cannot start private tmux: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-server").Run() })

	EnsureUTF8Locale()
	ws, err := ListWindowsInErr(sess)
	if err != nil || len(ws) != 1 {
		t.Fatalf("windows = %v, err = %v", ws, err)
	}
	if ws[0].Name != "●work" {
		t.Errorf("name = %q", ws[0].Name)
	}
}
