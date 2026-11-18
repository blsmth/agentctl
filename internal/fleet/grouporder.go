package fleet

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/logging"
)

// groupOrderFile is the saved group order's name in the agentctl state dir.
const groupOrderFile = "group-order.json"

// groupOrderState is the on-disk shape of the saved group order.
type groupOrderState struct {
	Groups []string `json:"groups"`
}

// GroupKey is the stable identity of a group in the saved order: the project
// directory (two repos can share a basename, so the display name will not do),
// falling back to the session name when the directory is unknown.
func GroupKey(g Group) string {
	if g.ProjectDir != "" {
		return g.ProjectDir
	}
	return g.Session
}

// GroupOrderPath is the saved group order's path in the state dir.
func GroupOrderPath() (string, error) {
	dir, err := config.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, groupOrderFile), nil
}

// LoadGroupOrder reads the saved group order from the state dir. A missing,
// unreadable or corrupt file yields nil (alphabetical order): never an error.
func LoadGroupOrder() []string {
	path, err := GroupOrderPath()
	if err != nil {
		return nil
	}
	return ReadGroupOrder(path)
}

// ReadGroupOrder reads the saved order at path; nil when the file is missing,
// unreadable or corrupt.
func ReadGroupOrder(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			logging.Log("fleet: reading %s: %v", path, err)
		}
		return nil
	}
	var s groupOrderState
	if err := json.Unmarshal(data, &s); err != nil {
		logging.Log("fleet: ignoring corrupt %s", path)
		return nil
	}
	return s.Groups
}

// WriteGroupOrder writes the saved order to path (0600, via a temp file and
// rename so a crash never leaves a torn file).
func WriteGroupOrder(path string, keys []string) error {
	data, err := json.Marshal(groupOrderState{Groups: keys})
	if err != nil {
		return fmt.Errorf("encoding group order: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+groupOrderFile+".*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod %s: %w", tmp.Name(), err)
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
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

// OrderGroupsBy orders groups by the saved key order first; groups not in it
// follow alphabetically (see OrderGroups). Saved keys with no live group are
// ignored. Returns a new slice; the input is left untouched.
func OrderGroupsBy(groups []Group, saved []string) []Group {
	alpha := OrderGroups(groups)
	if len(saved) == 0 {
		return alpha
	}
	pos := make(map[string]int, len(saved))
	for i, k := range saved {
		if _, dup := pos[k]; !dup {
			pos[k] = i
		}
	}
	out := make([]Group, 0, len(alpha))
	var rest []Group
	for _, g := range alpha {
		if _, ok := pos[GroupKey(g)]; ok {
			out = append(out, g)
		} else {
			rest = append(rest, g)
		}
	}
	slices.SortStableFunc(out, func(a, b Group) int { return pos[GroupKey(a)] - pos[GroupKey(b)] })
	return append(out, rest...)
}

// MergeGroupOrder builds the order to save when the live groups are now in
// the order live: saved keys with no live group (a project that is away) keep
// their slot, right after the key that preceded them in saved.
func MergeGroupOrder(saved, live []string) []string {
	liveSet := make(map[string]bool, len(live))
	for _, k := range live {
		liveSet[k] = true
	}
	out := slices.Clone(live)
	seen := make(map[string]bool, len(saved))
	prev := "" // nearest preceding key in saved that is already placed
	for _, k := range saved {
		if seen[k] {
			continue
		}
		seen[k] = true
		if liveSet[k] {
			prev = k
			continue
		}
		at := 0
		if prev != "" {
			at = slices.Index(out, prev) + 1
		}
		out = slices.Insert(out, at, k)
		prev = k
	}
	return out
}
