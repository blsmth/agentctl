package buildid

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "bin")
	if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	id, err := Stat(exe)
	if err != nil {
		t.Fatal(err)
	}
	id.PID = 42
	rt := filepath.Join(dir, "rt")
	if err := Write(rt, id); err != nil {
		t.Fatal(err)
	}
	got, err := Read(rt)
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != id.Path || got.PID != 42 || !SameBuild(got, id) {
		t.Errorf("round trip = %+v, want %+v", got, id)
	}
	if _, err := Read(filepath.Join(dir, "missing")); err == nil {
		t.Error("Read of missing dir: want error")
	}
}

func TestSameBuild(t *testing.T) {
	a := Identity{Dev: 1, Ino: 2, Size: 3, MtimeUnix: 4}
	if !SameBuild(a, a) {
		t.Error("identical should match")
	}
	for _, b := range []Identity{
		{Dev: 9, Ino: 2, Size: 3, MtimeUnix: 4},
		{Dev: 1, Ino: 9, Size: 3, MtimeUnix: 4},
		{Dev: 1, Ino: 2, Size: 9, MtimeUnix: 4},
		{Dev: 1, Ino: 2, Size: 3, MtimeUnix: 9},
		{Dev: 1, Ino: 2, Size: 3, MtimeUnix: 4, MtimeNsec: 5},
	} {
		if SameBuild(a, b) {
			t.Errorf("%+v should differ", b)
		}
	}
}
