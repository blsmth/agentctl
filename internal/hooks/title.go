package hooks

import (
	"context"
	"errors"
	"fmt"
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
	}
	if !decodeInput(&input) {
		return
	}
	windowID := resolveHookWindowID()
	if windowID == "" {
		return
	}
	runTitle(context.Background(), windowID, input.TranscriptPath, input.LastAssistantMessage)
}

// runTitle is Title after payload decoding.
func runTitle(ctx context.Context, windowID, transcript, lastMessage string) {
	s := titleSettings()
	if !s.Enabled {
		return
	}
	ws, err := windowstate.Read(windowID)
	if err != nil {
		return
	}
	in := titleInput(ws, transcript, lastMessage)
	now := titleNow()
	if !shorttitle.Due(ws.ShortTitle, in, now) {
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
	if errors.Is(genErr, llm.ErrNoKey) || errors.Is(genErr, errTitleBudget) {
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
// ticket title as context), the newest away_summary in the session log, and
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
	return shorttitle.Input{Task: ws.Task, Ticket: ws.TicketTitle, Summary: summary, LastText: lastMessage}
}
