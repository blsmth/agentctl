package serve

import (
	"time"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/sessions"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// promptRescanAfter is how long a scan that found no prompt is trusted for the
// same session and log before the detail route looks again.
const promptRescanAfter = 10 * time.Minute

// Seams for tests.
var (
	findSessionLog  = sessions.FindLogBySessionID
	firstHumanTextF = sessions.FirstHumanText
)

// promptFor returns the full initial prompt for the window detail. A stored
// prompt wins. Otherwise it is derived once from the session transcript (the
// first real user message), persisted to windowstate so later polls skip the
// log, and a miss is recorded so it is not re-scanned every poll. Without a
// derivable prompt it falls back to the short first prompt.
func (s *Server) promptFor(runtimeDir string, m fleet.Meta) string {
	if m.Prompt != "" {
		return m.Prompt
	}
	if m.WindowID == "" || runtimeDir == "" {
		return m.FirstPrompt
	}
	logPath := m.LogPath
	if logPath == "" {
		logPath = findSessionLog(m.SessionID)
	}
	if logPath == "" {
		return m.FirstPrompt
	}
	key := m.SessionID + "|" + logPath
	now := s.now()
	if m.PromptScanKey == key && now.Sub(m.PromptScanAt) < promptRescanAfter {
		return m.FirstPrompt
	}
	text := windowstate.CapPrompt(firstHumanTextF(logPath))
	err := windowstate.UpdateIn(runtimeDir, m.WindowID, func(w *windowstate.WindowState) {
		if text != "" {
			if w.Prompt == "" {
				w.Prompt = text
			}
			w.PromptScanKey, w.PromptScanAt = "", time.Time{}
			return
		}
		w.PromptScanKey, w.PromptScanAt = key, now
	})
	if err != nil {
		logging.Debug("serve: persisting derived prompt for %s: %v", m.WindowID, err)
	}
	if text == "" {
		return m.FirstPrompt
	}
	return text
}
