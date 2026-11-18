package serve

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/uploads"
)

var (
	testPNG  = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 32)...)
	testJPEG = append([]byte("\xff\xd8\xff\xe0\x00\x10JFIF"), bytes.Repeat([]byte{0}, 32)...)
)

type part struct {
	name, filename string
	data           []byte
}

func multipartBody(t *testing.T, parts ...part) (string, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, p := range parts {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", `form-data; name="`+p.name+`"; filename="`+p.filename+`"`)
		h.Set("Content-Type", "image/png") // declared type is never trusted
		w, err := mw.CreatePart(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(p.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String(), mw.FormDataContentType()
}

func (e *writeEnv) upload(t *testing.T, parts ...part) *httptest.ResponseRecorder {
	t.Helper()
	body, ct := multipartBody(t, parts...)
	return e.post(winPath+"/upload", body, func(r *http.Request) { r.Header.Set("Content-Type", ct) })
}

// uploadEnv is an idle window whose runtime dir is a temp dir.
func uploadEnv(t *testing.T) (*writeEnv, string) {
	t.Helper()
	e := newWriteEnv(t, true)
	g := writeGroup("idle", false, "go-engineer", "dispatch")
	g.RuntimeDir = filepath.Join(t.TempDir(), ".agentctl")
	e.fleet.set(g)
	e.panes.set(idlePane)
	return e, g.RuntimeDir
}

func uploadedIDs(t *testing.T, w *httptest.ResponseRecorder) []string {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var out uploadOut
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Images
}

func TestUploadStoresImages(t *testing.T) {
	e, rt := uploadEnv(t)
	ids := uploadedIDs(t, e.upload(t, part{imageField, "../../evil.sh", testPNG}, part{imageField, "x.png", testJPEG}))
	if len(ids) != 2 || !strings.HasSuffix(ids[0], ".png") || !strings.HasSuffix(ids[1], ".jpg") {
		t.Fatalf("ids = %v", ids)
	}
	for _, id := range ids {
		p := filepath.Join(rt, "uploads", "523", id)
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v", id, fi.Mode().Perm())
		}
	}
	di, _ := os.Stat(filepath.Join(rt, "uploads", "523"))
	if di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v", di.Mode().Perm())
	}
	if l := e.auditLines(t); len(l) != 2 || l[1].Action != "upload" || l[1].Result != "ok" {
		t.Errorf("audit = %+v", l)
	}
}

func TestUploadRejects(t *testing.T) {
	tests := []struct {
		name   string
		parts  []part
		status int
	}{
		{"non-image bytes with image name", []part{{imageField, "a.png", []byte("#!/bin/sh\nrm -rf ~\n")}}, 400},
		{"html", []part{{imageField, "a.jpg", []byte("<html><script>1</script></html>")}}, 400},
		{"oversize", []part{{imageField, "a.png", append(append([]byte{}, testPNG...), make([]byte, maxImageBytes)...)}}, 413},
		{"no image part", []part{{"other", "a.png", testPNG}}, 400},
		{"too many", []part{{imageField, "1", testPNG}, {imageField, "2", testPNG}, {imageField, "3", testPNG}, {imageField, "4", testPNG}, {imageField, "5", testPNG}}, 400},
		{"empty", []part{{imageField, "a.png", nil}}, 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, rt := uploadEnv(t)
			if w := e.upload(t, tt.parts...); w.Code != tt.status {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			if _, err := os.Stat(filepath.Join(rt, "uploads")); err == nil {
				t.Error("something was stored")
			}
		})
	}
}

func TestUploadGuards(t *testing.T) {
	body, ct := multipartBody(t, part{imageField, "a.png", testPNG})
	setCT := func(r *http.Request) { r.Header.Set("Content-Type", ct) }
	t.Run("csrf", func(t *testing.T) {
		e, _ := uploadEnv(t)
		w := e.post(winPath+"/upload", body, setCT, withoutHeader(CSRFHeader))
		if w.Code != http.StatusForbidden || errCode(t, w) != codeCSRF {
			t.Fatalf("status %d: %s", w.Code, w.Body)
		}
	})
	t.Run("no device cookie", func(t *testing.T) {
		e, _ := uploadEnv(t)
		r := httptest.NewRequest("POST", winPath+"/upload", strings.NewReader(body))
		r.RemoteAddr = "192.168.1.20:51515"
		r.Header.Set("Content-Type", ct)
		r.Header.Set(CSRFHeader, "1")
		w := httptest.NewRecorder()
		e.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("status %d: %s", w.Code, w.Body)
		}
	})
	t.Run("write disabled", func(t *testing.T) {
		e := newWriteEnv(t, false)
		w := e.post(winPath+"/upload", body, setCT)
		if w.Code != http.StatusForbidden || errCode(t, w) != codeWriteDisabled {
			t.Fatalf("status %d: %s", w.Code, w.Body)
		}
	})
	t.Run("json content type", func(t *testing.T) {
		e, _ := uploadEnv(t)
		if w := e.post(winPath+"/upload", `{}`); w.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("status %d: %s", w.Code, w.Body)
		}
	})
	t.Run("unknown window", func(t *testing.T) {
		e, _ := uploadEnv(t)
		w := e.post("/v1/sessions/agentctl-proj/windows/@999/upload", body, setCT)
		if w.Code != http.StatusNotFound {
			t.Fatalf("status %d: %s", w.Code, w.Body)
		}
	})
}

func TestReplyWithImages(t *testing.T) {
	for _, route := range []string{"reply", "queue"} {
		t.Run(route, func(t *testing.T) {
			e, rt := uploadEnv(t)
			ids := uploadedIDs(t, e.upload(t, part{imageField, "a.png", testPNG}, part{imageField, "b.png", testJPEG}))
			req, _ := json.Marshal(replyIn{Text: "see these", Images: ids})
			if w := e.post(winPath+"/"+route, string(req)); w.Code != http.StatusOK {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			p0, _ := uploads.Path(rt, "@523", ids[0])
			p1, _ := uploads.Path(rt, "@523", ids[1])
			want := "see these\n\n[Screenshot: " + p0 + "]\n[Screenshot: " + p1 + "]"
			if len(e.inj.got) != 1 || e.inj.got[0] != want {
				t.Fatalf("injected %q, want %q", e.inj.got, want)
			}
		})
	}
}

func TestReplyImageOnlyAndBadImages(t *testing.T) {
	e, rt := uploadEnv(t)
	ids := uploadedIDs(t, e.upload(t, part{imageField, "a.png", testPNG}))
	req, _ := json.Marshal(replyIn{Images: ids})
	if w := e.post(winPath+"/reply", string(req)); w.Code != http.StatusOK {
		t.Fatalf("image-only: status %d: %s", w.Code, w.Body)
	}
	p, _ := uploads.Path(rt, "@523", ids[0])
	if e.inj.got[0] != "[Screenshot: "+p+"]" {
		t.Errorf("injected %q", e.inj.got[0])
	}
	e.inj.got = nil
	for name, body := range map[string]string{
		"traversal":    `{"text":"x","images":["../../../etc/passwd"]}`,
		"unknown id":   `{"text":"x","images":["` + strings.Repeat("a", 32) + `.png"]}`,
		"too many":     `{"text":"x","images":["a","b","c","d","e"]}`,
		"empty":        `{"text":""}`,
		"command text": `{"text":"/permissions","images":["` + ids[0] + `"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if w := e.post(winPath+"/reply", body); w.Code != http.StatusBadRequest {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			if len(e.inj.got) != 0 {
				t.Fatalf("injected %q", e.inj.got)
			}
		})
	}
}

func TestReplyCannotUseAnotherWindowsImage(t *testing.T) {
	e, rt := uploadEnv(t)
	id, err := uploads.Save(rt, "@524", ".png", testPNG)
	if err != nil {
		t.Fatal(err)
	}
	if w := e.post(winPath+"/reply", `{"text":"x","images":["`+id+`"]}`); w.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
}

func TestReplyWithImageToPi(t *testing.T) {
	e := newWriteEnv(t, true)
	g := piGroup("idle")
	g.RuntimeDir = filepath.Join(t.TempDir(), ".agentctl")
	e.fleet.set(g)
	e.panes.set("")
	ids := uploadedIDs(t, e.upload(t, part{imageField, "a.png", testPNG}))
	req, _ := json.Marshal(replyIn{Text: "look", Images: ids})
	if w := e.post(winPath+"/reply", string(req)); w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	p, _ := uploads.Path(g.RuntimeDir, "@523", ids[0])
	if len(e.inj.got) != 1 || e.inj.got[0] != "look\n\n[Screenshot: "+p+"]" {
		t.Fatalf("injected %q", e.inj.got)
	}
	if !e.inj.tgts[0].Unclassified {
		t.Error("target not marked Unclassified")
	}
}
