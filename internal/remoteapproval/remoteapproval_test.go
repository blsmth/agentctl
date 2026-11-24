package remoteapproval

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/buildid"
)

func testRequest(t *testing.T) Request {
	t.Helper()
	id, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	return Request{
		ID: id, Kind: KindDispatch, AskingAgent: "orch", Session: "agentctl-x", WindowID: "@1",
		TargetAgent: "go-engineer", Prompt: "do the thing", Command: "agentctl dispatch --agent go-engineer --prompt-key k",
		Deadline: time.Now().Add(time.Minute),
	}
}

func TestPayloadHashCoversShownFields(t *testing.T) {
	base := testRequest(t)
	h := PayloadHash(base)
	if len(h) != 64 {
		t.Fatalf("hash %q", h)
	}
	other := base
	other.Deadline = other.Deadline.Add(time.Hour)
	if PayloadHash(other) != h {
		t.Error("deadline must not change the hash")
	}
	mutations := map[string]func(*Request){
		"prompt":  func(r *Request) { r.Prompt += "!" },
		"command": func(r *Request) { r.Command += " " },
		"target":  func(r *Request) { r.TargetAgent = "x" },
		"issue":   func(r *Request) { r.Issue = 7 },
		"window":  func(r *Request) { r.WindowID = "@2" },
		"asking":  func(r *Request) { r.AskingAgent = "y" },
		"kind":    func(r *Request) { r.Kind = KindBash },
		"id":      func(r *Request) { r.ID = strings.Repeat("a", 32) },
	}
	for name, m := range mutations {
		r := base
		m(&r)
		if PayloadHash(r) == h {
			t.Errorf("%s change did not change the hash", name)
		}
	}
}

// Every Details field is shown by the phone, so changing any one of them
// must change the hash. Reflection keeps a field added later honest.
func TestPayloadHashCoversEveryDetailsField(t *testing.T) {
	base := testRequest(t)
	h := PayloadHash(base)
	typ := reflect.TypeOf(Details{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		t.Run(f.Name, func(t *testing.T) {
			r := base
			v := reflect.ValueOf(&r.Details).Elem().Field(i)
			switch v.Kind() {
			case reflect.String:
				v.SetString("x")
			case reflect.Bool:
				v.SetBool(true)
			default:
				t.Fatalf("Details.%s: unhandled kind %s; extend this test", f.Name, v.Kind())
			}
			if PayloadHash(r) == h {
				t.Errorf("Details.%s change did not change the hash", f.Name)
			}
			if strings.Contains(f.Tag.Get("json"), "omitempty") {
				t.Errorf("Details.%s is omitempty; the canonical payload must not omit fields", f.Name)
			}
		})
	}
}

// The canonical payload carries an empty details object for kinds without
// one, with every key present, so the JSON stays fixed-shape.
func TestCanonicalPayloadEmptyDetails(t *testing.T) {
	r := testRequest(t)
	r.Kind = KindBash
	b, err := json.Marshal(canonicalPayload{Details: r.Details})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"details":{`, `"project":""`, `"other_project":false`, `"permission_mode":""`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("canonical JSON %s missing %s", b, key)
		}
	}
}

// A request round-trips its details over the wire.
func TestRequestDetailsJSON(t *testing.T) {
	r := testRequest(t)
	r.Details = Details{Project: "blsmth.com", Dir: "~/code/personal/blsmth.com", OtherProject: true, Model: "opus"}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var got Request
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Details != r.Details || PayloadHash(got) != PayloadHash(r) {
		t.Errorf("details round trip = %+v, want %+v", got.Details, r.Details)
	}
}

func TestRequestValidate(t *testing.T) {
	ok := testRequest(t)
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{KindDispatch, KindBash, KindCron} {
		r := ok
		r.Kind = kind
		if err := r.Validate(); err != nil {
			t.Errorf("kind %q: %v", kind, err)
		}
	}
	for name, m := range map[string]func(*Request){
		"bad id":      func(r *Request) { r.ID = "xyz" },
		"bad kind":    func(r *Request) { r.Kind = "shell" },
		"no command":  func(r *Request) { r.Command = "" },
		"no deadline": func(r *Request) { r.Deadline = time.Time{} },
	} {
		r := ok
		m(&r)
		if r.Validate() == nil {
			t.Errorf("%s: validated", name)
		}
	}
}

// fakeServe reads the request from the server end of a pipe and returns it.
func readRequest(t *testing.T, br *bufio.Reader) Request {
	t.Helper()
	line, err := ReadLine(br)
	if err != nil {
		t.Fatalf("server read: %v", err)
	}
	var r Request
	if err := json.Unmarshal(line, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestStartWaitDecision(t *testing.T) {
	tests := []struct {
		name    string
		reply   func(Request) any
		wantErr bool
		want    string
	}{
		{"allow", func(r Request) any { return Decision{Decision: Allow, Device: "phone", PayloadSHA256: PayloadHash(r)} }, false, Allow},
		{"deny", func(r Request) any { return Decision{Decision: Deny, PayloadSHA256: PayloadHash(r)} }, false, Deny},
		{"hash mismatch", func(Request) any { return Decision{Decision: Allow, PayloadSHA256: strings.Repeat("0", 64)} }, true, ""},
		{"no hash", func(Request) any { return Decision{Decision: Allow} }, true, ""},
		{"bogus decision", func(r Request) any { return Decision{Decision: "yes", PayloadSHA256: PayloadHash(r)} }, true, ""},
		{"refused", func(Request) any { return Decision{Error: "nope"} }, true, ""},
		{"garbage", func(Request) any { return "allow" }, true, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer func() { _ = server.Close() }()
			req := testRequest(t)
			go func() {
				br := bufio.NewReader(server)
				got := readRequest(t, br)
				if got.Op != OpRequest || got.Prompt != req.Prompt {
					t.Errorf("server got %+v", got)
				}
				_ = json.NewEncoder(server).Encode(tc.reply(got))
			}()
			a, err := Start(client, req)
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			d, err := a.Wait()
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if d.Decision != tc.want {
				t.Fatalf("decision %q, want %q", d.Decision, tc.want)
			}
		})
	}
}

func TestWaitConnectionDropIsError(t *testing.T) {
	client, server := net.Pipe()
	req := testRequest(t)
	go func() {
		_, _ = ReadLine(bufio.NewReader(server))
		_ = server.Close()
	}()
	a, err := Start(client, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Wait(); err == nil {
		t.Fatal("dropped connection must be an error")
	}
}

func TestCancelSendsCancelLine(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = server.Close() }()
	req := testRequest(t)
	got := make(chan Cancel, 1)
	go func() {
		br := bufio.NewReader(server)
		_ = readRequest(t, br)
		line, err := ReadLine(br)
		if err != nil {
			t.Errorf("reading cancel: %v", err)
			return
		}
		var c Cancel
		_ = json.Unmarshal(line, &c)
		got <- c
	}()
	a, err := Start(client, req)
	if err != nil {
		t.Fatal(err)
	}
	a.Cancel(ReasonLaptop)
	a.Cancel(ReasonLaptop) // idempotent
	c := <-got
	if c.Op != OpCancel || c.ID != req.ID || c.Reason != ReasonLaptop {
		t.Fatalf("cancel = %+v", c)
	}
}

// guardFixture is a serve dir with a pidfile and a fake ping responder.
type guardFixture struct {
	dir     string
	pid     int
	self    buildid.Identity
	pingPID int
	build   buildid.Identity
	peer    int
	dials   int
}

func newGuardFixture(t *testing.T) *guardFixture {
	t.Helper()
	dir := t.TempDir()
	f := &guardFixture{dir: dir, pid: 4242, pingPID: 4242, peer: 4242}
	f.self = buildid.Identity{Dev: 1, Ino: 2, Size: 3, MtimeUnix: 4, MtimeNsec: 5}
	f.build = f.self
	if err := os.WriteFile(filepath.Join(dir, PIDFile), []byte(strconv.Itoa(f.pid)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *guardFixture) guard(t *testing.T, onRequest func(net.Conn, Request)) Guard {
	t.Helper()
	return Guard{
		Dir: f.dir,
		Dial: func(string, time.Duration) (net.Conn, error) {
			f.dials++
			client, server := net.Pipe()
			go func() {
				defer func() { _ = server.Close() }()
				br := bufio.NewReader(server)
				line, err := ReadLine(br)
				if err != nil {
					return
				}
				var op struct {
					Op string `json:"op"`
				}
				_ = json.Unmarshal(line, &op)
				switch op.Op {
				case OpPing:
					_ = json.NewEncoder(server).Encode(map[string]any{"ping": PingInfo{PID: f.pingPID, Build: f.build}})
				case OpRequest:
					var r Request
					_ = json.Unmarshal(line, &r)
					if onRequest != nil {
						onRequest(server, r)
					}
				}
			}()
			return client, nil
		},
		Self:    func() (buildid.Identity, error) { return f.self, nil },
		Running: func(string) (int, bool) { return f.pid, true },
		PeerPID: func(net.Conn) (int, bool) { return f.peer, true },
	}
}

func TestGuardOpenVerified(t *testing.T) {
	f := newGuardFixture(t)
	g := f.guard(t, func(c net.Conn, r Request) {
		_ = json.NewEncoder(c).Encode(Decision{Decision: Deny, PayloadSHA256: PayloadHash(r)})
	})
	a, err := g.Open(testRequest(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer a.Close()
	d, err := a.Wait()
	if err != nil || d.Decision != Deny {
		t.Fatalf("d=%+v err=%v", d, err)
	}
}

func TestGuardRejects(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, f *guardFixture, g *Guard)
	}{
		{"ping pid mismatch", func(_ *testing.T, f *guardFixture, _ *Guard) { f.pingPID = 999 }},
		{"peer pid mismatch", func(_ *testing.T, f *guardFixture, _ *Guard) { f.peer = 999 }},
		{"build mismatch", func(_ *testing.T, f *guardFixture, _ *Guard) { f.build.Ino = 77 }},
		{"pidfile process not serve", func(_ *testing.T, _ *guardFixture, g *Guard) {
			g.Running = func(string) (int, bool) { return 0, false }
		}},
		{"pidfile names other live pid", func(_ *testing.T, _ *guardFixture, g *Guard) {
			g.Running = func(string) (int, bool) { return 1234, true }
		}},
		{"pidfile group readable", func(t *testing.T, f *guardFixture, _ *Guard) {
			if err := os.Chmod(filepath.Join(f.dir, PIDFile), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"pidfile other owner", func(_ *testing.T, _ *guardFixture, g *Guard) { g.Uid = func() int { return os.Getuid() + 1 } }},
		{"pidfile missing", func(t *testing.T, f *guardFixture, _ *Guard) {
			if err := os.Remove(filepath.Join(f.dir, PIDFile)); err != nil {
				t.Fatal(err)
			}
		}},
		{"pidfile symlink", func(t *testing.T, f *guardFixture, _ *Guard) {
			p := filepath.Join(f.dir, PIDFile)
			target := filepath.Join(f.dir, "real.pid")
			if err := os.Rename(p, target); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, p); err != nil {
				t.Fatal(err)
			}
		}},
		{"no self build", func(_ *testing.T, _ *guardFixture, g *Guard) {
			g.Self = func() (buildid.Identity, error) { return buildid.Identity{}, errors.New("ephemeral") }
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newGuardFixture(t)
			requested := false
			g := f.guard(t, func(net.Conn, Request) { requested = true })
			tc.setup(t, f, &g)
			a, err := g.Open(testRequest(t))
			if err == nil {
				a.Close()
				t.Fatal("Open succeeded on an unverified socket")
			}
			if !errors.Is(err, ErrUntrusted) {
				t.Fatalf("err = %v, want ErrUntrusted", err)
			}
			if requested {
				t.Fatal("request was sent to an unverified socket")
			}
		})
	}
}

// With phone access off the guard never dials: the hook sees "no phone" and
// the laptop popup alone answers.
func TestGuardPhoneOff(t *testing.T) {
	f := newGuardFixture(t)
	requested := false
	g := f.guard(t, func(net.Conn, Request) { requested = true })
	if err := os.WriteFile(filepath.Join(f.dir, PhoneOffFile), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := g.Open(testRequest(t))
	if err == nil {
		a.Close()
		t.Fatal("Open succeeded with phone access off")
	}
	if !errors.Is(err, ErrPhoneOff) || !errors.Is(err, ErrNotRunning) {
		t.Fatalf("err = %v, want ErrPhoneOff wrapping ErrNotRunning", err)
	}
	if f.dials != 0 || requested {
		t.Fatalf("dials = %d, requested = %v; want no contact with serve", f.dials, requested)
	}
}

func TestGuardNotRunning(t *testing.T) {
	g := Guard{Dir: t.TempDir(), Self: func() (buildid.Identity, error) { return buildid.Identity{}, nil }}
	if _, err := g.Open(testRequest(t)); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("err = %v, want ErrNotRunning", err)
	}
}

// PeerPID must report the listening process on a real unix socket.
func TestPeerPIDRealSocket(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("no peer credentials on this platform")
	}
	dir, err := os.MkdirTemp("", "ra")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			time.Sleep(100 * time.Millisecond)
			_ = c.Close()
		}
	}()
	c, err := net.Dial("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	pid, ok := PeerPID(c)
	if !ok || pid != os.Getpid() {
		t.Fatalf("PeerPID = %d, %v; want %d", pid, ok, os.Getpid())
	}
}
