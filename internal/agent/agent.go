package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/paths"
)

// AgentScope indicates where an agent was discovered.
type AgentScope int

const (
	// ScopeGlobal is an agent defined directly in ~/.claude/agents/.
	ScopeGlobal AgentScope = iota
	// ScopeLocal is a project-local agent symlinked into the registry.
	ScopeLocal
	// ScopeTemplate is a built-in embedded template (not yet installed).
	ScopeTemplate
)

// String returns a human-readable label for the scope.
func (s AgentScope) String() string {
	switch s {
	case ScopeGlobal:
		return "global"
	case ScopeLocal:
		return "local"
	case ScopeTemplate:
		return "template"
	default:
		return "unknown"
	}
}

// Agent represents a Claude Code agent discovered from the registry.
type Agent struct {
	Name         string
	Description  string
	Dir          string // Working directory, empty means "any" (portable agent)
	Model        string // Preferred model, empty means "inherit"
	FilePath     string // Resolved absolute path to the .md file
	Extends      string // Base agent name, empty if standalone
	ComposedBody string // Merged base+local body, empty if standalone
	// BaseName, BasePath and BaseBody describe the base an Extends agent
	// was composed from (all "" when standalone or unresolved). A launch
	// with --agent loads only the agent's own file, so dispatch injects
	// BaseBody itself.
	BaseName string
	BasePath string
	BaseBody string
	Tools    string // Tools field from frontmatter
	Harness  string // harness field from frontmatter ("" = config or default; see internal/harness)
	// Role is the frontmatter role: RoleOrchestrator, RoleWorker,
	// RoleReporter, or "" (a worker). Unknown values are logged and dropped at discovery.
	Role        string
	Scope       AgentScope
	SymTarget   string // Raw symlink target path (if local)
	ProjectName string // Extracted project name from symlink target
	// Shadows is the FilePath of the global agent this project-local agent
	// replaced in discovery (same name), or "" when it shadows nothing.
	Shadows string
}

// IsPortable returns true if the agent has no fixed working directory.
func (a Agent) IsPortable() bool {
	return a.Dir == ""
}

// Frontmatter holds the parsed YAML frontmatter from an agent .md file.
type Frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Tools       string `yaml:"tools"`
	Model       string `yaml:"model"`
	Extends     string `yaml:"extends"`
	// Harness names the agent program to run this agent on ("claude", "pi").
	// The agents.<name>.harness config key wins over it.
	Harness string `yaml:"harness"`
	// Role is "orchestrator" for an agent whose job is coordinating other
	// agents (it never gets worker finishing steps), "reporter" for an agent
	// that never changes code (it reports instead of committing and opening a
	// PR), "worker" or empty otherwise. See NormalizeRole.
	Role string `yaml:"role"`
}

// Frontmatter role values.
const (
	RoleOrchestrator = "orchestrator"
	RoleWorker       = "worker"
	RoleReporter     = "reporter"
)

// NormalizeRole returns role when it is a known value (RoleOrchestrator,
// RoleWorker, RoleReporter or ""), else "" after logging a warning naming
// source.
func NormalizeRole(role, source string) string {
	switch role {
	case "", RoleOrchestrator, RoleWorker, RoleReporter:
		return role
	}
	logging.Warn("agent: %s: ignoring unknown role %q (want %s, %s or %s)", source, role, RoleOrchestrator, RoleWorker, RoleReporter)
	return ""
}

// frontmatter is an alias kept for internal use.
type frontmatter = Frontmatter

// RegistryDir overrides the directory containing global agent .md files.
// Empty (the default) means ~/.claude/agents for the current HOME, resolved
// on each use rather than at package init, so a HOME set before use (by
// TestMain, say) is honored. Tests point it at a temp dir.
var RegistryDir string

// SharedDir overrides the directory containing files referenced by
// @shared/foo.md include directives in agent bodies. Empty (the default)
// means ~/.claude/shared, a sibling of the registry dir, NOT a "shared"
// subdirectory of it -- ExpandSharedRefs joins against this. Resolved on each
// use like RegistryDir.
var SharedDir string

// registryDir returns RegistryDir, or ~/.claude/agents when it is unset.
func registryDir() string {
	if RegistryDir != "" {
		return RegistryDir
	}
	return paths.ExpandHome("~/.claude/agents")
}

// sharedDir returns SharedDir, or ~/.claude/shared when it is unset.
func sharedDir() string {
	if SharedDir != "" {
		return SharedDir
	}
	return paths.ExpandHome("~/.claude/shared")
}

// SharedDirIn returns the shared-include directory (~/.claude/shared) for the
// given home. SharedDir is the same path for the current user.
func SharedDirIn(home string) string {
	return filepath.Join(home, ".claude", "shared")
}

// ProjectDirs returns the deduplicated list of project root directories to
// pass to DiscoverAll: first each non-empty dir in priority order, then the
// "~/"-expanded Dir of every agent configured in cfg.
func ProjectDirs(cfg *config.Config, first ...string) []string {
	seen := make(map[string]bool)
	var dirs []string
	add := func(d string) {
		if d == "" || seen[d] {
			return
		}
		seen[d] = true
		dirs = append(dirs, d)
	}
	for _, d := range first {
		add(d)
	}
	for _, name := range cfg.AgentNames() {
		add(paths.ExpandHome(cfg.ForAgent(name).Dir))
	}
	return dirs
}

// DiscoverFromCwd is DiscoverAll over ProjectDirs rooted at the current
// working directory: the scan used by every CLI entry point that has no
// more specific project root to offer.
func DiscoverFromCwd(cfg *config.Config) ([]Agent, error) {
	cwd, _ := os.Getwd()
	return DiscoverAll(cfg, ProjectDirs(cfg, cwd))
}

// DiscoverAll scans both the global registry (~/.claude/agents/) and
// project-local .claude/agents/ directories for agent definitions. projectDirs
// is a list of project root directories to check for .claude/agents/. Agents
// found in project directories are scoped as ScopeLocal and shadow global
// agents with the same name.
func DiscoverAll(cfg *config.Config, projectDirs []string) ([]Agent, error) {
	// Phase 1: scan global registry.
	globalAgents, err := scanDir(registryDir(), ScopeGlobal, cfg)
	if err != nil {
		return nil, fmt.Errorf("reading registry dir: %w", err)
	}

	// Phase 2: scan project-local dirs.
	seen := make(map[string]bool)  // dedup project dirs
	byName := make(map[string]int) // agent name → index in final slice
	var agents []Agent

	// Start with global agents indexed by name.
	for _, a := range globalAgents {
		byName[a.Name] = len(agents)
		agents = append(agents, a)
	}

	for _, projDir := range projectDirs {
		absDir, err := filepath.Abs(projDir)
		if err != nil {
			continue
		}
		if seen[absDir] {
			continue
		}
		seen[absDir] = true

		agentsDir := filepath.Join(absDir, ".claude", "agents")
		localAgents, err := scanDir(agentsDir, ScopeLocal, cfg)
		if err != nil {
			continue // dir doesn't exist or unreadable — skip silently
		}

		projectName := filepath.Base(absDir)
		for _, la := range localAgents {
			la.ProjectName = projectName
			if idx, exists := byName[la.Name]; exists {
				existing := agents[idx]
				if existing.Scope == ScopeGlobal {
					logging.Debug("agent.DiscoverAll: project agent %q (%s) shadows global agent (%s)", la.Name, la.FilePath, existing.FilePath)
					la.Shadows = existing.FilePath
					agents[idx] = la
				} else {
					// Same name in two project dirs — first one wins.
					logging.Debug("agent.DiscoverAll: duplicate project agent %q from %s (already from %s), skipping", la.Name, projectName, existing.ProjectName)
				}
			} else {
				byName[la.Name] = len(agents)
				agents = append(agents, la)
			}
		}
	}

	// Resolution pass: resolve extends references.
	agents = resolveExtends(agents, cfg)

	return agents, nil
}

// scanDir reads agent .md files from a single directory, returning them with
// the given scope. Returns an empty slice (not an error) if the directory does
// not exist.
func scanDir(dir string, scope AgentScope, cfg *config.Config) ([]Agent, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var agents []Agent
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".md") || name == "agent-registry.md" {
			continue
		}

		path := filepath.Join(dir, name)

		// For global dir, detect symlinks for scope classification.
		agentScope := scope
		var symTarget, projectName string
		if scope == ScopeGlobal {
			fi, lstatErr := os.Lstat(path)
			if lstatErr == nil && fi.Mode()&os.ModeSymlink != 0 {
				agentScope = ScopeLocal
				target, readErr := os.Readlink(path)
				if readErr == nil {
					symTarget = target
					projectName = ExtractProjectName(target)
				}
			}
		}

		// Resolve symlinks to get the real file path for --agent-file.
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			continue // skip broken symlinks
		}

		// For project-local agents, use the absolute path directly.
		if scope == ScopeLocal {
			abs, absErr := filepath.Abs(path)
			if absErr == nil {
				resolved = abs
			}
		}

		data, err := os.ReadFile(resolved)
		if err != nil {
			continue
		}

		fm, err := parseFrontmatter(data)
		if err != nil {
			continue
		}

		agentName := strings.TrimSuffix(name, ".md")
		if fm.Name != "" {
			agentName = fm.Name
		}
		if !ValidName(agentName) {
			logging.Warn("agent: skipping %s: invalid agent name %q (allowed: [A-Za-z0-9._-], max %d, no leading '.' or '-')", path, agentName, MaxNameLen)
			continue
		}
		if fm.Extends != "" && !ValidName(fm.Extends) {
			logging.Warn("agent: %s: ignoring invalid extends %q", path, fm.Extends)
			fm.Extends = ""
		}

		ac := cfg.ForAgent(agentName)
		agentDir := ""
		if ac.Dir != "" {
			agentDir = paths.ExpandHome(ac.Dir)
		}

		agents = append(agents, Agent{
			Name:        agentName,
			Description: fm.Description,
			Dir:         agentDir,
			Model:       ac.Model,
			FilePath:    resolved,
			Extends:     fm.Extends,
			Tools:       fm.Tools,
			Harness:     fm.Harness,
			Role:        NormalizeRole(fm.Role, path),
			Scope:       agentScope,
			SymTarget:   symTarget,
			ProjectName: projectName,
		})
	}

	return agents, nil
}

// resolveExtends performs the extends resolution pass on a collected slice of agents.
func resolveExtends(agents []Agent, cfg *config.Config) []Agent {
	for i, ag := range agents {
		if ag.Extends == "" {
			continue
		}

		// Find the base agent. We require the base to have a different name
		// (different FilePath) and to not itself have an extends field (true base).
		var base *Agent
		for j := range agents {
			b := &agents[j]
			if b.Name == ag.Extends && b.FilePath != ag.FilePath && b.Extends == "" {
				base = b
				break
			}
		}

		if base == nil {
			// The base agent wasn't found among discovered agents. Check if the
			// registry path for the base name is a regular file (not a symlink).
			basePath := filepath.Join(registryDir(), ag.Extends+".md")
			fi, err := os.Lstat(basePath)
			if err != nil || fi.Mode()&os.ModeSymlink != 0 {
				logging.Log("agent.Discover: extends=%q for agent %q not resolvable (symlink or missing)", ag.Extends, ag.Name)
				continue
			}
			// It's a regular file but wasn't in the agents list — parse it directly.
			data, err := os.ReadFile(basePath)
			if err != nil {
				logging.Log("agent.Discover: could not read base agent file %q: %v", basePath, err)
				continue
			}
			fm, err := parseFrontmatter(data)
			if err != nil {
				logging.Log("agent.Discover: could not parse base agent frontmatter %q: %v", basePath, err)
				continue
			}
			baseName := strings.TrimSuffix(filepath.Base(basePath), ".md")
			if fm.Name != "" {
				baseName = fm.Name
			}
			ac := cfg.ForAgent(baseName)
			dir := ""
			if ac.Dir != "" {
				dir = paths.ExpandHome(ac.Dir)
			}
			synthetic := Agent{
				Name:        baseName,
				Description: fm.Description,
				Dir:         dir,
				Model:       ac.Model,
				FilePath:    basePath,
				Harness:     fm.Harness,
				Role:        NormalizeRole(fm.Role, basePath),
			}
			base = &synthetic
		}

		localBody, err := ReadBody(ag.FilePath)
		if err != nil {
			logging.Log("agent.Discover: could not read local body for %q: %v", ag.Name, err)
			continue
		}
		baseBody, err := ReadBody(base.FilePath)
		if err != nil {
			logging.Log("agent.Discover: could not read base body for %q: %v", base.Name, err)
			continue
		}

		composed := Compose(ag, localBody, *base, baseBody)
		logging.Log("agent.Discover: composed agent %q from base %q (%d bytes)", ag.Name, base.Name, len(composed.ComposedBody))
		agents[i] = composed
	}

	return agents
}

// ParseFrontmatter extracts YAML frontmatter from a markdown file.
// It is the exported form of the internal parseFrontmatter function.
func ParseFrontmatter(data []byte) (Frontmatter, error) {
	return parseFrontmatter(data)
}

// parseFrontmatter extracts YAML frontmatter from a markdown file.
func parseFrontmatter(data []byte) (frontmatter, error) {
	content := string(data)
	if !strings.HasPrefix(content, "---\n") {
		return frontmatter{}, fmt.Errorf("no frontmatter found")
	}
	end := strings.Index(content[4:], "\n---")
	if end == -1 {
		return frontmatter{}, fmt.Errorf("no frontmatter closing delimiter")
	}

	var fm frontmatter
	if err := yaml.Unmarshal([]byte(content[4:4+end]), &fm); err != nil {
		return frontmatter{}, err
	}
	return fm, nil
}

// ExtractProjectName extracts the project/directory name from a symlink target.
// e.g. ~/src/other-project/.claude/agents/worker.md -> "other-project"
func ExtractProjectName(target string) string {
	parts := strings.Split(filepath.ToSlash(target), "/")
	for i, part := range parts {
		if part == ".claude" && i > 0 {
			return parts[i-1]
		}
	}
	dir := filepath.Dir(target)
	return filepath.Base(dir)
}
