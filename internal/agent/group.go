package agent

import (
	"path/filepath"
	"sort"
	"strings"
)

// InProject reports whether a is defined in dir's own .claude/agents (not a
// registry symlink into another repo).
func InProject(a Agent, dir string) bool {
	if a.Scope != ScopeLocal || dir == "" || a.FilePath == "" {
		return false
	}
	agentsDir := filepath.Join(dir, ".claude", "agents")
	rel, err := filepath.Rel(agentsDir, a.FilePath)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// SplitByProject partitions agents for display: the agents defined in dir's
// .claude/agents sorted by name, then everything else in its given order.
// Which agent a name resolves to is untouched; only order and grouping.
func SplitByProject(agents []Agent, dir string) (project, rest []Agent) {
	for _, a := range agents {
		if InProject(a, dir) {
			project = append(project, a)
		} else {
			rest = append(rest, a)
		}
	}
	sort.SliceStable(project, func(i, j int) bool { return project[i].Name < project[j].Name })
	return project, rest
}

// ProjectLabel is the heading for dir's agent group: its base name.
func ProjectLabel(dir string) string {
	return filepath.Base(filepath.Clean(dir))
}
