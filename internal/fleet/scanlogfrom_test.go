package fleet

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanLogFrom(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	line1 := `{"type":"pr-link","prNumber":1,"prUrl":"https://x/pull/1"}` + "\n"
	if err := os.WriteFile(path, []byte(line1), 0o600); err != nil {
		t.Fatal(err)
	}
	off, size, info, fresh, ok := ScanLogFrom(path, 0, 0)
	if !ok || !fresh || info.PRNumber != 1 || off != size {
		t.Fatalf("first: off=%d size=%d info=%+v fresh=%v ok=%v", off, size, info, fresh, ok)
	}
	off2, size2, info, fresh, ok := ScanLogFrom(path, off, size)
	if !ok || fresh || info.PRURL != "" || off2 != off || size2 != size {
		t.Fatalf("unchanged: %d %d %+v %v %v", off2, size2, info, fresh, ok)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"type":"pr-link","prNumber":2,"prUrl":"https://x/pull/2"}` + "\n") //nolint:errcheck
	f.Close()
	off3, size3, info, fresh, ok := ScanLogFrom(path, off, size)
	if !ok || fresh || info.PRNumber != 2 || off3 != size3 {
		t.Fatalf("appended: %d %d %+v %v %v", off3, size3, info, fresh, ok)
	}
	os.WriteFile(path, []byte("{}\n"), 0o600) //nolint:errcheck
	_, _, info, fresh, ok = ScanLogFrom(path, off3, size3)
	if !ok || !fresh || info.PRURL != "" {
		t.Fatalf("shrunk: %+v %v %v", info, fresh, ok)
	}
	if _, _, _, _, ok := ScanLogFrom(path+".nope", 0, 0); ok {
		t.Error("missing log: ok = true")
	}
}
