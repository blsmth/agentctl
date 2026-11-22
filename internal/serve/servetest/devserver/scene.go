package main

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/blsmth/agentctl/internal/demo"
	"github.com/blsmth/agentctl/internal/demo/phonefix"
	"github.com/blsmth/agentctl/internal/serve/servetest"
)

// scene is a loaded demo scene as the dev server serves it.
type scene struct {
	name   string
	groups []servetest.Group
	refs   []phonefix.Ref
	// approvals maps a blocked window's scene key to its open approval ID.
	approvals map[string]string
}

// loadScene resolves name the way `just demo-shot` does (a bare name is
// looked up in scenesDir, anything with a slash is a path), loads it and
// converts its fleet.
func loadScene(scenesDir, name, fixtures string) (*scene, error) {
	path, err := demo.ResolveScene(scenesDir, name)
	if err != nil {
		return nil, err
	}
	s, err := demo.LoadScene(path)
	if err != nil {
		return nil, err
	}
	groups, refs, err := phonefix.Convert(s, fixtures)
	if err != nil {
		return nil, fmt.Errorf("scene %s: %w", s.Name, err)
	}
	return &scene{name: s.Name, groups: groups, refs: refs, approvals: map[string]string{}}, nil
}

// openApprovals opens a remote approval for every blocked window, as its
// PreToolUse hook would have, so the phone's approval screens have content.
func (sc *scene) openApprovals(h *servetest.Harness) error {
	for _, r := range sc.refs {
		if !r.Blocked {
			continue
		}
		id, err := h.OpenApproval(servetest.ApprovalSpec{
			AskingAgent: r.Agent, Session: r.Session, WindowID: r.WindowID, Command: r.Command,
		})
		if err != nil {
			return fmt.Errorf("opening approval for %s: %w", r.Key, err)
		}
		sc.approvals[r.Key] = id
	}
	return nil
}

// serve answers GET /__scene: the scene's windows and open approvals.
func (sc *scene) serve(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"name": sc.name, "windows": sc.refs, "approvals": sc.approvals})
}
