//go:build darwin

package main

import (
	"syscall"
	"time"
)

func fileBirthTime(path string) time.Time {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return time.Time{}
	}
	return time.Unix(st.Birthtimespec.Sec, st.Birthtimespec.Nsec)
}
