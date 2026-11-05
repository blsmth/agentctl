//go:build linux

package sessions

import (
	"os"
	"time"
)

// fileBirthTime returns the birth time of a file on Linux.
// Linux doesn't expose birth time via syscall.Stat_t, so we fall back to
// modification time as the best available approximation.
func fileBirthTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}
