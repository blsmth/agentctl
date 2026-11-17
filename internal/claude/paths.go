package claude

import (
	"path/filepath"
	"regexp"

	"github.com/blsmth/agentctl/internal/claudehome"
)

var slugUnsafe = regexp.MustCompile(`[^A-Za-z0-9-]`)

// LogPath returns the expected JSONL path for a Claude Code session under the
// running process's own config dir (claudehome.Current: $CLAUDE_CONFIG_DIR,
// else ~/.claude, never default_account). Launchers that know the target account use
// LogPathIn instead. See LogPathIn for the layout.
// Returns "" if the config dir cannot be determined.
func LogPath(workDir, sessionID string) string {
	return LogPathIn(claudehome.Current(), workDir, sessionID)
}

// LogPathIn returns the expected JSONL path for a Claude Code session under
// configDir. Claude derives the project directory name by replacing every character outside
// [A-Za-z0-9-] in the working directory path with "-", producing a flat name like
// "-Users-alice-src-agentctl". The JSONL file is always
// <sessionID>.jsonl inside that directory.
// Returns "" when any input is empty.
func LogPathIn(configDir, workDir, sessionID string) string {
	if configDir == "" || workDir == "" || sessionID == "" {
		return ""
	}
	return filepath.Join(configDir, "projects", ProjectSlug(workDir), sessionID+".jsonl")
}

// ProjectSlug returns the directory name Claude Code uses under
// ~/.claude/projects for a working directory. Every character outside
// [A-Za-z0-9-] (including "/", ".", "_" and spaces) becomes "-".
// Returns "" for an empty workDir.
func ProjectSlug(workDir string) string {
	if workDir == "" {
		return ""
	}
	return slugUnsafe.ReplaceAllString(workDir, "-")
}
