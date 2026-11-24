package serve

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/artifacts"
)

func listIDs(t *testing.T, ae *artifactEnv) []string {
	t.Helper()
	w := ae.get("/v1/artifacts")
	var body struct {
		Artifacts []artifactView `json:"artifacts"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil {
		t.Fatalf("list = %d %s", w.Code, w.Body)
	}
	out := []string{}
	for _, a := range body.Artifacts {
		out = append(out, a.ID)
	}
	return out
}

func TestArtifactOrder(t *testing.T) {
	ae := newArtifactEnv(t, true)
	o := artifacts.AddOptions{Origin: artifacts.Origin{Project: "proj", ProjectDir: "/code/proj"}}
	a := ae.add(t, map[string]string{"a.txt": "a"}, o)
	ae.now = ae.now.Add(time.Hour)
	b := ae.add(t, map[string]string{"b.txt": "b"}, o)
	ae.now = ae.now.Add(time.Hour)
	c := ae.add(t, map[string]string{"c.txt": "c"}, o)
	if got, want := listIDs(t, ae), []string{c.ID, b.ID, a.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("default %v want %v", got, want)
	}

	body := func(ids ...string) string {
		j, _ := json.Marshal(map[string]any{"project": "/code/proj", "ids": ids})
		return string(j)
	}
	w := ae.send("POST", "/v1/artifacts/order", body(a.ID, c.ID, "zzzzzzzzzzzzzzzzzzzzzzzzzz", b.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("order = %d %s", w.Code, w.Body)
	}
	if got, want := listIDs(t, ae), []string{a.ID, c.ID, b.ID}; !reflect.DeepEqual(got, want) {
		t.Errorf("ordered %v want %v", got, want)
	}

	for name, bad := range map[string]string{
		"bad id":       `{"project":"/code/proj","ids":["../x"]}`,
		"unknown key":  `{"project":"/code/proj","ids":[],"x":1}`,
		"control char": `{"project":"a\u0000b","ids":[]}`,
	} {
		if w := ae.send("POST", "/v1/artifacts/order", bad); w.Code != http.StatusBadRequest {
			t.Errorf("%s = %d", name, w.Code)
		}
	}

	var actions []string
	for _, l := range ae.auditLines(t) {
		actions = append(actions, l.Action+":"+l.Result)
		if l.Action == "artifact-order" && l.Artifact != "" {
			t.Errorf("order audit has an artifact id: %+v", l)
		}
	}
	if !strings.Contains(strings.Join(actions, " "), "artifact-order:attempt artifact-order:ok") {
		t.Errorf("audit = %v", actions)
	}
}

func TestArtifactOrderNeedsWriteCSRFAndDevice(t *testing.T) {
	ro := newArtifactEnv(t, false)
	if w := ro.send("POST", "/v1/artifacts/order", `{"project":"","ids":[]}`); w.Code != http.StatusForbidden || decodeError(t, w.Body.String()).Code != codeWriteDisabled {
		t.Errorf("allow_write off = %d %s", w.Code, w.Body)
	}
	rw := newArtifactEnv(t, true)
	if w := rw.send("POST", "/v1/artifacts/order", `{"project":"","ids":[]}`, withoutHeader(CSRFHeader)); w.Code != http.StatusForbidden || decodeError(t, w.Body.String()).Code != codeCSRF {
		t.Errorf("no CSRF = %d %s", w.Code, w.Body)
	}
	r := httptest.NewRequest("POST", "/v1/artifacts/order", strings.NewReader(`{"project":"","ids":[]}`))
	r.RemoteAddr = "192.168.1.20:51515"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(CSRFHeader, "1")
	w := httptest.NewRecorder()
	rw.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("no device = %d", w.Code)
	}
}
