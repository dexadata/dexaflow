package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/config"
)

// TestDiscoverOIDCFlowDiscoveryFailureIsBootError locks that an unreachable
// issuer fails boot closed rather than deferring the failure to first login —
// the fail-closed posture the OIDC flow depends on.
func TestDiscoverOIDCFlowDiscoveryFailureIsBootError(t *testing.T) {
	cfg := &config.ServerConfig{}
	cfg.Auth.Provider = config.AuthProviderOIDC
	cfg.Auth.JWT.Secret = "secret"
	// A syntactically valid but unreachable issuer: discovery must fail.
	cfg.Auth.OIDC.Issuer = "https://127.0.0.1:1/does-not-exist"
	cfg.Auth.OIDC.ClientID = "client"
	cfg.Auth.OIDC.RedirectURL = "https://app.example/api/v2/auth/oidc/callback"
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if _, err := discoverOIDCFlow(context.Background(), cfg, logger); err == nil {
		t.Error("discoverOIDCFlow with an unreachable issuer = nil error, want a boot failure")
	}
}

// TestDiscoverOIDCFlowIsBoundedWhenTheIdPHangs covers the third shape the SSO
// audit found (#1153): an IdP that accepts the TCP connection and then never
// answers.
//
// Discovery runs before the HTTP listener is bound, and go-oidc issues it on a
// client with no timeout of its own, so a hung issuer parks boot forever. On
// Kubernetes the probe endpoint never comes up, the kubelet kills the container,
// and the deployment enters a restart loop whose events say only that the probe
// failed. Nothing anywhere names the IdP. Unreachable already failed fast (the
// connection is refused); hung is the case with no bound.
//
// A bound turns it into a named boot failure. It stays fail-closed either way:
// the server does not start with a flow that cannot verify anything.
func TestDiscoverOIDCFlowIsBoundedWhenTheIdPHangs(t *testing.T) {
	blocked := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-blocked
	}))
	t.Cleanup(func() { close(blocked); srv.Close() })

	restore := oidcDiscoveryTimeout
	oidcDiscoveryTimeout = 150 * time.Millisecond
	t.Cleanup(func() { oidcDiscoveryTimeout = restore })

	cfg := hangingDiscoveryConfig(srv.URL)

	type result struct{ err error }
	done := make(chan result, 1)
	go func() {
		_, err := discoverOIDCFlow(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
		done <- result{err}
	}()

	select {
	case got := <-done:
		if got.err == nil {
			t.Fatal("a hung issuer produced a usable flow")
		}
		for _, want := range []string{srv.URL, "did not answer"} {
			if !strings.Contains(got.err.Error(), want) {
				t.Errorf("the boot error does not contain %q, so the operator cannot tell the IdP hung: %v", want, got.err)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("discovery never returned: boot is unbounded against an IdP that accepts the connection and never answers, so the listener never binds and the kubelet restarts the pod forever")
	}
}

// TestDiscoveryTimeoutErrorDoesNotClaimTheIdPWasReached keeps the boot error
// honest about what the bound can and cannot tell an operator.
//
// A deadline on the discovery request fires for three shapes that the code
// cannot tell apart: the issuer accepted the connection and never answered, the
// name never resolved, or the egress packets were dropped without a reply. The
// last one is the common shape on Kubernetes, because a NetworkPolicy that DROPs
// (rather than REJECTs) makes a connect attempt wait out the deadline exactly
// like a hung server. An error that asserts "the connection was accepted, so the
// issuer is reachable" sends the operator to look at the IdP when the cause is a
// policy in their own cluster.
//
// Verified against the real chains: a blackholed address and a server that
// accepts and never answers both surface as `context deadline exceeded` from
// go-oidc, with nothing distinguishing them.
func TestDiscoveryTimeoutErrorDoesNotClaimTheIdPWasReached(t *testing.T) {
	err := hangingDiscoveryError(t)
	for _, forbidden := range []string{
		"connection was accepted",
		"the issuer resolves and is reachable",
	} {
		if strings.Contains(err.Error(), forbidden) {
			t.Errorf("the boot error asserts %q, which a silently dropped SYN does not support: %v", forbidden, err)
		}
	}
	if !strings.Contains(err.Error(), "NetworkPolicy") {
		t.Errorf("the boot error does not name the cause an operator can act on in their own cluster: %v", err)
	}
}

// TestDiscoveryTimeoutErrorKeepsTheUnderlyingCause pins that the named error
// wraps rather than replaces what go-oidc reported. The wrapped error carries
// the URL actually requested and the transport-level distinction (a dial timeout
// reads differently from a TLS handshake timeout), which is the only detail in
// the failure that separates the three shapes the prose has to hedge over.
// Replacing it with prose alone throws that away and breaks errors.Is for any
// caller that wants to classify a boot failure.
func TestDiscoveryTimeoutErrorKeepsTheUnderlyingCause(t *testing.T) {
	err := hangingDiscoveryError(t)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("the boot error does not wrap context.DeadlineExceeded, so the cause is unrecoverable from it: %v", err)
	}
	if !strings.Contains(err.Error(), ".well-known/openid-configuration") {
		t.Errorf("the boot error drops the URL that was actually requested: %v", err)
	}
}

// TestDiscoverOIDCFlowParentCancellationIsNotReportedAsAnIdPTimeout locks the
// distinction the bound depends on. A SIGTERM during boot cancels the parent
// context, which cancels the discovery request too; reporting that as "the IdP
// did not answer" would blame the IdP for an orderly shutdown.
func TestDiscoverOIDCFlowParentCancellationIsNotReportedAsAnIdPTimeout(t *testing.T) {
	blocked := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-blocked
	}))
	t.Cleanup(func() { close(blocked); srv.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := discoverOIDCFlow(ctx, hangingDiscoveryConfig(srv.URL), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil {
		t.Fatal("a canceled boot produced a usable flow")
	}
	if strings.Contains(err.Error(), "did not answer discovery") {
		t.Errorf("a canceled boot is reported as an IdP timeout: %v", err)
	}
}

// hangingDiscoveryConfig is an OIDC server config pointed at issuer.
func hangingDiscoveryConfig(issuer string) *config.ServerConfig {
	cfg := &config.ServerConfig{}
	cfg.Auth.Provider = config.AuthProviderOIDC
	cfg.Auth.JWT.Secret = "secret"
	cfg.Auth.OIDC.Issuer = issuer
	cfg.Auth.OIDC.ClientID = "client"
	cfg.Auth.OIDC.RedirectURL = "https://app.example/api/v2/auth/oidc/callback"
	return cfg
}

// hangingDiscoveryError runs discovery against an issuer that accepts the
// connection and never answers, and returns the boot error.
//
// The wait is bounded here and not only in the caller: removing the bound from
// discoverOIDCFlow must fail these tests in seconds, the way it fails boot in
// production, rather than park them until the package timeout dumps goroutines.
func hangingDiscoveryError(t *testing.T) error {
	t.Helper()
	blocked := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-blocked
	}))
	t.Cleanup(func() { close(blocked); srv.Close() })

	restore := oidcDiscoveryTimeout
	oidcDiscoveryTimeout = 150 * time.Millisecond
	t.Cleanup(func() { oidcDiscoveryTimeout = restore })

	errc := make(chan error, 1)
	go func() {
		_, err := discoverOIDCFlow(context.Background(), hangingDiscoveryConfig(srv.URL), slog.New(slog.NewTextHandler(io.Discard, nil)))
		errc <- err
	}()
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("a hung issuer produced a usable flow")
		}
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("discovery never returned: boot is unbounded against an IdP that accepts the connection and never answers")
		return nil
	}
}
