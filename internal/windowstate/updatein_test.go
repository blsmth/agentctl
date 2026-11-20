package windowstate_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/windowstate"
)

func TestUpdateIn(t *testing.T) {
	local := t.TempDir()
	windowstate.SetDirForTest(local)
	rt := t.TempDir()
	dir := windowstate.WindowsDirFor(rt)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "@1.json"), []byte(`{"state":"idle","future_key":42}`), 0o600); err != nil {
		t.Fatal(err)
	}

	err := windowstate.UpdateIn(rt, "@1", func(ws *windowstate.WindowState) { ws.ShortTitle.Title = "Mine" })
	if err != nil {
		t.Fatal(err)
	}
	recs := windowstate.AllFrom(dir)
	if got := recs["@1"]; got.ShortTitle.Title != "Mine" || got.State != "idle" {
		t.Errorf("record = %+v", got)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "@1.json"))
	if !strings.Contains(string(data), `"future_key":42`) {
		t.Errorf("unknown key dropped: %s", data)
	}
	if _, err := os.Stat(filepath.Join(local, "@1.json")); !os.IsNotExist(err) {
		t.Error("UpdateIn wrote to the process's own windows dir")
	}

	t.Run("no record", func(t *testing.T) {
		err := windowstate.UpdateIn(rt, "@2", func(*windowstate.WindowState) {})
		if !errors.Is(err, windowstate.ErrNoRecord) {
			t.Fatalf("err = %v, want ErrNoRecord", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "@2.json")); !os.IsNotExist(err) {
			t.Error("a record was created")
		}
	})
	t.Run("no runtime dir", func(t *testing.T) {
		if err := windowstate.UpdateIn("", "@1", func(*windowstate.WindowState) {}); err == nil {
			t.Error("want an error")
		}
	})
	t.Run("symlinked windows dir", func(t *testing.T) {
		rt2 := t.TempDir()
		if err := os.Symlink(dir, filepath.Join(rt2, "windows")); err != nil {
			t.Fatal(err)
		}
		if err := windowstate.UpdateIn(rt2, "@1", func(ws *windowstate.WindowState) { ws.ShortTitle.Title = "Evil" }); err == nil {
			t.Error("write through a symlinked windows dir must fail")
		}
		if windowstate.AllFrom(dir)["@1"].ShortTitle.Title != "Mine" {
			t.Error("the symlink target was written")
		}
	})
}
