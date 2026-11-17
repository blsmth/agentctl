package serve

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/safefs"
)

// Certificate files under the serve dir.
const (
	CACertFile     = "ca.crt"
	CAKeyFile      = "ca.key"
	ServerCertFile = "server.crt"
	ServerKeyFile  = "server.key"
)

const (
	caValidity = 10 * 365 * 24 * time.Hour
	// leafValidity stays under the 825 days iOS accepts for TLS server
	// certificates.
	leafValidity = 824 * 24 * time.Hour
	// leafRenewBefore regenerates the leaf when less than this is left.
	leafRenewBefore = 30 * 24 * time.Hour
	// clockSkew backdates NotBefore so a phone whose clock runs slightly
	// behind still accepts a freshly minted certificate.
	clockSkew = time.Hour
)

// CA is the local certificate authority the phone trusts.
type CA struct {
	Cert *x509.Certificate
	Key  *ecdsa.PrivateKey
}

// Fingerprint returns the SHA-256 of the CA certificate DER as colon-
// separated uppercase hex, the form iOS shows under Certificate Trust
// Settings.
func (ca *CA) Fingerprint() string {
	return Fingerprint(ca.Cert.Raw)
}

// Fingerprint formats the SHA-256 of der as AA:BB:...
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	h := strings.ToUpper(hex.EncodeToString(sum[:]))
	parts := make([]string, 0, len(sum))
	for i := 0; i < len(h); i += 2 {
		parts = append(parts, h[i:i+2])
	}
	return strings.Join(parts, ":")
}

// LoadOrCreateCA loads <dir>/ca.crt and ca.key, creating them when neither
// exists. A new CA is name-constrained to local names (see caConstraints);
// hostnames are the configured serve.hostnames, added to the permitted DNS
// domains when they are not already under a local suffix. An existing but unreadable or mismatched CA is an error: the CA is
// never regenerated automatically, because every paired phone trusts it.
func LoadOrCreateCA(dir, hostname string, hostnames []string, now time.Time) (*CA, error) {
	certPath, keyPath := filepath.Join(dir, CACertFile), filepath.Join(dir, CAKeyFile)
	_, certErr := os.Stat(certPath)
	_, keyErr := os.Stat(keyPath)
	if errors.Is(certErr, os.ErrNotExist) && errors.Is(keyErr, os.ErrNotExist) {
		return createCA(dir, hostname, hostnames, now)
	}
	return LoadCA(dir)
}

// LoadCA loads an existing CA from dir.
func LoadCA(dir string) (*CA, error) {
	cert, err := readCert(filepath.Join(dir, CACertFile))
	if err != nil {
		return nil, fmt.Errorf("loading CA (delete %s and %s to start over, then re-pair every device): %w",
			filepath.Join(dir, CACertFile), filepath.Join(dir, CAKeyFile), err)
	}
	key, err := readKey(filepath.Join(dir, CAKeyFile))
	if err != nil {
		return nil, fmt.Errorf("loading CA key: %w", err)
	}
	if !key.PublicKey.Equal(cert.PublicKey) {
		return nil, errors.New("loading CA: ca.key does not match ca.crt")
	}
	return &CA{Cert: cert, Key: key}, nil
}

func createCA(dir, hostname string, hostnames []string, now time.Time) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating CA key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	ski, err := subjectKeyID(&key.PublicKey)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: fmt.Sprintf("agentctl local CA (%s)", hostname), Organization: []string{"agentctl"}},
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              now.Add(caValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
		SubjectKeyId:          ski,

		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         permittedDNSDomains(hostnames),
		PermittedIPRanges:           permittedIPRanges(),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("creating CA certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parsing CA certificate: %w", err)
	}
	if err := writeKeyPair(dir, CACertFile, CAKeyFile, der, key); err != nil {
		return nil, err
	}
	return &CA{Cert: cert, Key: key}, nil
}

// SANs is the subject-alternative-name set of the server certificate.
type SANs struct {
	DNS []string
	IPs []net.IP
}

// DesiredSANs computes the leaf SANs: configured hostnames plus
// <short hostname>.local, and every non-loopback, non-link-local interface
// address. The result is lowercased, deduplicated and sorted so it can be
// compared with an existing certificate.
func DesiredSANs(hostnames []string, osHostname string, addrs []net.Addr) SANs {
	var dns []string
	add := func(h string) {
		h = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
		if h != "" && !slices.Contains(dns, h) {
			dns = append(dns, h)
		}
	}
	for _, h := range hostnames {
		add(h)
	}
	if short, _, _ := strings.Cut(osHostname, "."); short != "" {
		add(short + ".local")
	}
	var ips []net.IP
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		default:
			continue
		}
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			continue
		}
		if v4 := ip.To4(); v4 != nil {
			ip = v4
		}
		if !slices.ContainsFunc(ips, ip.Equal) {
			ips = append(ips, ip)
		}
	}
	return normalizeSANs(SANs{DNS: dns, IPs: ips})
}

func normalizeSANs(s SANs) SANs {
	dns := make([]string, 0, len(s.DNS))
	for _, d := range s.DNS {
		dns = append(dns, strings.ToLower(d))
	}
	slices.Sort(dns)
	dns = slices.Compact(dns)
	ips := make([]net.IP, 0, len(s.IPs))
	for _, ip := range s.IPs {
		if v4 := ip.To4(); v4 != nil {
			ip = v4
		}
		ips = append(ips, ip)
	}
	slices.SortFunc(ips, func(a, b net.IP) int { return strings.Compare(string(a.To16()), string(b.To16())) })
	ips = slices.CompactFunc(ips, net.IP.Equal)
	return SANs{DNS: dns, IPs: ips}
}

// Equal reports whether two SAN sets name the same hosts.
func (s SANs) Equal(o SANs) bool {
	a, b := normalizeSANs(s), normalizeSANs(o)
	return slices.Equal(a.DNS, b.DNS) && slices.EqualFunc(a.IPs, b.IPs, net.IP.Equal)
}

// EnsureLeaf returns the server certificate, regenerating it when it is
// missing, unreadable, not signed by ca, has different SANs than want, or
// has less than 30 days left. regenerated reports whether a new leaf was
// written.
//
// SANs the CA name constraints do not permit are dropped (with a warning),
// since a single such SAN makes the whole leaf fail verification.
func EnsureLeaf(dir string, ca *CA, want SANs, now time.Time) (cert tls.Certificate, regenerated bool, err error) {
	want, dropped := FilterSANs(ca, want)
	for _, d := range dropped {
		logging.Warn("%s", sanOutsideConstraints(d))
	}
	if c, ok := loadLeaf(dir, ca, want, now); ok {
		return c, false, nil
	}
	c, err := createLeaf(dir, ca, want, now)
	if err != nil {
		return tls.Certificate{}, false, err
	}
	return c, true, nil
}

// loadLeaf returns the on-disk leaf when it is still usable.
func loadLeaf(dir string, ca *CA, want SANs, now time.Time) (tls.Certificate, bool) {
	pair, err := tls.LoadX509KeyPair(filepath.Join(dir, ServerCertFile), filepath.Join(dir, ServerKeyFile))
	if err != nil {
		return tls.Certificate{}, false
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return tls.Certificate{}, false
	}
	if leaf.CheckSignatureFrom(ca.Cert) != nil {
		return tls.Certificate{}, false
	}
	if leaf.NotAfter.Sub(now) < leafRenewBefore || now.Before(leaf.NotBefore) {
		return tls.Certificate{}, false
	}
	if !(SANs{DNS: leaf.DNSNames, IPs: leaf.IPAddresses}).Equal(want) {
		return tls.Certificate{}, false
	}
	pair.Leaf = leaf
	pair.Certificate = append(pair.Certificate[:1:1], ca.Cert.Raw)
	return pair, true
}

func createLeaf(dir string, ca *CA, want SANs, now time.Time) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generating server key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return tls.Certificate{}, err
	}
	want = normalizeSANs(want)
	cn := "agentctl"
	if len(want.DNS) > 0 {
		cn = want.DNS[0]
	}
	tmpl := &x509.Certificate{
		SerialNumber:   serial,
		Subject:        pkix.Name{CommonName: cn, Organization: []string{"agentctl"}},
		NotBefore:      now.Add(-clockSkew),
		NotAfter:       now.Add(leafValidity),
		KeyUsage:       x509.KeyUsageDigitalSignature,
		ExtKeyUsage:    []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:       want.DNS,
		IPAddresses:    want.IPs,
		AuthorityKeyId: ca.Cert.SubjectKeyId,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.Key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("creating server certificate: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("parsing server certificate: %w", err)
	}
	if err := writeKeyPair(dir, ServerCertFile, ServerKeyFile, der, key); err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der, ca.Cert.Raw}, PrivateKey: key, Leaf: leaf}, nil
}

// ReadLeaf parses <dir>/server.crt for status output.
func ReadLeaf(dir string) (*x509.Certificate, error) {
	return readCert(filepath.Join(dir, ServerCertFile))
}

func writeKeyPair(dir, certName, keyName string, der []byte, key *ecdsa.PrivateKey) error {
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return fmt.Errorf("encoding key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := safefs.WriteFile(dir, filepath.Join(dir, keyName), keyPEM, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", keyName, err)
	}
	if err := safefs.WriteFile(dir, filepath.Join(dir, certName), certPEM, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", certName, err)
	}
	return nil
}

func readCert(path string) (*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("%s: no PEM certificate", path)
	}
	return x509.ParseCertificate(block.Bytes)
}

func readKey(path string) (*ecdsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("%s: no PEM key", path)
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	ek, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s: not an ECDSA key", path)
	}
	return ek, nil
}

func randomSerial() (*big.Int, error) {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, fmt.Errorf("generating serial: %w", err)
	}
	return n.Add(n, big.NewInt(1)), nil
}

func subjectKeyID(pub *ecdsa.PublicKey) ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("encoding public key: %w", err)
	}
	sum := sha256.Sum256(der)
	return sum[:20], nil
}
