package serve

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type fakeController struct {
	gotName   string
	restarted atomic.Bool
	err       error
}

func (f *fakeController) OpenPairing(name string) (PairInfo, error) {
	f.gotName = name
	if f.err != nil {
		return PairInfo{}, f.err
	}
	return PairInfo{Code: "CODE", PairURL: "https://h:1/pair?code=CODE", Fingerprint: "AA"}, nil
}

func (f *fakeController) RequestRestart() { f.restarted.Store(true) }

func (f *fakeController) Status() StatusInfo {
	return StatusInfo{PID: 42, Version: "v", URL: "https://h:1/"}
}

func startControl(t *testing.T, c controller) string {
	t.Helper()
	dir := shortTempDir(t)
	ln, err := listenControl(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go serveControl(ln, c)
	return dir
}

func TestControlSocket(t *testing.T) {
	fc := &fakeController{}
	dir := startControl(t, fc)

	fi, err := os.Stat(filepath.Join(dir, ControlSocket))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("socket mode %v, want 0600", fi.Mode().Perm())
	}

	info, err := RequestPairing(dir, "iphone")
	if err != nil {
		t.Fatal(err)
	}
	if info.Code != "CODE" || fc.gotName != "iphone" {
		t.Errorf("pair = %+v name %q", info, fc.gotName)
	}
	st, err := QueryStatus(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.PID != 42 {
		t.Errorf("status = %+v", st)
	}

	if err := RequestServerRestart(dir); err != nil || !fc.restarted.Load() {
		t.Errorf("restart not delivered: %v", err)
	}
	fc.err = errors.New("boom")
	if _, err := RequestPairing(dir, ""); err == nil || err.Error() != "boom" {
		t.Errorf("error not propagated: %v", err)
	}
	if _, err := controlCall(dir, controlRequest{Op: "nope"}); err == nil {
		t.Error("unknown op accepted")
	}
}

func TestControlNotRunning(t *testing.T) {
	dir := shortTempDir(t)
	if _, err := RequestPairing(dir, ""); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("err = %v, want ErrNotRunning", err)
	}
}

func TestListenControlReplacesStaleSocket(t *testing.T) {
	dir := shortTempDir(t)
	path := filepath.Join(dir, ControlSocket)
	// A socket file left behind by a crashed server: bound, then the
	// listener is closed without unlinking.
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("stale socket missing: %v", err)
	}
	ln2, err := listenControl(dir)
	if err != nil {
		t.Fatalf("stale socket not replaced: %v", err)
	}
	_ = ln2.Close()
}

func TestListenControlRefusesLiveSocket(t *testing.T) {
	dir := startControl(t, &fakeController{})
	if _, err := listenControl(dir); err == nil {
		t.Fatal("second listener took over a live socket")
	}
}

func TestListenControlRefusesNonSocket(t *testing.T) {
	dir := shortTempDir(t)
	if err := os.WriteFile(filepath.Join(dir, ControlSocket), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := listenControl(dir); err == nil {
		t.Fatal("regular file replaced")
	}
}

func TestControlRejectsGarbage(t *testing.T) {
	dir := startControl(t, &fakeController{})
	conn, err := net.Dial("unix", filepath.Join(dir, ControlSocket))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte("not json\n")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 256)
	n, _ := conn.Read(buf)
	if n == 0 {
		t.Fatal("no response to garbage")
	}
}
