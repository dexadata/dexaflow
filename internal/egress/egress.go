// Package egress guards outbound HTTP requests whose destination comes from
// tenant-controlled data, such as an alert connection's webhook URL. A Policy
// refuses loopback, private, link-local (including the 169.254.169.254 cloud
// metadata address), shared, unspecified, multicast and broadcast addresses,
// unless the operator allows a range explicitly.
//
// The check runs on the address the dialer is about to connect to, after DNS
// resolution, not on the URL's host name. A name that resolves to a public
// address when validated and to an internal one when used (DNS rebinding) is
// therefore still refused, and so is every redirect hop, because each hop is
// dialed through the same guard.
package egress

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"syscall"
	"time"
)

// ErrBlockedDestination is returned (wrapped) when a request would connect to
// an address the Policy refuses.
var ErrBlockedDestination = errors.New("egress: destination address is not allowed")

// blocked lists the ranges refused in addition to the address classes netip
// reports directly (loopback, private, link-local, unspecified, multicast).
var blocked = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),      // "this network"
	netip.MustParsePrefix("100.64.0.0/10"),  // shared address space (RFC 6598), used for metadata on some clouds
	netip.MustParsePrefix("192.0.0.0/24"),   // IETF protocol assignments
	netip.MustParsePrefix("198.18.0.0/15"),  // benchmarking
	netip.MustParsePrefix("240.0.0.0/4"),    // reserved, includes 255.255.255.255
	netip.MustParsePrefix("64:ff9b::/96"),   // NAT64, can embed any IPv4 address
	netip.MustParsePrefix("64:ff9b:1::/48"), // local-use NAT64
	netip.MustParsePrefix("2002::/16"),      // 6to4, can embed any IPv4 address
}

// Policy decides which destination addresses an outbound request may reach.
// The zero value is not usable; build one with NewPolicy.
type Policy struct {
	allowed []netip.Prefix
}

// NewPolicy returns a Policy that refuses every non-public address except
// those inside allowedCIDRs. Each entry is a CIDR ("10.20.0.0/16") or a single
// address ("192.168.5.7"). An entry that parses as neither is an error, so a
// typo fails at startup instead of silently allowing nothing.
func NewPolicy(allowedCIDRs []string) (*Policy, error) {
	p := &Policy{}
	for _, raw := range allowedCIDRs {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		if !strings.Contains(s, "/") {
			addr, err := netip.ParseAddr(s)
			if err != nil {
				return nil, fmt.Errorf("egress: allowed CIDR %q: %w", raw, err)
			}
			p.allowed = append(p.allowed, netip.PrefixFrom(addr.Unmap(), addr.Unmap().BitLen()))
			continue
		}
		prefix, err := netip.ParsePrefix(s)
		if err != nil {
			return nil, fmt.Errorf("egress: allowed CIDR %q: %w", raw, err)
		}
		p.allowed = append(p.allowed, prefix.Masked())
	}
	return p, nil
}

// Check returns nil when addr may be dialed and an error wrapping
// ErrBlockedDestination otherwise. IPv4-mapped IPv6 addresses are checked as
// the IPv4 address they carry.
func (p *Policy) Check(addr netip.Addr) error {
	addr = addr.Unmap()
	for _, prefix := range p.allowed {
		if prefix.Contains(addr) {
			return nil
		}
	}
	if !isPublic(addr) {
		return fmt.Errorf("%w: %s", ErrBlockedDestination, addr)
	}
	return nil
}

// isPublic reports whether addr is a globally routable unicast address.
func isPublic(addr netip.Addr) bool {
	if !addr.IsValid() || !addr.IsGlobalUnicast() || addr.IsPrivate() {
		return false
	}
	for _, prefix := range blocked {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

// control is the net.Dialer hook: it runs after name resolution, on the exact
// address about to be connected, so it sees what the request will reach.
func (p *Policy) control(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w: unparseable dial address %q", ErrBlockedDestination, address)
	}
	return p.Check(ap.Addr())
}

// Client returns an HTTP client whose every connection, redirects included,
// is checked against the Policy. It dials directly and ignores the proxy
// environment variables: through a proxy the dialed address would be the
// proxy's, and the real destination could not be checked.
func (p *Policy) Client(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: p.control}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	return &http.Client{Timeout: timeout, Transport: transport}
}
