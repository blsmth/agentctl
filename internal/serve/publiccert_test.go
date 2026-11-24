package serve

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caddyserver/certmagic"

	"github.com/blsmth/agentctl/internal/config"
)

const (
	testPublicHost    = "macbookpro.agentctl.example.com"
	testPublicSubject = "*.agentctl.example.com"
)

// testCA is a throwaway CA standing in for Let's Encrypt.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "fake public CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &testCA{cert: cert, key: key}
}

// sign issues a leaf for names over pub, valid from nb to na.
func (ca *testCA) sign(t testing.TB, names []string, pub any, nb, na time.Time) []byte {
	t.Helper()
	serial, err := randomSerial()
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: names[0]},
		DNSNames:     names,
		NotBefore:    nb,
		NotAfter:     na,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, pub, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

// leafPair is a tls.Certificate for names signed by ca.
func (ca *testCA) leafPair(t *testing.T, names []string, nb, na time.Time) *tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der := ca.sign(t, names, &key.PublicKey, nb, na)
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Certificate{Certificate: [][]byte{der, ca.cert.Raw}, PrivateKey: key, Leaf: leaf}
}

// fakeSource is a certSource with no ACME behind it.
type fakeSource struct {
	mu        sync.Mutex
	cert      *tls.Certificate
	managed   []string
	looked    []string
	manageErr error
	stopped   bool
}

func (f *fakeSource) Manage(_ context.Context, subject string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.managed = append(f.managed, subject)
	return f.manageErr
}

func (f *fakeSource) Lookup(subject string) *tls.Certificate {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.looked = append(f.looked, subject)
	if f.cert == nil {
		return nil
	}
	c := *f.cert
	return &c
}

func (f *fakeSource) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = true
}

func (f *fakeSource) set(c *tls.Certificate) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cert = c
}

func TestWildcardSubject(t *testing.T) {
	tests := []struct{ host, want string }{
		{"macbookpro.agentctl.blsmth.com", "*.agentctl.blsmth.com"},
		{"a.b.c.d.example", "*.b.c.d.example"},
		{"nodot", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := wildcardSubject(tt.host); got != tt.want {
			t.Errorf("wildcardSubject(%q) = %q, want %q", tt.host, got, tt.want)
		}
	}
}

func TestPublicCertUsesWildcardSubject(t *testing.T) {
	src := &fakeSource{}
	p := newPublicCert(testPublicHost, false, src, nil, nil)
	p.start(context.Background())
	waitFor(t, "Manage", func() bool {
		src.mu.Lock()
		defer src.mu.Unlock()
		return len(src.managed) == 1
	})
	_ = p.AdvertiseHost()
	_ = p.Status()
	src.mu.Lock()
	defer src.mu.Unlock()
	if src.managed[0] != testPublicSubject {
		t.Errorf("managed %q, want %q", src.managed[0], testPublicSubject)
	}
	for _, l := range src.looked {
		if l != testPublicSubject {
			t.Errorf("looked up %q, want only %q", l, testPublicSubject)
		}
	}
}

func TestGetCertificateRoutesBySNI(t *testing.T) {
	ca := newTestCA(t)
	now := time.Now()
	local := ca.leafPair(t, []string{"box.local"}, now.Add(-time.Hour), now.Add(time.Hour))
	public := ca.leafPair(t, []string{testPublicSubject}, now.Add(-time.Hour), now.Add(time.Hour))
	src := &fakeSource{}
	p := newPublicCert(testPublicHost, false, src, nil, nil)
	get := getCertificateFunc(local, p)

	pick := func(sni string) *tls.Certificate {
		t.Helper()
		c, err := get(&tls.ClientHelloInfo{ServerName: sni})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	isPublic := func(c *tls.Certificate) bool { return c.Leaf != nil && c.Leaf.Subject.CommonName == testPublicSubject }

	if isPublic(pick(testPublicHost)) {
		t.Error("public hostname got a public cert before one was held")
	}
	src.set(public)
	tests := []struct {
		sni        string
		wantPublic bool
	}{
		{testPublicHost, true},
		{strings.ToUpper(testPublicHost), true},
		{testPublicHost + ".", true},
		{"", false},
		{"box.local", false},
		{"192.168.1.10", false},
		{"other.agentctl.example.com", false}, // covered by the wildcard, not ours
		{"agentctl.example.com", false},
		{"x." + testPublicHost, false},
	}
	for _, tt := range tests {
		if got := isPublic(pick(tt.sni)); got != tt.wantPublic {
			t.Errorf("SNI %q: public = %v, want %v", tt.sni, got, tt.wantPublic)
		}
	}
	if _, err := getCertificateFunc(local, nil)(&tls.ClientHelloInfo{ServerName: testPublicHost}); err != nil {
		t.Errorf("nil PublicCert: %v", err)
	}
}

func TestPublicCertStateAndAdvertise(t *testing.T) {
	ca := newTestCA(t)
	clock := newClock()
	now := clock.Now()
	valid := ca.leafPair(t, []string{testPublicSubject}, now.Add(-time.Hour), now.Add(90*24*time.Hour))

	t.Run("nil is off", func(t *testing.T) {
		var p *PublicCert
		if p.AdvertiseHost() != "" || p.Status() != nil || p.Hostname() != "" {
			t.Error("nil PublicCert advertised or reported")
		}
		p.start(context.Background())
		p.stop()
	})
	t.Run("lifecycle", func(t *testing.T) {
		src := &fakeSource{}
		p := newPublicCert(testPublicHost, false, src, clock.Now, nil)
		if st := p.Status(); st.State != PublicCertNone || st.Hostname != testPublicHost || st.Subject != testPublicSubject {
			t.Errorf("initial status = %+v", st)
		}
		p.event("obtaining", nil)
		if st := p.Status(); st.State != PublicCertObtaining {
			t.Errorf("state = %q, want obtaining", st.State)
		}
		p.event("failed", errors.New("rate limited"))
		if st := p.Status(); st.State != PublicCertError || st.Error != "rate limited" {
			t.Errorf("status = %+v, want error", st)
		}
		if p.AdvertiseHost() != "" {
			t.Error("advertised without a certificate")
		}
		src.set(valid)
		p.event("obtained", nil)
		st := p.Status()
		if st.State != PublicCertValid || !st.Expires.Equal(valid.Leaf.NotAfter) || st.Error != "" {
			t.Errorf("status = %+v, want valid", st)
		}
		if p.AdvertiseHost() != testPublicHost {
			t.Errorf("AdvertiseHost = %q, want %q", p.AdvertiseHost(), testPublicHost)
		}
		p.event("failed", errors.New("renewal failed"))
		if st := p.Status(); st.State != PublicCertValid || st.Error != "renewal failed" {
			t.Errorf("status after renewal failure = %+v", st)
		}
	})
	t.Run("clock outside validity", func(t *testing.T) {
		src := &fakeSource{cert: ca.leafPair(t, []string{testPublicSubject}, now.Add(time.Hour), now.Add(2*time.Hour))}
		p := newPublicCert(testPublicHost, false, src, clock.Now, nil)
		if p.AdvertiseHost() != "" {
			t.Error("advertised a certificate not yet valid")
		}
		if st := p.Status(); st.State != PublicCertError || !strings.Contains(st.Error, "check the clock") {
			t.Errorf("status = %+v", st)
		}
	})
	t.Run("staging served but never advertised", func(t *testing.T) {
		src := &fakeSource{cert: valid}
		p := newPublicCert(testPublicHost, true, src, clock.Now, nil)
		if p.AdvertiseHost() != "" {
			t.Error("advertised a staging certificate")
		}
		if st := p.Status(); st.State != PublicCertValid || !st.Staging {
			t.Errorf("status = %+v", st)
		}
		c, _ := getCertificateFunc(&tls.Certificate{}, p)(&tls.ClientHelloInfo{ServerName: testPublicHost})
		if c.Leaf == nil {
			t.Error("staging certificate not served for its name")
		}
	})
	t.Run("manage error recorded", func(t *testing.T) {
		src := &fakeSource{manageErr: errors.New("storage broken")}
		p := newPublicCert(testPublicHost, false, src, clock.Now, nil)
		p.start(context.Background())
		waitFor(t, "an error state", func() bool { return p.Status().State == PublicCertError })
	})
}

func TestReadCloudflareToken(t *testing.T) {
	const secret = "cf-secret-token-value"
	dir := t.TempDir()
	path := filepath.Join(dir, CloudflareTokenFile)
	write := func(t *testing.T, body string, mode os.FileMode) {
		t.Helper()
		_ = os.Remove(path)
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name    string
		setup   func(t *testing.T)
		want    string
		wantErr string
	}{
		{name: "missing", setup: func(*testing.T) { _ = os.Remove(path) }, wantErr: "no Cloudflare API token"},
		{name: "0600", setup: func(t *testing.T) { write(t, secret+"\n", 0o600) }, want: secret},
		{name: "0400", setup: func(t *testing.T) { write(t, secret, 0o400) }, want: secret},
		{name: "group readable", setup: func(t *testing.T) { write(t, secret, 0o640) }, wantErr: "chmod 600"},
		{name: "world readable", setup: func(t *testing.T) { write(t, secret, 0o644) }, wantErr: "chmod 600"},
		{name: "empty", setup: func(t *testing.T) { write(t, " \n", 0o600) }, wantErr: "is empty"},
		{name: "two tokens", setup: func(t *testing.T) { write(t, secret+"\n"+secret, 0o600) }, wantErr: "one line"},
		{name: "unreadable", setup: func(t *testing.T) { write(t, secret, 0o000) }, wantErr: "reading Cloudflare API token"},
		{name: "directory", setup: func(t *testing.T) {
			_ = os.Remove(path)
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Remove(path) })
		}, wantErr: "not a regular file"},
		{name: "symlink", setup: func(t *testing.T) {
			_ = os.Remove(path)
			target := filepath.Join(dir, "elsewhere")
			if err := os.WriteFile(target, []byte(secret), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Remove(path) })
		}, wantErr: "not a regular file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "unreadable" && os.Geteuid() == 0 {
				t.Skip("root reads any file")
			}
			tt.setup(t)
			got, err := readCloudflareToken(path)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error leaks the token: %v", err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestServerAdvertisesPublicHostOnlyWithValidCert(t *testing.T) {
	ca := newTestCA(t)
	src := &fakeSource{}
	ts := newTestServerWith(t, config.ServeConfig{NtfyTopic: "t", NtfyDetails: true}, func(o *Options) {
		o.Public = newPublicCert(testPublicHost, false, src, o.Now, nil)
	})
	const lan = "https://192.168.1.10:8443"
	st := ts.Status()
	if st.URL != lan+"/" || st.PublicURL != "" {
		t.Errorf("before cert: URL %q PublicURL %q", st.URL, st.PublicURL)
	}
	if st.PublicCert == nil || st.PublicCert.State != PublicCertNone {
		t.Errorf("PublicCert = %+v", st.PublicCert)
	}

	now := ts.clock.Now()
	src.set(ca.leafPair(t, []string{testPublicSubject}, now.Add(-time.Hour), now.Add(90*24*time.Hour)))
	st = ts.Status()
	if want := "https://" + testPublicHost + ":8443/"; st.PublicURL != want {
		t.Errorf("with cert: PublicURL %q, want %q", st.PublicURL, want)
	}
	// The app needs the host-only device cookie set at pairing on the LAN
	// address, so the app URL, ntfy Click links and pairing stay there.
	if st.URL != lan+"/" {
		t.Errorf("with cert: app URL moved to %q", st.URL)
	}
	if ts.ntfy.AppURL != lan {
		t.Errorf("ntfy app url = %q, want %q", ts.ntfy.AppURL, lan)
	}
	info, err := ts.OpenPairing("phone")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(info.PairURL, lan+"/pair") {
		t.Errorf("pairing URL moved to the public host: %q", info.PairURL)
	}
}

func TestServerStatusUnchangedWithoutPublicHostname(t *testing.T) {
	ts := newTestServer(t, config.ServeConfig{NtfyTopic: "t"})
	st := ts.Status()
	if st.URL != "https://192.168.1.10:8443/" || st.PublicURL != "" || st.PublicCert != nil {
		t.Errorf("status = %+v", st)
	}
	if cfg := serverTLSConfig(tls.Certificate{}, nil); cfg.GetCertificate != nil || len(cfg.Certificates) != 1 {
		t.Error("TLS config changed without public_hostname")
	}
}

func TestSameOriginAcceptsPublicHostname(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "https://"+testPublicHost+":8443/v1/x", nil)
	r.Header.Set("Origin", "https://"+testPublicHost+":8443")
	if !sameOrigin(r) {
		t.Error("terminal origin check refused the public hostname")
	}
}

// fakeIssuer is a certmagic.Issuer signing with a test CA, no network.
type fakeIssuer struct {
	ca  *testCA
	err error
	t   testing.TB

	mu   sync.Mutex
	csrs [][]string
}

func (f *fakeIssuer) Issue(_ context.Context, csr *x509.CertificateRequest) (*certmagic.IssuedCertificate, error) {
	f.mu.Lock()
	f.csrs = append(f.csrs, csr.DNSNames)
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	der := f.ca.sign(f.t, csr.DNSNames, csr.PublicKey, time.Now().Add(-time.Hour), time.Now().Add(90*24*time.Hour))
	chain := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	chain = append(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.ca.cert.Raw})...)
	return &certmagic.IssuedCertificate{Certificate: chain}, nil
}

func (f *fakeIssuer) IssuerKey() string { return "agentctl-test" }

// TestCertmagicObtainsWildcardAndServesIt runs real certmagic with a fake
// issuer: the wildcard is requested (never the exact host), stored under
// <dir>/acme, and served over TLS for the public hostname only.
func TestCertmagicObtainsWildcardAndServesIt(t *testing.T) {
	dir := t.TempDir()
	ca := newTestCA(t)
	iss := &fakeIssuer{ca: ca, t: t}
	p := newPublicCertFromConfig(dir, config.ServeConfig{PublicHostname: "MacBookPro.agentctl.example.com"}, nil, iss)
	t.Cleanup(p.stop)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	p.start(ctx)
	waitFor(t, "the public certificate", func() bool { return p.AdvertiseHost() == testPublicHost })

	iss.mu.Lock()
	csrs := iss.csrs
	iss.mu.Unlock()
	if len(csrs) != 1 || len(csrs[0]) != 1 || csrs[0][0] != testPublicSubject {
		t.Fatalf("requested %v, want only %q", csrs, testPublicSubject)
	}
	if fi, err := os.Stat(filepath.Join(dir, ACMEDir)); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("acme dir: %v %v", fi, err)
	}
	if st := p.Status(); st.State != PublicCertValid || st.Subject != testPublicSubject {
		t.Errorf("status = %+v", st)
	}

	localCA := newTestCA(t)
	local := localCA.leafPair(t, []string{"box.local"}, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	addr := serveTLS(t, serverTLSConfig(*local, p))
	if cn := handshakeCN(t, addr, testPublicHost); cn != testPublicSubject {
		t.Errorf("public hostname got %q", cn)
	}
	if cn := handshakeCN(t, addr, "other.agentctl.example.com"); cn != "box.local" {
		t.Errorf("other name under the wildcard got %q, want the local leaf", cn)
	}
	if cn := handshakeCN(t, addr, ""); cn != "box.local" {
		t.Errorf("no SNI got %q, want the local leaf", cn)
	}
}

// TestServeComesUpWhenACMEFails: a failing issuer is reported and retried
// in the background while TLS keeps serving the local leaf.
func TestServeComesUpWhenACMEFails(t *testing.T) {
	dir := t.TempDir()
	iss := &fakeIssuer{ca: newTestCA(t), t: t, err: errors.New("acme: rate limited")}
	p := newPublicCertFromConfig(dir, config.ServeConfig{PublicHostname: testPublicHost}, nil, iss)
	t.Cleanup(p.stop)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	localCA := newTestCA(t)
	local := localCA.leafPair(t, []string{"box.local"}, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	addr := serveTLS(t, serverTLSConfig(*local, p))
	p.start(ctx)
	if cn := handshakeCN(t, addr, testPublicHost); cn != "box.local" {
		t.Errorf("public hostname before any cert got %q", cn)
	}
	waitFor(t, "an error state", func() bool { return p.Status().State == PublicCertError })
	if st := p.Status(); !strings.Contains(st.Error, "rate limited") {
		t.Errorf("status = %+v", st)
	}
	if p.AdvertiseHost() != "" {
		t.Error("advertised without a certificate")
	}
	if cn := handshakeCN(t, addr, testPublicHost); cn != "box.local" {
		t.Errorf("public hostname after failure got %q", cn)
	}
}

func TestPublicCertFromConfig(t *testing.T) {
	t.Run("unset is nil", func(t *testing.T) {
		if p := newPublicCertFromConfig(t.TempDir(), config.ServeConfig{}, nil, nil); p != nil {
			t.Error("PublicCert built without public_hostname")
		}
	})
	t.Run("invalid hostname reported, not fatal", func(t *testing.T) {
		p := newPublicCertFromConfig(t.TempDir(), config.ServeConfig{PublicHostname: "blsmth.com"}, nil, nil)
		st := p.Status()
		if st == nil || st.State != PublicCertError || !strings.Contains(st.Error, "three labels") {
			t.Errorf("status = %+v", st)
		}
		p.start(context.Background())
		p.stop()
	})
	t.Run("missing token reported at startup", func(t *testing.T) {
		dir := t.TempDir()
		// The real ACME issuer is built but never started: no network.
		p := newPublicCertFromConfig(dir, config.ServeConfig{PublicHostname: testPublicHost}, nil, nil)
		t.Cleanup(p.stop)
		st := p.Status()
		if st.State != PublicCertError || !strings.Contains(st.Error, CloudflareTokenFile) {
			t.Errorf("status = %+v", st)
		}
	})
	t.Run("loose token reported at startup", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(tokenPath(dir), []byte("tok"), 0o644); err != nil {
			t.Fatal(err)
		}
		p := newPublicCertFromConfig(dir, config.ServeConfig{PublicHostname: testPublicHost}, nil, nil)
		t.Cleanup(p.stop)
		if st := p.Status(); st.State != PublicCertError || !strings.Contains(st.Error, "chmod 600") {
			t.Errorf("status = %+v", st)
		}
	})
}

func TestCloudflareProviderRefusesBadToken(t *testing.T) {
	dir := t.TempDir()
	prov := &cloudflareTokenProvider{path: tokenPath(dir)}
	if _, err := prov.AppendRecords(context.Background(), "example.com.", nil); err == nil || !strings.Contains(err.Error(), "no Cloudflare API token") {
		t.Errorf("AppendRecords err = %v", err)
	}
	if _, err := prov.DeleteRecords(context.Background(), "example.com.", nil); err == nil {
		t.Error("DeleteRecords succeeded without a token")
	}
}

func TestCertmagicEvents(t *testing.T) {
	var got []string
	on := certmagicEvents(func(kind string, err error) {
		s := kind
		if err != nil {
			s += ":" + err.Error()
		}
		got = append(got, s)
	})
	ctx := context.Background()
	_ = on(ctx, "cert_obtaining", nil)
	_ = on(ctx, "cert_failed", map[string]any{"error": errors.New("boom")})
	_ = on(ctx, "cert_failed", map[string]any{})
	_ = on(ctx, "cert_obtained", nil)
	_ = on(ctx, "cached_managed_cert", nil)
	_ = on(ctx, "tls_get_certificate", nil)
	want := []string{"obtaining", "failed:boom", "failed:certificate request failed", "obtained", "obtained"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("events = %v, want %v", got, want)
	}
}

// serveTLS runs an HTTPS server on loopback with cfg, the way Run does (a
// tls.NewListener, so nothing adds certificates), and returns its addr.
func serveTLS(t *testing.T, cfg *tls.Config) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := newHTTPServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.ErrorLog = log.New(io.Discard, "", 0)
	go func() { _ = srv.Serve(tls.NewListener(ln, cfg)) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

// handshakeCN completes a TLS handshake with sni and returns the served
// leaf's common name.
func handshakeCN(t *testing.T, addr, sni string) string {
	t.Helper()
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", addr, &tls.Config{
		ServerName:         sni,
		InsecureSkipVerify: true, //nolint:gosec // the test inspects which certificate is served
	})
	if err != nil {
		t.Fatalf("handshake with SNI %q: %v", sni, err)
	}
	defer func() { _ = conn.Close() }()
	return conn.ConnectionState().PeerCertificates[0].Subject.CommonName
}
