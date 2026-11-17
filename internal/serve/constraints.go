package serve

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// localDNSSuffixes are the DNS domains a new CA may issue for. The phone
// trusts the CA as a root, so the constraints bound what a stolen ca.key
// could impersonate: local names only, never a public domain.
var localDNSSuffixes = []string{"local", "lan", "home", "home.arpa", "internal"}

// localIPRanges are the IP ranges a new CA may issue for: RFC 1918,
// CGNAT/Tailscale, loopback and IPv6 ULA.
var localIPRanges = []string{
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"100.64.0.0/10",
	"127.0.0.0/8",
	"::1/128",
	"fc00::/7",
}

// permittedDNSDomains returns localDNSSuffixes plus every configured
// hostname not already under one of them.
func permittedDNSDomains(hostnames []string) []string {
	out := append([]string(nil), localDNSSuffixes...)
	for _, h := range hostnames {
		h = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
		if h == "" || dnsPermitted(h, out) {
			continue
		}
		out = append(out, h)
	}
	return out
}

func permittedIPRanges() []*net.IPNet {
	out := make([]*net.IPNet, 0, len(localIPRanges))
	for _, c := range localIPRanges {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic(err) // constant table
		}
		out = append(out, n)
	}
	return out
}

// dnsPermitted mirrors crypto/x509's domain constraint matching: "home"
// permits "home" and any name under it; ".home" permits only names under it.
func dnsPermitted(name string, domains []string) bool {
	name = strings.ToLower(name)
	for _, d := range domains {
		d = strings.ToLower(d)
		if d == "" {
			return true
		}
		if strings.HasPrefix(d, ".") {
			if strings.HasSuffix(name, d) {
				return true
			}
			continue
		}
		if name == d || strings.HasSuffix(name, "."+d) {
			return true
		}
	}
	return false
}

// Constrained reports whether the CA carries name constraints (a CA created
// before constraints existed does not; `agentctl serve ca rotate` fixes that).
func (ca *CA) Constrained() bool {
	c := ca.Cert
	return len(c.PermittedDNSDomains) > 0 || len(c.PermittedIPRanges) > 0
}

// permits reports whether the CA's name constraints allow name (a DNS name)
// or ip. An unconstrained CA permits everything.
func (ca *CA) permitsDNS(name string) bool {
	c := ca.Cert
	if len(c.PermittedDNSDomains) == 0 {
		return true
	}
	return dnsPermitted(name, c.PermittedDNSDomains)
}

func (ca *CA) permitsIP(ip net.IP) bool {
	c := ca.Cert
	if len(c.PermittedIPRanges) == 0 {
		return true
	}
	for _, n := range c.PermittedIPRanges {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// FilterSANs drops every SAN the CA's name constraints do not permit, so the
// leaf still verifies (Go and iOS reject a leaf with any SAN outside the
// constraints). dropped names the removed entries.
func FilterSANs(ca *CA, want SANs) (kept SANs, dropped []string) {
	for _, d := range want.DNS {
		if ca.permitsDNS(d) {
			kept.DNS = append(kept.DNS, d)
		} else {
			dropped = append(dropped, d)
		}
	}
	for _, ip := range want.IPs {
		if ca.permitsIP(ip) {
			kept.IPs = append(kept.IPs, ip)
		} else {
			dropped = append(dropped, ip.String())
		}
	}
	return normalizeSANs(kept), dropped
}

// sanOutsideConstraints is the warning logged for a dropped SAN.
func sanOutsideConstraints(name string) string {
	return fmt.Sprintf("serve: hostname %s is outside the CA name constraints and is left out of the certificate; to include it, add it to serve.hostnames and run `agentctl serve ca rotate`", name)
}

// RotateCA deletes the CA, the server certificate and every paired device:
// devices trusted the old CA, so they must reinstall the profile and pair
// again. When create is true (no server running) a new CA is generated right
// away; otherwise the server creates it on its next start. It returns the new
// CA, or nil when creation was deferred.
func RotateCA(dir, hostname string, hostnames []string, create bool, now time.Time) (*CA, error) {
	for _, name := range []string{ServerCertFile, ServerKeyFile, CACertFile, CAKeyFile} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("removing %s: %w", name, err)
		}
	}
	store, err := OpenDeviceStore(dir)
	if err != nil {
		return nil, err
	}
	if err := store.Clear(); err != nil {
		return nil, err
	}
	if !create {
		return nil, nil
	}
	return LoadOrCreateCA(dir, hostname, hostnames, now)
}
