// Package buildid records and compares the identity of an agentctl executable
// on disk. The monitor writes its own identity to <runtimeDir>/monitor.build at
// startup so that it (and the hook watchdog) can tell when `just install` has
// replaced the binary underneath a long-running process.
package buildid

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/blsmth/agentctl/internal/safefs"
)

// FileName is the runtime-dir file holding the monitor's recorded Identity.
const FileName = "monitor.build"

// Identity identifies one build of an executable on disk.
type Identity struct {
	Path      string `json:"path"`
	Dev       uint64 `json:"dev"`
	Ino       uint64 `json:"ino"`
	Size      int64  `json:"size"`
	MtimeUnix int64  `json:"mtime_unix"`
	// MtimeNsec is the sub-second part of the mtime, so a reinstall within the
	// same second is still noticed.
	MtimeNsec int64 `json:"mtime_nsec"`
	PID       int   `json:"pid,omitempty"`

	// Mode is the file mode at stat time. It is not persisted.
	Mode fs.FileMode `json:"-"`
}

// Stat returns the Identity of the file at path (following symlinks).
func Stat(path string) (Identity, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return Identity{}, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return Identity{}, fmt.Errorf("buildid: no stat_t for %s", path)
	}
	mt := fi.ModTime()
	return Identity{
		Path:      path,
		Dev:       uint64(st.Dev), //nolint:unconvert // int32 on darwin
		Ino:       uint64(st.Ino), //nolint:unconvert
		Size:      fi.Size(),
		MtimeUnix: mt.Unix(),
		MtimeNsec: int64(mt.Nanosecond()),
		Mode:      fi.Mode(),
	}, nil
}

// SameBuild reports whether a and b have the same dev, inode, size and mtime.
// Path, PID and Mode are ignored.
func SameBuild(a, b Identity) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Size == b.Size &&
		a.MtimeUnix == b.MtimeUnix && a.MtimeNsec == b.MtimeNsec
}

// Write records id in runtimeDir/monitor.build.
func Write(runtimeDir string, id Identity) error {
	data, err := json.Marshal(id)
	if err != nil {
		return fmt.Errorf("marshaling build identity: %w", err)
	}
	path := filepath.Join(runtimeDir, FileName)
	if err := safefs.WriteFile(runtimeDir, path, data, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", FileName, err)
	}
	return nil
}

// Read loads the Identity recorded in runtimeDir/monitor.build.
func Read(runtimeDir string) (Identity, error) {
	data, err := os.ReadFile(filepath.Join(runtimeDir, FileName))
	if err != nil {
		return Identity{}, err
	}
	var id Identity
	if err := json.Unmarshal(data, &id); err != nil {
		return Identity{}, fmt.Errorf("parsing %s: %w", FileName, err)
	}
	return id, nil
}
