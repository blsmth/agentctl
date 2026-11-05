//go:build !darwin && !linux

package main

import (
	"os"
	"time"
)

// fileBirthTime falls back to modification time on platforms without a
// dedicated birth-time implementation (see birthtime_darwin.go and
// birthtime_linux.go).
func fileBirthTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}
