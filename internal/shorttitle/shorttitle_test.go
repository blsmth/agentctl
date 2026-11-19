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
	a := Input{Task: "x", LastText: "y"}
	b := Input{Task: "x y"}
	if a.Hash() == b.Hash() {
		t.Fatal("field boundaries are not part of the input hash")
	}
}

func TestDue(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	first := Input{Task: "sort the photos"}
	refine := Input{Task: "sort the photos", LastText: "Done with 2019"}
	titled := windowstate.ShortTitle{Title: "Sorting Photos", At: now.Add(-time.Minute), InputHash: first.Hash()}
	tests := []struct {
		name string
		st   windowstate.ShortTitle
		in   Input
		now  time.Time
		want bool
	}{
		{"no title yet", windowstate.ShortTitle{}, first, now, true},
		{"nothing to title from", windowstate.ShortTitle{}, Input{}, now, false},
		{"response alone is not enough", windowstate.ShortTitle{}, Input{LastText: "hi"}, now, false},
		{"ticket window", windowstate.ShortTitle{}, Input{Ticket: "Mobile app"}, now, true},
		{"first title failed, later Stop makes the one title", windowstate.ShortTitle{Failures: 1, RetryAfter: now.Add(-time.Second)}, refine, now, true},
		{"first title, no response yet", titled, first, now, false},
		{"first title, next prompt", titled, Input{Task: first.Task, FirstPrompt: "more"}, now, false},
		{"refinement at the first Stop", titled, refine, now, true},
		{"refinement long after the first title", titled, refine, now.Add(48 * time.Hour), true},
		{"same input as last time", windowstate.ShortTitle{Title: "T", InputHash: refine.Hash()}, refine, now, false},
		{"frozen", windowstate.ShortTitle{Title: "T", Final: true, InputHash: "old"}, Input{Task: "x", LastText: "new"}, now.Add(24 * time.Hour), false},
		{"refinement backoff pending", windowstate.ShortTitle{Title: "T", RetryAfter: now.Add(time.Minute), Failures: 1}, refine, now, false},
		{"refinement backoff over", windowstate.ShortTitle{Title: "T", RetryAfter: now.Add(-time.Second), Failures: 1}, refine, now, true},
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
	first := Input{Task: "sort"}
	refine := Input{Task: "sort", LastText: "Sorted."}
	var st windowstate.ShortTitle
	Apply(&st, first, "Sorting Photos", "chat", nil, now)
	want := windowstate.ShortTitle{Title: "Sorting Photos", Kind: "chat", At: now, InputHash: first.Hash()}
	if st != want {
		t.Fatalf("first = %+v, want %+v", st, want)
	}
	// Failures keep the old title, stay unfrozen, and back off further each time.
	for i, d := range []time.Duration{10 * time.Minute, 30 * time.Minute, 2 * time.Hour, 12 * time.Hour, 12 * time.Hour} {
		Apply(&st, refine, "", "go", errors.New("down"), now)
		if st.Title != "Sorting Photos" || st.Kind != "chat" || st.Final || st.Failures != i+1 || !st.RetryAfter.Equal(now.Add(d)) {
			t.Fatalf("failure %d = %+v", i+1, st)
		}
	}
	// A refinement clears the backoff and freezes.
	Apply(&st, refine, "Sorting Photos Again", "go", nil, now)
	want = windowstate.ShortTitle{Title: "Sorting Photos Again", Kind: "go", At: now, InputHash: refine.Hash(), Final: true}
	if st != want {
		t.Fatalf("refinement = %+v, want %+v", st, want)
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
	in := Input{Ticket: "Mobile app", Task: "make titles short", LastText: strings.Repeat("ζ", maxLastTextRunes) + "THE END"}
	fc := &fakeCompleter{out: "\"Shortening Row Titles.\"\n"}
	got, _, err := Generate(context.Background(), fc, in)
	if err != nil || got != "Shortening Row Titles" {
		t.Fatalf("Generate = %q, %v", got, err)
	}
	for _, s := range []string{"Ticket:\nMobile app", "Task:\nmake titles short", "THE END", "Organizing your Photos", "2 to 5 words"} {
		if !strings.Contains(fc.prompt, s) {
			t.Errorf("prompt missing %q", s)
		}
	}
	if n := strings.Count(fc.prompt, "ζ"); n != maxLastTextRunes-len("THE END") {
		t.Errorf("last message carries %d runes of filler; the tail must be kept", n)
	}

	if _, _, err := Generate(context.Background(), &fakeCompleter{out: "..."}, in); !errors.Is(err, ErrEmptyTitle) {
		t.Errorf("blank reply err = %v", err)
	}
	boom := errors.New("boom")
	if got, _, err := Generate(context.Background(), &fakeCompleter{out: "Ignored", err: boom}, in); !errors.Is(err, boom) || got != "" {
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

func TestGenerateKind(t *testing.T) {
	tests := []struct {
		name, reply, mode string
		wantKind, want    string
		wantErr           error
	}{
		{"good", "go | Fixing X Now", "", "go", "Fixing X Now", nil},
		{"whitespace and case", "  Review|  Checking This PR \n", "", "review", "Checking This PR", nil},
		{"markdown", "`go` | **Fixing X**", "", "go", "Fixing X", nil},
		{"unknown kind", "rust | Fixing X", "", "", "Fixing X", nil},
		{"label before kind", "Title: go | Fixing X", "", "go", "Fixing X", nil},
		{"markdown label before kind", "**Title:** go | Fixing X", "", "go", "Fixing X", nil},
		{"label before invented kind", "Title: refactor | Fixing X", "", "", "Fixing X", nil},
		{"invented kinds", "refactor | Fixing X", "", "", "Fixing X", nil},
		{"multi-word head kept", "Fix A | B", "", "", "Fix A B", nil},
		{"unknown kind empty rest", "rust | ", "", "", "rust", nil},
		{"no pipe", "Fixing X", "", "", "Fixing X", nil},
		{"extra pipes", "web | Fixing | X | Y", "", "web", "Fixing X Y", nil},
		{"empty title half", "go |  ", "", "", "", ErrEmptyTitle},
		{"review-pr override", "go | Reading A Diff", ReviewPRMode, "review", "Reading A Diff", nil},
		{"override without kind", "Reading A Diff", ReviewPRMode, "review", "Reading A Diff", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, kind, err := Generate(context.Background(), &fakeCompleter{out: tt.reply}, Input{Task: "t", Mode: tt.mode})
			if !errors.Is(err, tt.wantErr) || got != tt.want || kind != tt.wantKind {
				t.Fatalf("Generate = %q, %q, %v; want %q, %q, %v", got, kind, err, tt.want, tt.wantKind, tt.wantErr)
			}
		})
	}
}

func TestPromptKindsAndHints(t *testing.T) {
	p := Prompt(Input{Task: "t", Agent: "go-engineer", Mode: "review-pr"})
	for _, k := range Kinds() {
		if !strings.Contains(p, "- "+k+": ") {
			t.Errorf("prompt missing kind %q", k)
		}
	}
	for _, s := range []string{"<kind> | <title>", "Agent:\ngo-engineer", "Dispatch mode:\nreview-pr", "moving, renaming or upgrading"} {
		if !strings.Contains(p, s) {
			t.Errorf("prompt missing %q", s)
		}
	}
	if strings.Contains(Prompt(Input{Task: "t"}), "Agent:\n") {
		t.Error("empty agent should add no section")
	}
}

func TestHashIgnoresAgentAndMode(t *testing.T) {
	a := Input{Task: "t", Ticket: "k", LastText: "l"}
	b := a
	b.Agent, b.Mode = "go-engineer", "review-pr"
	if a.Hash() != b.Hash() {
		t.Fatal("agent and mode must not change the hash")
	}
	// Pinned: hashes recorded before kinds existed must not change.
	if got := (Input{Task: "a"}).Hash(); got != Hash("a\x00\x00") {
		t.Fatalf("hash layout changed: %s", got)
	}
}

func TestSanitizeDropsKindPrefix(t *testing.T) {
	if got := Sanitize("go | Fixing X"); got != "Fixing X" {
		t.Errorf("Sanitize = %q", got)
	}
}

func TestTakeBackfillBudgetLeavesReserve(t *testing.T) {
	useStateDir(t)
	now := time.Now()
	n := 0
	for TakeBackfillBudget(now) {
		n++
		if n > HourlyBudget {
			t.Fatal("backfill budget never ran out")
		}
	}
	if want := HourlyBudget - BackfillReserve; n != want {
		t.Fatalf("backfill took %d, want %d", n, want)
	}
	for i := 0; i < BackfillReserve; i++ {
		if !TakeBudget(now) {
			t.Fatalf("hook request %d refused: reserve not left", i+1)
		}
	}
	if TakeBudget(now) {
		t.Fatal("budget exceeded")
	}
}

func TestRecapInput(t *testing.T) {
	now := time.Now()
	in := Input{Recap: "Rebuilt the hub sort. Next: tests."}
	if in.Empty() {
		t.Fatal("recap-only input is empty")
	}
	if !in.Refinement() {
		t.Fatal("recap input is not a refinement")
	}
	if !Due(windowstate.ShortTitle{}, in, now) {
		t.Fatal("recap input not due on an untitled window")
	}
	if Hash := (Input{Task: "x"}).Hash(); Hash == (Input{Task: "x", Recap: "y"}).Hash() {
		t.Fatal("recap not part of the hash")
	}
	if p := Prompt(in); !strings.Contains(p, "Session so far:\nRebuilt the hub sort.") {
		t.Fatalf("prompt lacks the recap:\n%s", p)
	}
	var st windowstate.ShortTitle
	Apply(&st, in, "Rebuilding Hub Sort", "go", nil, now)
	if !st.Final {
		t.Fatal("title from a recap not final")
	}
}
