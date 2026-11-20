package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/logging"
)

// hubSelectionFile is the name, in the agentctl state dir, of the file the
// hub list publishes its selection to. The follow-hub HUD (`agentctl hud
// --follow-hub`) reads it each tick and retargets.
const hubSelectionFile = "hub-selection.json"

// HubSelection is the hub's selected agent window: which tmux session and
// window it is, and the runtime dir ("<project>/.agentctl") its windowstate
// records and agentctl logs live in. The zero value means "no agent selected"
// (nothing, a group header, or a shell row).
type HubSelection struct {
	Session    string `json:"session,omitempty"`
	WindowID   string `json:"window_id,omitempty"`
	RuntimeDir string `json:"runtime_dir,omitempty"`
	// Name and Title are display hints for the follower's header: the row's
	// instance name and short title as the hub list shows them.
	Name  string `json:"name,omitempty"`
	Title string `json:"title,omitempty"`
}

// valid reports whether s names a followable agent window.
func (s HubSelection) valid() bool {
	return s.Session != "" && s.WindowID != "" && s.RuntimeDir != ""
}

// sameTarget reports whether s and o name the same window in the same
// project; display hints are ignored.
func (s HubSelection) sameTarget(o HubSelection) bool {
	return s.Session == o.Session && s.WindowID == o.WindowID && s.RuntimeDir == o.RuntimeDir
}

// hubSelectionPath is the selection file's path; a seam for tests.
var hubSelectionPath = func() (string, error) {
	dir, err := config.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, hubSelectionFile), nil
}

// readHubSelection reads the selection file at path. A missing file is the
// zero selection with no error; an unreadable or corrupt one is an error
// (the follower shows its placeholder either way).
func readHubSelection(path string) (HubSelection, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return HubSelection{}, nil
		}
		return HubSelection{}, fmt.Errorf("reading hub selection: %w", err)
	}
	var s HubSelection
	if err := json.Unmarshal(data, &s); err != nil {
		return HubSelection{}, fmt.Errorf("parsing hub selection %s: %w", path, err)
	}
	return s, nil
}

// writeHubSelection writes s to path (0600, temp file + rename).
func writeHubSelection(path string, s HubSelection) error {
	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("encoding hub selection: %w", err)
	}
	return writeFileAtomic(path, append(data, '\n'))
}

// writeFileAtomic writes data to path with mode 0600 through a temp file in
// the same dir and a rename, so a reader never sees a torn file.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod %s: %w", tmp.Name(), err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("renaming to %s: %w", path, err)
	}
	return nil
}

// hubSelection returns the hub's current selection as published to the
// follow-hub HUD: the selected agent row, or the zero value when nothing, a
// header or a shell row is selected, or the row has no known runtime dir.
func (m FleetModel) hubSelection() HubSelection {
	if m.selIsHeader || m.selSession == "" || m.selWindowID == "" {
		return HubSelection{}
	}
	for _, g := range m.groups {
		if g.Session != m.selSession {
			continue
		}
		for _, r := range g.Rows {
			if r.WindowID != m.selWindowID {
				continue
			}
			return selectionForRow(g, r)
		}
	}
	return HubSelection{}
}

// selectionForRow builds the published selection for row r of group g.
func selectionForRow(g fleet.Group, r fleet.Row) HubSelection {
	if r.Shell || g.RuntimeDir == "" {
		return HubSelection{}
	}
	return HubSelection{
		Session:    g.Session,
		WindowID:   r.WindowID,
		RuntimeDir: g.RuntimeDir,
		Name:       r.Name,
		Title:      r.Recap.Title,
	}
}

// publishSelection writes the current selection to the selection file when
// it differs from the last one written. It runs only while the activity pane
// (the follower) is open, and does nothing until the list has loaded, so a
// hot-reloaded hub does not blank the follower before its pending focus is
// re-applied. Opening the pane forces a fresh write (selPublished is reset).
// A failed write is logged once per distinct selection and retried on the
// next change.
func (m *FleetModel) publishSelection() {
	if !m.loaded || !m.activity.open() {
		return
	}
	sel := m.hubSelection()
	if m.selPublished && sel == m.lastSelection {
		return
	}
	m.lastSelection, m.selPublished = sel, true
	path, err := hubSelectionPath()
	if err == nil {
		err = writeHubSelection(path, sel)
	}
	if err != nil {
		logging.Log("hub: publishing selection: %v", err)
	}
}
