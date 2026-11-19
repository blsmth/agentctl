package hooks

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/backendauth"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/llm"
	"github.com/blsmth/agentctl/internal/logging"
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
	titleNow        = time.Now
)

// Title is hook-title, registered async on Stop, UserPromptSubmit and
// SessionStart: it refreshes the window's short row title from what exists
// right now (task, newest away_summary, last assistant message), at most
// once per shorttitle.RetitleInterval unless the window has no title yet or
// a new away_summary appeared. Claude Code never waits on it. It does
// nothing (no network, no writes) when titles are off or no key is
// available, and it never spawns anything.
//
// The hook can fire twice for one event (project settings.local.json plus a
// per-agent --settings file); the input hash, re-checked inside the
// windowstate transaction, makes the second run a no-op.
func Title() {
	if !guardAgentctl() {
		return
	}
	var input struct {
		TranscriptPath       string `json:"transcript_path"`
		LastAssistantMessage string `json:"last_assistant_message"`
		// Prompt is the UserPromptSubmit payload's user text; other events
		// have none.
		Prompt string `json:"prompt"`
	}
	if !decodeInput(&input) {
		return
	}
	windowID := resolveHookWindowID()
	if windowID == "" {
		return
	}
	runTitle(context.Background(), windowID, input.TranscriptPath, input.LastAssistantMessage, input.Prompt)
}

// runTitle is Title after payload decoding.
func runTitle(ctx context.Context, windowID, transcript, lastMessage, prompt string) {
	s := titleSettings()
	if !s.Enabled {
		return
	}
	ws, err := windowstate.Read(windowID)
	if err != nil {
		return
	}
	if fp, ok := firstPromptFor(ws, prompt); ok {
		ws.FirstPrompt = recordFirstPrompt(windowID, fp)
	}
	in := titleInput(ws, transcript, lastMessage)
	now := titleNow()
	if !shorttitle.Due(ws.ShortTitle, in, now) {
		return
	}
	// Out of credits: skip silently, with no budget spent and no backoff
	// recorded, until the marker goes stale and one request probes again.
	// A :free model cannot 402, so a marker set elsewhere does not pause it.
	if _, out := titleNoCredits(s.BackendName, now); out && !strings.HasSuffix(s.Model, ":free") {
		logging.Debug("hook-title: backend %s out of credits, skipping", s.BackendName)
		return
	}
	if err := titleKeyAvailable(s); err != nil {
		logging.Debug("hook-title: %v", err)
		return
	}
	// Claim the window before the request: the hook can fire twice for one
	// event (project settings.local.json plus a per-agent --settings file),
	// and only one of them may spend a request. The lease outlives the hook
	// timeout, so a killed hook only delays the next try.
	claimed := false
	err = windowstate.Update(windowID, func(w *windowstate.WindowState) {
		if !shorttitle.Due(w.ShortTitle, in, now) {
			return
		}
		w.ShortTitle.RetryAfter = now.Add(titleLease)
		claimed = true
	})
	if err != nil || !claimed {
		logUpdateErr("hook-title", err)
		return
	}
	title, genErr := generateTitle(ctx, s, in, now)
	if errors.Is(genErr, llm.ErrNoKey) || errors.Is(genErr, errTitleBudget) || errors.Is(genErr, backendauth.ErrNoCredits) {
		// Not a failure of this input: drop the lease, write nothing else.
		logging.Log("hook-title: window=%s: %v", windowID, genErr)
		err = windowstate.Update(windowID, func(w *windowstate.WindowState) {
			if w.ShortTitle.RetryAfter.Equal(now.Add(titleLease)) {
				w.ShortTitle.RetryAfter = time.Time{}
			}
		})
		logUpdateErr("hook-title", err)
		return
	}
	if genErr != nil {
		logging.Log("hook-title: window=%s: %v", windowID, genErr)
	}
	err = windowstate.Update(windowID, func(w *windowstate.WindowState) {
		shorttitle.Apply(&w.ShortTitle, in, title, genErr, now)
	})
	logUpdateErr("hook-title", err)
	if err == nil && genErr == nil {
		logging.Debug("hook-title: window=%s title=%q", windowID, title)
	}
}

// titleLease is how long a claimed window is skipped by other hook-title
// runs while its request is in flight.
const titleLease = 2 * titleHookTimeoutSeconds * time.Second

// errTitleBudget means the global hourly cap is spent.
var errTitleBudget = errors.New("hourly title request cap reached")

// generateTitle spends one unit of the hourly budget and asks the backend.
func generateTitle(ctx context.Context, s config.TitleSettings, in shorttitle.Input, now time.Time) (string, error) {
	if !titleTakeBudget(now) {
		return "", fmt.Errorf("%w (%d/hour)", errTitleBudget, shorttitle.HourlyBudget)
	}
	comp, err := newTitleCompleter(s)
	if err != nil {
		return "", err
	}
	return shorttitle.Generate(ctx, comp, in)
}

// titleInput gathers what the title is made from: the ad-hoc task (or the
// ticket title as context; for a bare window, its first prompt), the newest away_summary in the session log, and
// the last assistant message (the Stop payload's, else the one hook-check-done
// recorded).
func titleInput(ws windowstate.WindowState, transcript, lastMessage string) shorttitle.Input {
	logPath := transcript
	if logPath == "" {
		logPath = ws.LogPath
	}
	var summary string
	if logPath != "" {
		summary = fleet.ReadLogInfo(logPath).Recap
	}
	if lastMessage == "" {
		lastMessage = ws.LastText
	}
	return shorttitle.Input{Task: ws.Task, FirstPrompt: ws.FirstPrompt, Ticket: ws.TicketTitle, Summary: summary, LastText: lastMessage}
}

// maxFirstPromptRunes caps the stored first prompt.
const maxFirstPromptRunes = 500

// injectedBlockRes match synthetic blocks Claude Code or agentctl put in a
// user turn; Go's RE2 has no backreferences, so one per tag.
var injectedBlockRes = []*regexp.Regexp{
	regexp.MustCompile(`(?s)<task-notification>.*?</task-notification>`),
	regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`),
}

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
	if text == "" || strings.HasPrefix(text, "/") {
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
