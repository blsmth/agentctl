// Package shorttitle turns what a window is doing (its task or first prompt,
// and the agent's first response) into a 2-5 word row title, via a Nous
// backend (internal/llm.NousClient).
//
// A window gets at most two titles: the first from its task or first prompt,
// then one refinement from the first response (an input with LastText). The
// refinement sets windowstate ShortTitle.Final and the title is frozen.
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
	// RequestTimeout bounds one title request, key resolution included.
	RequestTimeout = 15 * time.Second

	// Prompt input caps, in runes.
	maxTaskRunes     = 300
	maxPromptRunes   = 500
	maxLastTextRunes = 1500
)

// Input is the text a title is made from. Ticket is context only: ticket rows
// keep showing their "#N title". FirstPrompt is the first user prompt of a
// bare window (no Task, no Ticket). LastText is the agent's first response;
// an Input with it is the refinement, one without it the first title.
type Input struct {
	Task        string
	FirstPrompt string
	Ticket      string
	LastText    string
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

// Empty reports whether there is nothing to title from: no task, first prompt
// or ticket. A response alone is not enough.
func (in Input) Empty() bool {
	return normalize(in.Task) == "" && normalize(in.FirstPrompt) == "" && normalize(in.Ticket) == ""
}

// Refinement reports whether in carries the agent's response, so a title made
// from it is the window's last.
func (in Input) Refinement() bool { return normalize(in.LastText) != "" }

// Hash identifies the input as a whole.
func (in Input) Hash() string {
	parts := []string{normalize(in.Task), normalize(in.Ticket), normalize(in.LastText)}
	// Appended only when set, so windows without a first prompt keep the
	// hashes recorded before the field existed.
	if fp := normalize(in.FirstPrompt); fp != "" {
		parts = append(parts, fp)
	}
	return Hash(strings.Join(parts, "\x00"))
}

// Due reports whether a window with title state st should be titled from in
// at now. Never when the title is frozen (st.Final), a backoff is pending,
// there is nothing to title from, or in is what the last attempt used. A
// window with no title is due for its first one; a window with a first title
// is due only for the refinement, once in carries the agent's response.
// Time and new recaps play no part.
func Due(st windowstate.ShortTitle, in Input, now time.Time) bool {
	if st.Final || in.Empty() || now.Before(st.RetryAfter) {
		return false
	}
	if st.InputHash != "" && st.InputHash == in.Hash() {
		return false
	}
	if st.Title == "" {
		return true
	}
	return in.Refinement()
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

// Apply records the outcome of titling in at now: the new title (frozen when
// in was a refinement), or (on err) the old title kept, unfrozen, with a
// retry-after backoff.
func Apply(st *windowstate.ShortTitle, in Input, title string, err error, now time.Time) {
	if err != nil || title == "" {
		st.Failures++
		st.RetryAfter = now.Add(backoff(st.Failures))
		return
	}
	*st = windowstate.ShortTitle{
		Title:     title,
		At:        now,
		InputHash: in.Hash(),
		Final:     in.Refinement(),
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
	section("First request", headRunes(normalize(in.FirstPrompt), maxPromptRunes))
	// The end of a long message is the freshest part.
	section("Agent's first response", tailRunes(normalize(in.LastText), maxLastTextRunes))
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
