package sessions

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/blsmth/agentctl/internal/harness"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/transcript"
)

// piDirOverride replaces pi's session root for tests ("" = harness.PiSessionsDir,
// unless claudeDir is overridden, in which case pi sessions are not read).
var piDirOverride string

// SetPiSessionsDirForTest points pi session discovery at dir.
func SetPiSessionsDirForTest(dir string) { piDirOverride = dir }

// piSessionsRoot returns the pi session root to read, or "".
func piSessionsRoot() string {
	if piDirOverride != "" {
		return piDirOverride
	}
	if claudeDir != "" {
		return "" // a test pinned Claude's dir; keep it hermetic
	}
	return harness.PiSessionsDir()
}

// findPiSessionsIn adds the agentctl-launched pi sessions under root
// (<root>/--<path>--/<timestamp>_<id>.jsonl) to result. pi sessions not
// tagged with an agent by agentctl's bridge extension are skipped, like
// Claude sessions without an agentSetting.
func findPiSessionsIn(root string, result map[string][]Session) {
	dirs, err := os.ReadDir(root)
	if err != nil {
		if !os.IsNotExist(err) {
			logging.Debug("sessions: reading pi sessions dir %s: %v", root, err)
		}
		return
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		dirPath := filepath.Join(root, d.Name())
		project := piProjectPath(d.Name())
		entries, err := os.ReadDir(dirPath)
		if err != nil {
			continue
		}
		for _, e := range entries {
			id := transcript.PiSessionID(e.Name())
			if id == "" || !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			logFile := filepath.Join(dirPath, e.Name())
			agent := sessionAgent(logFile)
			if agent == "" {
				continue
			}
			start, end := sessionTimeRange(logFile)
			if start.IsZero() {
				if info, err := e.Info(); err == nil {
					start = info.ModTime()
				}
			}
			result[agent] = append(result[agent], Session{
				ID:        id,
				Agent:     agent,
				Project:   project,
				StartTime: start,
				EndTime:   end,
				LogFile:   logFile,
				Harness:   harness.Pi,
			})
		}
	}
}

// piProjectPath turns pi's "--Users-alice-src--" dir name into the same
// display form decodeDirName gives a Claude project dir ("/-Users-alice-src").
func piProjectPath(name string) string {
	return "/-" + strings.Trim(name, "-")
}

// findPiLog returns the pi session file for sessionID, or "".
func findPiLog(sessionID string) string {
	root := piSessionsRoot()
	if root == "" || sessionID == "" || strings.ContainsAny(sessionID, `/\*?[`) {
		return ""
	}
	matches, err := filepath.Glob(filepath.Join(root, "*", "*_"+sessionID+".jsonl"))
	if err != nil || len(matches) != 1 {
		return ""
	}
	return matches[0]
}

// HarnessOf names the harness that recorded sessionID: "pi" when only a pi
// session file has that ID, else harness.Default.
func HarnessOf(sessionID string) string {
	if FindLogBySessionID(sessionID) == "" && findPiLog(sessionID) != "" {
		return harness.Pi
	}
	return harness.Default
}
