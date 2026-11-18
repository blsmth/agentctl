package tmux

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Client is one row of `tmux list-clients` across all sessions.
type Client struct {
	Name     string
	WindowID string // window the client is currently showing
	Activity int64  // client_activity, unix seconds
	Flags    string
	Session  string // client_session
}

// ListClientsFormat is the -F format ParseClients expects.
const ListClientsFormat = "#{client_name}\t#{window_id}\t#{client_activity}\t#{client_flags}\t#{client_session}"

// ListClients lists every attached tmux client, in every session. A failure
// (no server, no clients) yields nil.
func ListClients() []Client {
	out, err := exec.Command("tmux", "list-clients", "-F", ListClientsFormat).Output()
	if err != nil {
		return nil
	}
	return ParseClients(string(out))
}

// ListClientsTimeout is ListClients bounded by timeout, reporting failure so a
// caller on a hot path can fall back instead of treating it as "no clients".
func ListClientsTimeout(timeout time.Duration) ([]Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tmux", "list-clients", "-F", ListClientsFormat).Output()
	if err != nil {
		return nil, err
	}
	return ParseClients(string(out)), nil
}

// ParseClients parses list-clients output in ListClientsFormat, skipping
// malformed lines.
func ParseClients(out string) []Client {
	var clients []Client
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) < 3 || f[0] == "" {
			continue
		}
		act, _ := strconv.ParseInt(strings.TrimSpace(f[2]), 10, 64)
		c := Client{Name: f[0], WindowID: f[1], Activity: act}
		if len(f) > 3 {
			c.Flags = f[3]
		}
		if len(f) > 4 {
			c.Session = f[4]
		}
		clients = append(clients, c)
	}
	return clients
}

// IsCandidateViewer reports whether c can be a human looking at a window:
// control-mode clients cannot display anything and the phone's own terminal
// clients (agentctl serve viewer sessions) are not the laptop.
func (c Client) IsCandidateViewer() bool {
	return !strings.Contains(c.Flags, "control-mode") && !IsMobileViewerSession(c.Session)
}

// LaptopViewing reports whether any candidate client currently shows windowID
// (a link-window copy, such as the hub's nested view, shares the ID).
func LaptopViewing(clients []Client, windowID string) bool {
	if windowID == "" {
		return false
	}
	for _, c := range clients {
		if c.IsCandidateViewer() && c.WindowID == windowID {
			return true
		}
	}
	return false
}
