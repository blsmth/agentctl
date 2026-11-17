package sessions

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Sync scans all Claude project directories and creates or updates .meta.json
// sidecar files for each JSONL session file. After syncing, it purges metadata
// older than retentionDays (default 30 if ≤0). Progress is reported to stderr.
//
// Returns the number of files added, updated, and pruned.
func Sync(retentionDays int) (added int, updated int, pruned int, err error) {
	type root struct {
		dir     string
		entries []os.DirEntry
	}
	var roots []root
	var readErr error
	total := 0
	for _, dir := range ProjectsRoots() {
		entries, rerr := os.ReadDir(dir)
		if rerr != nil {
			if readErr == nil {
				readErr = rerr
			}
			continue
		}
		roots = append(roots, root{dir, entries})
		total += countJSONLFiles(dir, entries)
	}
	if len(roots) == 0 {
		return 0, 0, 0, fmt.Errorf("sessions: sync read claude projects: %w", readErr)
	}
	processed := 0

	for _, r := range roots {
		a, u := syncRoot(r.dir, r.entries, &processed, total)
		added += a
		updated += u
	}

	fmt.Fprintln(os.Stderr) // newline after the progress indicator

	if retentionDays <= 0 {
		retentionDays = 30
	}
	maxAge := time.Duration(retentionDays) * 24 * time.Hour
	pruned, err = Purge(maxAge)
	if err != nil {
		return added, updated, 0, fmt.Errorf("sessions: sync purge: %w", err)
	}
	return added, updated, pruned, nil
}

// syncRoot syncs the sessions under one projects dir.
func syncRoot(claudeProjectsDir string, projectDirs []os.DirEntry, processed *int, total int) (added, updated int) {
	for _, pd := range projectDirs {
		if !pd.IsDir() {
			continue
		}
		projectPath := decodeDirName(pd.Name())
		dirPath := filepath.Join(claudeProjectsDir, pd.Name())

		entries, err := os.ReadDir(dirPath)
		if err != nil {
			continue
		}

		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}

			*processed++
			fmt.Fprintf(os.Stderr, "\rSyncing sessions... %d/%d", *processed, total)

			logFile := filepath.Join(dirPath, e.Name())
			sessionID := strings.TrimSuffix(e.Name(), ".jsonl")
			// The meta is written under the same encoded project dir name.
			metaPath := filepath.Join(sessionDataDir(), pd.Name(), sessionID+".meta.json")

			jsonlInfo, err := e.Info()
			if err != nil {
				continue
			}

			if _, statErr := os.Stat(metaPath); os.IsNotExist(statErr) {
				// Meta doesn't exist — extract and write.
				if writeErr := syncOneSession(logFile, projectPath, sessionID); writeErr == nil {
					added++
				}
				continue
			}

			// Meta exists — re-extract only if the source JSONL is newer.
			metaInfo, err := os.Stat(metaPath)
			if err != nil {
				continue
			}
			if jsonlInfo.ModTime().After(metaInfo.ModTime()) {
				if writeErr := syncOneSession(logFile, projectPath, sessionID); writeErr == nil {
					updated++
				}
			}
		}
	}
	return added, updated
}

// syncOneSession extracts metadata from the given JSONL file and writes it via
// WriteMeta. The projectPath is the decoded (possibly lossy) working directory.
func syncOneSession(logFile, projectPath, sessionID string) error {
	start, end := sessionTimeRange(logFile)
	if start.IsZero() {
		info, err := os.Stat(logFile)
		if err != nil {
			return err
		}
		start = info.ModTime()
	}

	agent := sessionAgent(logFile)
	if agent == "" {
		agent = "(unknown)"
	}

	s := Session{
		ID:        sessionID,
		Agent:     agent,
		Project:   projectPath,
		StartTime: start,
		EndTime:   end,
		LogFile:   logFile,
	}

	meta, err := ExtractMeta(s)
	if err != nil {
		return err
	}
	return WriteMeta(meta)
}

// countJSONLFiles counts the total JSONL files across all project directories.
func countJSONLFiles(projectsDir string, dirs []os.DirEntry) int {
	count := 0
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(projectsDir, d.Name()))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".jsonl") {
				count++
			}
		}
	}
	return count
}

// SyncOne creates or refreshes the .meta.json sidecar for the single session
// sessionID: it locates the session's JSONL log under the Claude projects
// directory and runs ExtractMeta + WriteMeta on it. It is what the Stop and
// SessionEnd hooks run (detached) now that no monitor writes sidecars.
//
// transcriptPath, when it names an existing file, is used directly (the hook
// payload carries it), avoiding a scan of the projects directory.
func SyncOne(sessionID, transcriptPath string) error {
	if sessionID == "" || strings.ContainsAny(sessionID, `/\`) || sessionID == "." || sessionID == ".." {
		return fmt.Errorf("sessions: invalid session id %q", sessionID)
	}
	logFile := ""
	// Only trust a transcript named for this session.
	if transcriptPath != "" && filepath.Base(transcriptPath) == sessionID+".jsonl" {
		if st, err := os.Stat(transcriptPath); err == nil && st.Mode().IsRegular() {
			logFile = transcriptPath
		}
	}
	if logFile == "" {
		logFile = FindLogBySessionID(sessionID)
	}
	if logFile == "" {
		return fmt.Errorf("sessions: no log found for session %q", sessionID)
	}
	projectPath := decodeDirName(filepath.Base(filepath.Dir(logFile)))
	return syncOneSession(logFile, projectPath, sessionID)
}
