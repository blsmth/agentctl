package serve

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/blsmth/agentctl/internal/safefs"
)

// DevicesFile is the paired-device list under the serve dir.
const DevicesFile = "devices.json"

// lastSeenEvery bounds how often an authenticated request rewrites
// devices.json to bump last_seen.
const lastSeenEvery = 5 * time.Minute

// Device is a paired phone or browser. Only the SHA-256 of its token is
// stored; the token itself exists only in the device's cookie.
type Device struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Created     time.Time `json:"created"`
	LastSeen    time.Time `json:"last_seen,omitzero"`
	TokenSHA256 string    `json:"token_sha256"`
}

// DeviceStore persists devices to devices.json. The CLI (`serve devices rm`)
// and the running server both mutate the file, so every mutation takes an
// flock, re-reads the file, applies the change and replaces it atomically;
// readers reload whenever the file's mtime or size changes. A revocation from
// the CLI therefore takes effect on the server's next request.
type DeviceStore struct {
	path string

	mu      sync.Mutex
	devices []Device
	stamp   fileStamp
}

type fileStamp struct {
	mod  time.Time
	size int64
	ok   bool
}

// OpenDeviceStore returns the store for <dir>/devices.json. A missing file is
// an empty store.
func OpenDeviceStore(dir string) (*DeviceStore, error) {
	s := &DeviceStore{path: filepath.Join(dir, DevicesFile)}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

// List returns every device, oldest first.
func (s *DeviceStore) List() ([]Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadLocked(); err != nil {
		return nil, err
	}
	return append([]Device(nil), s.devices...), nil
}

// Add pairs a new device and returns it together with its token, which the
// caller must hand to the device and then forget.
func (s *DeviceStore) Add(name string, now time.Time) (Device, string, error) {
	token, err := randomToken(32)
	if err != nil {
		return Device{}, "", err
	}
	idBytes := make([]byte, 6)
	if _, err := rand.Read(idBytes); err != nil {
		return Device{}, "", fmt.Errorf("generating device id: %w", err)
	}
	d := Device{
		ID:          hex.EncodeToString(idBytes),
		Name:        strings.TrimSpace(name),
		Created:     now.UTC(),
		TokenSHA256: hashToken(token),
	}
	if d.Name == "" {
		d.Name = "device-" + d.ID[:4]
	}
	err = s.mutate(func(list []Device) ([]Device, error) {
		return append(list, d), nil
	})
	if err != nil {
		return Device{}, "", err
	}
	return d, token, nil
}

// Remove revokes the device whose id or name is ref. A name shared by
// several devices is ambiguous and must be removed by id.
func (s *DeviceStore) Remove(ref string) (Device, error) {
	var removed Device
	err := s.mutate(func(list []Device) ([]Device, error) {
		idx := -1
		for i, d := range list {
			if d.ID == ref {
				idx = i
				break
			}
		}
		if idx < 0 {
			for i, d := range list {
				if d.Name != ref {
					continue
				}
				if idx >= 0 {
					return nil, fmt.Errorf("several devices are named %q: remove by id", ref)
				}
				idx = i
			}
		}
		if idx < 0 {
			return nil, fmt.Errorf("no device with id or name %q", ref)
		}
		removed = list[idx]
		return append(list[:idx:idx], list[idx+1:]...), nil
	})
	return removed, err
}

// Authenticate returns the device whose token hashes to token's hash. It
// compares in constant time and bumps last_seen at most every lastSeenEvery.
func (s *DeviceStore) Authenticate(token string, now time.Time) (Device, bool) {
	if token == "" {
		return Device{}, false
	}
	want := hashToken(token)
	s.mu.Lock()
	if err := s.reloadLocked(); err != nil {
		s.mu.Unlock()
		return Device{}, false
	}
	var found Device
	ok := false
	for _, d := range s.devices {
		if subtle.ConstantTimeCompare([]byte(d.TokenSHA256), []byte(want)) == 1 {
			found, ok = d, true
		}
	}
	s.mu.Unlock()
	if !ok {
		return Device{}, false
	}
	if now.Sub(found.LastSeen) >= lastSeenEvery {
		_ = s.mutate(func(list []Device) ([]Device, error) {
			for i := range list {
				if list[i].ID == found.ID {
					list[i].LastSeen = now.UTC()
				}
			}
			return list, nil
		})
		found.LastSeen = now.UTC()
	}
	return found, true
}

// Clear revokes every device.
func (s *DeviceStore) Clear() error {
	return s.mutate(func([]Device) ([]Device, error) { return []Device{}, nil })
}

// mutate applies fn to the on-disk list under an exclusive flock and writes
// the result back.
func (s *DeviceStore) mutate(fn func([]Device) ([]Device, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := lockFile(s.path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	list, err := readDevices(s.path)
	if err != nil {
		return err
	}
	list, err = fn(list)
	if err != nil {
		return err
	}
	if list == nil {
		list = []Device{}
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding devices: %w", err)
	}
	if err := safefs.WriteFile(filepath.Dir(s.path), s.path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", s.path, err)
	}
	s.devices = list
	s.stamp = statFile(s.path)
	return nil
}

// reloadLocked re-reads the file when it changed since the last read.
func (s *DeviceStore) reloadLocked() error {
	st := statFile(s.path)
	if s.devices != nil && st.same(s.stamp) {
		return nil
	}
	list, err := readDevices(s.path)
	if err != nil {
		return err
	}
	s.devices, s.stamp = list, st
	return nil
}

func (a fileStamp) same(b fileStamp) bool {
	return a.ok == b.ok && a.size == b.size && a.mod.Equal(b.mod)
}

func statFile(path string) fileStamp {
	fi, err := os.Stat(path)
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{mod: fi.ModTime(), size: fi.Size(), ok: true}
}

func readDevices(path string) ([]Device, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return []Device{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var list []Device
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].Created.Before(list[j].Created) })
	if list == nil {
		list = []Device{}
	}
	return list, nil
}

// lockFile takes an exclusive flock on path (created 0600) and returns the
// release function.
func lockFile(path string) (func(), error) {
	f, err := safefs.OpenNoFollow(filepath.Dir(path), path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening lock %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// randomToken returns n random bytes, base64url-encoded without padding.
func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// hashToken is the stored form of a device token.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
