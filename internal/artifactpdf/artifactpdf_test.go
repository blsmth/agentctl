package artifactpdf

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/artifacts"
)

func newStore(t *testing.T) *artifacts.Store {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "artifacts")
	return &artifacts.Store{Dir: func() (string, error) { return dir, nil }, Now: time.Now}
}

func addFile(t *testing.T, s *artifacts.Store, name, content string) artifacts.Meta {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := s.Add(artifacts.AddOptions{Source: artifacts.Source{Path: p}, NoNotify: true})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func addDir(t *testing.T, s *artifacts.Store, files map[string]string) artifacts.Meta {
	t.Helper()
	d := filepath.Join(t.TempDir(), "site")
	for rel, content := range files {
		p := filepath.Join(d, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m, err := s.Add(artifacts.AddOptions{Source: artifacts.Source{Path: d}, NoNotify: true})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// call is one stubbed browser run.
type call struct {
	bin  string
	args []string
}

// stubBrowser replaces the exec seam and the browser lookup. fn runs in place
// of the browser with the parsed --print-to-pdf path and the URL.
func stubBrowser(t *testing.T, fn func(ctx context.Context, out, url string) (string, error)) *[]call {
	t.Helper()
	var mu sync.Mutex
	var calls []call
	oldRun, oldCfg := runBrowser, configured
	configured = func() string { return "" }
	statFile = func(string) (os.FileInfo, error) { return os.Stat(os.Args[0]) }
	runBrowser = func(ctx context.Context, bin string, args []string) (string, error) {
		mu.Lock()
		calls = append(calls, call{bin, args})
		mu.Unlock()
		var out string
		for _, a := range args {
			if v, ok := strings.CutPrefix(a, "--print-to-pdf="); ok {
				out = v
			}
		}
		return fn(ctx, out, args[len(args)-1])
	}
	t.Cleanup(func() { runBrowser, configured, statFile = oldRun, oldCfg, os.Stat })
	return &calls
}

func get(t *testing.T, url string) (int, string, http.Header) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func writePDF(out string) error { return os.WriteFile(out, []byte("%PDF-1.4 fake"), 0o644) }

func TestRenderArgsAndEntryURL(t *testing.T) {
	s := newStore(t)
	m := addFile(t, s, "report.html", "<title>R</title><p>hi</p>")
	calls := stubBrowser(t, func(_ context.Context, out, url string) (string, error) {
		code, body, hdr := get(t, url)
		if code != 200 || !strings.Contains(body, "<p>hi</p>") || !strings.HasPrefix(hdr.Get("Content-Type"), "text/html") {
			t.Errorf("entry: %d %q %v", code, body, hdr)
		}
		return "", writePDF(out)
	})
	var buf bytes.Buffer
	if err := Render(context.Background(), s, m.ID, 0, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(buf.String(), "%PDF-") {
		t.Fatalf("output = %q", buf.String())
	}
	if len(*calls) != 1 {
		t.Fatalf("calls = %d", len(*calls))
	}
	args := (*calls)[0].args
	for _, want := range []string{
		"--headless=new", "--disable-gpu", "--no-first-run", "--no-default-browser-check",
		"--no-pdf-header-footer", "--hide-scrollbars", "--blink-settings=preferredColorScheme=1",
		"--virtual-time-budget=15000", "--timeout=30000",
	} {
		if !contains(args, want) {
			t.Errorf("args missing %s: %v", want, args)
		}
	}
	var profile, out string
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, "--user-data-dir="); ok {
			profile = v
		}
		if v, ok := strings.CutPrefix(a, "--print-to-pdf="); ok {
			out = v
		}
	}
	if profile == "" || out == "" || filepath.Dir(profile) != filepath.Dir(out) {
		t.Errorf("profile %q, out %q", profile, out)
	}
	if !strings.HasPrefix(args[len(args)-1], "http://127.0.0.1:") || !strings.HasSuffix(args[len(args)-1], "/report.html?print=1") {
		t.Errorf("url = %s", args[len(args)-1])
	}
	if _, err := os.Stat(filepath.Dir(out)); !os.IsNotExist(err) {
		t.Errorf("temp dir not removed: %v", err)
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func TestServerKinds(t *testing.T) {
	s := newStore(t)
	md := addFile(t, s, "notes.md", "# Title\n\n| a | b |\n|---|---|\n| 1 | 2 |\n")
	txt := addFile(t, s, "n.txt", "<plain>")
	dir := addDir(t, s, map[string]string{
		"index.html": `<link rel=stylesheet href="css/a.css">`,
		"css/a.css":  "body{color:red}",
	})
	img := addFile(t, s, "pic.png", "\x89PNG")
	if img.Kind != artifacts.KindImage {
		t.Fatalf("kind = %s", img.Kind)
	}
	tests := []struct {
		name, id, rel string
		code          int
		ctype, body   string
	}{
		{"markdown page", md.ID, "notes.md", 200, "text/html", "<h1"},
		{"markdown raw", md.ID, "notes.md?raw=1", 200, "text/plain", "# Title"},
		{"text page", txt.ID, "n.txt", 200, "text/html", "&lt;plain&gt;"},
		{"image page", img.ID, "pic.png", 200, "text/html", `src="pic.png?raw=1"`},
		{"image raw", img.ID, "pic.png?raw=1", 200, "image/png", "PNG"},
		{"dir entry", dir.ID, "index.html", 200, "text/html", "css/a.css"},
		{"dir asset", dir.ID, "css/a.css", 200, "text/css", "color:red"},
		{"missing", dir.ID, "nope.js", 404, "text/html", ""},
		{"dotdot", dir.ID, "css/../index.html", 400, "text/html", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := s.Get(tt.id)
			if err != nil {
				t.Fatal(err)
			}
			entry, stop, err := serveSource(s, m, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			base := entry[:strings.LastIndex(entry, m.Entry)]
			code, body, hdr := get(t, base+tt.rel)
			if code != tt.code || !strings.HasPrefix(hdr.Get("Content-Type"), tt.ctype) || !strings.Contains(body, tt.body) {
				t.Errorf("got %d %s %q", code, hdr.Get("Content-Type"), body)
			}
		})
	}
}

func TestServerRefusesWrongTokenAndHost(t *testing.T) {
	s := newStore(t)
	m := addFile(t, s, "r.html", "x")
	entry, stop, err := serveSource(s, m, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if code, _, _ := get(t, entry); code != 200 {
		t.Fatalf("good url = %d", code)
	}
	host := entry[len("http://") : strings.Index(entry[len("http://"):], "/")+len("http://")]
	if code, _, _ := get(t, "http://"+host+"/r.html"); code != 404 {
		t.Errorf("no token = %d", code)
	}
	req, _ := http.NewRequest(http.MethodGet, entry, nil)
	req.Host = "evil.example"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("wrong host = %d", resp.StatusCode)
	}
}

func TestRenderRevisions(t *testing.T) {
	s := newStore(t)
	m := addFile(t, s, "r.html", "<p>one</p>")
	p := filepath.Join(t.TempDir(), "r.html")
	if err := os.WriteFile(p, []byte("<p>two</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(m.ID, artifacts.UpdateOptions{Source: artifacts.Source{Path: p}}); err != nil {
		t.Fatal(err)
	}
	var seen []string
	stubBrowser(t, func(_ context.Context, out, url string) (string, error) {
		_, body, _ := get(t, url)
		seen = append(seen, body)
		return "", writePDF(out)
	})
	for _, rev := range []int{0, 1} {
		if err := Render(context.Background(), s, m.ID, rev, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	if seen[0] != "<p>two</p>" || seen[1] != "<p>one</p>" {
		t.Errorf("seen = %q", seen)
	}
	err := Render(context.Background(), s, m.ID, 9, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "no revision 9") {
		t.Errorf("bad rev err = %v", err)
	}
	if err := Render(context.Background(), s, "nosuchid", 0, io.Discard); !errors.Is(err, artifacts.ErrNotFound) {
		t.Errorf("unknown id err = %v", err)
	}
}

func TestRenderFailures(t *testing.T) {
	s := newStore(t)
	m := addFile(t, s, "r.html", "x")
	t.Run("no output carries stderr", func(t *testing.T) {
		stubBrowser(t, func(context.Context, string, string) (string, error) {
			return "boom: sandbox failed\n", errors.New("exit status 1")
		})
		err := Render(context.Background(), s, m.ID, 0, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "boom: sandbox failed") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("empty output", func(t *testing.T) {
		stubBrowser(t, func(_ context.Context, out, _ string) (string, error) {
			return "", os.WriteFile(out, nil, 0o644)
		})
		if err := Render(context.Background(), s, m.ID, 0, io.Discard); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("not a pdf", func(t *testing.T) {
		stubBrowser(t, func(_ context.Context, out, _ string) (string, error) {
			return "", os.WriteFile(out, []byte("<html>"), 0o644)
		})
		if err := Render(context.Background(), s, m.ID, 0, io.Discard); err == nil || !strings.Contains(err.Error(), "not a PDF") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("canceled", func(t *testing.T) {
		stubBrowser(t, func(ctx context.Context, _, _ string) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := Render(ctx, s, m.ID, 0, io.Discard); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("no browser names the config key", func(t *testing.T) {
		stubBrowser(t, nil)
		statFile = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
		lookPath = func(string) (string, error) { return "", errors.New("not found") }
		t.Cleanup(func() { lookPath = defaultLookPath })
		err := Render(context.Background(), s, m.ID, 0, io.Discard)
		if !errors.Is(err, ErrNoBrowser) || !strings.Contains(err.Error(), ConfigKey) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestTailDropsVerboseNoise(t *testing.T) {
	got := tail("[1:2:0101/000000:VERBOSE1:updater.cc:1] x\nreal error\n")
	if got != ":\nreal error" {
		t.Errorf("tail = %q", got)
	}
	if long := tail(strings.Repeat("e", 5000)); len(long) > stderrTail+10 {
		t.Errorf("tail not bounded: %d", len(long))
	}
}

func TestConcurrentRendersDoNotShare(t *testing.T) {
	s := newStore(t)
	a := addFile(t, s, "a.html", "<p>A</p>")
	b := addFile(t, s, "b.html", "<p>B</p>")
	var mu sync.Mutex
	dirs := map[string]bool{}
	hosts := map[string]bool{}
	stubBrowser(t, func(_ context.Context, out, url string) (string, error) {
		mu.Lock()
		dirs[filepath.Dir(out)] = true
		hosts[strings.Split(url, "/")[2]] = true
		mu.Unlock()
		time.Sleep(50 * time.Millisecond) // overlap the two runs
		return "", writePDF(out)
	})
	var wg sync.WaitGroup
	for _, id := range []string{a.ID, b.ID} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := Render(context.Background(), s, id, 0, io.Discard); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(dirs) != 2 || len(hosts) != 2 {
		t.Errorf("dirs %d hosts %d, want 2 each", len(dirs), len(hosts))
	}
}

func TestFindBrowser(t *testing.T) {
	exe := func(p string) (os.FileInfo, error) { return os.Stat(os.Args[0]) }
	oldStat, oldLook, oldCfg := statFile, lookPath, configured
	t.Cleanup(func() { statFile, lookPath, configured = oldStat, oldLook, oldCfg })

	t.Run("configured wins", func(t *testing.T) {
		configured = func() string { return "/opt/br" }
		statFile = exe
		if got, err := FindBrowser(); err != nil || got != "/opt/br" {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	t.Run("configured but missing is an error, no fallback", func(t *testing.T) {
		configured = func() string { return "/opt/br" }
		statFile = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
		_, err := FindBrowser()
		if err == nil || !strings.Contains(err.Error(), ConfigKey) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("app bundle order", func(t *testing.T) {
		configured = func() string { return "" }
		statFile = func(p string) (os.FileInfo, error) {
			if strings.Contains(p, "Chromium.app") || strings.Contains(p, "Edge") {
				return os.Stat(os.Args[0])
			}
			return nil, os.ErrNotExist
		}
		got, _ := FindBrowser()
		if !strings.Contains(got, "Chromium.app") {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("PATH order", func(t *testing.T) {
		statFile = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
		lookPath = func(n string) (string, error) {
			if n == "chromium" || n == "chromium-browser" {
				return "/usr/bin/" + n, nil
			}
			return "", errors.New("no")
		}
		if got, _ := FindBrowser(); got != "/usr/bin/chromium" {
			t.Fatalf("got %q", got)
		}
	})
}
