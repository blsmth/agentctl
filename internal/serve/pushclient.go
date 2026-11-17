package serve

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// errRedirectRefused is returned for any redirect a push service answers
// with. Push services do not redirect; following one would let an endpoint
// that passed Subscription.Validate send the request somewhere else.
var errRedirectRefused = errors.New("push: redirect refused")

// errDialRefused is returned when a push request would connect to an
// address that is not public.
var errDialRefused = errors.New("push: refusing to connect to a non-public address")

// cgnatPrefix is the shared address space (RFC 6598), used by carrier NAT
// and Tailscale: never a push service.
var cgnatPrefix = netip.MustParsePrefix("100.64.0.0/10")

// newPushClient is the HTTP client for Web Push. Subscription.Validate
// already restricts endpoints to known push services; this is defense in
// depth against DNS that resolves one of them to a private address
// (rebinding, a hostile resolver): the dialer refuses every non-public IP
// after resolution, redirects are refused, and environment proxies are not
// used (the dial check would only see the proxy).
func newPushClient() *http.Client {
	dialer := &net.Dialer{Timeout: pushTimeout, Control: refuseNonPublicDial}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   pushTimeout,
		ResponseHeaderTimeout: pushTimeout,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          4,
	}
	return &http.Client{Timeout: pushTimeout, Transport: transport, CheckRedirect: refuseRedirect}
}

func refuseRedirect(*http.Request, []*http.Request) error { return errRedirectRefused }

// refuseNonPublicDial is a net.Dialer Control: it runs after DNS resolution,
// on the IP actually being dialed, and refuses loopback, private, link-local,
// unspecified, multicast and CGNAT addresses.
func refuseNonPublicDial(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: %s", errDialRefused, address)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !isPublicAddr(ip) {
		return fmt.Errorf("%w: %s", errDialRefused, address)
	}
	return nil
}

// isPublicAddr reports whether ip is a routable public unicast address.
func isPublicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	switch {
	case !ip.IsValid(),
		ip.IsLoopback(),
		ip.IsPrivate(),
		ip.IsLinkLocalUnicast(),
		ip.IsLinkLocalMulticast(),
		ip.IsInterfaceLocalMulticast(),
		ip.IsMulticast(),
		ip.IsUnspecified(),
		ip.Is4() && ip.As4()[0] == 0, // 0.0.0.0/8, "this network"
		cgnatPrefix.Contains(ip):
		return false
	}
	return true
}
