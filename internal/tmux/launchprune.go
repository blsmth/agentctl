package tmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// LaunchFileMaxAge is how long prompt and script files stay in the launch dir
// before housekeeping removes them. Restore rebuilds its command and never
// reads a system-prompt file, so an old one is only a record of what a window
// was launched with.
const LaunchFileMaxAge = 7 * 24 * time.Hour

// launchPrunePatterns are the files LaunchAgent and dispatch write into the
// launch dir that nothing deletes: system-prompt files (<mode>-sys-*.txt,
// dispatch.writeTempPrompt) and launch scripts (launch-*.sh). Anything else
// there (launch logs, harness files such as pi's bridge extension) is left alone.
var launchPrunePatterns = []string{"*-sys-*.txt", "launch-*.sh"}

// PruneLaunchDir deletes regular files in dir matching launchPrunePatterns
// whose mtime is at least maxAge before now and whose path is not in
// processArgs (the argument lines of running processes, so a file a live
// window was launched with is kept). A missing dir is not an error. It
// returns how many files it removed.
func PruneLaunchDir(dir string, now time.Time, maxAge time.Duration, processArgs func() (string, error)) (int, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("reading launch dir: %w", err)
	}
	cutoff := now.Add(-maxAge)
	var old []string
	for _, e := range entries {
		if !e.Type().IsRegular() || !matchesLaunchPrune(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		old = append(old, filepath.Join(dir, e.Name()))
	}
	if len(old) == 0 {
		return 0, nil
	}
	args := ""
	if processArgs != nil {
		if args, err = processArgs(); err != nil {
			// Cannot tell what is in use: delete nothing.
			return 0, fmt.Errorf("listing processes: %w", err)
		}
	}
	removed := 0
	for _, p := range old {
		if args != "" && strings.Contains(args, p) {
			continue
		}
		if err := os.Remove(p); err == nil {
			removed++
		}
	}
	return removed, nil
}

func matchesLaunchPrune(name string) bool {
	for _, pat := range launchPrunePatterns {
		if ok, _ := filepath.Match(pat, name); ok {
			return true
		}
	}
	return false
}

// RunningProcessArgs returns the argument lines of every running process
// (`ps -axo args=`), bounded by a short timeout.
func RunningProcessArgs() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-axo", "args=").Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}
