// Package shorttitle turns what a window is doing (its task, newest
// away_summary and last assistant message) into a 2-5 word row title, via a
// Nous backend (internal/llm.NousClient).
//
// It holds the pure decisions and the request; hook-title (internal/hooks)
// gathers the input and stores the result in the window's windowstate
// ShortTitle, which fleet readers pick up with no network or extra files.
package shorttitle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/llm"
	"github.com/blsmth/agentctl/internal/windowstate"
)

const (
	// RetitleInterval is the least time between two titles for one window,
	// unless it has none yet or a new away_summary appeared.
	RetitleInterval = 10 * time.Minute
	// RequestTimeout bounds one title request, key resolution included.
	RequestTimeout = 15 * time.Second

	// Prompt input caps, in runes.
	maxTaskRunes     = 300
	maxSummaryRunes  = 1000
	maxLastTextRunes = 1500
)

// Input is the text a title is made from. Ticket is context only: ticket rows
// keep showing their "#N title".
type Input struct {
	Task     string
	Ticket   string
	Summary  string
	LastText string
}

// normalize collapses whitespace so cosmetic differences hash alike.
func normalize(s string) string { return strings.Join(strings.Fields(s), " ") }

// Hash is the hex SHA-256 of whitespace-normalized s, "" for blank s.
func Hash(s string) string {
	n := normalize(s)
	if n == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(n))
	return hex.EncodeToString(sum[:])
}

// Empty reports whether there is nothing to title from.
func (in Input) Empty() bool {
	return normalize(in.Task) == "" && normalize(in.Summary) == "" && normalize(in.LastText) == ""
}

// Hash identifies the input as a whole.
func (in Input) Hash() string {
	return Hash(strings.Join([]string{normalize(in.Task), normalize(in.Ticket), normalize(in.Summary), normalize(in.LastText)}, "\x00"))
}

// Due reports whether a window with title state st should be retitled from
// in at now: there is something to title from (for a ticket window, an
// away_summary), no backoff is pending, the
// input changed since the last attempt, and either the window has no title,
// a new away_summary appeared, or RetitleInterval has passed.
func Due(st windowstate.ShortTitle, in Input, now time.Time) bool {
	if in.Empty() || now.Before(st.RetryAfter) {
		return false
	}
	// A ticket window shows its "#N title" until an away_summary takes over
	// the recap, so a title made before then would never be seen.
	if normalize(in.Task) == "" && normalize(in.Ticket) != "" && normalize(in.Summary) == "" {
		return false
	}
	if st.InputHash != "" && st.InputHash == in.Hash() {
		return false
	}
	if st.Title == "" {
		return true
	}
	if sh := Hash(in.Summary); sh != "" && sh != st.SummaryHash {
		return true
	}
	return now.Sub(st.At) >= RetitleInterval
}

// backoff is the retry delay after the n-th consecutive failure.
func backoff(n int) time.Duration {
	switch {
	case n <= 1:
		return 10 * time.Minute
	case n == 2:
		return 30 * time.Minute
	case n == 3:
		return 2 * time.Hour
	default:
		return 12 * time.Hour
	}
}

// Apply records the outcome of titling in at now: the new title, or (on err)
// the old title kept with a retry-after backoff.
func Apply(st *windowstate.ShortTitle, in Input, title string, err error, now time.Time) {
	if err != nil || title == "" {
		st.Failures++
		st.RetryAfter = now.Add(backoff(st.Failures))
		return
	}
	*st = windowstate.ShortTitle{
		Title:       title,
		At:          now,
		InputHash:   in.Hash(),
		SummaryHash: Hash(in.Summary),
	}
}

// ErrEmptyTitle is returned when the reply sanitizes to nothing.
var ErrEmptyTitle = errors.New("shorttitle: empty title after sanitizing")

// Generate asks comp for a title for in and sanitizes the reply. It never
// returns a blank title without an error.
func Generate(ctx context.Context, comp llm.Completer, in Input) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()
	out, err := comp.Complete(ctx, Prompt(in))
	if err != nil {
		return "", err
	}
	t := Sanitize(out)
	if t == "" {
		return "", ErrEmptyTitle
	}
	return t, nil
}

// Prompt is the instruction sent for in.
func Prompt(in Input) string {
	var b strings.Builder
	b.WriteString("Write a short title for what a coding agent is working on, from the notes below.\n")
	b.WriteString("Rules: 2 to 5 words, in title case, describing the work. Prefer a present participle, ")
	b.WriteString("for example \"Organizing your Photos\" or \"Fixing Hub Sort Order\". ")
	b.WriteString("No quotes, no markdown, no trailing punctuation. Reply with the title only.\n")
	section := func(label, text string) {
		if text != "" {
			b.WriteString("\n" + label + ":\n" + text + "\n")
		}
	}
	section("Ticket", headRunes(normalize(in.Ticket), maxTaskRunes))
	section("Task", headRunes(normalize(in.Task), maxTaskRunes))
	section("Latest recap", headRunes(normalize(in.Summary), maxSummaryRunes))
	// The end of a long message is the freshest part.
	section("Agent's last message", tailRunes(normalize(in.LastText), maxLastTextRunes))
	return b.String()
}

// headRunes is the first max runes of s.
func headRunes(s string, max int) string {
	if r := []rune(s); len(r) > max {
		return string(r[:max])
	}
	return s
}

// tailRunes is the last max runes of s.
func tailRunes(s string, max int) string {
	if r := []rune(s); len(r) > max {
		return string(r[len(r)-max:])
	}
	return s
}
