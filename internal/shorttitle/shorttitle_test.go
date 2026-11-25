package shorttitle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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
	Apply(&st, first, "Sorting Photos", "plan", nil, now)
	want := windowstate.ShortTitle{Title: "Sorting Photos", Kind: "plan", At: now, InputHash: first.Hash()}
	if st != want {
		t.Fatalf("first = %+v, want %+v", st, want)
	}
	// Failures keep the old title, stay unfrozen, and back off further each time.
	for i, d := range []time.Duration{10 * time.Minute, 30 * time.Minute, 2 * time.Hour, 12 * time.Hour, 12 * time.Hour} {
		Apply(&st, refine, "", "fix", errors.New("down"), now)
		if st.Title != "Sorting Photos" || st.Kind != "plan" || st.Final || st.Failures != i+1 || !st.RetryAfter.Equal(now.Add(d)) {
			t.Fatalf("failure %d = %+v", i+1, st)
		}
	}
	// A refinement clears the backoff and freezes.
	Apply(&st, refine, "Sorting Photos Again", "fix", nil, now)
	want = windowstate.ShortTitle{Title: "Sorting Photos Again", Kind: "fix", At: now, InputHash: refine.Hash(), Final: true}
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

func useCap(t *testing.T, n int) {
	t.Helper()
	old := hourlyCapFn
	hourlyCapFn = func() int { return n }
	t.Cleanup(func() { hourlyCapFn = old })
}

func TestTakeBudget(t *testing.T) {
	dir := useStateDir(t)
	now := time.Now()
	for i := 0; i < HourlyCap(); i++ {
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
	for i := 0; i < HourlyCap()*2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if TakeBudget(now) {
				granted.Add(1)
			}
		}()
	}
	wg.Wait()
	if int(granted.Load()) != HourlyCap() {
		t.Fatalf("granted %d, want %d", granted.Load(), HourlyCap())
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
		{"good", "fix | Fixing X Now", "", "fix", "Fixing X Now", nil},
		{"whitespace and case", "  Review|  Checking This PR \n", "", "review", "Checking This PR", nil},
		{"markdown", "`fix` | **Fixing X**", "", "fix", "Fixing X", nil},
		{"legacy language word dropped", "go | Fixing X", "", "", "Fixing X", nil},
		{"new kind", "refactor | Fixing X", "", "refactor", "Fixing X", nil},
		{"unknown kind", "rust | Fixing X", "", "", "Fixing X", nil},
		{"label before kind", "Title: fix | Fixing X", "", "fix", "Fixing X", nil},
		{"markdown label before kind", "**Title:** fix | Fixing X", "", "fix", "Fixing X", nil},
		{"label before invented kind", "Title: rust | Fixing X", "", "", "Fixing X", nil},
		{"multi-word head kept", "Fix A | B", "", "", "Fix A B", nil},
		{"unknown kind empty rest", "rust | ", "", "", "rust", nil},
		{"no pipe", "Fixing X", "", "", "Fixing X", nil},
		{"extra pipes", "ops | Fixing | X | Y", "", "ops", "Fixing X Y", nil},
		{"empty title half", "fix |  ", "", "", "", ErrEmptyTitle},
		{"review-pr override", "fix | Reading A Diff", ReviewPRMode, "review", "Reading A Diff", nil},
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
	for _, s := range []string{"<kind> | <title>", "Agent:\ngo-engineer", "Dispatch mode:\nreview-pr", "moving or upgrading data"} {
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
	if got := Sanitize("fix | Fixing X"); got != "Fixing X" {
		t.Errorf("Sanitize = %q", got)
	}
}

func TestTakeBackfillBudgetLeavesReserve(t *testing.T) {
	useStateDir(t)
	now := time.Now()
	n := 0
	for TakeBackfillBudget(now) {
		n++
		if n > HourlyCap() {
			t.Fatal("backfill budget never ran out")
		}
	}
	if want := HourlyCap() - BackfillReserve; n != want {
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
	Apply(&st, in, "Rebuilding Hub Sort", "fix", nil, now)
	if !st.Final {
		t.Fatal("title from a recap not final")
	}
}

func TestPromptParentSection(t *testing.T) {
	base := Input{Task: "Fix it"}
	if p := Prompt(base); strings.Contains(p, "sub-agent") || strings.Contains(p, "orchestrator") {
		t.Errorf("no parent: prompt has a parent section:\n%s", p)
	}
	with := base
	with.ParentTitle = "Fixing False Dialog Detection"
	p := Prompt(with)
	for _, want := range []string{"sub-agent", `"Fixing False Dialog Detection"`, "Line-Anchoring Dialog Markers", "Checking Dialog Marker Anchoring", "`<kind> | <title>`"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func TestPromptChildTaskCap(t *testing.T) {
	long := strings.Repeat("a", 250) + " " + strings.Repeat("b", 400) + " END"
	if p := Prompt(Input{Task: long}); strings.Contains(p, "END") || strings.Contains(p, strings.Repeat("b", 100)) {
		t.Error("task cap of a plain window should stay 300 runes")
	}
	if p := Prompt(Input{Task: long, ParentTitle: "X"}); !strings.Contains(p, "END") {
		t.Error("sub-agent task should keep its tail within the larger cap")
	}
}

func TestHashIgnoresParentTitle(t *testing.T) {
	a := Input{Task: "t", Ticket: "k", LastText: "l"}
	b := a
	b.ParentTitle = "Parent"
	if a.Hash() != b.Hash() {
		t.Error("ParentTitle must not change Hash")
	}
}

func TestRole(t *testing.T) {
	tests := []struct{ mode, kind, want string }{
		{"review-pr", "fix", "review"},
		{"review-pr", "", "review"},
		{"improve", "build", "improve"},
		{"improve", "", "improve"},
		// Every current kind is its own role.
		{"adhoc", "build", "build"},
		{"adhoc", "fix", "fix"},
		{"adhoc", "debug", "debug"},
		{"adhoc", "refactor", "refactor"},
		{"adhoc", "tests", "tests"},
		{"adhoc", "docs", "docs"},
		{"adhoc", "review", "review"},
		{"adhoc", "ship", "ship"},
		{"adhoc", "ops", "ops"},
		{"adhoc", "migrate", "migrate"},
		{"adhoc", "research", "research"},
		{"adhoc", "plan", "plan"},
		// Legacy kinds stored before the intent vocabulary.
		{"adhoc", "go", "build"},
		{"adhoc", "python", "build"},
		{"adhoc", "swift", "build"},
		{"adhoc", "web", "build"},
		{"adhoc", "infra", "ops"},
		{"adhoc", "migration", "migrate"},
		{"adhoc", "chat", "plan"},
		{"adhoc", "", "build"},
		{"adhoc", "bogus", "build"},
	}
	for _, tt := range tests {
		t.Run(tt.mode+"/"+tt.kind, func(t *testing.T) {
			if got := Role(tt.mode, tt.kind); got != tt.want {
				t.Errorf("Role(%q,%q) = %q, want %q", tt.mode, tt.kind, got, tt.want)
			}
		})
	}
}

func TestKindsVocabulary(t *testing.T) {
	want := []string{"build", "fix", "debug", "refactor", "tests", "docs", "review", "ship", "ops", "migrate", "research", "plan"}
	if got := Kinds(); !reflect.DeepEqual(got, want) {
		t.Errorf("Kinds = %v, want %v", got, want)
	}
	for _, old := range []string{"go", "python", "swift", "web", "infra", "migration", "chat"} {
		if IsKind(old) || normalizeKind(old) != "" {
			t.Errorf("legacy kind %q must not be accepted", old)
		}
	}
}

func TestTakeBudgetReserve(t *testing.T) {
	useStateDir(t)
	useCap(t, 100)
	now := time.Now()
	n := 0
	for TakeRestoredBudget(now) {
		n++
		if n > 100 {
			t.Fatal("restored budget never ran out")
		}
	}
	if want := 100 - NewSessionReserve; n != want {
		t.Fatalf("restored took %d, want %d", n, want)
	}
	if TakeRestoredBudget(now) {
		t.Fatal("low-priority take succeeded with only the reserve left")
	}
	for i := 0; i < NewSessionReserve; i++ {
		if !TakeBudget(now) {
			t.Fatalf("high-priority take %d refused", i+1)
		}
	}
	if TakeBudget(now) {
		t.Fatal("cap exceeded")
	}
}

func TestTakeBudgetReserveClampedForSmallCap(t *testing.T) {
	useStateDir(t)
	useCap(t, 30)
	now := time.Now()
	if !TakeRestoredBudget(now) || !TakeBackfillBudget(now) {
		t.Fatal("low-priority takes must still work at a small cap")
	}
	n := 2
	for TakeRestoredBudget(now) {
		n++
	}
	if n != 15 {
		t.Fatalf("low-priority takes = %d, want 15 (half the cap)", n)
	}
	for i := n; i < 30; i++ {
		if !TakeBudget(now) {
			t.Fatalf("high-priority take refused at %d", i)
		}
	}
	if TakeBudget(now) {
		t.Fatal("cap exceeded")
	}
}
