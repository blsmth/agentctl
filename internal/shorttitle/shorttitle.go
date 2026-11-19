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
//
// Agent and Mode are hints for the model only (the window's agent name and
// dispatch mode); they are not part of Hash.
type Input struct {
	Task        string
	FirstPrompt string
	Ticket      string
	LastText    string
	Agent       string
	Mode        string
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
// retry-after backoff. A failed attempt keeps the old kind too.
func Apply(st *windowstate.ShortTitle, in Input, title, kind string, err error, now time.Time) {
	if err != nil || title == "" {
		st.Failures++
		st.RetryAfter = now.Add(backoff(st.Failures))
		return
	}
	*st = windowstate.ShortTitle{
		Title:     title,
		Kind:      kind,
		At:        now,
		InputHash: in.Hash(),
		Final:     in.Refinement(),
	}
}

// ErrEmptyTitle is returned when the reply sanitizes to nothing.
var ErrEmptyTitle = errors.New("shorttitle: empty title after sanitizing")

// Generate asks comp for a kind and title for in. The reply is
// "<kind> | <title>"; a reply without a known kind keeps the whole reply as
// the title and returns an empty kind. A review-pr window is always kind
// "review". It never returns a blank title without an error.
func Generate(ctx context.Context, comp llm.Completer, in Input) (title, kind string, err error) {
	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()
	out, err := comp.Complete(ctx, Prompt(in))
	if err != nil {
		return "", "", err
	}
	kind, title = ParseReply(out)
	if title == "" {
		return "", "", ErrEmptyTitle
	}
	if in.Mode == ReviewPRMode {
		kind = "review"
	}
	return title, kind, nil
}

// ParseReply splits a model reply into its kind and sanitized title.
func ParseReply(out string) (kind, title string) {
	line := firstLine(out)
	if head, rest, ok := strings.Cut(line, "|"); ok {
		if k := normalizeKind(head); k != "" {
			return k, sanitize(rest, false)
		}
		// A single stray word before the pipe ("refactor | ...") is a kind
		// the model invented: drop it, never let it into the title.
		if isSingleWord(head) {
			if t := sanitize(rest, false); t != "" {
				return "", t
			}
		}
	}
	return "", sanitize(out, false)
}

// Prompt is the instruction sent for in.
func Prompt(in Input) string {
	var b strings.Builder
	b.WriteString("Write a short title for what a coding agent is working on, from the notes below, and classify the kind of work.\n")
	b.WriteString("Title rules: 2 to 5 words, in title case, describing the work. Prefer a present participle, ")
	b.WriteString("for example \"Organizing your Photos\" or \"Fixing Hub Sort Order\". ")
	b.WriteString("No quotes, no markdown, no trailing punctuation.\n")
	b.WriteString("Kind: exactly one of these words:\n")
	for _, k := range kinds {
		b.WriteString("- " + k.name + ": " + k.meaning + "\n")
	}
	b.WriteString("Reply on one line as `<kind> | <title>`, for example `go | Fixing Hub Sort Order`. Nothing else.\n")
	section := func(label, text string) {
		if text != "" {
			b.WriteString("\n" + label + ":\n" + text + "\n")
		}
	}
	section("Agent", headRunes(normalize(in.Agent), 80))
	section("Dispatch mode", headRunes(normalize(in.Mode), 80))
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
