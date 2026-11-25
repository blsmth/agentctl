package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/blsmth/agentctl/internal/artifacts"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/windowstate"
)

type artifactHarness struct {
	env     artifactEnv
	errBuf  *bytes.Buffer
	opened  []string
	running bool
	origin  artifacts.Origin
	tty     bool
}

func newArtifactHarness(t *testing.T) *artifactHarness {
	t.Helper()
	h := &artifactHarness{errBuf: &bytes.Buffer{}}
	dir := filepath.Join(t.TempDir(), "artifacts")
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	store := &artifacts.Store{Dir: func() (string, error) { return dir, nil }, Now: func() time.Time { return now }}
	h.origin = artifacts.Origin{ProjectDir: "/r/proj", Project: "proj", Agent: "go-engineer", WindowID: "@7"}
	h.env = artifactEnv{
		store:    func() *artifacts.Store { return store },
		origin:   func() artifacts.Origin { return h.origin },
		serveURL: func() (string, bool) { return "https://box.local:8443", h.running },
		openBrowser: func(u string) error {
			h.opened = append(h.opened, u)
			return nil
		},
		interactive: func() bool { return h.tty },
		errw:        h.errBuf,
		now:         func() time.Time { return now },
	}
	return h
}

func writeTmp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestArtifactAddPrintsURLAndNotesServeDown(t *testing.T) {
	h := newArtifactHarness(t)
	src := writeTmp(t, "r.md", "# Report\n")
	var out bytes.Buffer
	if err := h.env.add(&out, ArtifactAddOptions{Path: src}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], `"Report"`) {
		t.Fatalf("stdout = %q", out.String())
	}
	id := strings.Fields(lines[0])[3]
	if !artifacts.CapabilityID(id) {
		t.Errorf("id %q is not a capability id", id)
	}
	// The direct link first (any browser, no pairing), then the phone app.
	if lines[1] != "https://box.local:8443/a/"+id+"/latest/" || lines[2] != "app: https://box.local:8443/#/artifacts/"+id {
		t.Errorf("stdout = %q", out.String())
	}
	if !strings.Contains(h.errBuf.String(), "serve is not running") {
		t.Errorf("stderr = %q", h.errBuf.String())
	}
	h.errBuf.Reset()
	h.running = true
	out.Reset()
	if err := h.env.add(&out, ArtifactAddOptions{Path: src}); err != nil || h.errBuf.Len() != 0 {
		t.Errorf("running serve: err=%v stderr=%q", err, h.errBuf.String())
	}
}

func TestArtifactAddJSONAndOrigin(t *testing.T) {
	h := newArtifactHarness(t)
	src := writeTmp(t, "p.html", "<title>Page</title>")
	var out bytes.Buffer
	if err := h.env.add(&out, ArtifactAddOptions{Path: src, JSON: true, Pin: true, Description: "d"}); err != nil {
		t.Fatal(err)
	}
	var v artifactView
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		t.Fatalf("bad json %q: %v", out.String(), err)
	}
	if v.Title != "Page" || v.Kind != "html" || !v.Pinned || v.Agent != "go-engineer" || v.WindowID != "@7" ||
		v.Project != "proj" || v.ServeRunning || v.URL != "https://box.local:8443/a/"+v.ID+"/latest/" ||
		v.AppURL != "https://box.local:8443/#/artifacts/"+v.ID {
		t.Errorf("view = %+v", v)
	}
}

func TestArtifactStdin(t *testing.T) {
	h := newArtifactHarness(t)
	e := h.env
	e.stdin = strings.NewReader("<title>Piped</title>")
	var out bytes.Buffer
	if err := e.add(&out, ArtifactAddOptions{Path: "-", Entry: "report.html", JSON: true}); err != nil {
		t.Fatal(err)
	}
	var v artifactView
	_ = json.Unmarshal(out.Bytes(), &v)
	if v.Title != "Piped" || v.Entry != "report.html" {
		t.Errorf("view = %+v", v)
	}
	e.stdin = strings.NewReader("x")
	if err := e.add(&out, ArtifactAddOptions{Path: "-"}); err == nil || !strings.Contains(err.Error(), "--entry") {
		t.Errorf("stdin without hint: %v", err)
	}
	if err := e.add(&out, ArtifactAddOptions{}); err == nil {
		t.Error("no path accepted")
	}
}

func TestArtifactListScope(t *testing.T) {
	h := newArtifactHarness(t)
	src := writeTmp(t, "a.txt", "a")
	var sink bytes.Buffer
	add := func(o artifacts.Origin) {
		h.origin = o
		if err := h.env.add(&sink, ArtifactAddOptions{Path: src, JSON: true}); err != nil {
			t.Fatal(err)
		}
	}
	add(artifacts.Origin{ProjectDir: "/r/one", Project: "one", Agent: "a1"})
	add(artifacts.Origin{ProjectDir: "/r/two", Project: "two", Agent: "a2"})
	h.origin = artifacts.Origin{ProjectDir: "/r/one", Project: "one"}

	count := func(o ArtifactListOptions) int {
		t.Helper()
		o.JSON = true
		var out bytes.Buffer
		if err := h.env.list(&out, o); err != nil {
			t.Fatal(err)
		}
		var vs []artifactView
		if err := json.Unmarshal(out.Bytes(), &vs); err != nil {
			t.Fatal(err)
		}
		return len(vs)
	}
	if n := count(ArtifactListOptions{}); n != 1 {
		t.Errorf("default scope = %d, want 1 (current project)", n)
	}
	if n := count(ArtifactListOptions{All: true}); n != 2 {
		t.Errorf("--all = %d, want 2", n)
	}
	if n := count(ArtifactListOptions{Project: "two"}); n != 1 {
		t.Errorf("--project two = %d", n)
	}
	if n := count(ArtifactListOptions{All: true, Agent: "a2"}); n != 1 {
		t.Errorf("--agent = %d", n)
	}
	if n := count(ArtifactListOptions{All: true, Since: "1h"}); n != 2 {
		t.Errorf("--since 1h = %d", n)
	}
	if err := h.env.list(&sink, ArtifactListOptions{Since: "bogus"}); err == nil {
		t.Error("bad --since accepted")
	}
	var out bytes.Buffer
	h.origin = artifacts.Origin{ProjectDir: "/r/none"}
	if err := h.env.list(&out, ArtifactListOptions{}); err != nil || !strings.Contains(out.String(), "No artifacts in this project") {
		t.Errorf("empty list = %q %v", out.String(), err)
	}
	out.Reset()
	if err := h.env.list(&out, ArtifactListOptions{All: true}); err != nil || !strings.Contains(out.String(), "ID") || !strings.Contains(out.String(), "a1") {
		t.Errorf("table = %q %v", out.String(), err)
	}
}

func addOne(t *testing.T, h *artifactHarness) artifactView {
	t.Helper()
	var out bytes.Buffer
	if err := h.env.add(&out, ArtifactAddOptions{Path: writeTmp(t, "a.txt", "a"), JSON: true}); err != nil {
		t.Fatal(err)
	}
	var v artifactView
	_ = json.Unmarshal(out.Bytes(), &v)
	h.errBuf.Reset()
	return v
}

func TestArtifactUpdateShowURLOpen(t *testing.T) {
	h := newArtifactHarness(t)
	v := addOne(t, h)
	var out bytes.Buffer
	if err := h.env.update(&out, ArtifactUpdateOptions{ID: v.ID, Path: writeTmp(t, "a.txt", "b"), Note: "second"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "rev 2") {
		t.Errorf("update out = %q", out.String())
	}
	out.Reset()
	if err := h.env.show(&out, v.ID, false); err != nil || !strings.Contains(out.String(), "second") ||
		!strings.Contains(out.String(), "https://box.local:8443/a/"+v.ID+"/1/") ||
		!strings.Contains(out.String(), "url:         https://box.local:8443/a/"+v.ID+"/latest/") ||
		!strings.Contains(out.String(), "app:         https://box.local:8443/#/artifacts/"+v.ID) {
		t.Errorf("show = %q %v", out.String(), err)
	}
	out.Reset()
	if err := h.env.url(&out, v.ID, 0); err != nil || strings.TrimSpace(out.String()) != "https://box.local:8443/a/"+v.ID+"/latest/" {
		t.Errorf("url = %q %v", out.String(), err)
	}
	out.Reset()
	if err := h.env.url(&out, v.ID, 1); err != nil || strings.TrimSpace(out.String()) != "https://box.local:8443/a/"+v.ID+"/1/" {
		t.Errorf("url rev = %q %v", out.String(), err)
	}
	if err := h.env.url(&out, v.ID, 9); err == nil || !strings.Contains(err.Error(), "no revision 9") {
		t.Errorf("url bad rev: %v", err)
	}
	if err := h.env.open(&out, v.ID, 2); err != nil || len(h.opened) != 1 || h.opened[0] != "https://box.local:8443/a/"+v.ID+"/2/" {
		t.Errorf("open: %v %v", err, h.opened)
	}
	h.env.openBrowser = func(string) error { return errors.New("no display") }
	if err := h.env.open(&out, v.ID, 0); err == nil || !strings.Contains(err.Error(), v.ID) {
		t.Errorf("open failure should still name the URL: %v", err)
	}
}

func TestArtifactPinRm(t *testing.T) {
	h := newArtifactHarness(t)
	v := addOne(t, h)
	var out bytes.Buffer
	if err := h.env.pin(&out, v.ID, true); err != nil {
		t.Fatal(err)
	}
	if m, _ := h.env.store().Get(v.ID); !m.Pinned {
		t.Error("not pinned")
	}
	if err := h.env.pin(&out, v.ID, false); err != nil {
		t.Fatal(err)
	}
	// Non-interactive without --yes refuses.
	if err := h.env.rm(&out, v.ID, false); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("rm without yes: %v", err)
	}
	// Interactive: n aborts, y removes.
	h.tty = true
	e := h.env
	e.stdin = strings.NewReader("n\n")
	if err := e.rm(&out, v.ID, false); err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Errorf("rm n: %v", err)
	}
	e.stdin = strings.NewReader("y\n")
	if err := e.rm(&out, v.ID, false); err != nil {
		t.Fatal(err)
	}
	// Missing id is a clear error, with and without --yes.
	for _, yes := range []bool{true, false} {
		err := h.env.rm(&out, v.ID, yes)
		if !errors.Is(err, artifacts.ErrNotFound) || !strings.Contains(err.Error(), v.ID) {
			t.Errorf("rm missing (yes=%v): %v", yes, err)
		}
	}
	if err := h.env.pin(&out, "aaaaaaaaaa", true); !errors.Is(err, artifacts.ErrNotFound) {
		t.Errorf("pin missing: %v", err)
	}
}

func TestArtifactPrune(t *testing.T) {
	h := newArtifactHarness(t)
	v := addOne(t, h) // updated at the harness clock
	later := h.env.now().Add(40 * 24 * time.Hour)
	h.env.now = func() time.Time { return later }
	store := h.env.store()
	store.Now = func() time.Time { return later }

	var out bytes.Buffer
	if err := h.env.prune(&out, "", true); err != nil || !strings.Contains(out.String(), "Would prune "+v.ID) {
		t.Fatalf("dry run = %q %v", out.String(), err)
	}
	if _, err := store.Get(v.ID); err != nil {
		t.Fatal("dry run deleted")
	}
	out.Reset()
	if err := h.env.prune(&out, "7d", false); err != nil || !strings.Contains(out.String(), "Pruned "+v.ID) {
		t.Fatalf("prune = %q %v", out.String(), err)
	}
	out.Reset()
	if err := h.env.prune(&out, "7d", false); err != nil || !strings.Contains(out.String(), "Nothing to prune") {
		t.Errorf("second prune = %q %v", out.String(), err)
	}
	if err := h.env.prune(&out, "nope", false); err == nil {
		t.Error("bad --older-than accepted")
	}
}

func TestServeBaseURL(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.ServeConfig
		host string
		want string
	}{
		{"defaults", config.ServeConfig{}, "mac", "https://mac.local:8443"},
		{"hostname already .local", config.ServeConfig{}, "mac.local", "https://mac.local:8443"},
		{"first configured hostname", config.ServeConfig{Hostnames: []string{"agents.example.com", "x"}, Listen: ":9443"}, "mac", "https://agents.example.com:9443"},
		{"port 443 omitted", config.ServeConfig{Listen: ":443"}, "mac", "https://mac.local"},
		{"wildcard listen", config.ServeConfig{Listen: "0.0.0.0:8443"}, "mac", "https://mac.local:8443"},
		{"specific listen host", config.ServeConfig{Listen: "192.168.1.5:8443"}, "mac", "https://192.168.1.5:8443"},
		{"ipv6 listen", config.ServeConfig{Listen: "[fd00::1]:8443"}, "mac", "https://[fd00::1]:8443"},
		{"no hostname", config.ServeConfig{}, "", "https://localhost.local:8443"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := serveBaseURL(tt.cfg, tt.host); got != tt.want {
				t.Errorf("= %q, want %q", got, tt.want)
			}
		})
	}
}

func TestArtifactOrigin(t *testing.T) {
	origPane, origWin, origSess, origRead, origCwd := artifactPane, artifactWindowFor, artifactSessionOf, artifactReadWin, artifactCwd
	t.Cleanup(func() {
		artifactPane, artifactWindowFor, artifactSessionOf, artifactReadWin, artifactCwd = origPane, origWin, origSess, origRead, origCwd
	})
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(repo, ".worktrees", "agent-x-issue-1", "internal")

	t.Run("plain shell: project from cwd, worktree-aware, no window", func(t *testing.T) {
		artifactPane = func() string { return "" }
		artifactCwd = func() (string, error) { return wt, nil }
		artifactWindowFor = func(string) string { t.Fatal("tmux consulted without a pane"); return "" }
		o := artifactOrigin()
		if o.ProjectDir != repo || o.Project != filepath.Base(repo) || o.Agent != "" || o.WindowID != "" {
			t.Errorf("origin = %+v", o)
		}
	})
	t.Run("window: trusted pane to windowstate", func(t *testing.T) {
		artifactPane = func() string { return "%9" }
		artifactCwd = func() (string, error) { return "/elsewhere", nil }
		artifactWindowFor = func(p string) string {
			if p != "%9" {
				t.Errorf("pane = %q", p)
			}
			return "@12"
		}
		artifactSessionOf = func(string) string { return "agentctl-proj" }
		artifactReadWin = func(id string) (windowstate.WindowState, error) {
			if id != "@12" {
				t.Errorf("window = %q", id)
			}
			return windowstate.WindowState{AgentName: "go-engineer", SessionID: "uuid-1", Branch: "agent/b", WorkDir: wt}, nil
		}
		o := artifactOrigin()
		want := artifacts.Origin{ProjectDir: repo, Project: filepath.Base(repo), Agent: "go-engineer", WindowID: "@12",
			Session: "agentctl-proj", SessionID: "uuid-1", Branch: "agent/b"}
		if o != want {
			t.Errorf("origin = %+v\nwant     %+v", o, want)
		}
	})
	t.Run("window with unreadable state keeps the window id", func(t *testing.T) {
		artifactPane = func() string { return "%9" }
		artifactWindowFor = func(string) string { return "@12" }
		artifactReadWin = func(string) (windowstate.WindowState, error) { return windowstate.WindowState{}, errors.New("gone") }
		if o := artifactOrigin(); o.WindowID != "@12" || o.Agent != "" {
			t.Errorf("origin = %+v", o)
		}
	})
}

func TestPruneArtifactsHookIsNonFatal(t *testing.T) {
	// testenv points the state dir at a temp dir; with nothing stored the
	// hook is a quiet no-op, and it never panics or returns an error.
	pruneArtifacts()
}

func TestArtifactAddNoNotifyPersisted(t *testing.T) {
	h := newArtifactHarness(t)
	var out bytes.Buffer
	if err := h.env.add(&out, ArtifactAddOptions{Path: writeTmp(t, "a.txt", "x"), JSON: true, NoNotify: true}); err != nil {
		t.Fatal(err)
	}
	var v artifactView
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	m, err := h.env.store().Get(v.ID)
	if err != nil || !m.NoNotify {
		t.Errorf("no_notify not stored: %+v %v", m, err)
	}
}

// Regression: a meta.json with no revisions must be an error, not an index
// panic.
func TestArtifactURLNoRevisions(t *testing.T) {
	h := newArtifactHarness(t)
	v := addOne(t, h)
	dir, _ := h.env.store().Dir()
	metaPath := filepath.Join(dir, v.ID, "meta.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	m["revs"] = []any{}
	data, _ = json.Marshal(m)
	if err := os.WriteFile(metaPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	for _, rev := range []int{0, 3} {
		if err := h.env.url(&out, v.ID, rev); err == nil || !strings.Contains(err.Error(), "no revisions") {
			t.Errorf("rev %d: err = %v", rev, err)
		}
	}
}

func TestWriteTableAlignsWideCells(t *testing.T) {
	rows := [][]string{
		{"ID", "PIN", "TITLE", "AGENT"},
		{"a1", "📌", "pinned", "x"},
		{"a2", "", "plain", "y"},
		{"a3", "", "日本語", "z"},
	}
	var out bytes.Buffer
	if err := writeTable(&out, rows); err != nil {
		t.Fatal(err)
	}
	// Every row's AGENT cell must start at the same display column.
	var col int
	for i, line := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		prefix := line[:strings.LastIndex(line, " ")+1]
		w := ansi.StringWidth(prefix)
		if i == 0 {
			col = w
		} else if w != col {
			t.Errorf("row %d agent column at %d, want %d: %q", i, w, col, line)
		}
	}
}

func TestTruncWidth(t *testing.T) {
	if got := truncWidth("short", 10); got != "short" {
		t.Errorf("got %q", got)
	}
	if got := truncWidth("日本語日本語", 6); ansi.StringWidth(got) > 6 || !strings.HasSuffix(got, "…") {
		t.Errorf("got %q", got)
	}
}

func TestArtifactCommentsShowAndList(t *testing.T) {
	h := newArtifactHarness(t)
	v := addOne(t, h)
	var out bytes.Buffer
	if err := h.env.comments(&out, v.ID, false); err != nil || strings.TrimSpace(out.String()) != "no comments" {
		t.Fatalf("empty = %q %v", out.String(), err)
	}
	out.Reset()
	if err := h.env.comments(&out, v.ID, true); err != nil || strings.TrimSpace(out.String()) != "[]" {
		t.Fatalf("empty json = %q %v", out.String(), err)
	}
	c, err := h.env.store().AddComment(v.ID, 1, "phone", "make it blue\nand bigger")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.env.store().MarkComment(v.ID, c.ID, artifacts.CommentDelivered); err != nil {
		t.Fatal(err)
	}
	for _, show := range []bool{false, true} {
		out.Reset()
		var err error
		if show {
			err = h.env.show(&out, v.ID, false)
		} else {
			err = h.env.comments(&out, v.ID, false)
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"#1", "phone", "[delivered]", "make it blue", "and bigger"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("show=%v output lacks %q: %q", show, want, out.String())
			}
		}
	}
	out.Reset()
	if err := h.env.comments(&out, v.ID, true); err != nil || !strings.Contains(out.String(), `"status": "delivered"`) {
		t.Errorf("json = %q %v", out.String(), err)
	}
}

// TestArtifactAppLinkStaysLocalWithPublicHost: with a public base (the
// serve.public_hostname certificate held), content links move but the app
// link keeps the paired local host, where the device cookie lives.
func TestArtifactAppLinkStaysLocalWithPublicHost(t *testing.T) {
	h := newArtifactHarness(t)
	h.running = true
	h.env.serveURL = func() (string, bool) { return "https://mac.agentctl.example.com:8443", true }
	h.env.appBase = func() string { return "https://box.local:8443" }
	src := writeTmp(t, "p.html", "<title>Page</title>")
	var out bytes.Buffer
	if err := h.env.add(&out, ArtifactAddOptions{Path: src, JSON: true}); err != nil {
		t.Fatal(err)
	}
	var v artifactView
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		t.Fatalf("bad json %q: %v", out.String(), err)
	}
	if v.URL != "https://mac.agentctl.example.com:8443/a/"+v.ID+"/latest/" {
		t.Errorf("url = %q", v.URL)
	}
	if v.AppURL != "https://box.local:8443/#/artifacts/"+v.ID {
		t.Errorf("app_url = %q", v.AppURL)
	}
}

// stubPDF replaces the renderer; it records the call and writes fake bytes.
func stubPDF(h *artifactHarness, calls *[]int) {
	h.env.pdf = func(_ context.Context, _ *artifacts.Store, id string, rev int, w io.Writer) error {
		*calls = append(*calls, rev)
		_, err := io.WriteString(w, "%PDF-fake "+id)
		return err
	}
}

func TestArtifactPDF(t *testing.T) {
	h := newArtifactHarness(t)
	var revs []int
	stubPDF(h, &revs)
	m, err := h.env.store().Add(artifacts.AddOptions{Source: artifacts.Source{Path: writeTmp(t, "r.md", "# Quarterly Report\n")}, NoNotify: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	dir := t.TempDir()
	t.Chdir(dir)

	t.Run("default name in cwd", func(t *testing.T) {
		var out bytes.Buffer
		if err := h.env.exportPDF(ctx, &out, ArtifactPDFOptions{ID: m.ID}); err != nil {
			t.Fatal(err)
		}
		got, _ := filepath.EvalSymlinks(strings.TrimSpace(out.String()))
		want, _ := filepath.EvalSymlinks(filepath.Join(dir, m.Slug+".pdf"))
		if got != want || m.Slug == "" {
			t.Errorf("printed %q, want %q", got, want)
		}
		b, _ := os.ReadFile(m.Slug + ".pdf")
		if string(b) != "%PDF-fake "+m.ID {
			t.Errorf("file = %q", b)
		}
	})
	t.Run("refuses overwrite, force allows", func(t *testing.T) {
		n := len(revs)
		err := h.env.exportPDF(ctx, io.Discard, ArtifactPDFOptions{ID: m.ID})
		if err == nil || !strings.Contains(err.Error(), "--force") {
			t.Fatalf("err = %v", err)
		}
		if len(revs) != n {
			t.Error("rendered before refusing")
		}
		if err := h.env.exportPDF(ctx, io.Discard, ArtifactPDFOptions{ID: m.ID, Force: true}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("out file and directory", func(t *testing.T) {
		if err := h.env.exportPDF(ctx, io.Discard, ArtifactPDFOptions{ID: m.ID, Out: "x.pdf"}); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir("sub", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := h.env.exportPDF(ctx, io.Discard, ArtifactPDFOptions{ID: m.ID, Out: "sub"}); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{"x.pdf", filepath.Join("sub", m.Slug+".pdf")} {
			if _, err := os.Stat(p); err != nil {
				t.Error(err)
			}
		}
	})
	t.Run("stdout", func(t *testing.T) {
		var out bytes.Buffer
		if err := h.env.exportPDF(ctx, &out, ArtifactPDFOptions{ID: m.ID, Out: "-"}); err != nil {
			t.Fatal(err)
		}
		if out.String() != "%PDF-fake "+m.ID {
			t.Errorf("stdout = %q", out.String())
		}
	})
	t.Run("bad rev and unknown id", func(t *testing.T) {
		err := h.env.exportPDF(ctx, io.Discard, ArtifactPDFOptions{ID: m.ID, Rev: 9, Out: "-"})
		if err == nil || !strings.Contains(err.Error(), "no revision 9") {
			t.Errorf("rev err = %v", err)
		}
		if err := h.env.exportPDF(ctx, io.Discard, ArtifactPDFOptions{ID: "nosuchid", Out: "-"}); !errors.Is(err, artifacts.ErrNotFound) {
			t.Errorf("id err = %v", err)
		}
	})
	t.Run("render failure leaves no file", func(t *testing.T) {
		h.env.pdf = func(context.Context, *artifacts.Store, string, int, io.Writer) error { return errors.New("no browser") }
		err := h.env.exportPDF(ctx, io.Discard, ArtifactPDFOptions{ID: m.ID, Out: "fail.pdf"})
		if err == nil || !strings.Contains(err.Error(), "no browser") {
			t.Fatalf("err = %v", err)
		}
		if _, err := os.Stat("fail.pdf"); !os.IsNotExist(err) {
			t.Error("file left behind")
		}
	})
}

func TestPDFPathFallsBackToShortID(t *testing.T) {
	t.Chdir(t.TempDir())
	got, err := pdfPath("", "", "abcdefghij")
	if err != nil || filepath.Base(got) != "abcdef.pdf" {
		t.Errorf("got %q, %v", got, err)
	}
}
