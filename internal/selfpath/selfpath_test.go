package selfpath

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolve(t *testing.T) {
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(real, "agentctl-real")
	if err := os.WriteFile(target, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(real, "agentctl")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		lookup  func() (string, error)
		want    string
		wantErr bool
	}{
		{"symlink is resolved", func() (string, error) { return link, nil }, target, false},
		{"plain path unchanged", func() (string, error) { return target, nil }, target, false},
		{"missing target kept as-is", func() (string, error) { return "/nonexistent/agentctl", nil }, "/nonexistent/agentctl", false},
		{"lookup error", func() (string, error) { return "", errors.New("boom") }, "", true},
		{"empty path", func() (string, error) { return "", nil }, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Resolve(tt.lookup)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPathIsAbsolute(t *testing.T) {
	if p := Path(); !filepath.IsAbs(p) {
		t.Errorf("Path() = %q, want absolute", p)
	}
}

func fakeLookPath(p string, err error) func(string) (string, error) {
	return func(string) (string, error) { return p, err }
}

func TestPersisted(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	// "permanent" dir: pretend tmpDir is somewhere else.
	tmpDir := filepath.Join(root, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mk := func(dir, name string) string {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	exe := mk(filepath.Join(root, "bin"), "agentctl")
	other := mk(filepath.Join(root, "other"), "agentctl")
	cellar := mk(filepath.Join(root, "Cellar", "1.0"), "agentctl")
	brewLink := filepath.Join(root, "brew-agentctl")
	if err := os.Symlink(cellar, brewLink); err != nil {
		t.Fatal(err)
	}
	spaced := mk(filepath.Join(root, "my bin"), "agentctl")
	tmpExe := mk(tmpDir, "agentctl")
	buildExe := mk(filepath.Join(root, "go-build123", "b001", "exe"), "agentctl")

	lookup := func(p string) func() (string, error) { return func() (string, error) { return p, nil } }

	tests := []struct {
		name    string
		exe     string
		onPath  func(string) (string, error)
		want    string
		wantErr bool
	}{
		{"same file on PATH -> bare", exe, fakeLookPath(exe, nil), "agentctl", false},
		{"PATH symlink to same file -> bare (not Cellar path)", brewLink, fakeLookPath(cellar, nil), "agentctl", false},
		{"different file on PATH -> absolute", exe, fakeLookPath(other, nil), exe, false},
		{"not on PATH -> absolute", exe, fakeLookPath("", errors.New("not found")), exe, false},
		{"space in path is quoted", spaced, fakeLookPath("", errors.New("nf")), "'" + spaced + "'", false},
		{"temp dir refused", tmpExe, fakeLookPath(tmpExe, nil), "", true},
		{"go-build refused", buildExe, fakeLookPath("", errors.New("nf")), "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Persisted(lookup(tt.exe), tt.onPath, tmpDir)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && !errors.Is(err, ErrEphemeral) {
				t.Errorf("err = %v, want ErrEphemeral", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestQuote(t *testing.T) {
	tests := map[string]string{
		"/usr/bin/agentctl": "/usr/bin/agentctl",
		"/a b/agentctl":     "'/a b/agentctl'",
		"/it's/x":           `'/it'\''s/x'`,
		"":                  "''",
	}
	for in, want := range tests {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %q, want %q", in, got, want)
		}
	}
}
