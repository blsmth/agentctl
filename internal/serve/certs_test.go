package serve

import (
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func ipnet(s string) net.Addr {
	ip, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	n.IP = ip
	return n
}

func TestDesiredSANs(t *testing.T) {
	addrs := []net.Addr{
		ipnet("127.0.0.1/8"),
		ipnet("::1/128"),
		ipnet("192.168.1.10/24"),
		ipnet("fe80::1/64"),
		ipnet("169.254.3.4/16"),
		ipnet("10.0.0.5/8"),
		ipnet("fd00::5/64"),
		ipnet("192.168.1.10/24"), // duplicate
		&net.IPAddr{IP: net.ParseIP("100.64.1.2")},
		&net.UnixAddr{Name: "/tmp/x", Net: "unix"},
	}
	got := DesiredSANs([]string{"Agentctl.Home.", "agentctl.home", " "}, "Brendans-MBP.lan", addrs)
	wantDNS := []string{"agentctl.home", "brendans-mbp.local"}
	if !slices.Equal(got.DNS, wantDNS) {
		t.Errorf("DNS = %v, want %v", got.DNS, wantDNS)
	}
	var ips []string
	for _, ip := range got.IPs {
		ips = append(ips, ip.String())
	}
	wantIPs := []string{"10.0.0.5", "100.64.1.2", "192.168.1.10", "fd00::5"}
	if !slices.Equal(ips, wantIPs) {
		t.Errorf("IPs = %v, want %v", ips, wantIPs)
	}
}

func TestSANsEqual(t *testing.T) {
	a := SANs{DNS: []string{"b.local", "a.home"}, IPs: []net.IP{net.ParseIP("10.0.0.1"), net.ParseIP("192.168.1.1")}}
	b := SANs{DNS: []string{"A.home", "b.local"}, IPs: []net.IP{net.ParseIP("192.168.1.1").To4(), net.ParseIP("10.0.0.1")}}
	if !a.Equal(b) {
		t.Error("equivalent SANs reported different")
	}
	c := SANs{DNS: a.DNS, IPs: []net.IP{net.ParseIP("10.0.0.1")}}
	if a.Equal(c) {
		t.Error("different IP sets reported equal")
	}
}

func TestCACreatedOnceAndNeverRegenerated(t *testing.T) {
	dir := t.TempDir()
	now := newClock().Now()
	ca, err := LoadOrCreateCA(dir, "host", now)
	if err != nil {
		t.Fatal(err)
	}
	c := ca.Cert
	if !c.IsCA || !c.BasicConstraintsValid || c.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Errorf("CA constraints wrong: %+v", c)
	}
	if c.Subject.CommonName != "agentctl local CA (host)" {
		t.Errorf("CN = %q", c.Subject.CommonName)
	}
	if years := c.NotAfter.Sub(now).Hours() / 24 / 365; years < 9.9 || years > 10.1 {
		t.Errorf("CA validity %.1f years", years)
	}
	for _, f := range []string{CACertFile, CAKeyFile} {
		fi, err := os.Stat(filepath.Join(dir, f))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v", f, fi.Mode().Perm())
		}
	}

	again, err := LoadOrCreateCA(dir, "host", now.Add(20*365*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if again.Fingerprint() != ca.Fingerprint() {
		t.Error("CA regenerated on reload (even when expired it must not be)")
	}

	// A damaged CA is an error, never silently replaced.
	before, _ := os.ReadFile(filepath.Join(dir, CACertFile))
	if err := os.WriteFile(filepath.Join(dir, CAKeyFile), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateCA(dir, "host", now); err == nil {
		t.Fatal("damaged CA key accepted")
	}
	after, _ := os.ReadFile(filepath.Join(dir, CACertFile))
	if string(before) != string(after) {
		t.Error("CA certificate rewritten after a load error")
	}

	// Only one of the two files present is also an error.
	dir2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir2, CACertFile), before, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateCA(dir2, "host", now); err == nil {
		t.Fatal("CA cert without key accepted")
	}
	if _, err := os.Stat(filepath.Join(dir2, CAKeyFile)); !os.IsNotExist(err) {
		t.Error("a new CA key was generated next to an existing CA cert")
	}
}

func TestCAMismatchedKey(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	now := newClock().Now()
	if _, err := LoadOrCreateCA(a, "h", now); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateCA(b, "h", now); err != nil {
		t.Fatal(err)
	}
	key, _ := os.ReadFile(filepath.Join(b, CAKeyFile))
	if err := os.WriteFile(filepath.Join(a, CAKeyFile), key, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCA(a); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("err = %v", err)
	}
}

func TestEnsureLeaf(t *testing.T) {
	sans := SANs{DNS: []string{"host.local", "agentctl.home"}, IPs: []net.IP{net.ParseIP("192.168.1.10"), net.ParseIP("fd00::5")}}
	tests := []struct {
		name      string
		second    func(dir string, ca *CA, now time.Time) (SANs, *CA, time.Time)
		wantRegen bool
	}{
		{
			name:   "unchanged is reused",
			second: func(_ string, ca *CA, now time.Time) (SANs, *CA, time.Time) { return sans, ca, now.Add(24 * time.Hour) },
		},
		{
			name: "SAN order does not matter",
			second: func(_ string, ca *CA, now time.Time) (SANs, *CA, time.Time) {
				return SANs{DNS: []string{"agentctl.home", "HOST.local"}, IPs: []net.IP{net.ParseIP("fd00::5"), net.ParseIP("192.168.1.10")}}, ca, now
			},
		},
		{
			name: "new IP regenerates",
			second: func(_ string, ca *CA, now time.Time) (SANs, *CA, time.Time) {
				s := sans
				s.IPs = []net.IP{net.ParseIP("192.168.1.99"), net.ParseIP("fd00::5")}
				return s, ca, now
			},
			wantRegen: true,
		},
		{
			name: "new hostname regenerates",
			second: func(_ string, ca *CA, now time.Time) (SANs, *CA, time.Time) {
				s := sans
				s.DNS = append([]string{"extra.home"}, s.DNS...)
				return s, ca, now
			},
			wantRegen: true,
		},
		{
			name: "under 30 days left regenerates",
			second: func(_ string, ca *CA, now time.Time) (SANs, *CA, time.Time) {
				return sans, ca, now.Add(leafValidity - 29*24*time.Hour)
			},
			wantRegen: true,
		},
		{
			name: "31 days left is reused",
			second: func(_ string, ca *CA, now time.Time) (SANs, *CA, time.Time) {
				return sans, ca, now.Add(leafValidity - 31*24*time.Hour)
			},
		},
		{
			name: "different CA regenerates",
			second: func(_ string, _ *CA, now time.Time) (SANs, *CA, time.Time) {
				other, err := LoadOrCreateCA(t.TempDir(), "other", now)
				if err != nil {
					t.Fatal(err)
				}
				return sans, other, now
			},
			wantRegen: true,
		},
		{
			name: "corrupt leaf regenerates",
			second: func(dir string, ca *CA, now time.Time) (SANs, *CA, time.Time) {
				if err := os.WriteFile(filepath.Join(dir, ServerCertFile), []byte("junk"), 0o600); err != nil {
					t.Fatal(err)
				}
				return sans, ca, now
			},
			wantRegen: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			now := newClock().Now()
			ca, err := LoadOrCreateCA(dir, "host", now)
			if err != nil {
				t.Fatal(err)
			}
			first, regen, err := EnsureLeaf(dir, ca, sans, now)
			if err != nil || !regen {
				t.Fatalf("first EnsureLeaf: regen=%v err=%v", regen, err)
			}
			checkLeaf(t, first.Leaf, ca, sans, now)

			want, ca2, now2 := tt.second(dir, ca, now)
			second, regen, err := EnsureLeaf(dir, ca2, want, now2)
			if err != nil {
				t.Fatal(err)
			}
			if regen != tt.wantRegen {
				t.Fatalf("regenerated = %v, want %v", regen, tt.wantRegen)
			}
			if !regen && second.Leaf.SerialNumber.Cmp(first.Leaf.SerialNumber) != 0 {
				t.Error("reused leaf has a different serial")
			}
			checkLeaf(t, second.Leaf, ca2, want, now2)
			if len(second.Certificate) != 2 {
				t.Errorf("chain length %d, want leaf + CA", len(second.Certificate))
			}
		})
	}
}

func checkLeaf(t *testing.T, leaf *x509.Certificate, ca *CA, want SANs, now time.Time) {
	t.Helper()
	if leaf == nil {
		t.Fatal("no parsed leaf")
	}
	if !(SANs{DNS: leaf.DNSNames, IPs: leaf.IPAddresses}).Equal(want) {
		t.Errorf("leaf SANs = %v %v, want %v %v", leaf.DNSNames, leaf.IPAddresses, want.DNS, want.IPs)
	}
	if d := leaf.NotAfter.Sub(leaf.NotBefore); d > 825*24*time.Hour {
		t.Errorf("leaf validity %v exceeds the 825 days iOS accepts", d)
	}
	if leaf.IsCA || !slices.Contains(leaf.ExtKeyUsage, x509.ExtKeyUsageServerAuth) {
		t.Errorf("leaf usage wrong")
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	name := "192.168.1.10"
	if len(want.DNS) > 0 {
		name = want.DNS[0]
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: name, CurrentTime: now}); err != nil {
		t.Errorf("leaf does not verify against the CA: %v", err)
	}
}

func TestFingerprintFormat(t *testing.T) {
	fp := Fingerprint([]byte("x"))
	parts := strings.Split(fp, ":")
	if len(parts) != 32 || strings.ToUpper(fp) != fp {
		t.Errorf("fingerprint %q", fp)
	}
}
