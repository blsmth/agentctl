package serve

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/blsmth/agentctl/internal/logging"
)

// Files for the optional public certificate (serve.public_hostname), under
// the serve dir.
const (
	// CloudflareTokenFile holds the Cloudflare API token (Zone:DNS:Edit) used
	// for the DNS-01 challenge. It must be 0600 or stricter.
	CloudflareTokenFile = "cloudflare.token"
	// ACMEDir is certmagic's storage: the ACME account, the certificate and
	// its key, and lock files. 0700.
	ACMEDir = "acme"
)

// maxTokenBytes bounds what readCloudflareToken reads.
const maxTokenBytes = 4 << 10

// Public certificate states reported by `serve status`.
const (
	PublicCertNone      = "none"
	PublicCertObtaining = "obtaining"
	PublicCertValid     = "valid"
	PublicCertError     = "error"
)

// PublicCertStatus is the public certificate's state for `serve status`.
type PublicCertStatus struct {
	Hostname string `json:"hostname"`
	// Subject is the certificate's name: the wildcard *.<parent of Hostname>.
	Subject string `json:"subject"`
	State   string `json:"state"`
	// Expires is the held certificate's NotAfter (zero when none is held).
	Expires time.Time `json:"expires,omitzero"`
	// Error is the most recent failure (config, token, ACME), if any. A
	// valid certificate with an Error means a renewal attempt failed.
	Error string `json:"error,omitempty"`
	// Staging is set when serve.acme.staging is on: the certificate is from
	// the Let's Encrypt staging CA, which no browser trusts, so it is served
	// for its name but never advertised.
	Staging bool `json:"staging,omitempty"`
}

// certSource is the ACME backend behind PublicCert: certmagic in
// production (newCertmagicSource), a fake in tests. Names are certificate
// subjects (the wildcard), which are also certmagic's storage keys.
type certSource interface {
	// Manage starts obtaining and renewing subject in the background. It
	// must not wait on the network.
	Manage(ctx context.Context, subject string) error
	// Lookup returns the held certificate for subject, or nil.
	Lookup(subject string) *tls.Certificate
	// Stop ends background maintenance.
	Stop()
}

// PublicCert serves and tracks the publicly trusted certificate for
// serve.public_hostname. The certificate is the wildcard for the
// hostname's parent, so the machine's name never appears in Certificate
// Transparency logs; only the exact hostname is routed to it. A nil
// *PublicCert is valid and means the feature is off: it serves nothing and
// advertises nothing.
type PublicCert struct {
	host    string
	subject string
	staging bool
	src     certSource
	now     func() time.Time

	mu        sync.Mutex
	obtaining bool
	lastErr   string
}

// newPublicCert returns a PublicCert for host backed by src. configErr, when
// set, is reported as the state's error (an invalid hostname, or a backend
// that could not be built); src may then be nil.
func newPublicCert(host string, staging bool, src certSource, now func() time.Time, configErr error) *PublicCert {
	if now == nil {
		now = time.Now
	}
	p := &PublicCert{host: host, subject: wildcardSubject(host), staging: staging, src: src, now: now}
	if configErr != nil {
		p.lastErr = configErr.Error()
	}
	return p
}

// Hostname is the configured public hostname ("" when p is nil).
func (p *PublicCert) Hostname() string {
	if p == nil {
		return ""
	}
	return p.host
}

// start hands the hostname to the backend. It never blocks on the network
// and never fails serve: problems are logged and reported in Status.
func (p *PublicCert) start(ctx context.Context) {
	if p == nil || p.src == nil || p.subject == "" {
		return
	}
	go func() {
		if err := p.src.Manage(ctx, p.subject); err != nil {
			p.event("failed", err)
		}
	}()
}

// stop ends the backend's maintenance.
func (p *PublicCert) stop() {
	if p != nil && p.src != nil {
		p.src.Stop()
	}
}

// event records a backend event: "obtaining", "obtained" or "failed".
func (p *PublicCert) event(kind string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch kind {
	case "obtaining":
		p.obtaining = true
		logging.Log("serve: public certificate: requesting %s", p.subject)
	case "obtained":
		p.obtaining = false
		p.lastErr = ""
		logging.Log("serve: public certificate: %s ready, serving it for %s", p.subject, p.host)
	case "failed":
		p.obtaining = false
		msg := "unknown error"
		if err != nil {
			msg = err.Error()
		}
		p.lastErr = msg
		logging.Warn("serve: public certificate for %s: %s (will retry)", p.subject, msg)
	}
}

// held returns the backend's certificate for the public hostname, or nil.
func (p *PublicCert) held() *tls.Certificate {
	if p == nil || p.src == nil || p.subject == "" {
		return nil
	}
	c := p.src.Lookup(p.subject)
	if c == nil || len(c.Certificate) == 0 {
		return nil
	}
	if c.Leaf == nil {
		leaf, err := x509.ParseCertificate(c.Certificate[0])
		if err != nil {
			return nil
		}
		c.Leaf = leaf
	}
	return c
}

// validNow reports whether c is inside its validity window by our clock.
func (p *PublicCert) validNow(c *tls.Certificate) bool {
	now := p.now()
	return !now.Before(c.Leaf.NotBefore) && now.Before(c.Leaf.NotAfter)
}

// AdvertiseHost is the public hostname while a valid, publicly trusted
// certificate for it is held, else "". Callers fall back to the local host.
func (p *PublicCert) AdvertiseHost() string {
	if p == nil || p.staging {
		return ""
	}
	if c := p.held(); c != nil && p.validNow(c) {
		return p.host
	}
	return ""
}

// Status reports the certificate state. Nil when the feature is off.
func (p *PublicCert) Status() *PublicCertStatus {
	if p == nil {
		return nil
	}
	c := p.held()
	p.mu.Lock()
	defer p.mu.Unlock()
	st := &PublicCertStatus{Hostname: p.host, Subject: p.subject, Staging: p.staging, Error: p.lastErr}
	switch {
	case c != nil && p.validNow(c):
		st.State, st.Expires = PublicCertValid, c.Leaf.NotAfter
	case c != nil:
		st.State, st.Expires = PublicCertError, c.Leaf.NotAfter
		st.Error = fmt.Sprintf("certificate is valid %s to %s, not now (%s): check the clock",
			c.Leaf.NotBefore.UTC().Format(time.RFC3339), c.Leaf.NotAfter.UTC().Format(time.RFC3339), p.now().UTC().Format(time.RFC3339))
	case p.obtaining:
		st.State = PublicCertObtaining
	case p.lastErr != "":
		st.State = PublicCertError
	default:
		st.State = PublicCertNone
	}
	return st
}

// getCertificateFunc is the HTTPS listener's certificate selector: a TLS
// SNI equal to the public hostname gets the public certificate when one is
// held; everything else (IPs, .local names, serve.hostnames, no SNI, or no
// public certificate yet) gets the local-CA leaf, so paired phones are
// unaffected.
func getCertificateFunc(local *tls.Certificate, pub *PublicCert) func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		if pub != nil && hello != nil && sniMatches(hello.ServerName, pub.host) {
			if c := pub.held(); c != nil {
				return c, nil
			}
		}
		return local, nil
	}
}

// wildcardSubject is the certificate subject for host: "*." plus host
// without its first label (mac.agentctl.example.com becomes
// *.agentctl.example.com). Hosts are validated to have at least three
// labels (config.ServeConfig.EffectivePublicHostname); a host without a
// dot yields "".
func wildcardSubject(host string) string {
	_, parent, ok := strings.Cut(host, ".")
	if !ok || parent == "" {
		return ""
	}
	return "*." + parent
}

// sniMatches compares a TLS server name with host, ignoring case and a
// trailing dot. Only the exact public hostname matches: other names the
// wildcard would cover get the local leaf.
func sniMatches(sni, host string) bool {
	return host != "" && strings.EqualFold(strings.TrimSuffix(sni, "."), host)
}

// readCloudflareToken reads the Cloudflare API token from path. It refuses
// anything but a regular file readable by the owner alone, and never puts
// the token in an error.
func readCloudflareToken(path string) (string, error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("no Cloudflare API token: create %s (mode 0600) holding a token scoped to Zone:DNS:Edit", path)
	}
	if err != nil {
		return "", fmt.Errorf("reading Cloudflare API token: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", path)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return "", fmt.Errorf("%s has mode %04o, readable by others: run chmod 600 %s", path, perm, path)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("reading Cloudflare API token: %w", err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxTokenBytes))
	if err != nil {
		return "", fmt.Errorf("reading Cloudflare API token: %w", err)
	}
	tok := strings.TrimSpace(string(data))
	if tok == "" {
		return "", fmt.Errorf("%s is empty", path)
	}
	if strings.ContainsAny(tok, " \t\r\n") {
		return "", fmt.Errorf("%s must hold only the token, on one line", path)
	}
	return tok, nil
}

// tokenPath is <dir>/cloudflare.token.
func tokenPath(dir string) string { return filepath.Join(dir, CloudflareTokenFile) }
