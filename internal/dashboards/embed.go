// Package dashboards embeds opinionated Grafana dashboard JSON files and
// exposes helpers to list and retrieve them at runtime.
package dashboards

import (
	"embed"
	"fmt"
	"io/fs"
)

//go:embed *.json
var FS embed.FS

// List returns the filenames of all embedded dashboards, sorted
// alphabetically (the order embed.FS uses).
func List() []string {
	entries, err := fs.ReadDir(FS, ".")
	if err != nil {
		// embed.FS never errors on a valid embedded directory.
		panic(fmt.Sprintf("dashboards: failed to read embedded FS: %v", err))
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

// Get returns the raw JSON bytes for the named dashboard file.
// name must be a basename like "fleet-overview.json".
// It returns an error if no such dashboard is embedded.
func Get(name string) ([]byte, error) {
	data, err := FS.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("dashboard %q not found: %w", name, err)
	}
	return data, nil
}
