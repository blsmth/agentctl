package claude

import (
	"os"
	"path/filepath"
	"regexp"
)

var slugUnsafe = regexp.MustCompile(`[^A-Za-z0-9-]`)

// LogPath returns the expected JSONL path for a Claude Code session.
// Claude derives the project directory name by replacing every character outside
// [A-Za-z0-9-] in the working directory path with "-", producing a flat name like
// "-Users-alice-src-agentctl". The JSONL file is always
// <sessionID>.jsonl inside that directory.
// Returns "" if the home directory cannot be determined.
func LogPath(workDir, sessionID string) string {
	home, err := os.UserHomeDir()
	if err != nil || workDir == "" || sessionID == "" {
		return ""
	}
	return filepath.Join(home, ".claude", "projects", ProjectSlug(workDir), sessionID+".jsonl")
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
