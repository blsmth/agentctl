//go:build linux

package cmd

import "os"

// processCWD resolves a process's current working directory on Linux by
// reading the /proc/<pid>/cwd symlink -- no subprocess needed. Returns "" on
// any failure (process gone, permission denied, etc.); callers must treat ""
// as "unknown", never guess a value.
func processCWD(pid string) string {
	link, err := os.Readlink("/proc/" + pid + "/cwd")
	if err != nil {
		return ""
	}
	return link
}
