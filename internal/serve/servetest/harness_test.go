package servetest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/panestate"
	"github.com/blsmth/agentctl/internal/serve"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "servetest-log-")
	if err != nil {
		panic(err)
	}
	logging.InitFile(filepath.Join(dir, "serve.log"))
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func newHarness(t *testing.T) *Harness {
	t.Helper()
	h, err := New(Options{Dir: t.TempDir(), Rediscover: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	return h
}

// call sends a request through the harness from loopback, with the device
// cookie and CSRF header unless anon.
func call(t *testing.T, h *Harness, method, path, body string, anon bool) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, path, rd)
	r.RemoteAddr = "127.0.0.1:40000"
	if !anon {
		r.AddCookie(&http.Cookie{Name: serve.DeviceCookie, Value: h.Token})
		r.Header.Set(serve.CSRFHeader, "1")
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.Handler().ServeHTTP(w, r)
	return w
}

func TestHarnessServesAppThroughDeviceAuth(t *testing.T) {
	h := newHarness(t)
	if w := call(t, h, "GET", "/app.js", "", true); w.Code != http.StatusUnauthorized {
		t.Fatalf("no cookie: /app.js = %d, want 401", w.Code)
	}
	for _, p := range []string{"/", "/app.js", "/swipe.js", "/sw.js", "/v1/fleet"} {
		if w := call(t, h, "GET", p, "", false); w.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", p, w.Code)
		}
	}
	var hz struct{ Features []string }
	_ = json.Unmarshal(call(t, h, "GET", "/healthz", "", false).Body.Bytes(), &hz)
	if !strings.Contains(strings.Join(hz.Features, ","), "write") {
		t.Errorf("features %v lack write", hz.Features)
	}
}

func TestHarnessRefusesNonLoopback(t *testing.T) {
	h := newHarness(t)
	r := httptest.NewRequest("GET", ControlPrefix+"actions", nil)
	r.RemoteAddr = "192.168.1.20:5000"
	w := httptest.NewRecorder()
	h.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("LAN request = %d, want 403", w.Code)
	}
}

func TestHarnessFleetFixture(t *testing.T) {
	h := newHarness(t)
	var body struct {
		Groups []struct {
			Session string
			Windows []struct {
				WindowID string `json:"window_id"`
				State    string
				Done     bool
				Parent   string
				Mode     string
			}
		}
	}
	if err := json.Unmarshal(call(t, h, "GET", "/v1/fleet", "", false).Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, g := range body.Groups {
		for _, w := range g.Windows {
			got[w.WindowID] = w.State + "/" + w.Parent
		}
	}
	want := map[string]string{"@11": "idle/", "@12": "idle/@11", "@13": "working/@11", "@14": "blocked/@12", "@15": "idle/", "@31": "idle/", "@41": "idle/"}
	for id, v := range want {
		if got[id] != v {
			t.Errorf("%s = %q, want %q", id, got[id], v)
		}
	}
}

func TestHarnessRecordsWrites(t *testing.T) {
	h := newHarness(t)
	const win = "/v1/sessions/agentctl-shop/windows/"

	if w := call(t, h, "POST", win+"@15/close", "{}", false); w.Code != http.StatusOK {
		t.Fatalf("close idle = %d %s", w.Code, w.Body)
	}
	if w := call(t, h, "POST", win+"@13/close", "{}", false); w.Code != http.StatusConflict {
		t.Errorf("close working = %d, want 409", w.Code)
	}
	if w := call(t, h, "POST", "/v1/sessions/agentctl-hub/windows/@41/close", "{}", false); w.Code != http.StatusConflict {
		t.Errorf("close hub = %d, want 409", w.Code)
	}
	if w := call(t, h, "POST", win+"@11/reply", `{"text":"ship it"}`, false); w.Code != http.StatusOK {
		t.Errorf("reply = %d %s", w.Code, w.Body)
	}
	d, ok := panestate.ParseDialog(panestate.StripANSI(DialogPane))
	if !ok {
		t.Fatal("DialogPane does not parse")
	}
	if w := call(t, h, "POST", win+"@14/answer", `{"key":"1","dialog_hash":"`+d.Hash+`"}`, false); w.Code != http.StatusOK {
		t.Errorf("answer = %d %s", w.Code, w.Body)
	}

	var got []string
	for _, a := range h.Actions() {
		got = append(got, a.Kind+":"+a.Window+":"+a.Key+a.Text)
	}
	want := "close:@15: reply:@11:ship it key:@14:1"
	if strings.Join(got, " ") != want {
		t.Errorf("actions = %q, want %q", strings.Join(got, " "), want)
	}
	if _, ok := h.world.find(func(x Window) bool { return x.ID == "@15" }); ok {
		t.Error("closed window still in the fixture")
	}

	call(t, h, "POST", ControlPrefix+"reset", "", true)
	if len(h.Actions()) != 0 {
		t.Error("reset kept actions")
	}
	if _, ok := h.world.find(func(x Window) bool { return x.ID == "@15" }); !ok {
		t.Error("reset did not restore the fixture")
	}
}

func TestHarnessPatchAndFixtureSwap(t *testing.T) {
	h := newHarness(t)
	w := call(t, h, "POST", ControlPrefix+"window", `{"session":"agentctl-shop","id":"@15","pane":"working"}`, true)
	if w.Code != http.StatusOK {
		t.Fatalf("patch = %d %s", w.Code, w.Body)
	}
	if w := call(t, h, "POST", "/v1/sessions/agentctl-shop/windows/@15/close", "{}", false); w.Code != http.StatusConflict {
		t.Errorf("close of a busy pane = %d, want 409", w.Code)
	}
	if w := call(t, h, "POST", ControlPrefix+"window", `{"session":"agentctl-shop","id":"@99"}`, true); w.Code != http.StatusNotFound {
		t.Errorf("patch of a missing window = %d, want 404", w.Code)
	}
	if w := call(t, h, "PUT", ControlPrefix+"fleet", `{"fixture":"empty"}`, true); w.Code != http.StatusOK {
		t.Fatalf("swap = %d %s", w.Code, w.Body)
	}
	if b := call(t, h, "GET", "/v1/fleet", "", false).Body.String(); !strings.Contains(b, `"groups":[]`) {
		t.Errorf("empty fixture served %s", b)
	}
	if w := call(t, h, "PUT", ControlPrefix+"fleet", `{"groups":[{"session":"s","windows":[{"id":"@1","state":"nope"}]}]}`, true); w.Code != http.StatusBadRequest {
		t.Errorf("invalid fixture = %d, want 400", w.Code)
	}
}

func TestHarnessApprovalRoundTrip(t *testing.T) {
	h := newHarness(t)
	w := call(t, h, "POST", ControlPrefix+"approvals", `{"session":"agentctl-shop","window_id":"@14","asking_agent":"reviewer","command":"rm -rf build"}`, true)
	if w.Code != http.StatusOK {
		t.Fatalf("open = %d %s", w.Code, w.Body)
	}
	var opened struct{ ID string }
	_ = json.Unmarshal(w.Body.Bytes(), &opened)

	var list struct {
		Approvals []struct {
			ID            string `json:"id"`
			PayloadSHA256 string `json:"payload_sha256"`
		}
	}
	_ = json.Unmarshal(call(t, h, "GET", "/v1/approvals", "", false).Body.Bytes(), &list)
	if len(list.Approvals) != 1 || list.Approvals[0].ID != opened.ID {
		t.Fatalf("approvals = %+v", list.Approvals)
	}
	body := `{"decision":"allow","payload_sha256":"` + list.Approvals[0].PayloadSHA256 + `"}`
	if w := call(t, h, "POST", "/v1/approvals/"+opened.ID, body, false); w.Code != http.StatusOK {
		t.Fatalf("answer = %d %s", w.Code, w.Body)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, a := range h.Actions() {
			if a.Kind == "approval" && a.ID == opened.ID {
				if a.Decision != "allow" || a.Device != DeviceName {
					t.Errorf("recorded %+v", a)
				}
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("decision never reached the hook side")
}
