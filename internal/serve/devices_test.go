package serve

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDeviceTokenNeverStoredInPlaintext(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenDeviceStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	clock := newClock()
	d, tok, err := s.Add("phone", clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil || len(raw) != 32 {
		t.Fatalf("token is not 256 bits of base64url: %q (%v)", tok, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, DevicesFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), tok) {
		t.Fatal("devices.json contains the plaintext token")
	}
	if !strings.Contains(string(data), d.TokenSHA256) || d.TokenSHA256 != hashToken(tok) {
		t.Fatal("devices.json does not contain the token hash")
	}
	fi, err := os.Stat(filepath.Join(dir, DevicesFile))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("devices.json mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestDeviceAuthenticate(t *testing.T) {
	s, err := OpenDeviceStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	clock := newClock()
	d, tok, err := s.Add("phone", clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		token string
		ok    bool
	}{
		{"valid", tok, true},
		{"empty", "", false},
		{"wrong", tok + "x", false},
		{"hash instead of token", d.TokenSHA256, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := s.Authenticate(tt.token, clock.Now())
			if ok != tt.ok || (ok && got.ID != d.ID) {
				t.Errorf("Authenticate = %+v, %v; want ok=%v", got, ok, tt.ok)
			}
		})
	}
}

func TestDeviceLastSeenThrottled(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenDeviceStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	clock := newClock()
	_, tok, err := s.Add("phone", clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	first, _ := s.Authenticate(tok, clock.Now())
	if !first.LastSeen.Equal(clock.Now()) {
		t.Fatalf("last_seen = %v, want %v", first.LastSeen, clock.Now())
	}
	clock.Advance(time.Minute)
	second, _ := s.Authenticate(tok, clock.Now())
	if !second.LastSeen.Equal(first.LastSeen) {
		t.Errorf("last_seen bumped within %v", lastSeenEvery)
	}
	clock.Advance(lastSeenEvery)
	s.Authenticate(tok, clock.Now())
	other, _ := OpenDeviceStore(dir)
	list, _ := other.List()
	if len(list) != 1 || !list[0].LastSeen.Equal(clock.Now()) {
		t.Errorf("persisted last_seen = %+v, want %v", list, clock.Now())
	}
}

func TestDeviceRemove(t *testing.T) {
	setup := func(t *testing.T) (*DeviceStore, []Device) {
		s, err := OpenDeviceStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		clock := newClock()
		var out []Device
		for _, n := range []string{"phone", "ipad", "ipad", ""} {
			clock.Advance(time.Second)
			d, _, err := s.Add(n, clock.Now())
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, d)
		}
		return s, out
	}
	tests := []struct {
		name    string
		ref     func([]Device) string
		wantErr string
		left    int
	}{
		{name: "by name", ref: func([]Device) string { return "phone" }, left: 3},
		{name: "by id", ref: func(d []Device) string { return d[1].ID }, left: 3},
		{name: "ambiguous name", ref: func([]Device) string { return "ipad" }, wantErr: "remove by id", left: 4},
		{name: "unknown", ref: func([]Device) string { return "nope" }, wantErr: "no device", left: 4},
		{name: "default name", ref: func(d []Device) string { return d[3].Name }, left: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, devs := setup(t)
			_, err := s.Remove(tt.ref(devs))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			list, _ := s.List()
			if len(list) != tt.left {
				t.Errorf("%d devices left, want %d", len(list), tt.left)
			}
		})
	}
}

func TestDeviceDefaultName(t *testing.T) {
	s, _ := OpenDeviceStore(t.TempDir())
	d, _, err := s.Add("  ", newClock().Now())
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "device-"+d.ID[:4] {
		t.Errorf("name = %q", d.Name)
	}
}

func TestLastSeenDoesNotResurrectRevokedDevice(t *testing.T) {
	dir := t.TempDir()
	server, _ := OpenDeviceStore(dir)
	clock := newClock()
	d, tok, _ := server.Add("phone", clock.Now())
	if _, ok := server.Authenticate(tok, clock.Now()); !ok {
		t.Fatal("auth failed")
	}
	cli, _ := OpenDeviceStore(dir)
	if _, err := cli.Remove(d.ID); err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * lastSeenEvery)
	if _, ok := server.Authenticate(tok, clock.Now()); ok {
		t.Fatal("revoked device authenticated")
	}
	list, _ := cli.List()
	if len(list) != 0 {
		t.Fatalf("revoked device came back: %+v", list)
	}
}
