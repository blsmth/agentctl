package hooks

import (
	"os"

	"github.com/blsmth/agentctl/internal/tmux"
)

// tmuxClient is one row of `tmux list-clients` across all sessions. The
// listing and parsing live in internal/tmux, shared with the statusline.
type tmuxClient tmux.Client

// listClients lists every attached tmux client, in every session. Package-level
// var so tests do not shell out, like clientDimensions.
var listClients = tmuxListClients

func tmuxListClients() []tmuxClient { return fromTmuxClients(tmux.ListClients()) }

// parseClients parses list-clients output, skipping malformed lines.
func parseClients(out string) []tmuxClient { return fromTmuxClients(tmux.ParseClients(out)) }

func fromTmuxClients(cs []tmux.Client) []tmuxClient {
	var res []tmuxClient
	for _, c := range cs {
		res = append(res, tmuxClient(c))
	}
	return res
}

// popupTarget is the client an approval popup is opened, sized and closed on.
type popupTarget struct {
	// Client is the tmux client name for `-c`; empty means no -c (tmux's
	// default client).
	Client string
	// Viewing reports whether Client is currently showing the issuing window.
	Viewing bool
	// WindowID is the issuing window, "" if unresolved.
	WindowID string
}

// selectPopupClient picks the client for an approval popup. Rule 1: clients
// whose current window is windowID (a link-window copy shares the ID, so a
// window viewed through the hub matches), highest activity wins. Rule 2:
// otherwise the most recently active client overall. Rule 3: no clients, empty
// target. Control-mode clients cannot display a popup and are ignored, and
// so are the phone's terminal clients (agentctl serve's viewer sessions): a
// popup there is invisible on the laptop, and a phone watching must not
// count as a human looking at the window.
func selectPopupClient(clients []tmuxClient, windowID string) popupTarget {
	t := popupTarget{WindowID: windowID}
	var best *tmuxClient
	for i := range clients {
		c := &clients[i]
		if !tmux.Client(*c).IsCandidateViewer() {
			continue
		}
		viewing := windowID != "" && c.WindowID == windowID
		switch {
		case best == nil,
			viewing && !t.Viewing,
			viewing == t.Viewing && c.Activity > best.Activity:
			best, t.Viewing = c, viewing
		}
	}
	if best != nil {
		t.Client = best.Name
	}
	return t
}

// resolvePopupTarget picks the popup client for the calling window.
func resolvePopupTarget() popupTarget {
	return selectPopupClient(listClients(), resolveCallingWindowID())
}

// clientArgs returns the `-c <client>` arguments, or none when unset.
func (t popupTarget) clientArgs() []string {
	if t.Client == "" {
		return nil
	}
	return []string{"-c", t.Client}
}

// label names the asking agent for a popup title. When the popup landed on a
// client that is not viewing the issuing window, the instance name
// ("code-reviewer:3") is used so the human can tell who is asking; the raw
// window ID is never shown.
func (t popupTarget) label(agentName string) string {
	if t.Viewing || t.WindowID == "" {
		return agentName
	}
	if inst := os.Getenv("AGENTCTL_INSTANCE_NAME"); inst != "" {
		return inst
	}
	if agentName == "" {
		return "agent"
	}
	return agentName
}
