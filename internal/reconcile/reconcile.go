// Package reconcile notices tmux windows that have disappeared and finishes
// them: it claims the window's windowstate record, fires the Gone handlers
// once, and removes the window's mailbox. It needs no long-lived process: the
// hidden `agentctl reconcile` command runs it from a tmux hook or a detached
// spawn, and the monitor (when enabled) calls the same Claim/Finish code, so
// the outcome is identical whichever notices first and the windowstate claim
// guarantees only one of them fires the handlers.
package reconcile

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/mailbox"
	"github.com/blsmth/agentctl/internal/metrics/events"
	"github.com/blsmth/agentctl/internal/state"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/transcript"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// DefaultSettle is how long Run waits between its two window listings. A
// hook-triggered run has no previous tick to compare with, so absence must be
// seen twice, this far apart, before a record is touched.
const DefaultSettle = 300 * time.Millisecond

// LockName is the runtime-dir file Run takes a non-blocking flock on.
const LockName = "reconcile.lock"

// Deps are the side effects of reconciling, injectable for tests.
type Deps struct {
	RuntimeDir string
	// List lists this project session's windows. An error is NOT zero windows.
	List func() ([]tmux.WindowInfo, error)
	// HasSession reports whether the project's tmux session exists.
	HasSession func() bool
	Settle     time.Duration
	Sleep      func(time.Duration)
	// Handle runs the Gone transition handlers for a claimed window.
	Handle func(t state.Transition, agentCfg config.AgentConfig)
	// AgentConfig resolves a base agent name's config.
	AgentConfig func(base string) config.AgentConfig
	// RemoveInbox deletes a window's mailbox.
	RemoveInbox func(windowID string) error
}

// DefaultDeps wires Deps to tmux, config and mailbox for the current session
// and sync handlers (the caller is a short-lived process: handler work must
// finish before it exits).
func DefaultDeps(runtimeDir string, handle func(state.Transition, config.AgentConfig)) Deps {
	session := tmux.SessionName
	return Deps{
		RuntimeDir:  runtimeDir,
		List:        func() ([]tmux.WindowInfo, error) { return tmux.ListWindowsInErr(session) },
		HasSession:  func() bool { return tmux.HasSessionNamed(session) },
		Settle:      DefaultSettle,
		Sleep:       time.Sleep,
		Handle:      handle,
		AgentConfig: func(base string) config.AgentConfig { return config.Load().ForAgent(base) },
		RemoveInbox: mailbox.RemoveInbox,
	}
}

// Claimed is a window whose record this process removed and whose Gone
// handlers it now owes.
type Claimed struct {
	WindowID string
	Record   windowstate.WindowState
}

// Claim takes ownership of windowID's record (see windowstate.Claim). ok is
// false when someone else already did, or there was no record.
func Claim(windowID string, confirm func() bool) (Claimed, bool) {
	ws, ok := windowstate.Claim(windowID, confirm)
	if !ok {
		return Claimed{}, false
	}
	return Claimed{WindowID: windowID, Record: ws}, true
}

// identity derives the agent's instance and base name from its record.
func identity(ws windowstate.WindowState) (instance, base string) {
	instance = tmux.StripAgentPrefix(ws.Instance)
	if instance == "" {
		instance = tmux.StripAgentPrefix(ws.RenderedName)
	}
	base = ws.AgentName
	if base == "" {
		base = tmux.BaseAgentName(instance)
	}
	return instance, base
}

// untracked reports shell and improvement windows, which are cleaned up but
// never fire handlers.
func untracked(instance string) bool {
	return instance == "shell" || strings.HasPrefix(instance, "improve~")
}

func parseStatus(s string) state.AgentStatus {
	switch s {
	case "working":
		return state.StatusWorking
	case "blocked":
		return state.StatusBlocked
	case "gone":
		return state.StatusGone
	}
	return state.StatusIdle
}

// Finish fires the Gone transition (From is the record's state) unless the
// window is shell/improve~ or unidentifiable, then removes the window's
// mailbox. A handler panic is recovered and logged.
func Finish(c Claimed, d Deps) {
	instance, base := identity(c.Record)
	logging.Log("reconcile: window gone window=%s instance=%q (was %q)", c.WindowID, instance, c.Record.State)
	// The metrics serve reads Gone from the event log; the in-process monitor
	// tracker is gone. No-op unless metrics are enabled.
	events.RecordGone(d.RuntimeDir, c.WindowID, c.Record)
	if base != "" && !untracked(instance) && d.Handle != nil {
		t := state.Transition{
			Agent:    base,
			WindowID: c.WindowID,
			From:     parseStatus(c.Record.State),
			To:       state.StatusGone,
		}
		var cfg config.AgentConfig
		if d.AgentConfig != nil {
			cfg = d.AgentConfig(base)
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					logging.Log("reconcile: panic in Gone handler window=%s: %v\n%s", c.WindowID, r, debug.Stack())
				}
			}()
			d.Handle(t, cfg)
		}()
	}
	if d.RemoveInbox != nil {
		if err := d.RemoveInbox(c.WindowID); err != nil {
			logging.Log("reconcile: removing mailbox for %s: %v", c.WindowID, err)
		}
	}
}

// Run reconciles windowstate against tmux once and returns the IDs of the
// windows it reaped. It is idempotent and safe to run concurrently with itself
// and with the monitor:
//   - a non-blocking flock on <runtimeDir>/reconcile.lock makes a second Run
//     exit at once (the holder is already doing it);
//   - a failed list, or a session that no longer exists, does nothing (a
//     session teardown is restore territory; removing records would break it);
//   - a record is reaped only when its window is absent from two listings
//     Settle apart and from a third taken under the record's own lock, which
//     is what keeps a recycled @N ID's new record safe;
//   - the claim is atomic, so each window's handlers fire once.
func Run(d Deps) []string {
	if d.RuntimeDir == "" {
		return nil
	}
	unlock, ok := tryLock(filepath.Join(d.RuntimeDir, LockName))
	if !ok {
		logging.Debug("reconcile: another reconcile holds the lock")
		return nil
	}
	claimed := claimGone(d)
	unlock() // handlers can be slow (auto-improve launches a session)
	var ids []string
	for _, c := range claimed {
		Finish(c, d)
		ids = append(ids, c.WindowID)
	}
	return ids
}

func claimGone(d Deps) []Claimed {
	if !d.HasSession() {
		logging.Log("reconcile: session is gone, leaving records for restore")
		return nil
	}
	first, err := d.List()
	if err != nil {
		logging.Log("reconcile: list-windows failed, doing nothing: %v", err)
		return nil
	}
	present := ids(first)
	var candidates []string
	for id := range windowstate.All() {
		if !present[id] {
			candidates = append(candidates, id)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	if d.Settle > 0 && d.Sleep != nil {
		d.Sleep(d.Settle)
	}
	second, err := d.List()
	if err != nil {
		logging.Log("reconcile: second list-windows failed, doing nothing: %v", err)
		return nil
	}
	present = ids(second)
	var out []Claimed
	for _, id := range candidates {
		if present[id] {
			continue
		}
		if c, ok := Claim(id, func() bool { return absent(d, id) }); ok {
			out = append(out, c)
		}
	}
	return out
}

// absent re-lists and reports whether id is missing. A failed list says no.
func absent(d Deps, id string) bool {
	ws, err := d.List()
	return err == nil && !ids(ws)[id]
}

func ids(ws []tmux.WindowInfo) map[string]bool {
	m := make(map[string]bool, len(ws))
	for _, w := range ws {
		m[w.ID] = true
	}
	return m
}

// tryLock takes a non-blocking exclusive flock on path.
func tryLock(path string) (func(), bool) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		logging.Log("reconcile: opening lock: %v", err)
		return nil, false
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close() //nolint:errcheck
		return nil, false
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck
		f.Close()                                   //nolint:errcheck
	}, true
}

// ProtectedSessionLogs returns the agentctl session-log paths
// (<dir>/<session-id>.log) that back live windows, for
// logging.PruneSessionLogs, built from windowstate records alone. A session's
// log is named after its AGENTCTL_SESSION_ID (the Claude session UUID at
// launch), so both the record's SessionID and the UUID in its linked JSONL
// name are protected: the two diverge after /clear while the log keeps the
// launch-time name.
func ProtectedSessionLogs(dir string, records map[string]windowstate.WindowState) []string {
	if dir == "" {
		return nil
	}
	seen := make(map[string]bool)
	var out []string
	add := func(id string) {
		if id == "" {
			return
		}
		p := filepath.Join(dir, id+".log")
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, ws := range records {
		add(ws.SessionID)
		if ws.LogPath != "" {
			add(transcript.SessionIDFromLog(filepath.Base(ws.LogPath)))
		}
	}
	return out
}
