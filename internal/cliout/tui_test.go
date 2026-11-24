package cliout

import (
	"errors"
	"testing"
)

func stubTTY(t *testing.T, err error) {
	t.Helper()
	old, oldExit := openTTY, tuiExit
	openTTY = func() error { return err }
	t.Cleanup(func() { openTTY, tuiExit = old, oldExit })
}

func TestRunTUI(t *testing.T) {
	t.Run("tty runs the tui", func(t *testing.T) {
		stubTTY(t, nil)
		var tui, fb bool
		if err := RunTUI(func() { tui = true }, func() error { fb = true; return nil }); err != nil {
			t.Fatal(err)
		}
		if !tui || fb {
			t.Fatalf("tui=%v fallback=%v", tui, fb)
		}
	})
	t.Run("no tty falls back", func(t *testing.T) {
		stubTTY(t, errors.New("device not configured"))
		var tui, fb bool
		if err := RunTUI(func() { tui = true }, func() error { fb = true; return nil }); err != nil {
			t.Fatal(err)
		}
		if tui || !fb {
			t.Fatalf("tui=%v fallback=%v", tui, fb)
		}
	})
	t.Run("no tty without fallback exits 1", func(t *testing.T) {
		stubTTY(t, errors.New("x"))
		code := -1
		tuiExit = func(c int) { code = c }
		_ = RunTUI(func() { t.Fatal("tui ran") }, nil)
		if code != 1 {
			t.Fatalf("exit code %d", code)
		}
	})
}
