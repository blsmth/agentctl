package serve

import (
	"net/http"
	"unicode"
	"unicode/utf8"

	"github.com/blsmth/agentctl/internal/fleet"
	"github.com/blsmth/agentctl/internal/logging"
)

const (
	// maxGroupOrderKeys caps the keys in one group-order request.
	maxGroupOrderKeys = 500
	// maxGroupKeyBytes caps one key (a project directory or session name).
	maxGroupKeyBytes = 4096
)

type groupOrderIn struct {
	Keys []string `json:"keys"`
}

type groupOrderOut struct {
	Keys []string `json:"keys"`
}

// handleGroupOrder saves the phone's project group order. keys are the
// visible groups' keys (fleet.GroupKey) in their new order; they are merged
// with the saved order exactly as the hub does, so projects that are not
// running keep their slots. The saved file is the one fleet.Load applies for
// every consumer.
func (s *Server) handleGroupOrder(_ *http.Request, in groupOrderIn, _ *auditEntry) outcome {
	if len(in.Keys) > maxGroupOrderKeys {
		return failure(http.StatusBadRequest, codeBadRequest, "too many keys")
	}
	for _, k := range in.Keys {
		if k == "" || len(k) > maxGroupKeyBytes || !utf8.ValidString(k) || hasControl(k) {
			return failure(http.StatusBadRequest, codeBadRequest, "invalid group key")
		}
	}
	groups, err := s.fleet.Fleet()
	if err != nil {
		logging.Warn("serve: loading fleet: %v", err)
		return failure(http.StatusInternalServerError, codeInternal, "loading fleet")
	}
	known := make(map[string]bool, len(groups))
	for _, g := range groups {
		known[g.Key] = true
	}
	// Only live groups are ordered; duplicates and keys of vanished groups
	// are dropped (the merge keeps a vanished group's saved slot).
	live := make([]string, 0, len(in.Keys))
	seen := make(map[string]bool, len(in.Keys))
	for _, k := range in.Keys {
		if known[k] && !seen[k] {
			seen[k] = true
			live = append(live, k)
		}
	}
	path, err := s.groupOrderPath()
	if err != nil {
		logging.Warn("serve: group order path: %v", err)
		return failure(http.StatusInternalServerError, codeInternal, "locating the saved order")
	}
	merged := fleet.MergeGroupOrder(fleet.ReadGroupOrder(path), live)
	if err := fleet.WriteGroupOrder(path, merged); err != nil {
		logging.Warn("serve: saving group order: %v", err)
		return failure(http.StatusInternalServerError, codeInternal, "saving the order")
	}
	// Push the new order to every connected client now rather than at the
	// next rediscovery tick.
	s.bus.Refresh()
	return success(groupOrderOut{Keys: merged})
}

func hasControl(s string) bool {
	for _, c := range s {
		if unicode.IsControl(c) {
			return true
		}
	}
	return false
}
