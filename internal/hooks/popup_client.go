package hooks

import (
	"os/exec"
	"strconv"
	"strings"
)

// tmuxClient is one row of `tmux list-clients` across all sessions.
type tmuxClient struct {
	Name     string
	WindowID string // window the client is currently showing
	Activity int64  // client_activity, unix seconds
	Flags    string
}

// listClients lists every attached tmux client, in every session. Package-level
// var so tests do not shell out, like clientDimensions.
var listClients = tmuxListClients

const listClientsFormat = "#{client_name}\t#{window_id}\t#{client_activity}\t#{client_flags}"

func tmuxListClients() []tmuxClient {
	out, err := exec.Command("tmux", "list-clients", "-F", listClientsFormat).Output()
	if err != nil {
		return nil
	}
	return parseClients(string(out))
}

// parseClients parses list-clients output in listClientsFormat, skipping
// malformed lines.
func parseClients(out string) []tmuxClient {
	var clients []tmuxClient
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) < 3 || f[0] == "" {
			continue
		}
		act, _ := strconv.ParseInt(strings.TrimSpace(f[2]), 10, 64)
		c := tmuxClient{Name: f[0], WindowID: f[1], Activity: act}
		if len(f) > 3 {
			c.Flags = f[3]
		}
		clients = append(clients, c)
	}
	return clients
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
// target. Control-mode clients cannot display a popup and are ignored.
func selectPopupClient(clients []tmuxClient, windowID string) popupTarget {
	t := popupTarget{WindowID: windowID}
	var best *tmuxClient
	for i := range clients {
		c := &clients[i]
		if strings.Contains(c.Flags, "control-mode") {
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
// client that is not viewing the issuing window, the window ID is added so the
// human can tell who is asking.
func (t popupTarget) label(agentName string) string {
	if t.Viewing || t.WindowID == "" {
		return agentName
	}
	if agentName == "" {
		agentName = "agent"
	}
	return agentName + " (" + t.WindowID + ")"
}
