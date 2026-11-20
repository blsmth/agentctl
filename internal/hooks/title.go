package hooks

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/backendauth"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/llm"
	"github.com/blsmth/agentctl/internal/llmusage"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/pricecache"
	"github.com/blsmth/agentctl/internal/shorttitle"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// titleHookTimeoutSeconds is the Claude Code timeout registered for the
// async hook-title: one RequestTimeout request plus key resolution.
const titleHookTimeoutSeconds = 30

// Title seams; tests replace them so no test touches the network or the
// keychain.
var (
	titleSettings = func() config.TitleSettings { return config.Load().EffectiveTitles() }
	// titleKeyAvailable checks for a key without running api_key_cmd or
	// making a network call (backendauth.Available).
	titleKeyAvailable = func(s config.TitleSettings) error { return backendauth.Available(s.BackendName, s.Backend) }
	newTitleCompleter = func(s config.TitleSettings) (llm.Completer, error) {
		return llm.NewNousClient(s.BackendName, s.Backend, s.Model)
	}
	// titleNoCredits reports a fresh out-of-credits marker for the backend
	// (a local file read, no network).
	titleNoCredits  = backendauth.NoCreditsFresh
	titleTakeBudget = shorttitle.TakeBudget
	// titleTakeBackfillBudget is the housekeeping backfill's budget, which
	// leaves shorttitle.BackfillReserve requests to the hooks.
	titleTakeBackfillBudget = shorttitle.TakeBackfillBudget
	titleNow                = time.Now
)

// titlePriceRefresh keeps the machine-wide model price cache fresh (at most
// one refresh a day, machine-wide), silently. Tests replace it.
var titlePriceRefresh = func(ctx context.Context, s config.TitleSettings) {
	backends := map[string]config.BackendConfig{s.BackendName: s.Backend}
	if _, err := pricecache.RefreshIfStale(ctx, backends, pricecache.DefaultKey); err != nil {
		logging.Debug("hook-title: price refresh: %v", err)
	}
}

// Title is hook-title, registered async on Stop, UserPromptSubmit and
// SessionStart. A window gets at most two titles: the first from its task, or
// for a bare window its first real prompt, and one refinement at the first
// Stop that carries a last assistant message. After that the title is frozen
// (windowstate ShortTitle.Final): no time-based retitle. A failed refinement
// keeps the first title and is retried at a later Stop once its backoff has
// passed. Claude Code never waits on it. It does nothing (no network, no
// writes) when titles are off or no key is available, and it never spawns
// anything.
//
// A Stop that lands while the first-title request is in flight loses its
// refinement to the lease and waits for the next Stop. In a dispatched window
// /clear re-titles from the old Task, which survives the reset.
//
// The hook can fire twice for one event (project settings.local.json plus a
// per-agent --settings file); the claim inside the windowstate transaction
// makes the second run a no-op.
func Title() {
	if !guardAgentctl() {
		return
	}
	var input struct {
		LastAssistantMessage string `json:"last_assistant_message"`
		// Prompt is the UserPromptSubmit payload's user text; other events
		// have none.
		Prompt string `json:"prompt"`
		// Source is SessionStart's: startup, resume, clear or compact.
		Source string `json:"source"`
	}
	if !decodeInput(&input) {
		return
	}
	// SessionStart resets the title state on /clear (resetTitleOnClear); the
	// title restarts from the next prompt, so there is nothing to do here.
	if input.Source == sessionSourceClear {
		return
	}
	windowID := resolveHookWindowID()
	if windowID == "" {
		return
	}
	runTitle(context.Background(), windowID, input.LastAssistantMessage, input.Prompt)
}

// sessionSourceClear is the SessionStart source after /clear.
const sessionSourceClear = "clear"

// runTitle is Title after payload decoding. lastMessage is the Stop payload's
// last assistant message; only Stop carries one, so it also marks the
// refinement.
func runTitle(ctx context.Context, windowID, lastMessage, prompt string) {
	s := titleSettings()
	if !s.Enabled {
		logging.Debug("hook-title: window=%s skipped: titles disabled", windowID)
		return
	}
	// Deferred so a slow catalog fetch never delays the title, and so runs
	// that find nothing to title still keep the price cache fresh.
	defer titlePriceRefresh(ctx, s)
	ws, err := windowstate.Read(windowID)
	if err != nil {
		return
	}
	if ws.ShortTitle.Manual {
		logging.Debug("hook-title: window=%s skipped: manual title", windowID)
		return
	}
	if ws.ShortTitle.Final {
		logging.Debug("hook-title: window=%s skipped: title is final", windowID)
		return
	}
	if fp, ok := firstPromptFor(ws, prompt); ok {
		ws.FirstPrompt = recordFirstPrompt(windowID, fp)
	}
	in := titleInput(ws, lastMessage)
	now := titleNow()
	if !shorttitle.Due(ws.ShortTitle, in, now) {
		if now.Before(ws.ShortTitle.RetryAfter) {
			logging.Debug("hook-title: window=%s skipped: backoff until %s", windowID, ws.ShortTitle.RetryAfter.Format(time.TimeOnly))
		} else {
			logging.Debug("hook-title: window=%s skipped: not due", windowID)
		}
		return
	}
	if !titleReady(s, now) {
		return
	}
	titleWindow(ctx, s, windowID, in, now, titleTakeBudget, nil)
}

// titleMeta attributes a title request in the usage ledger: the window and its
// project (the work dir's repo name, worktree paths folded into their repo).
func titleMeta(windowID string) llmusage.Meta {
	m := llmusage.Meta{Feature: "titles", Window: windowID}
	if ws, err := windowstate.Read(windowID); err == nil {
		m.Project = projectName(ws.WorkDir)
	}
	return m
}

// projectName is the base name of dir, with a trailing "/.worktrees/<slug>"
// folded into its repo.
func projectName(dir string) string {
	if dir == "" {
		return ""
	}
	if i := strings.Index(dir, "/.worktrees/"); i > 0 {
		dir = dir[:i]
	}
	return filepath.Base(dir)
}

// titleReady reports whether a title request may be tried at all: the backend
// is not out of credits and a key is available. Both checks are local (a
// marker file, backendauth.Available), never the network.
func titleReady(s config.TitleSettings, now time.Time) bool {
	// Out of credits: skip silently, with no budget spent and no backoff
	// recorded, until the marker goes stale and one request probes again.
	// A :free model cannot 402, so a marker set elsewhere does not pause it.
	if _, out := titleNoCredits(s.BackendName, now); out && !strings.HasSuffix(s.Model, ":free") {
		logging.Debug("hook-title: skipped: backend %s out of credits", s.BackendName)
		return false
	}
	if err := titleKeyAvailable(s); err != nil {
		logging.Debug("hook-title: skipped: no key available: %v", err)
		return false
	}
	return true
}

// titleOutcome is what titleWindow did.
type titleOutcome int

const (
	// titleNotClaimed: the window was not due inside its transaction (another
	// run holds it, it was titled meanwhile, or it is gone). No request.
	titleNotClaimed titleOutcome = iota
	// titleDeferred: no key, no budget or no credits. No request was
	// recorded against the window and its lease was dropped.
	titleDeferred
	// titleRequested: a request was sent and its outcome recorded.
	titleRequested
)

// titleWindow claims windowID, asks the backend for a title of in (spending
// one unit of budget through take) and records the outcome. still, when set,
// must also hold for the record inside the claim transaction.
func titleWindow(ctx context.Context, s config.TitleSettings, windowID string, in shorttitle.Input, now time.Time,
	take func(time.Time) bool, still func(windowstate.WindowState) bool,
) titleOutcome {
	// Claim the window before the request: the hook can fire twice for one
	// event (project settings.local.json plus a per-agent --settings file),
	// and only one of them may spend a request. The lease outlives the hook
	// timeout, so a killed hook only delays the next try.
	claimed := false
	err := windowstate.Update(windowID, func(w *windowstate.WindowState) {
		if !shorttitle.Due(w.ShortTitle, in, now) || (still != nil && !still(*w)) {
			return
		}
		w.ShortTitle.RetryAfter = now.Add(titleLease)
		claimed = true
	})
	if err != nil || !claimed {
		logUpdateErr("hook-title", err)
		return titleNotClaimed
	}
	ctx = llmusage.WithMeta(ctx, titleMeta(windowID))
	title, kind, genErr := generateTitle(ctx, s, in, now, take)
	if errors.Is(genErr, llm.ErrNoKey) || errors.Is(genErr, errTitleBudget) || errors.Is(genErr, backendauth.ErrNoCredits) {
		// Not a failure of this input: drop the lease, write nothing else.
		logging.Log("hook-title: window=%s skipped: %v", windowID, genErr)
		err = windowstate.Update(windowID, func(w *windowstate.WindowState) {
			if w.ShortTitle.RetryAfter.Equal(now.Add(titleLease)) {
				w.ShortTitle.RetryAfter = time.Time{}
			}
		})
		logUpdateErr("hook-title", err)
		return titleDeferred
	}
	if genErr != nil {
		logging.Log("hook-title: window=%s failed: %v", windowID, genErr)
	}
	err = windowstate.Update(windowID, func(w *windowstate.WindowState) {
		// Only while our claim is still held: a /clear that reset the record
		// while the request was in flight must not get the old work's title.
		if !w.ShortTitle.RetryAfter.Equal(now.Add(titleLease)) {
			return
		}
		shorttitle.Apply(&w.ShortTitle, in, title, kind, genErr, now)
	})
	logUpdateErr("hook-title", err)
	if err == nil && genErr == nil {
		logging.Log("hook-title: window=%s generated kind=%s title=%q", windowID, kind, title)
	}
	return titleRequested
}

// titleLease is how long a claimed window is skipped by other hook-title
// runs while its request is in flight.
const titleLease = 2 * titleHookTimeoutSeconds * time.Second

// errTitleBudget means the global hourly cap is spent.
var errTitleBudget = errors.New("hourly title request cap reached")

// generateTitle spends one unit of the hourly budget (through take) and asks
// the backend.
func generateTitle(ctx context.Context, s config.TitleSettings, in shorttitle.Input, now time.Time,
	take func(time.Time) bool,
) (title, kind string, err error) {
	if !take(now) {
		return "", "", fmt.Errorf("%w (%d/hour)", errTitleBudget, shorttitle.HourlyBudget)
	}
	comp, err := newTitleCompleter(s)
	if err != nil {
		return "", "", err
	}
	return shorttitle.Generate(ctx, comp, in)
}

// titleInput gathers what the title is made from: the ad-hoc task (or the
// ticket title; for a bare window, its first prompt) and the Stop payload's
// last assistant message, which is empty on every other event.
func titleInput(ws windowstate.WindowState, lastMessage string) shorttitle.Input {
	return shorttitle.Input{Task: ws.Task, FirstPrompt: ws.FirstPrompt, Ticket: ws.TicketTitle, LastText: lastMessage, Agent: ws.AgentName, Mode: ws.Mode}
}

// resetTitleOnClear forgets a window's title and first prompt so the next
// prompt titles the new work fresh. A human title (ShortTitle.Manual, set
// from the hub) survives: the human named the window, not the work. Called
// from SessionStart with the window's record inside its transaction.
func resetTitleOnClear(ws *windowstate.WindowState) {
	if !ws.ShortTitle.Manual {
		ws.ShortTitle = windowstate.ShortTitle{}
	}
	ws.FirstPrompt = ""
}

// maxFirstPromptRunes caps the stored first prompt.
const maxFirstPromptRunes = 500

// injectedBlockRes match synthetic blocks Claude Code or agentctl put in a
// user turn; Go's RE2 has no backreferences, so one per tag.
var injectedBlockRes = []*regexp.Regexp{
	regexp.MustCompile(`(?s)<task-notification>.*?</task-notification>`),
	regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`),
}

// slashCommandRe matches a slash command ("/compact ..."); an absolute path
// such as "/Users/me/x.go fails" does not match.
var slashCommandRe = regexp.MustCompile(`^/[\w:-]+(\s|$)`)

// realPrompt returns the user-typed part of a prompt, and false when there
// is none: empty text, a slash command, or only injected content (task
// notifications, system reminders, `agentctl notice:` lines).
func realPrompt(prompt string) (string, bool) {
	for _, re := range injectedBlockRes {
		prompt = re.ReplaceAllString(prompt, "")
	}
	var kept []string
	for _, line := range strings.Split(prompt, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "agentctl notice:") {
			continue
		}
		kept = append(kept, line)
	}
	text := strings.TrimSpace(strings.Join(kept, "\n"))
	if text == "" || slashCommandRe.MatchString(text) {
		return "", false
	}
	if r := []rune(text); len(r) > maxFirstPromptRunes {
		text = string(r[:maxFirstPromptRunes])
	}
	return text, true
}

// firstPromptFor reports the prompt to record as the window's first prompt:
// a real user request on a window with no Task, no ticket title and no first
// prompt yet. Dispatched windows already have their Task.
func firstPromptFor(ws windowstate.WindowState, prompt string) (string, bool) {
	if ws.FirstPrompt != "" || ws.Task != "" || ws.TicketTitle != "" {
		return "", false
	}
	return realPrompt(prompt)
}

// recordFirstPrompt stores fp when the window still has none and returns what
// the record holds afterwards (a concurrent run may have won). A failed write
// still returns fp so this run can title from it.
func recordFirstPrompt(windowID, fp string) string {
	held := fp
	err := windowstate.Update(windowID, func(w *windowstate.WindowState) {
		if w.FirstPrompt == "" {
			w.FirstPrompt = fp
		}
		held = w.FirstPrompt
	})
	logUpdateErr("hook-title", err)
	return held
}
