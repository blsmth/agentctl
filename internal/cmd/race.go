package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/logstream"
	"github.com/blsmth/agentctl/internal/race"
	"github.com/blsmth/agentctl/internal/sessions"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/tui"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// RaceOptions are the flags of `agentctl race`.
type RaceOptions struct {
	Target   string // @N, session ID, or transcript path; "" = this window
	Params   race.Params
	Speed    float64       // replay multiplier on real time; 0 = use Duration
	Duration time.Duration // fit the agent's timeline into this
	JSON     bool
}

// Seams for tests.
var (
	raceTmuxPane      = func() string { return os.Getenv("TMUX_PANE") }
	raceWindowForPane = tmux.WindowIDForPane
	raceReadWindow    = windowstate.Read
	raceFindSession   = sessions.FindLogBySessionID
	raceRunTUI        = func(m tea.Model) error {
		_, err := tea.NewProgram(m).Run()
		return err
	}
	raceRemoteWindow = remoteWindowRecord
)

// RunRace replays a transcript as a race between the agent and a human.
func RunRace(w io.Writer, opts RaceOptions) error {
	if err := validateRace(opts); err != nil {
		return err
	}
	path, err := resolveRaceLog(opts.Target)
	if err != nil {
		return err
	}
	r, err := loadRace(path, opts.Params)
	if err != nil {
		return err
	}
	if opts.JSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(r.Summarize(path))
	}
	if len(r.Steps) == 0 {
		fmt.Fprintf(w, "Nothing to race: %s has no agent steps yet.\n", path)
		return nil
	}
	factor := race.Factor(r.AgentTotal, opts.Duration, opts.Speed)
	return raceRunTUI(tui.NewRaceModel(r, raceTitle(path), factor, time.Now()))
}

func validateRace(opts RaceOptions) error {
	p := opts.Params
	switch {
	case p.WPM <= 0:
		return errors.New("--wpm must be positive")
	case p.ReadWPM <= 0:
		return errors.New("--read-wpm must be positive")
	case p.Think < 0:
		return errors.New("--think must not be negative")
	case p.MaxGap <= 0:
		return errors.New("--max-gap must be positive")
	case p.RealLife && (p.Schedule.FocusHours <= 0 || p.Schedule.FocusHours > 16):
		return errors.New("--focus-hours must be greater than 0 and at most 16")
	case p.RealLife && (p.Schedule.DaysPerWeek < 1 || p.Schedule.DaysPerWeek > 7):
		return errors.New("--days-per-week must be between 1 and 7")
	case opts.Speed < 0:
		return errors.New("--speed must not be negative")
	case opts.Speed == 0 && opts.Duration <= 0:
		return errors.New("--duration must be positive")
	}
	return nil
}

// loadRace parses the transcript as it stands now (a session still being
// written is snapshotted) and builds the race.
func loadRace(path string, p race.Params) (*race.Race, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening transcript: %w", err)
	}
	defer f.Close()
	entries, err := logstream.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("reading transcript %s: %w", path, err)
	}
	return race.Build(entries, p), nil
}

// resolveRaceLog turns the target into a transcript path: an existing file,
// a window (@N, or this pane's window when empty), or a session ID.
func resolveRaceLog(target string) (string, error) {
	switch {
	case target == "":
		pane := raceTmuxPane()
		if pane == "" {
			return "", errors.New("not inside tmux: pass a window (@N), a session ID, or a transcript path")
		}
		id := raceWindowForPane(pane)
		if id == "" {
			return "", fmt.Errorf("could not resolve the window of pane %s", pane)
		}
		return logForWindow(id)
	case strings.HasPrefix(target, "@"):
		return logForWindow(target)
	}
	if fi, err := os.Stat(target); err == nil && !fi.IsDir() {
		return target, nil
	}
	if strings.ContainsAny(target, `/\`) || strings.HasSuffix(target, ".jsonl") {
		return "", fmt.Errorf("no transcript at %s", target)
	}
	if p := raceFindSession(target); p != "" {
		return p, nil
	}
	if handleLike(target) {
		ix := fleet.NewIndex(liveGroups(), allClosed())
		s, err := ix.Resolve(target)
		var amb *fleet.AmbiguousError
		if errors.As(err, &amb) {
			return "", err
		}
		if err == nil {
			if p := raceFindSession(s.SessionID); p != "" {
				return p, nil
			}
			return "", fmt.Errorf("no transcript found for session %s %q", s.Handle, s.Label)
		}
	}
	return "", fmt.Errorf("no transcript found for session %q", target)
}

// logForWindow finds a window's transcript from its windowstate record:
// LogPath when recorded, else the log named by its SessionID. The current
// project's record is tried first, then the window's own session.
func logForWindow(id string) (string, error) {
	ws, _ := raceReadWindow(id)
	if ws.LogPath == "" && ws.SessionID == "" {
		ws = raceRemoteWindow(id)
	}
	if ws.LogPath != "" {
		if _, err := os.Stat(ws.LogPath); err == nil {
			return ws.LogPath, nil
		}
	}
	if ws.SessionID != "" {
		if p := raceFindSession(ws.SessionID); p != "" {
			return p, nil
		}
		return "", fmt.Errorf("window %s: no transcript found for session %s", id, ws.SessionID)
	}
	return "", fmt.Errorf("window %s has no recorded session (is it an agent window?)", id)
}

// remoteWindowRecord reads the record of a window in another project's
// session (anything `agentctl status` lists).
func remoteWindowRecord(id string) windowstate.WindowState {
	rows, _, err := collectStatusRows(StatusOptions{})
	if err != nil {
		return windowstate.WindowState{}
	}
	sr, err := findWindow(rows, id)
	if err != nil {
		return windowstate.WindowState{}
	}
	ws, _ := windowstate.ReadIn(sr.group.RuntimeDir, sr.row.WindowID)
	return ws
}

// raceTitle names the transcript in the header: its file name, with a
// session UUID cut to its first 8 characters, after its directory unless
// that is a Claude Code project dir (an encoded path, "-Users-...").
func raceTitle(path string) string {
	dir := filepath.Base(filepath.Dir(path))
	base := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if len(base) == 36 && strings.Count(base, "-") == 4 {
		base = base[:8]
	}
	if dir == "." || dir == string(filepath.Separator) || strings.HasPrefix(dir, "-") {
		return base
	}
	return dir + "/" + base
}
