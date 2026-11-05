//go:build linux

package main

import (
	"os"
	"time"
)

// Linux doesn't expose birth time via syscall.Stat_t.
// Fall back to modification time, which is the best approximation available.
func fileBirthTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}
