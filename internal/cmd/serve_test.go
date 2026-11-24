package cmd

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/blsmth/agentctl/internal/serve"
	"github.com/blsmth/agentctl/internal/servectl"
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
	ca, err := serve.LoadOrCreateCA(dir, "host", nil, now)
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
	for _, want := range []string{"running:", "no", "phone access:    on", "devices:", "0", "cert expires:", ca.Fingerprint()} {
		if !strings.Contains(out, want) {
			t.Errorf("status missing %q:\n%s", want, out)
		}
	}
	if err := servectl.SetPhone(dir, false); err != nil {
		t.Fatal(err)
	}
	b.Reset()
	if err := printServeStatus(&b, dir, now); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "phone access:    off") {
		t.Errorf("status with phone off:\n%s", b.String())
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

func TestRotateCANotRunning(t *testing.T) {
	dir, err := makeShortDir(t)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	old, err := serve.LoadOrCreateCA(dir, "host", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := rotateCA(&b, dir, "host", nil, now); err != nil {
		t.Fatal(err)
	}
	fresh, err := serve.LoadCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if fresh.Fingerprint() == old.Fingerprint() {
		t.Fatal("fingerprint unchanged")
	}
	for _, want := range []string{old.Fingerprint(), fresh.Fingerprint(), "revoked", "agentctl serve pair"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestPrintPushStatus(t *testing.T) {
	var b bytes.Buffer
	printPushStatus(&b, serve.StatusInfo{
		PushDevices: []string{"phone", "tablet"},
		LastPush:    &serve.LastPush{Time: time.Now(), Kind: "blocked", Result: "sent to 2/2"},
	})
	for _, want := range []string{"push subscriptions:", "2 (phone, tablet)", "last push:", "blocked sent to 2/2"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("missing %q:\n%s", want, b.String())
		}
	}
	b.Reset()
	printPushStatus(&b, serve.StatusInfo{})
	if !strings.Contains(b.String(), "push subscriptions:\t0") || !strings.Contains(b.String(), "none since start") {
		t.Errorf("empty status:\n%s", b.String())
	}
}

func TestPrintPublicCertStatus(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		pc     *serve.PublicCertStatus
		url    string
		want   []string
		absent []string
	}{
		{name: "unset prints nothing", pc: nil, absent: []string{"public"}},
		{
			name:   "obtaining",
			pc:     &serve.PublicCertStatus{Hostname: "mac.agentctl.example.com", Subject: "*.agentctl.example.com", State: serve.PublicCertObtaining},
			want:   []string{"public host:\tmac.agentctl.example.com", "public cert name:\t*.agentctl.example.com", "public cert:\tobtaining"},
			absent: []string{"public url"},
		},
		{
			name: "valid",
			pc:   &serve.PublicCertStatus{Hostname: "mac.example.com", State: serve.PublicCertValid, Expires: now.Add(60 * 24 * time.Hour)},
			url:  "https://mac.example.com:8443/",
			want: []string{"public url:\thttps://mac.example.com:8443/", "public cert:\tvalid, expires", "(60 days)"}, absent: []string{"staging", "error"},
		},
		{
			name: "staging",
			pc:   &serve.PublicCertStatus{Hostname: "mac.example.com", State: serve.PublicCertValid, Expires: now.Add(24 * time.Hour), Staging: true},
			want: []string{"staging: untrusted, not advertised"},
		},
		{
			name: "error",
			pc:   &serve.PublicCertStatus{Hostname: "mac.example.com", State: serve.PublicCertError, Error: "no Cloudflare API token"},
			want: []string{"public cert:\terror", "public cert error:\tno Cloudflare API token"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b bytes.Buffer
			printPublicCertStatus(&b, tt.pc, tt.url, now)
			for _, w := range tt.want {
				if !strings.Contains(b.String(), w) {
					t.Errorf("missing %q:\n%s", w, b.String())
				}
			}
			for _, a := range tt.absent {
				if strings.Contains(b.String(), a) {
					t.Errorf("unexpected %q:\n%s", a, b.String())
				}
			}
		})
	}
}
