package serve

import (
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/uploads"
)

const dispatchUploadPath = "/v1/dispatch/upload"

// dispatchUploadBody builds a multipart body with a project_dir field (when
// projectDir is non-nil) followed by the image parts.
func dispatchUploadBody(t *testing.T, projectDir *string, parts ...part) (string, string) {
	t.Helper()
	var sb strings.Builder
	mw := multipart.NewWriter(&sb)
	if projectDir != nil {
		if err := mw.WriteField(projectDirField, *projectDir); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range parts {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", `form-data; name="`+p.name+`"; filename="`+p.filename+`"`)
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
	return sb.String(), mw.FormDataContentType()
}

func (e *writeEnv) dispatchUpload(t *testing.T, dir string, parts ...part) *httptest.ResponseRecorder {
	t.Helper()
	body, ct := dispatchUploadBody(t, &dir, parts...)
	return e.post(dispatchUploadPath, body, func(r *http.Request) { r.Header.Set("Content-Type", ct) })
}

// dispatchUploadEnv has a project at /code/proj whose runtime dir is a temp
// dir, and a second project /code/other with its own.
func dispatchUploadEnv(t *testing.T) (e *writeEnv, rt, otherRT string) {
	t.Helper()
	e = newWriteEnv(t, true)
	g := writeGroup("idle", false, "go-engineer", "dispatch")
	g.RuntimeDir = filepath.Join(t.TempDir(), ".agentctl")
	o := writeGroup("idle", false, "go-engineer", "dispatch")
	o.Session, o.Key, o.Dir, o.Project = "agentctl-other", "/code/other", "/code/other", "other"
	o.RuntimeDir = filepath.Join(t.TempDir(), ".agentctl")
	e.fleet.set(g, o)
	return e, g.RuntimeDir, o.RuntimeDir
}

func TestDispatchUploadStoresImages(t *testing.T) {
	e, rt, otherRT := dispatchUploadEnv(t)
	ids := uploadedIDs(t, e.dispatchUpload(t, "/code/proj", part{imageField, "../../evil.sh", testPNG}, part{imageField, "x.png", testJPEG}))
	if len(ids) != 2 || !strings.HasSuffix(ids[0], ".png") || !strings.HasSuffix(ids[1], ".jpg") {
		t.Fatalf("ids = %v", ids)
	}
	for _, id := range ids {
		fi, err := os.Stat(filepath.Join(rt, "uploads", uploads.DispatchKey, id))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v", id, fi.Mode().Perm())
		}
	}
	if di, _ := os.Stat(filepath.Join(rt, "uploads", uploads.DispatchKey)); di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v", di.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(otherRT, "uploads")); err == nil {
		t.Error("another project stored something")
	}
	l := e.auditLines(t)
	if len(l) != 2 || l[1].Action != "dispatch-upload" || l[1].Result != "ok" || l[1].Session != "agentctl-proj" || len(l[1].BodySHA256) != 64 {
		t.Errorf("audit = %+v", l)
	}
}

func TestDispatchUploadRejects(t *testing.T) {
	proj := "/code/proj"
	bigPNG := append(append([]byte{}, testPNG...), make([]byte, maxImageBytes)...)
	tests := []struct {
		name   string
		dir    *string
		parts  []part
		status int
		code   string
	}{
		{"non-image bytes", &proj, []part{{imageField, "a.png", []byte("#!/bin/sh\nrm -rf ~\n")}}, 400, codeBadRequest},
		{"oversize", &proj, []part{{imageField, "a.png", bigPNG}}, 413, codeTooLarge},
		{"too many", &proj, []part{{imageField, "1", testPNG}, {imageField, "2", testPNG}, {imageField, "3", testPNG}, {imageField, "4", testPNG}, {imageField, "5", testPNG}}, 400, codeBadRequest},
		{"no image part", &proj, []part{{"other", "a.png", testPNG}}, 400, codeBadRequest},
		{"missing project_dir", nil, []part{{imageField, "a.png", testPNG}}, 400, codeUnknownProject},
		{"unknown project_dir", ptr("/code/nope"), []part{{imageField, "a.png", testPNG}}, 400, codeUnknownProject},
		{"inexact project_dir", ptr("/code/proj/"), []part{{imageField, "a.png", testPNG}}, 400, codeUnknownProject},
		{"relative project_dir", ptr("proj"), []part{{imageField, "a.png", testPNG}}, 400, codeUnknownProject},
		{"traversal project_dir", ptr("/code/proj/../other"), []part{{imageField, "a.png", testPNG}}, 400, codeUnknownProject},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, rt, otherRT := dispatchUploadEnv(t)
			body, ct := dispatchUploadBody(t, tt.dir, tt.parts...)
			w := e.post(dispatchUploadPath, body, func(r *http.Request) { r.Header.Set("Content-Type", ct) })
			if w.Code != tt.status || errCode(t, w) != tt.code {
				t.Fatalf("status %d: %s, want %d %s", w.Code, w.Body, tt.status, tt.code)
			}
			for _, d := range []string{rt, otherRT} {
				if _, err := os.Stat(filepath.Join(d, "uploads")); err == nil {
					t.Error("something was stored")
				}
			}
		})
	}
}

func ptr(s string) *string { return &s }

func TestDispatchUploadGuards(t *testing.T) {
	proj := "/code/proj"
	body, ct := dispatchUploadBody(t, &proj, part{imageField, "a.png", testPNG})
	setCT := func(r *http.Request) { r.Header.Set("Content-Type", ct) }
	t.Run("csrf", func(t *testing.T) {
		e, _, _ := dispatchUploadEnv(t)
		w := e.post(dispatchUploadPath, body, setCT, withoutHeader(CSRFHeader))
		if w.Code != http.StatusForbidden || errCode(t, w) != codeCSRF {
			t.Fatalf("status %d: %s", w.Code, w.Body)
		}
	})
	t.Run("no device cookie", func(t *testing.T) {
		e, _, _ := dispatchUploadEnv(t)
		r := httptest.NewRequest("POST", dispatchUploadPath, strings.NewReader(body))
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
		w := e.post(dispatchUploadPath, body, setCT)
		if w.Code != http.StatusForbidden || errCode(t, w) != codeWriteDisabled {
			t.Fatalf("status %d: %s", w.Code, w.Body)
		}
		if slices.Contains(e.Server.router.Features(), "dispatch-upload") {
			t.Error("dispatch-upload advertised with writes disabled")
		}
		if l := e.auditLines(t); len(l) != 1 || l[0].Action != "dispatch-upload" {
			t.Errorf("audit = %+v", l)
		}
	})
	t.Run("json content type", func(t *testing.T) {
		e, _, _ := dispatchUploadEnv(t)
		if w := e.post(dispatchUploadPath, `{}`); w.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("status %d: %s", w.Code, w.Body)
		}
	})
	t.Run("feature advertised", func(t *testing.T) {
		e, _, _ := dispatchUploadEnv(t)
		if !slices.Contains(e.Server.router.Features(), "dispatch-upload") {
			t.Errorf("features = %v", e.Server.router.Features())
		}
	})
}

func (e *writeEnv) dispatchWith(t *testing.T, dir, prompt string, ids []string) *httptest.ResponseRecorder {
	t.Helper()
	req, _ := json.Marshal(dispatchIn{Agent: "go-engineer", ProjectDir: dir, Prompt: prompt, Images: ids})
	return e.post("/v1/dispatch", string(req))
}

func TestDispatchWithImages(t *testing.T) {
	e, rt, _ := dispatchUploadEnv(t)
	ids := uploadedIDs(t, e.dispatchUpload(t, "/code/proj", part{imageField, "a.png", testPNG}, part{imageField, "b.png", testJPEG}))
	// Duplicates collapse.
	w := e.dispatchWith(t, "/code/proj", "the bug\nis here\n", []string{ids[0], ids[1], ids[0]})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	p0, _ := uploads.Path(rt, uploads.DispatchKey, ids[0])
	p1, _ := uploads.Path(rt, uploads.DispatchKey, ids[1])
	want := "the bug\nis here\n\n[Screenshot: " + p0 + "]\n[Screenshot: " + p1 + "]"
	if len(e.disp.got) != 1 || e.disp.got[0].Prompt != want {
		t.Fatalf("dispatches = %+v, want one with prompt %q", e.disp.got, want)
	}
}

func TestDispatchImageRejects(t *testing.T) {
	e, rt, _ := dispatchUploadEnv(t)
	ids := uploadedIDs(t, e.dispatchUpload(t, "/code/proj", part{imageField, "a.png", testPNG}))
	otherIDs := uploadedIDs(t, e.dispatchUpload(t, "/code/other", part{imageField, "a.png", testPNG}))
	windowID, err := uploads.Save(rt, "@523", ".png", testPNG)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		prompt string
		ids    []string
		status int
	}{
		{"unknown id", "x", []string{strings.Repeat("a", 32) + ".png"}, 400},
		{"traversal", "x", []string{"../../../etc/passwd"}, 400},
		{"other project's id", "x", otherIDs, 400},
		{"window upload id", "x", []string{windowID}, 400},
		{"too many", "x", []string{"a", "b", "c", "d", "e"}, 400},
		{"image only", "  ", ids, 400},
		{"prompt overflows with images", strings.Repeat("x", maxPromptRunes-10), ids, 413},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := e.dispatchWith(t, "/code/proj", tt.prompt, tt.ids)
			if w.Code != tt.status {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			if len(e.disp.got) != 0 {
				t.Fatalf("dispatched %+v", e.disp.got)
			}
		})
	}
	// The same ID is valid for the project it was uploaded to.
	if w := e.dispatchWith(t, "/code/other", "ok", otherIDs); w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
}
