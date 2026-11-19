package shorttitle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/testenv"
	"github.com/blsmth/agentctl/internal/windowstate"
)

func TestMain(m *testing.M) { testenv.Main(m) }

func TestSanitize(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Organizing your Photos", "Organizing your Photos"},
		{"  \"Fixing Hub Sort Order.\"  ", "Fixing Hub Sort Order"},
		{"“Fixing Hub Sort Order”", "Fixing Hub Sort Order"},
		{"**Title:** Wiring Short Titles!", "Wiring Short Titles"},
		{"Title: `Adding Nous Completer`", "Adding Nous Completer"},
		{"# Refactoring   the\tCache\n\nBecause the user asked", "Refactoring the Cache"},
		{"- Reviewing Brendan's PR", "Reviewing Brendan's PR"},
		{"'Quoted Title'", "Quoted Title"},
		{"One Two Three Four Five Six Seven Eight", "One Two Three Four Five Six"},
		{"Internationalizing Supercalifragilistic Configuration", "Internationalizing Supercalifragilistic"},
		{strings.Repeat("a", 50), strings.Repeat("a", 40)},
		{"Fixing Things...", "Fixing Things"},
		{"", ""},
		{"   \n  ", ""},
		{"\"\"", ""},
		{"...", ""},
		{"12345", ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := Sanitize(tt.in)
			if got != tt.want {
				t.Fatalf("Sanitize(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if len([]rune(got)) > MaxChars || len(strings.Fields(got)) > MaxWords {
				t.Fatalf("Sanitize(%q) = %q exceeds caps", tt.in, got)
			}
		})
	}
}

func TestHash(t *testing.T) {
	if Hash("  a\n b ") != Hash("a b") || Hash("a b") == Hash("a c") {
		t.Fatal("Hash does not normalize whitespace or collides")
	}
	if Hash("   ") != "" {
		t.Fatal("blank text has a hash")
	}
	a := Input{Task: "x", Summary: "y"}
	b := Input{Task: "x y"}
	if a.Hash() == b.Hash() {
		t.Fatal("field boundaries are not part of the input hash")
	}
}

func TestDue(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	in := Input{Task: "sort the photos", Summary: "Sorted 2019.", LastText: "Done with 2019"}
	titled := windowstate.ShortTitle{Title: "Sorting Photos", At: now.Add(-time.Minute), InputHash: "old", SummaryHash: Hash(in.Summary)}
	tests := []struct {
		name string
		st   windowstate.ShortTitle
		in   Input
		now  time.Time
		want bool
	}{
		{"no title yet", windowstate.ShortTitle{}, in, now, true},
		{"nothing to title from", windowstate.ShortTitle{}, Input{Ticket: "only context"}, now, false},
		{"ticket window without a summary", windowstate.ShortTitle{}, Input{Ticket: "Mobile app", LastText: "Edited a.go"}, now, false},
		{"ticket window with a summary", windowstate.ShortTitle{}, Input{Ticket: "Mobile app", Summary: "Built the app.", LastText: "Edited a.go"}, now, true},
		{"same input as last time", windowstate.ShortTitle{Title: "T", At: now.Add(-time.Hour), InputHash: in.Hash()}, in, now, false},
		{"throttled within the interval", titled, in, now, false},
		{"interval passed", titled, in, now.Add(RetitleInterval), true},
		{"new away_summary beats the throttle", titled, Input{Task: in.Task, Summary: "Now on 2020.", LastText: in.LastText}, now, true},
		{"summary gone is not new", titled, Input{Task: in.Task, LastText: "other"}, now, false},
		{"backoff pending", windowstate.ShortTitle{RetryAfter: now.Add(time.Minute), Failures: 1}, in, now, false},
		{"backoff over", windowstate.ShortTitle{RetryAfter: now.Add(-time.Second), Failures: 1}, in, now, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Due(tt.st, tt.in, tt.now); got != tt.want {
				t.Fatalf("Due = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestApply(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	in := Input{Task: "sort", Summary: "Sorted."}
	var st windowstate.ShortTitle
	Apply(&st, in, "Sorting Photos", nil, now)
	want := windowstate.ShortTitle{Title: "Sorting Photos", At: now, InputHash: in.Hash(), SummaryHash: Hash(in.Summary)}
	if st != want {
		t.Fatalf("success = %+v, want %+v", st, want)
	}
	// Failures keep the old title and back off further each time.
	for i, d := range []time.Duration{10 * time.Minute, 30 * time.Minute, 2 * time.Hour, 12 * time.Hour, 12 * time.Hour} {
		Apply(&st, Input{Task: "other"}, "", errors.New("down"), now)
		if st.Title != "Sorting Photos" || st.Failures != i+1 || !st.RetryAfter.Equal(now.Add(d)) {
			t.Fatalf("failure %d = %+v", i+1, st)
		}
	}
	// A success clears the backoff.
	Apply(&st, in, "Sorting Photos Again", nil, now)
	if st.Failures != 0 || !st.RetryAfter.IsZero() {
		t.Fatalf("success after failures = %+v", st)
	}
}

type fakeCompleter struct {
	out    string
	err    error
	prompt string
}

func (f *fakeCompleter) Complete(_ context.Context, p string) (string, error) {
	f.prompt = p
	return f.out, f.err
}

func TestGenerate(t *testing.T) {
	in := Input{Ticket: "Mobile app", Task: "make titles short", Summary: "Added the Nous client.", LastText: strings.Repeat("ζ", maxLastTextRunes) + "THE END"}
	fc := &fakeCompleter{out: "\"Shortening Row Titles.\"\n"}
	got, err := Generate(context.Background(), fc, in)
	if err != nil || got != "Shortening Row Titles" {
		t.Fatalf("Generate = %q, %v", got, err)
	}
	for _, s := range []string{"Ticket:\nMobile app", "Task:\nmake titles short", "Latest recap:\nAdded the Nous client.", "THE END", "Organizing your Photos", "2 to 5 words"} {
		if !strings.Contains(fc.prompt, s) {
			t.Errorf("prompt missing %q", s)
		}
	}
	if n := strings.Count(fc.prompt, "ζ"); n != maxLastTextRunes-len("THE END") {
		t.Errorf("last message carries %d runes of filler; the tail must be kept", n)
	}

	if _, err := Generate(context.Background(), &fakeCompleter{out: "..."}, in); !errors.Is(err, ErrEmptyTitle) {
		t.Errorf("blank reply err = %v", err)
	}
	boom := errors.New("boom")
	if got, err := Generate(context.Background(), &fakeCompleter{out: "Ignored", err: boom}, in); !errors.Is(err, boom) || got != "" {
		t.Errorf("error reply = %q, %v", got, err)
	}
}

func useStateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := stateDir
	stateDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { stateDir = old })
	return dir
}

func TestTakeBudget(t *testing.T) {
	dir := useStateDir(t)
	now := time.Now()
	for i := 0; i < HourlyBudget; i++ {
		if !TakeBudget(now) {
			t.Fatalf("call %d refused", i+1)
		}
	}
	if TakeBudget(now) {
		t.Fatal("budget exceeded")
	}
	if !TakeBudget(now.Add(time.Hour + time.Second)) {
		t.Fatal("budget did not free up after an hour")
	}
	p := filepath.Join(dir, "titles", "budget.json")
	if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("budget file mode = %v, %v", fi.Mode().Perm(), err)
	}
	if fi, err := os.Stat(filepath.Dir(p)); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("budget dir mode = %v, %v", fi.Mode().Perm(), err)
	}
}

func TestTakeBudgetConcurrent(t *testing.T) {
	useStateDir(t)
	now := time.Now()
	var granted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < HourlyBudget*2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if TakeBudget(now) {
				granted.Add(1)
			}
		}()
	}
	wg.Wait()
	if granted.Load() != HourlyBudget {
		t.Fatalf("granted %d, want %d", granted.Load(), HourlyBudget)
	}
}

func TestTakeBudgetFailsClosed(t *testing.T) {
	old := stateDir
	stateDir = func() (string, error) { return "", errors.New("no home") }
	t.Cleanup(func() { stateDir = old })
	if TakeBudget(time.Now()) {
		t.Fatal("budget granted without a state dir")
	}
}

func TestFirstPromptIsInput(t *testing.T) {
	in := Input{FirstPrompt: "sort my photos"}
	if in.Empty() {
		t.Fatal("first prompt alone should not be empty")
	}
	if !Due(windowstate.ShortTitle{}, in, time.Now()) {
		t.Fatal("first prompt should be due with no title")
	}
	if !strings.Contains(Prompt(in), "First request:\nsort my photos") {
		t.Fatalf("prompt = %q", Prompt(in))
	}
	if (Input{Task: "a"}).Hash() == (Input{Task: "a", FirstPrompt: "b"}).Hash() {
		t.Fatal("first prompt must change the hash")
	}
}
