//go:build darwin

package cmd

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// processCWD resolves a process's current working directory on macOS via
// `lsof -a -p <pid> -d cwd -Fn`, which (for a single matching PID) prints a
// small handful of lines including exactly one prefixed with "n" holding the
// path. Bounded to a short timeout so a wedged lsof -- observed in the wild
// under heavy disk contention -- can't hang `agentctl health`. Returns "" on
// any failure (including timeout); callers must treat "" as "unknown", never
// guess a value.
func processCWD(pid string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "lsof", "-a", "-p", pid, "-d", "cwd", "-Fn").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n") {
			return strings.TrimPrefix(line, "n")
		}
	}
	return ""
}
