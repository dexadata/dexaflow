package egress

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

func TestPolicyBlocksNonPublicDestinations(t *testing.T) {
	p, err := NewPolicy(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		"127.0.0.1", "::1", // loopback
		"10.1.2.3", "172.16.0.1", "192.168.1.1", "fc00::1", // private
		"169.254.169.254", "fe80::1", // link-local, incl. cloud metadata
		"fd00:ec2::254",   // IPv6 metadata (unique local)
		"100.100.100.200", // shared address space (CGNAT), metadata on some clouds
		"0.0.0.0", "::",   // unspecified
		"224.0.0.1", "ff02::1", // multicast
		"255.255.255.255",            // broadcast
		"::ffff:127.0.0.1",           // IPv4-mapped loopback
		"::ffff:169.254.169.254",     // IPv4-mapped metadata
		"::127.0.0.1", "::a9fe:a9fe", // IPv4-compatible loopback and metadata
		"::ffff:0:7f00:1",    // SIIT-translated loopback
		"64:ff9b::a9fe:a9fe", // NAT64 carrying the metadata address
		"64:ff9b::7f00:1",    // NAT64 carrying loopback
		"2002:7f00:1::1",     // 6to4 carrying loopback
		"64:ff9b:1::1",       // local-use NAT64
		"2001::1",            // Teredo / IETF assignments
		"fec0::1",            // deprecated site-local
		"100::1",             // discard-only
		"fe80::1%eth0",       // zoned link-local
		"168.63.129.16",      // Azure host endpoint
		"100.64.0.0", "100.127.255.255", "198.18.0.1", "198.19.255.255", "240.0.0.1",
	} {
		if err := p.Check(netip.MustParseAddr(s)); !errors.Is(err, ErrBlockedDestination) {
			t.Errorf("Check(%s) = %v, want ErrBlockedDestination", s, err)
		}
	}
}

func TestPolicyAllowsPublicDestinations(t *testing.T) {
	p, err := NewPolicy(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		"8.8.8.8", "1.1.1.1", "2001:4860:4860::8888",
		"100.63.255.255", "100.128.0.0", "198.17.255.255", "198.20.0.0", // just outside blocked ranges
		"64:ff9b::808:808", // NAT64 carrying 8.8.8.8 (IPv6-only cluster behind DNS64)
		"2002:808:808::1",  // 6to4 carrying 8.8.8.8
	} {
		if err := p.Check(netip.MustParseAddr(s)); err != nil {
			t.Errorf("Check(%s) = %v, want allowed", s, err)
		}
	}
}

func TestPolicyAllowedCIDRsOverrideTheBlock(t *testing.T) {
	p, err := NewPolicy([]string{"10.20.0.0/16", "192.168.5.7"})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"10.20.3.4", "192.168.5.7"} {
		if err := p.Check(netip.MustParseAddr(s)); err != nil {
			t.Errorf("Check(%s) = %v, want allowed by allowed_cidrs", s, err)
		}
	}
	if err := p.Check(netip.MustParseAddr("10.21.0.1")); !errors.Is(err, ErrBlockedDestination) {
		t.Errorf("Check(10.21.0.1) = %v, want blocked (outside the allowed range)", err)
	}
}

// TestPolicyAllowedEntriesMatchHowAddressesAreChecked: mapped, zoned and
// space-padded entries match the address Check actually compares.
func TestPolicyAllowedEntriesMatchHowAddressesAreChecked(t *testing.T) {
	p, err := NewPolicy([]string{"::ffff:10.0.0.0/104", " 192.168.5.7 ", "fe80::1%eth0", "10.30.0.0/16"})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"10.1.2.3", "192.168.5.7", "fe80::1%eth1", "64:ff9b::a1e:1"} {
		if err := p.Check(netip.MustParseAddr(s)); err != nil {
			t.Errorf("Check(%s) = %v, want allowed", s, err)
		}
	}
}

func TestNewPolicyRejectsAnInvalidCIDR(t *testing.T) {
	if _, err := NewPolicy([]string{"10.0.0.0/33"}); err == nil {
		t.Error("NewPolicy accepted an invalid CIDR")
	}
	if _, err := NewPolicy([]string{"not-an-ip"}); err == nil {
		t.Error("NewPolicy accepted a non-address")
	}
}

// TestClientRefusesALoopbackServer: the guarded client checks the address it
// actually dials, so a URL naming a loopback server never connects.
func TestClientRefusesALoopbackServer(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	p, err := NewPolicy(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	// By IP and by a name that resolves to loopback: the name is resolved at
	// dial time and the resolved address is what the guard checks.
	for _, url := range []string{srv.URL, "http://localhost:" + port} {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, url, http.NoBody)
		resp, err := p.Client(5 * time.Second).Do(req)
		if err == nil {
			_ = resp.Body.Close()
			t.Fatalf("POST %s succeeded, want blocked", url)
		}
		if !errors.Is(err, ErrBlockedDestination) {
			t.Errorf("POST %s error = %v, want ErrBlockedDestination", url, err)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("blocked server received %d requests, want 0", n)
	}
}

// TestClientReachesAnAllowedServer: an operator-allowed range is reachable.
func TestClientReachesAnAllowedServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer srv.Close()
	p, err := NewPolicy([]string{"127.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, http.NoBody)
	resp, err := p.Client(5 * time.Second).Do(req)
	if err != nil {
		t.Fatalf("POST to an allowed range: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}
}

// TestClientRefusesARedirectToABlockedDestination: every hop is dialed through
// the same guard, so an allowed endpoint cannot bounce the request inward.
func TestClientRefusesARedirectToABlockedDestination(t *testing.T) {
	// The redirector listens on 127.0.0.1, which the policy allows; the hop it
	// redirects to (127.0.0.2) is not. Both are IPv4, so the test needs no IPv6.
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.2:1/", http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()
	p, err := NewPolicy([]string{"127.0.0.1/32"})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, redirector.URL, http.NoBody)
	resp, err := p.Client(5 * time.Second).Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("redirect to 127.0.0.2 succeeded, want blocked")
	}
	if !errors.Is(err, ErrBlockedDestination) {
		t.Errorf("redirect error = %v, want ErrBlockedDestination", err)
	}
}

// TestClientBoundsRedirects: a redirect loop on an allowed host stops after
// maxRedirects hops.
func TestClientBoundsRedirects(t *testing.T) {
	var hops atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hops.Add(1)
		http.Redirect(w, r, r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	p, err := NewPolicy([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, http.NoBody)
	resp, err := p.Client(5 * time.Second).Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("redirect loop succeeded, want it stopped")
	}
	if n := hops.Load(); n != maxRedirects+1 {
		t.Errorf("hops = %d, want %d (the request plus %d redirects)", n, maxRedirects+1, maxRedirects)
	}
}
