package serve

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/config"
)

// TestTLSEndToEnd serves the real handler with the generated leaf and checks
// that a client trusting only the CA can reach /healthz and pair.
func TestTLSEndToEnd(t *testing.T) {
	ts := newTestServer(t, config.ServeConfig{})
	sans := SANs{DNS: []string{"host.local"}, IPs: []net.IP{net.ParseIP("127.0.0.1")}}
	leaf, _, err := EnsureLeaf(ts.dir, ts.opts.CA, sans, ts.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(ts.Handler())
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{leaf}}
	srv.Config.ErrorLog = log.New(io.Discard, "", 0) // the untrusting client below fails the handshake
	srv.StartTLS()
	defer srv.Close()

	pool := x509.NewCertPool()
	pool.AddCert(ts.opts.CA.Cert)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, Time: ts.clock.Now}}}

	resp, err := client.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("TLS request with only the CA trusted failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("healthz = %d %s", resp.StatusCode, body)
	}

	untrusting := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: x509.NewCertPool()}}}
	if _, err := untrusting.Get(srv.URL + "/healthz"); err == nil {
		t.Fatal("client without the CA accepted the certificate")
	}
}
