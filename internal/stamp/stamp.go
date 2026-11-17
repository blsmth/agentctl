// Package stamp throttles an action across processes. Hooks fire concurrently
// (several can land within milliseconds), so "check the stamp's age, then
// touch it" is a race; Try does both under one flock.
package stamp

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Try reports whether the caller may run the throttled action now: true when
// the stamp at path is absent or at least interval old, in which case it is
// re-stamped with now before the flock is released. Of N concurrent callers
// within one interval exactly one gets true. Any error returns false: a
// throttle that cannot record itself must not let every caller through.
func Try(path string, interval time.Duration, now time.Time) bool {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return false
	}
	defer f.Close() //nolint:errcheck
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return false
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck

	buf := make([]byte, 32)
	n, _ := f.ReadAt(buf, 0)
	if last, err := strconv.ParseInt(strings.TrimSpace(string(buf[:n])), 10, 64); err == nil {
		if d := now.Sub(time.Unix(0, last)); d >= 0 && d < interval {
			return false
		}
	}
	if err := f.Truncate(0); err != nil {
		return false
	}
	if _, err := f.WriteAt([]byte(fmt.Sprint(now.UnixNano())), 0); err != nil {
		return false
	}
	return true
}
