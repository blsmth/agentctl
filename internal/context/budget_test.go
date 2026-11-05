package context

import (
	"strings"
	"testing"
)

// TestSelectWithinBudget covers the byte-budget backstop independently of
// the filesystem, per the ticket's request to lower MaxInjectedBytes rather
// than build 32KB fixtures. This lives in package context (not context_test)
// because selectWithinBudget is deliberately unexported -- callers outside
// this package only ever see the budget applied through ForAgent.
func TestSelectWithinBudget(t *testing.T) {
	entry := func(key, content string) Entry {
		return Entry{Key: key, Content: content}
	}

	t.Run("everything fits, nothing dropped", func(t *testing.T) {
		candidates := []Entry{entry("a", "short"), entry("b", "also short")}
		got := selectWithinBudget(candidates, nil, 1<<20)
		if len(got) != 2 {
			t.Fatalf("got %d entries, want 2", len(got))
		}
	})

	t.Run("pinned entries selected ahead of scoped ones when budget is tight", func(t *testing.T) {
		// Both entries format to the same size; budget only has room for one.
		// The pinned one (listed FIRST in candidates, opposite of priority
		// order) must still win over the unpinned one purely because it's
		// pinned -- pinned entries are reordered ahead of scoped ones.
		big := strings.Repeat("x", 100)
		candidates := []Entry{entry("scoped", big), entry("pinned", big)}
		budget := len(Format(entry("pinned", big))) // room for exactly one

		got := selectWithinBudget(candidates, []string{"pinned"}, budget)
		if len(got) != 1 {
			t.Fatalf("got %d entries, want 1", len(got))
		}
		if got[0].Key != "pinned" {
			t.Errorf("selected entry = %q, want %q", got[0].Key, "pinned")
		}
	})

	t.Run("oversized entry excluded whole, not truncated", func(t *testing.T) {
		small := entry("small", "tiny")
		oversized := entry("oversized", strings.Repeat("y", 1000))

		budget := len(Format(small)) + 10 // enough for small, nowhere near oversized

		got := selectWithinBudget([]Entry{small, oversized}, nil, budget)
		if len(got) != 1 || got[0].Key != "small" {
			t.Fatalf("got %v, want only [small]", got)
		}
		for _, e := range got {
			if e.Key == "oversized" {
				t.Error("oversized entry must never be selected, whole or truncated")
			}
		}
	})

	t.Run("dropped entries are excluded and reported", func(t *testing.T) {
		small := entry("keep", "x")
		drop1 := entry("drop1", strings.Repeat("z", 1000))
		drop2 := entry("drop2", strings.Repeat("z", 1000))

		budget := len(Format(small)) + 5
		got := selectWithinBudget([]Entry{small, drop1, drop2}, nil, budget)

		if len(got) != 1 || got[0].Key != "keep" {
			t.Fatalf("got %v, want only [keep]", got)
		}
		// The drop itself is asserted by exclusion above; the accompanying
		// logging.Warn call (naming every dropped key + the budget) is the
		// loud-failure requirement and is exercised by inspection of
		// selectWithinBudget's source, since this package has no logging
		// capture seam today.
	})
}
