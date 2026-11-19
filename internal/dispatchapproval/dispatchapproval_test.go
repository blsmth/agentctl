package dispatchapproval

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/testenv"
)

func TestMain(m *testing.M) {
	testenv.Main(m)
}

// useDir points the marker dir at a temp dir and pins the clock to base.
func useDir(t *testing.T) (dir string, clock *time.Time) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), DirName)
	prevDir, prevNow := dirFn, now
	base := time.Now()
	clock = &base
	dirFn = func() (string, error) { return dir, nil }
	now = func() time.Time { return *clock }
	t.Cleanup(func() { dirFn, now = prevDir, prevNow })
	return dir, clock
}

func TestRecordConsume(t *testing.T) {
	args := []string{"--review-pr", "5"}
	tests := []struct {
		name    string
		window  string
		args    []string
		advance time.Duration
		wantErr bool
	}{
		{name: "same window and args", window: "@1", args: args},
		{name: "other window", window: "@2", args: args, wantErr: true},
		{name: "other args", window: "@1", args: []string{"--review-pr", "6"}, wantErr: true},
		{name: "within TTL", window: "@1", args: args, advance: TTL - time.Second},
		{name: "expired", window: "@1", args: args, advance: TTL + time.Second, wantErr: true},
		{name: "no window", window: "", args: args, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, clock := useDir(t)
			if err := Record("@1", args); err != nil {
				t.Fatal(err)
			}
			*clock = clock.Add(tt.advance)
			err := Consume(tt.window, tt.args)
			if tt.wantErr {
				if !errors.Is(err, ErrNotApproved) {
					t.Fatalf("err = %v, want ErrNotApproved", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestConsumeIsSingleUse(t *testing.T) {
	useDir(t)
	args := []string{"--review-pr", "5"}
	if err := Record("@1", args); err != nil {
		t.Fatal(err)
	}
	if err := Consume("@1", args); err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if err := Consume("@1", args); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("replayed consume: err = %v, want ErrNotApproved", err)
	}
}

func TestMarkerPermissions(t *testing.T) {
	dir, _ := useDir(t)
	if err := Record("@1", []string{"--review-pr", "5"}); err != nil {
		t.Fatal(err)
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v, want 0700", di.Mode().Perm())
	}
	fi, err := os.Stat(filepath.Join(dir, Key("@1", []string{"--review-pr", "5"})))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("marker mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestConsumeRejectsForgedMarkers(t *testing.T) {
	args := []string{"--review-pr", "5"}
	tests := []struct {
		name  string
		write func(t *testing.T, path string)
	}{
		{"future timestamp", func(t *testing.T, path string) {
			data := `{"window":"@1","created":"` + time.Now().Add(time.Hour).Format(time.RFC3339Nano) + `"}`
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"window mismatch inside", func(t *testing.T, path string) {
			data := `{"window":"@9","created":"` + time.Now().Format(time.RFC3339Nano) + `"}`
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"garbage", func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlink", func(t *testing.T, path string) {
			target := filepath.Join(t.TempDir(), "m")
			data := `{"window":"@1","created":"` + time.Now().Format(time.RFC3339Nano) + `"}`
			if err := os.WriteFile(target, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, _ := useDir(t)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			tt.write(t, filepath.Join(dir, Key("@1", args)))
			if err := Consume("@1", args); !errors.Is(err, ErrNotApproved) {
				t.Fatalf("err = %v, want ErrNotApproved", err)
			}
		})
	}
}

func TestRecordPrunesExpired(t *testing.T) {
	dir, clock := useDir(t)
	if err := Record("@1", []string{"a"}); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, Key("@1", []string{"a"}))
	past := clock.Add(-TTL - time.Minute)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	if err := Record("@1", []string{"b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("expired marker not pruned: %v", err)
	}
}

func TestKeyDistinguishesArgBoundaries(t *testing.T) {
	if Key("@1", []string{"ab", "c"}) == Key("@1", []string{"a", "bc"}) {
		t.Error("keys collide across argument boundaries")
	}
}
