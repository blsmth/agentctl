package servetest

import "path/filepath"

// OpenedWindow is the agent window a fake open gives a new project group.
const OpenedWindow = "@950"

// openProject records an open (Kind "open", Dir the project) instead of
// running `agentctl open --detach`. A project whose session name
// ("agentctl-<base>") is already in the fixture is left alone, as a running
// session is; otherwise a group with one idle agent is added, as starting
// the session would.
func (w *world) openProject(dir string) (string, error) {
	base := filepath.Base(dir)
	session := "agentctl-" + base
	w.record(Action{Kind: "open", Session: session, Dir: dir})
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, g := range w.groups {
		if g.Session == session {
			return session + "\n", nil
		}
	}
	w.groups = append(w.groups, Group{
		Session: session, Project: base, Dir: dir,
		Windows: []Window{{ID: OpenedWindow, Index: 1, Agent: "orchestrator", State: "idle", Activity: "session started"}},
	})
	return session + "\n", nil
}
