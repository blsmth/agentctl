package artifacts

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// orderFile is the store-level file holding the saved per-project order.
const orderFile = "order.json"

// orderDoc is order.json: project key -> artifact ids in display order.
type orderDoc struct {
	Order map[string][]string `json:"order"`
}

// OrderKey is the key an artifact's project is saved under: its project
// directory, else its display name ("" for an artifact with no project).
func OrderKey(m Meta) string {
	if m.ProjectDir != "" {
		return m.ProjectDir
	}
	return m.Project
}

// readOrder loads the saved order; a missing or corrupt file is no order.
func readOrder(root string) map[string][]string {
	data, err := os.ReadFile(filepath.Join(root, orderFile))
	if err != nil {
		return nil
	}
	var d orderDoc
	if json.Unmarshal(data, &d) != nil {
		return nil
	}
	return d.Order
}

// writeOrder replaces order.json atomically (0600).
func writeOrder(root string, order map[string][]string) error {
	data, err := json.MarshalIndent(orderDoc{Order: order}, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(root, ".order-*")
	if err != nil {
		return fmt.Errorf("writing artifact order: %w", err)
	}
	name := f.Name()
	_, werr := f.Write(append(data, '\n'))
	cerr := f.Close()
	if err := errors.Join(werr, cerr, os.Chmod(name, 0o600)); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("writing artifact order: %w", err)
	}
	if err := os.Rename(name, filepath.Join(root, orderFile)); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("writing artifact order: %w", err)
	}
	return nil
}

// ApplyOrder rearranges list (newest first) by the saved per-project order.
// Within a project, artifacts missing from its saved list (new ones) come
// first in their incoming order, then the saved order; saved ids that are not
// in list are dropped. Each project keeps the slots it already occupied in
// list, so the interleaving of projects is unchanged.
func ApplyOrder(list []Meta, order map[string][]string) []Meta {
	if len(order) == 0 {
		return list
	}
	slots := map[string][]int{}
	for i, m := range list {
		k := OrderKey(m)
		slots[k] = append(slots[k], i)
	}
	out := make([]Meta, len(list))
	for k, idx := range slots {
		saved := order[k]
		if len(saved) == 0 {
			for _, i := range idx {
				out[i] = list[i]
			}
			continue
		}
		byID := make(map[string]Meta, len(idx))
		for _, i := range idx {
			byID[list[i].ID] = list[i]
		}
		inSaved := make(map[string]bool, len(saved))
		for _, id := range saved {
			inSaved[id] = true
		}
		merged := make([]Meta, 0, len(idx))
		for _, i := range idx {
			if !inSaved[list[i].ID] {
				merged = append(merged, list[i])
			}
		}
		for _, id := range saved {
			if m, ok := byID[id]; ok {
				merged = append(merged, m)
				delete(byID, id) // a duplicate id in the file counts once
			}
		}
		for n, i := range idx {
			out[i] = merged[n]
		}
	}
	return out
}

// SetOrder saves the order of one project's artifacts (key as OrderKey). ids
// that do not exist or belong to another project are dropped, duplicates
// count once, and an empty result forgets the project's order. The saved
// lists of other projects are pruned of artifacts that no longer exist. It
// returns the list as stored.
func (s *Store) SetOrder(key string, ids []string) ([]string, error) {
	root, err := s.root(true)
	if err != nil {
		return nil, err
	}
	unlock, err := s.lock(root)
	if err != nil {
		return nil, err
	}
	defer unlock()
	all, err := listAll(root)
	if err != nil {
		return nil, err
	}
	keyOf := make(map[string]string, len(all))
	for _, m := range all {
		keyOf[m.ID] = OrderKey(m)
	}
	order := map[string][]string{}
	for k, saved := range readOrder(root) {
		if k == key {
			continue
		}
		var keep []string
		for _, id := range saved {
			if keyOf[id] == k {
				keep = append(keep, id)
			}
		}
		if len(keep) > 0 {
			order[k] = keep
		}
	}
	stored := []string{}
	seen := map[string]bool{}
	for _, id := range ids {
		if k, ok := keyOf[id]; ok && k == key && !seen[id] {
			seen[id] = true
			stored = append(stored, id)
		}
	}
	if len(stored) > 0 {
		order[key] = stored
	}
	if err := writeOrder(root, order); err != nil {
		return nil, err
	}
	touchChanged(root)
	return stored, nil
}

// orderOf reads the saved order of the store at root (nil when none).
func orderOf(root string) map[string][]string {
	if _, err := os.Stat(filepath.Join(root, orderFile)); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return readOrder(root)
}
