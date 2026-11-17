package serve

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestCANameConstraints(t *testing.T) {
	ca, err := LoadOrCreateCA(t.TempDir(), "host", []string{"agentctl.home", "Mac.Example.org.", ""}, newClock().Now())
	if err != nil {
		t.Fatal(err)
	}
	c := ca.Cert
	if !c.PermittedDNSDomainsCritical || !ca.Constrained() {
		t.Fatal("CA is not name-constrained (critical)")
	}
	want := []string{"local", "lan", "home", "home.arpa", "internal", "mac.example.org"}
	if !slices.Equal(c.PermittedDNSDomains, want) {
		t.Errorf("permitted DNS = %v, want %v", c.PermittedDNSDomains, want)
	}
	var ranges []string
	for _, n := range c.PermittedIPRanges {
		ranges = append(ranges, n.String())
	}
	if !slices.Equal(ranges, localIPRanges) {
		t.Errorf("permitted IPs = %v, want %v", ranges, localIPRanges)
	}
}

// forgeLeaf signs a leaf for the given names with the CA key, bypassing
// FilterSANs: what an attacker holding ca.key could do.
func forgeLeaf(t *testing.T, ca *CA, dns []string, ips []net.IP) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := newClock().Now()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(99),
		Subject:      pkix.Name{CommonName: "forged"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dns,
		IPAddresses:  ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.Key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return leaf
}

func TestCAConstraintsEnforcedByVerify(t *testing.T) {
	ca, err := LoadOrCreateCA(t.TempDir(), "host", []string{"mac.example.org"}, newClock().Now())
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	tests := []struct {
		name   string
		dns    []string
		ips    []net.IP
		verify string
		ok     bool
	}{
		{name: "local name", dns: []string{"host.local"}, verify: "host.local", ok: true},
		{name: "home.arpa name", dns: []string{"agentctl.home.arpa"}, verify: "agentctl.home.arpa", ok: true},
		{name: "configured hostname", dns: []string{"mac.example.org"}, verify: "mac.example.org", ok: true},
		{name: "private IP", ips: []net.IP{net.ParseIP("192.168.1.10")}, verify: "192.168.1.10", ok: true},
		{name: "ULA IP", ips: []net.IP{net.ParseIP("fd00::5")}, verify: "fd00::5", ok: true},
		{name: "public domain", dns: []string{"example.com"}, verify: "example.com"},
		{name: "public domain beside a local one", dns: []string{"host.local", "www.google.com"}, verify: "host.local"},
		{name: "sibling of configured hostname", dns: []string{"example.org"}, verify: "example.org"},
		{name: "public IP", ips: []net.IP{net.ParseIP("8.8.8.8")}, verify: "8.8.8.8"},
		{name: "public IPv6", ips: []net.IP{net.ParseIP("2001:4860::8888")}, verify: "2001:4860::8888"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			leaf := forgeLeaf(t, ca, tt.dns, tt.ips)
			_, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: tt.verify, CurrentTime: newClock().Now()})
			if (err == nil) != tt.ok {
				t.Errorf("Verify err = %v, want ok=%v", err, tt.ok)
			}
		})
	}
}

func TestEnsureLeafDropsOutOfConstraintSANs(t *testing.T) {
	dir := t.TempDir()
	now := newClock().Now()
	ca, err := LoadOrCreateCA(dir, "host", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	want := SANs{
		DNS: []string{"host.local", "agentctl.example.com"},
		IPs: []net.IP{net.ParseIP("192.168.1.10"), net.ParseIP("2001:db8::1")},
	}
	kept, dropped := FilterSANs(ca, want)
	if !slices.Equal(kept.DNS, []string{"host.local"}) || len(kept.IPs) != 1 || !kept.IPs[0].Equal(net.ParseIP("192.168.1.10")) {
		t.Errorf("kept = %v %v", kept.DNS, kept.IPs)
	}
	if !slices.Equal(dropped, []string{"agentctl.example.com", "2001:db8::1"}) {
		t.Errorf("dropped = %v", dropped)
	}
	msg := sanOutsideConstraints("agentctl.example.com")
	if !strings.Contains(msg, "agentctl.example.com") || !strings.Contains(msg, "agentctl serve ca rotate") {
		t.Errorf("warning = %q", msg)
	}

	leaf, _, err := EnsureLeaf(dir, ca, want, now)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(leaf.Leaf.DNSNames, "agentctl.example.com") {
		t.Error("out-of-constraint hostname kept in the leaf")
	}
	checkLeaf(t, leaf.Leaf, ca, kept, now)

	// Asking again with the same input reuses the filtered leaf.
	if _, regen, err := EnsureLeaf(dir, ca, want, now); err != nil || regen {
		t.Errorf("second EnsureLeaf regen=%v err=%v", regen, err)
	}
}

func TestUnconstrainedCAPermitsEverything(t *testing.T) {
	ca := &CA{Cert: &x509.Certificate{}}
	kept, dropped := FilterSANs(ca, SANs{DNS: []string{"example.com"}, IPs: []net.IP{net.ParseIP("8.8.8.8")}})
	if len(dropped) != 0 || len(kept.DNS) != 1 || len(kept.IPs) != 1 {
		t.Errorf("kept %v dropped %v", kept, dropped)
	}
	if ca.Constrained() {
		t.Error("empty CA reported constrained")
	}
}

func TestRotateCA(t *testing.T) {
	for _, create := range []bool{true, false} {
		name := "deferred"
		if create {
			name = "immediate"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			now := newClock().Now()
			ca, err := LoadOrCreateCA(dir, "host", nil, now)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := EnsureLeaf(dir, ca, SANs{DNS: []string{"host.local"}}, now); err != nil {
				t.Fatal(err)
			}
			store, _ := OpenDeviceStore(dir)
			_, tok, _ := store.Add("phone", now)

			fresh, err := RotateCA(dir, "host", []string{"mac.example.org"}, create, now)
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range []string{ServerCertFile, ServerKeyFile} {
				if _, err := os.Stat(filepath.Join(dir, f)); !os.IsNotExist(err) {
					t.Errorf("%s survived rotation", f)
				}
			}
			if _, ok := store.Authenticate(tok, now); ok {
				t.Error("device survived rotation")
			}
			if !create {
				if fresh != nil {
					t.Fatal("deferred rotation created a CA")
				}
				if _, err := os.Stat(filepath.Join(dir, CACertFile)); !os.IsNotExist(err) {
					t.Fatal("old CA still on disk")
				}
				fresh, err = LoadOrCreateCA(dir, "host", nil, now) // next server start
				if err != nil {
					t.Fatal(err)
				}
			}
			if fresh.Fingerprint() == ca.Fingerprint() {
				t.Error("rotation kept the fingerprint")
			}
			loaded, err := LoadCA(dir)
			if err != nil || loaded.Fingerprint() != fresh.Fingerprint() {
				t.Errorf("on-disk CA %v does not match the rotated one", err)
			}
			if create && !slices.Contains(fresh.Cert.PermittedDNSDomains, "mac.example.org") {
				t.Error("rotation ignored configured hostnames")
			}
		})
	}
}
