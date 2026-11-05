package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/blsmth/agentctl/internal/logging"
)

// ProjectConfig holds project-level settings from .agentctl/config.json.
// It is checked into the repository and applies to all agents working in the
// project. Settings are additive — they can add restrictions but never weaken
// user-level or global restrictions.
type ProjectConfig struct {
	BlockedCommands   []string               `json:"blocked_commands,omitempty"`
	BlockedPatterns   []string               `json:"blocked_patterns,omitempty"`
	ProtectedBranches []string               `json:"protected_branches,omitempty"`
	Agents            map[string]AgentConfig `json:"agents,omitempty"`
}

// ExpandProtectedBranches converts protected_branches entries into
// blocked_patterns regex strings of the form:
//
//	git\s+push\s+.*origin\s+<branch>\b
func (pc *ProjectConfig) ExpandProtectedBranches() []string {
	if len(pc.ProtectedBranches) == 0 {
		return nil
	}
	patterns := make([]string, 0, len(pc.ProtectedBranches))
	for _, branch := range pc.ProtectedBranches {
		patterns = append(patterns, fmt.Sprintf(`git\s+push\s+.*origin\s+%s\b`, branch))
	}
	return patterns
}

// LoadProject walks up from dir looking for .agentctl/config.json.
// Returns nil (no error) if not found — project config is optional.
// Logs the resolved path via logging.Log for diagnostics.
func LoadProject(dir string) *ProjectConfig {
	current := dir
	for {
		candidate := filepath.Join(current, ".agentctl", "config.json")
		data, err := os.ReadFile(candidate)
		if err == nil {
			var cfg ProjectConfig
			if jsonErr := json.Unmarshal(data, &cfg); jsonErr != nil {
				logging.Log("LoadProject: invalid JSON at %s: %v", candidate, jsonErr)
				return nil
			}
			logging.Log("LoadProject: loaded project config from %s", candidate)
			return &cfg
		}

		parent := filepath.Dir(current)
		if parent == current {
			// Reached filesystem root without finding a config.
			break
		}
		current = parent
	}
	return nil
}
