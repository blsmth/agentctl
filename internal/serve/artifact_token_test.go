package serve

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/artifacts"
)

// tokenFor returns the capability URL prefix the cookie route redirects to.
func (ae *artifactEnv) tokenFor(t *testing.T, id string, rev string) (root, tok string) {
	t.Helper()
	w := ae.get("/a/" + id + "/" + rev + "/")
	loc := w.Header().Get("Location")
	if w.Code != http.StatusFound || !strings.HasPrefix(loc, "/a/t/") {
		t.Fatalf("no capability redirect: %d %q", w.Code, loc)
	}
	return loc, strings.Split(loc, "/")[3]
}

func TestArtifactTokenScope(t *testing.T) {
	ae := newArtifactEnv(t, false)
	a := ae.add(t, map[string]string{"index.html": "<p>A1</p>", "s.css": "a"}, artifacts.AddOptions{})
	b := ae.add(t, map[string]string{"index.html": "<p>B</p>"}, artifacts.AddOptions{})
	if _, err := ae.store.Update(a.ID, artifacts.UpdateOptions{Source: artifacts.Source{Path: writeTemp(t, "index.html", "<p>A2</p>")}}); err != nil {
		t.Fatal(err)
	}
	root, tok := ae.tokenFor(t, a.ID, "1")
	if root != "/a/t/"+tok+"/"+a.ID+"/1/" {
		t.Fatalf("root = %q", root)
	}

	ok := do(ae.Handler(), "GET", root+"s.css")
	if ok.Code != http.StatusOK || ok.Body.String() != "a" {
		t.Fatalf("valid token = %d %s", ok.Code, ok.Body)
	}
	assertSandboxedAt(t, ok, true)
	// A token path never needs or reads the cookie: a bogus one changes nothing.
	if w := do(ae.Handler(), "GET", root+"s.css", withCookie("bogus")); w.Code != http.StatusOK {
		t.Errorf("token with a bad cookie = %d", w.Code)
	}

	tampered := []byte(tok)
	last := len(tampered) - 1
	if tampered[last] == 'A' {
		tampered[last] = 'B'
	} else {
		tampered[last] = 'A'
	}
	parts := strings.SplitN(tok, ".", 3)
	otherDevice := parts[0] + ".000000000000." + parts[2]
	later := parts[0] + "z." + parts[1] + "." + parts[2] // a longer expiry, same MAC
	for name, target := range map[string]string{
		"other artifact":     "/a/t/" + tok + "/" + b.ID + "/1/",
		"other rev":          "/a/t/" + tok + "/" + a.ID + "/2/",
		"latest":             "/a/t/" + tok + "/" + a.ID + "/latest/",
		"tampered mac":       "/a/t/" + string(tampered) + "/" + a.ID + "/1/",
		"other device":       "/a/t/" + otherDevice + "/" + a.ID + "/1/",
		"extended expiry":    "/a/t/" + later + "/" + a.ID + "/1/",
		"garbage":            "/a/t/xyz/" + a.ID + "/1/",
		"empty token":        "/a/t//" + a.ID + "/1/",
		"token root garbage": "/a/t/xyz/" + a.ID + "/1",
	} {
		w := do(ae.Handler(), "GET", target)
		if w.Code != http.StatusForbidden && w.Code != http.StatusBadRequest {
			t.Errorf("%s: %s = %d", name, target, w.Code)
		}
		if strings.Contains(w.Body.String(), "<p>") {
			t.Errorf("%s leaked content", name)
		}
		assertSandboxedAt(t, w, true)
	}

	// Traversal on a capability path is refused before routing, too.
	for _, rel := range []string{"../2/index.html", "%2e%2e/2/index.html", "..%2f2%2findex.html"} {
		if w := do(ae.Handler(), "GET", root+rel); w.Code != http.StatusBadRequest {
			t.Errorf("traversal %q = %d", rel, w.Code)
		}
	}

	// Expiry: valid for at least the TTL, refused after TTL + rounding.
	ae.clock.Advance(artifactTokenTTL - time.Minute)
	if w := do(ae.Handler(), "GET", root); w.Code != http.StatusOK {
		t.Errorf("before expiry = %d", w.Code)
	}
	ae.clock.Advance(2*time.Hour + time.Minute)
	if w := do(ae.Handler(), "GET", root); w.Code != http.StatusForbidden {
		t.Errorf("after expiry = %d", w.Code)
	}
}

func TestArtifactTokenRevokedDevice(t *testing.T) {
	ae := newArtifactEnv(t, false)
	a := ae.add(t, map[string]string{"a.txt": "x"}, artifacts.AddOptions{})
	root, _ := ae.tokenFor(t, a.ID, "latest")
	if w := do(ae.Handler(), "GET", root); w.Code != http.StatusOK {
		t.Fatalf("before revoke = %d", w.Code)
	}
	if _, err := ae.devices.Remove(ae.dev.ID); err != nil {
		t.Fatal(err)
	}
	if w := do(ae.Handler(), "GET", root); w.Code != http.StatusForbidden {
		t.Errorf("after revoke = %d", w.Code)
	}
}

// Minting is stable within the hour (the viewer's iframe does not reload on
// every list refresh) and per device.
func TestArtifactTokenStableAndPerDevice(t *testing.T) {
	ae := newArtifactEnv(t, false)
	a := ae.add(t, map[string]string{"a.txt": "x"}, artifacts.AddOptions{})
	t1 := ae.mintArtifactToken(a.ID, 1, ae.dev.ID)
	ae.clock.Advance(time.Second)
	if t2 := ae.mintArtifactToken(a.ID, 1, ae.dev.ID); t2 != t1 && ae.clock.Now().Minute() != 0 {
		t.Errorf("token changed within the hour: %s vs %s", t1, t2)
	}
	other, _ := ae.pairDevice(t, "laptop")
	if ae.mintArtifactToken(a.ID, 1, other.ID) == t1 {
		t.Error("two devices got the same token")
	}
}

func TestArtifactKeyPersisted(t *testing.T) {
	dir := t.TempDir()
	k1, err := loadOrCreateArtifactKey(dir)
	if err != nil || len(k1) != artifactKeyLen {
		t.Fatalf("key = %d %v", len(k1), err)
	}
	fi, err := os.Stat(filepath.Join(dir, ArtifactKeyFile))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("key file mode = %v %v", fi, err)
	}
	k2, err := loadOrCreateArtifactKey(dir)
	if err != nil || string(k1) != string(k2) {
		t.Errorf("key not reused: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ArtifactKeyFile), []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateArtifactKey(dir); err == nil {
		t.Error("a damaged key was accepted")
	}
}
