package oidc

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/config"
)

// TestIdPCallsAreBoundedByAClientTimeout is the regression test for the half of
// #1153 that a context deadline cannot reach.
//
// An IdP that accepts the TCP connection and never writes a response is the
// shape that matters: a refused connection fails fast and a DNS miss fails
// fast, so neither exercises anything. This server accepts and hangs.
//
// The bound under test is the CLIENT's, not the caller's. Provider.Verifier
// builds its key set over context.Background(), so a deadline on the request
// context never reaches the JWKS fetch; the client does, because the key set
// inherits the provider's client.
func TestIdPCallsAreBoundedByAClientTimeout(t *testing.T) {
	ln := silentListener(t)

	// Shrink the wait so the test is a test and not a coffee break. The shipped
	// value is asserted separately below, so shrinking here cannot hide it.
	client := &http.Client{Timeout: 400 * time.Millisecond}

	start := time.Now()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		"http://"+ln.Addr().String()+"/.well-known/openid-configuration", http.NoBody)
	if err != nil {
		t.Fatalf("building the probe request: %v", err)
	}
	resp, err := client.Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a silent IdP answered, so this test proves nothing about timeouts")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("the call took %s against a client timeout of 400ms; the timeout is not being applied", elapsed)
	}

	// And the shipped client actually carries one. A HTTPClient() that returned
	// http.DefaultClient would pass every behavioral assertion above, because
	// the assertion used its own client.
	if got := HTTPClient().Timeout; got <= 0 {
		t.Fatalf("HTTPClient() has no timeout (%v); go-oidc and oauth2 both fall back to http.DefaultClient, which has none either", got)
	}
	if got := HTTPClient().Timeout; got != httpTimeout {
		t.Fatalf("HTTPClient() timeout is %v, want %v", got, httpTimeout)
	}
}

// TestNewFlowGivesUpOnASilentIssuer drives the real entry point, so the wiring
// is under test and not just the constant.
func TestNewFlowGivesUpOnASilentIssuer(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the shipped client timeout")
	}
	ln := silentListener(t)

	// No deadline on the context ON PURPOSE. If NewFlow only works because its
	// caller passes one, the JWKS path is still unbounded and this test would be
	// lying about what protects it.
	start := time.Now()
	_, err := NewFlow(context.Background(), config.OIDCSection{
		Issuer:   "http://" + ln.Addr().String(),
		ClientID: "probe",
	}, "secret")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("NewFlow succeeded against an issuer that never answered")
	}
	if elapsed > httpTimeout+10*time.Second {
		t.Fatalf("NewFlow took %s with no context deadline; nothing bounded it", elapsed)
	}
}

// silentListener accepts connections and never writes a byte, which is the only
// peer shape that exercises a timeout: a refused connection and a DNS miss both
// fail fast on their own.
//
// The accept loop hands each connection to the cleanup rather than deferring
// inside the loop, because a defer in a loop releases nothing until the
// function returns and this loop does not return until the test ends.
func silentListener(t *testing.T) net.Listener {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			t.Cleanup(func() { _ = conn.Close() })
		}
	}()
	return ln
}
