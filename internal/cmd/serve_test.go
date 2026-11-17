package cmd

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/serve"
)

func TestPrintPairing(t *testing.T) {
	var b bytes.Buffer
	printPairing(&b, serve.PairInfo{
		Code:         "CODE",
		Expires:      time.Now().Add(5 * time.Minute),
		PairURL:      "https://192.168.1.10:8443/pair?code=CODE",
		BootstrapURL: "http://192.168.1.10:8444/",
		Fingerprint:  "AA:BB",
	})
	out := b.String()
	for _, want := range []string{"AA:BB", "https://192.168.1.10:8443/pair?code=CODE", "http://192.168.1.10:8444/", "Step 1", "Step 2", "▀"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestPrintPairingWithoutBootstrap(t *testing.T) {
	var b bytes.Buffer
	printPairing(&b, serve.PairInfo{PairURL: "https://h:1/pair?code=X", Fingerprint: "AA"})
	if strings.Contains(b.String(), "Step 1") {
		t.Errorf("printed a bootstrap step without a URL:\n%s", b.String())
	}
}

func TestPrintDevices(t *testing.T) {
	tests := []struct {
		name string
		list []serve.Device
		want []string
	}{
		{name: "empty", want: []string{"No paired devices", "agentctl serve pair"}},
		{
			name: "rows",
			list: []serve.Device{
				{ID: "abc123", Name: "iphone", Created: time.Date(2026, 1, 2, 3, 4, 0, 0, time.Local)},
				{ID: "def456", Name: "ipad", Created: time.Date(2026, 1, 2, 3, 4, 0, 0, time.Local), LastSeen: time.Date(2026, 2, 3, 4, 5, 0, 0, time.Local)},
			},
			want: []string{"ID", "abc123", "iphone", "never", "def456", "2026-02-03 04:05"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b bytes.Buffer
			printDevices(&b, tt.list)
			for _, w := range tt.want {
				if !strings.Contains(b.String(), w) {
					t.Errorf("missing %q in:\n%s", w, b.String())
				}
			}
			if strings.Contains(b.String(), "token") {
				t.Error("device listing mentions tokens")
			}
		})
	}
}

func TestPrintServeStatusNotRunning(t *testing.T) {
	dir, err := makeShortDir(t)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ca, err := serve.LoadOrCreateCA(dir, "host", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := serve.EnsureLeaf(dir, ca, serve.SANs{DNS: []string{"host.local"}}, now); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := printServeStatus(&b, dir, now); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"running:", "no", "devices:", "0", "cert expires:", ca.Fingerprint()} {
		if !strings.Contains(out, want) {
			t.Errorf("status missing %q:\n%s", want, out)
		}
	}
}

// makeShortDir returns a temp dir short enough for a unix socket path.
func makeShortDir(t *testing.T) (string, error) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "acs")
	if err != nil {
		return "", err
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir, nil
}
