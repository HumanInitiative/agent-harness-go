package websearch

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Guard decides which URLs and network destinations may be fetched. Its job
// is to stop server-side request forgery: a model (or a prompt-injected page)
// asking us to fetch http://169.254.169.254/ or http://localhost:8080/admin
// must never reach those addresses.
//
// URLs are checked up front (CheckURL) and, more importantly, every actual
// TCP connection is checked after DNS resolution (DialContext). Checking the
// resolved IP at connect time is what defeats DNS rebinding, where a name
// resolves to a public IP during validation and a private one when used.
type Guard struct {
	// AllowedPorts lists the ports URLs may use. Empty means 80 and 443.
	AllowedPorts []int
	// AllowPrivateNetworks disables the IP checks. Only for tests against
	// httptest servers; never enable it in production.
	AllowPrivateNetworks bool
}

// blockedPrefixes are destinations that are never public internet hosts.
// netip's IsPrivate/IsLoopback/... cover the common ranges; these cover
// the rest of the special-purpose registry entries that matter for SSRF.
var blockedPrefixes = mustPrefixes(
	"0.0.0.0/8",       // "this network"
	"100.64.0.0/10",   // carrier-grade NAT, often internal
	"192.0.0.0/24",    // IETF protocol assignments
	"192.0.2.0/24",    // documentation
	"198.18.0.0/15",   // benchmarking
	"198.51.100.0/24", // documentation
	"203.0.113.0/24",  // documentation
	"240.0.0.0/4",     // reserved, includes 255.255.255.255
	"64:ff9b::/96",    // NAT64: could embed a private IPv4 address
	"64:ff9b:1::/48",  // local-use NAT64
	"2001:db8::/32",   // documentation
	"100::/64",        // discard-only
)

func mustPrefixes(cidrs ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(cidrs))
	for i, c := range cidrs {
		out[i] = netip.MustParsePrefix(c)
	}
	return out
}

// IsPublicIP reports whether ip is a globally routable unicast address,
// i.e. not loopback, private, link-local (including cloud metadata at
// 169.254.169.254), multicast, unspecified, or otherwise special-purpose.
func IsPublicIP(ip netip.Addr) bool {
	ip = ip.Unmap() // ::ffff:127.0.0.1 is 127.0.0.1
	if !ip.IsValid() ||
		ip.IsLoopback() ||
		ip.IsPrivate() || // 10/8, 172.16/12, 192.168/16, fc00::/7
		ip.IsLinkLocalUnicast() || // 169.254/16, fe80::/10
		ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsUnspecified() {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// CheckURL validates a URL before any request is made: http(s) only, no
// embedded credentials, an allowed port, and — when the host is an IP
// literal — a public IP. Hostnames are checked at connection time instead,
// after DNS resolution.
func (g Guard) CheckURL(u *url.URL) error {
	if u == nil {
		return fmt.Errorf("%w: empty URL", ErrInvalidURL)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: scheme %q is not http or https", ErrInvalidURL, u.Scheme)
	}
	if u.User != nil {
		return fmt.Errorf("%w: URLs with embedded credentials are not allowed", ErrInvalidURL)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%w: missing host", ErrInvalidURL)
	}
	if !g.portAllowed(u) {
		return fmt.Errorf("%w: port %s is not allowed", ErrInvalidURL, u.Port())
	}
	if g.AllowPrivateNetworks {
		return nil
	}
	if ip, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil && !IsPublicIP(ip) {
		return fmt.Errorf("%w: %s is not a public address", ErrSSRF, ip)
	}
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return fmt.Errorf("%w: %s is a loopback name", ErrSSRF, host)
	}
	return nil
}

func (g Guard) portAllowed(u *url.URL) bool {
	port := u.Port()
	if port == "" {
		return true // the scheme's default port, 80 or 443
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return false
	}
	allowed := g.AllowedPorts
	if len(allowed) == 0 {
		allowed = []int{80, 443}
	}
	for _, p := range allowed {
		if p == n {
			return true
		}
	}
	return false
}

// DialContext dials like net.Dialer but refuses, at the socket level, to
// connect to any non-public IP. Use it as http.Transport.DialContext.
func (g Guard) DialContext(timeout time.Duration) func(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{
		Timeout: timeout,
		// Control runs for every address the dialer actually tries, after
		// resolution and immediately before connect, so the address checked
		// is exactly the address used.
		Control: func(_, address string, _ syscall.RawConn) error {
			if g.AllowPrivateNetworks {
				return nil
			}
			ap, err := netip.ParseAddrPort(address)
			if err != nil {
				return fmt.Errorf("%w: cannot parse dial address %q", ErrSSRF, address)
			}
			if !IsPublicIP(ap.Addr()) {
				return fmt.Errorf("%w: %s is not a public address", ErrSSRF, ap.Addr())
			}
			return nil
		},
	}
	return dialer.DialContext
}
