package agent

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/blsmth/agentctl/internal/config"
)

// GlobalCheck is the outcome of ResolvesGlobal: whether an agent name, as
// discovery resolves it, is defined only by files in the global registry.
type GlobalCheck struct {
	Global bool
	// Path is the definition file discovery picked ("" when not found).
	Path string
	// Reason says why Global is false, for an error message.
	Reason string
}

// ResolvesGlobal discovers agents over ProjectDirs(cfg, dirs...) (the same
// scan dispatch uses to resolve an agent body) and reports whether name
// resolves to the global definition: a regular file directly in the registry
// dir (~/.claude/agents, which may itself be a symlink, as each account's
// agents dir is to it), compared with os.SameFile rather than by path. A
// project-local file, a registry entry that is a symlink (what `agentctl
// sync` links project agents in as), or a global agent that extends a
// project-local base is not global.
func ResolvesGlobal(cfg *config.Config, name string, dirs ...string) GlobalCheck {
	agents, err := DiscoverAll(cfg, ProjectDirs(cfg, dirs...))
	if err != nil {
		return GlobalCheck{Reason: fmt.Sprintf("discovering agents: %v", err)}
	}
	return globalIn(agents, name)
}

// globalIn is ResolvesGlobal over an existing discovery result.
func globalIn(agents []Agent, name string) GlobalCheck {
	a, ok := findAgent(agents, name, "")
	if !ok {
		return GlobalCheck{Reason: fmt.Sprintf("no agent named %q", name)}
	}
	res := GlobalCheck{Path: a.FilePath}
	if !IsRegistryFile(a.FilePath) {
		res.Reason = fmt.Sprintf("%s is not a file in %s", a.FilePath, registryDir())
		return res
	}
	if a.Extends != "" {
		// Mirror resolveExtends: a discovered base (which a project file
		// may shadow) wins over the registry file of that name.
		if base, ok := findAgent(agents, a.Extends, a.FilePath); ok && !IsRegistryFile(base.FilePath) {
			res.Reason = fmt.Sprintf("it extends %q, defined by %s", a.Extends, base.FilePath)
			return res
		}
	}
	res.Global = true
	return res
}

// findAgent returns the first agent named name whose file is not skipPath.
// A base for extends must also not extend anything itself.
func findAgent(agents []Agent, name, skipPath string) (Agent, bool) {
	for _, a := range agents {
		if a.Name != name || a.FilePath == skipPath {
			continue
		}
		if skipPath != "" && a.Extends != "" {
			continue
		}
		return a, true
	}
	return Agent{}, false
}

// IsRegistryFile reports whether path is the same file as a regular (not
// symlinked) entry of the same name directly in the global registry dir.
func IsRegistryFile(path string) bool {
	if path == "" {
		return false
	}
	entry := filepath.Join(registryDir(), filepath.Base(path))
	lfi, err := os.Lstat(entry)
	if err != nil || !lfi.Mode().IsRegular() {
		return false
	}
	efi, err := os.Stat(entry)
	if err != nil {
		return false
	}
	pfi, err := os.Stat(path)
	if err != nil {
		return false
	}
	return os.SameFile(efi, pfi)
}
