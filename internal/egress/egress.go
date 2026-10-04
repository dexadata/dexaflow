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
	netip.MustParsePrefix("0.0.0.0/8"),        // "this network"
	netip.MustParsePrefix("100.64.0.0/10"),    // shared address space (RFC 6598), used for metadata on some clouds
	netip.MustParsePrefix("192.0.0.0/24"),     // IETF protocol assignments
	netip.MustParsePrefix("198.18.0.0/15"),    // benchmarking
	netip.MustParsePrefix("240.0.0.0/4"),      // reserved, includes 255.255.255.255
	netip.MustParsePrefix("168.63.129.16/32"), // Azure host endpoint (WireServer), public-looking but node-local
	netip.MustParsePrefix("::/96"),            // IPv4-compatible IPv6 (deprecated), can carry 127.0.0.1
	netip.MustParsePrefix("::ffff:0:0:0/96"),  // SIIT IPv4-translated
	netip.MustParsePrefix("64:ff9b:1::/48"),   // local-use NAT64, any prefix length, so not decodable
	netip.MustParsePrefix("100::/64"),         // discard-only
	netip.MustParsePrefix("2001::/23"),        // IETF protocol assignments, includes Teredo 2001::/32
	netip.MustParsePrefix("fec0::/10"),        // deprecated site-local
}

// Well-known NAT64 (RFC 6052) and 6to4 (RFC 3056) embed a whole IPv4 address.
// They are checked as that address rather than refused outright, so an
// IPv6-only cluster behind DNS64 can still reach a public IPv4-only endpoint
// while an embedded private or metadata address stays refused.
var (
	nat64WellKnown = netip.MustParsePrefix("64:ff9b::/96")
	sixToFour      = netip.MustParsePrefix("2002::/16")
)

// embeddedIPv4 returns the IPv4 address a NAT64 or 6to4 address carries.
func embeddedIPv4(addr netip.Addr) (netip.Addr, bool) {
	b := addr.As16()
	switch {
	case nat64WellKnown.Contains(addr):
		return netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}), true
	case sixToFour.Contains(addr):
		return netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}), true
	}
	return netip.Addr{}, false
}

// Policy decides which destination addresses an outbound request may reach.
// The zero value is not usable; build one with NewPolicy.
type Policy struct {
	allowed []netip.Prefix
}

// NewPolicy returns a Policy that refuses every non-public address except
// those inside allowedCIDRs. Each entry is a CIDR ("10.20.0.0/16") or a single
// address ("192.168.5.7"). An entry that parses as neither is an error, so a
// typo fails at startup instead of silently allowing nothing. IPv4-mapped
// entries are stored as IPv4 and zones are dropped, matching how Check sees
// the dialed address.
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
			addr = addr.Unmap().WithZone("")
			p.allowed = append(p.allowed, netip.PrefixFrom(addr, addr.BitLen()))
			continue
		}
		prefix, err := netip.ParsePrefix(s)
		if err != nil {
			return nil, fmt.Errorf("egress: allowed CIDR %q: %w", raw, err)
		}
		if prefix.Addr().Is4In6() && prefix.Bits() >= 96 {
			prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
		}
		p.allowed = append(p.allowed, prefix.Masked())
	}
	return p, nil
}

// Check returns nil when addr may be dialed and an error wrapping
// ErrBlockedDestination otherwise. IPv4-mapped, NAT64 and 6to4 addresses are
// checked as the IPv4 address they carry; a zone is ignored.
func (p *Policy) Check(addr netip.Addr) error {
	addr = addr.Unmap().WithZone("")
	for _, prefix := range p.allowed {
		if prefix.Contains(addr) {
			return nil
		}
	}
	if v4, ok := embeddedIPv4(addr); ok {
		return p.Check(v4)
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
// proxy's, and the real destination could not be checked. It follows at most
// maxRedirects redirects.
func (p *Policy) Client(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: p.control}
	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	return &http.Client{Timeout: timeout, Transport: transport, CheckRedirect: limitRedirects}
}

// maxRedirects bounds how many redirects a guarded request follows. Every hop
// is dialed through the guard anyway; the bound limits how far a tenant's
// endpoint can bounce a POST (and its custom headers) before giving up.
const maxRedirects = 3

// limitRedirects stops after maxRedirects hops.
func limitRedirects(_ *http.Request, via []*http.Request) error {
	if len(via) > maxRedirects {
		return fmt.Errorf("egress: stopped after %d redirects", maxRedirects)
	}
	return nil
}

// sensitive are the destinations an allow list most likely re-opens by
// accident: the control plane's own loopback and the cloud metadata endpoints.
var sensitive = []struct {
	name string
	addr netip.Addr
}{
	{"loopback 127.0.0.1", netip.MustParseAddr("127.0.0.1")},
	{"loopback ::1", netip.MustParseAddr("::1")},
	{"metadata 169.254.169.254", netip.MustParseAddr("169.254.169.254")},
	{"metadata fd00:ec2::254", netip.MustParseAddr("fd00:ec2::254")},
	{"Azure host endpoint 168.63.129.16", netip.MustParseAddr("168.63.129.16")},
}

// ReopenedSensitive names the loopback and metadata destinations the allow
// list lets through, so the operator can be warned that an allowed range is
// broader than it looks. Empty when none is.
func (p *Policy) ReopenedSensitive() []string {
	var out []string
	for _, s := range sensitive {
		if p.Check(s.addr) == nil {
			out = append(out, s.name)
		}
	}
	return out
}
