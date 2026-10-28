package api

import (
	"net"
	"strings"

	"github.com/gin-gonic/gin"
)

// hostPatternTenant is the placeholder auth.trusted_issuer.host_pattern
// replaces with the session's tenant (config.HostPatternTenant; a test keeps
// the two equal).
const hostPatternTenant = "{tenant}"

// hostBinding ties a browser session to its tenant's host
// (auth.trusted_issuer.host_pattern, #1463).
//
// An operator that serves each tenant on its own host in front of one engine
// would otherwise have nothing that ties a session to the host it is used on:
// a handoff for acme posted to globex's host opens an acme session there, and
// a session cookie a page on one tenant's host plants for its own tenant is
// accepted on every other tenant's host. Tenant scoping of the data holds
// either way; what the binding removes is a person working in one tenant
// under another tenant's address, and the session fixation path that needs
// exactly that.
//
// A nil *hostBinding binds nothing, which is the default.
type hostBinding struct {
	pattern string
	// trusted are the peers whose X-Forwarded-Host names the host
	// (server.trusted_proxies). From any other peer the header is the
	// client's own choice and is ignored.
	trusted []*net.IPNet
}

// newHostBinding returns the binding for pattern, or nil when it is empty.
// trustedProxies are parsed the way the router parses them: an IP or a CIDR.
func newHostBinding(pattern string, trustedProxies []string) *hostBinding {
	if pattern == "" {
		return nil
	}
	b := &hostBinding{pattern: strings.ToLower(pattern)}
	for _, p := range trustedProxies {
		if ip := net.ParseIP(p); ip != nil {
			if v4 := ip.To4(); v4 != nil {
				ip = v4
			}
			b.trusted = append(b.trusted, &net.IPNet{IP: ip, Mask: net.CIDRMask(len(ip)*8, len(ip)*8)})
			continue
		}
		if _, n, err := net.ParseCIDR(p); err == nil {
			b.trusted = append(b.trusted, n)
		}
	}
	return b
}

// allows reports whether tenant's session may be used on this request's host.
// A nil binding allows every host.
func (b *hostBinding) allows(c *gin.Context, tenant string) bool {
	if b == nil {
		return true
	}
	want := strings.Replace(b.pattern, hostPatternTenant, strings.ToLower(tenant), 1)
	got := strings.ToLower(b.requestHost(c))
	if !strings.Contains(b.pattern, ":") {
		got = stripPort(got)
	}
	return got == want
}

// requestHost is the host the browser addressed: X-Forwarded-Host (its first
// value) when the direct peer is a trusted proxy, else the Host header.
func (b *hostBinding) requestHost(c *gin.Context) string {
	if fwd := c.GetHeader("X-Forwarded-Host"); fwd != "" && b.fromTrustedProxy(c) {
		first, _, _ := strings.Cut(fwd, ",")
		return strings.TrimSpace(first)
	}
	return c.Request.Host
}

func (b *hostBinding) fromTrustedProxy(c *gin.Context) bool {
	ip := net.ParseIP(c.RemoteIP())
	if ip == nil {
		return false
	}
	for _, n := range b.trusted {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// stripPort drops a :port from host, keeping a bracketed IPv6 address whole.
func stripPort(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}
