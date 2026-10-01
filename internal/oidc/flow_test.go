package oidc

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/config"
)

const testFlowSecret = "oidc-flow-test-secret"

func newTestFlow(t *testing.T, cfg config.OIDCSection) *Flow {
	t.Helper()
	flow, err := NewFlow(context.Background(), cfg, testFlowSecret)
	if err != nil {
		t.Fatalf("NewFlow: %v", err)
	}
	return flow
}

func TestNewFlowDiscoveryFailureIsFatal(t *testing.T) {
	cfg := config.OIDCSection{Issuer: "http://127.0.0.1:1/does-not-exist", ClientID: "c"}
	if _, err := NewFlow(context.Background(), cfg, testFlowSecret); err == nil {
		t.Fatal("NewFlow with an unreachable issuer = nil error, want a discovery failure")
	}
}

func TestGenerateCodeVerifier(t *testing.T) {
	a := GenerateCodeVerifier()
	b := GenerateCodeVerifier()
	if a == "" || b == "" {
		t.Fatal("GenerateCodeVerifier returned an empty verifier")
	}
	if a == b {
		t.Error("two GenerateCodeVerifier calls returned identical values (not high-entropy)")
	}
}

func TestFlowCodecRoundTrip(t *testing.T) {
	f := newFakeIDP(t)
	flow := newTestFlow(t, baseOIDCConfig(f))

	codec := flow.Codec()
	if codec == nil {
		t.Fatal("Codec() = nil")
	}
	in := StatePayload{State: "st-123", Nonce: "nonce-abc", Verifier: "vfy-xyz", Next: "/dags"}
	tok, err := codec.Encode(in, time.Minute)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := codec.Decode(tok)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got != in {
		t.Errorf("round-trip = %+v, want %+v", got, in)
	}

	if _, err := codec.Decode("not-a-jwt-at-all"); !errors.Is(err, ErrInvalidState) {
		t.Errorf("Decode(garbage) err = %v, want ErrInvalidState", err)
	}
}

func TestFlowVerifierIsWired(t *testing.T) {
	f := newFakeIDP(t)
	flow := newTestFlow(t, baseOIDCConfig(f))
	if flow.Verifier() == nil {
		t.Fatal("Verifier() = nil, want the flow's ID-token verifier")
	}
}

func TestAuthCodeURL(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	flow := newTestFlow(t, cfg)

	verifier := GenerateCodeVerifier()
	raw := flow.AuthCodeURL("state-123", "nonce-456", verifier)
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse AuthCodeURL: %v", err)
	}
	q := u.Query()

	if got := q.Get("state"); got != "state-123" {
		t.Errorf("state = %q, want state-123", got)
	}
	if got := q.Get("nonce"); got != "nonce-456" {
		t.Errorf("nonce = %q, want nonce-456", got)
	}
	if got := q.Get("client_id"); got != cfg.ClientID {
		t.Errorf("client_id = %q, want %q", got, cfg.ClientID)
	}
	if got := q.Get("redirect_uri"); got != cfg.RedirectURL {
		t.Errorf("redirect_uri = %q, want %q", got, cfg.RedirectURL)
	}
	if got := q.Get("scope"); got != "openid email profile groups" {
		t.Errorf("scope = %q, want the configured scopes", got)
	}
	if got := q.Get("code_challenge_method"); got != "S256" {
		t.Errorf("code_challenge_method = %q, want S256 (PKCE)", got)
	}
	// The PKCE challenge must be the S256 hash of the verifier, never the verifier
	// itself in the clear.
	sum := sha256.Sum256([]byte(verifier))
	wantChallenge := base64.RawURLEncoding.EncodeToString(sum[:])
	if got := q.Get("code_challenge"); got != wantChallenge {
		t.Errorf("code_challenge = %q, want S256(verifier) = %q", got, wantChallenge)
	}
	if got := q.Get("code_challenge"); got == verifier {
		t.Error("code_challenge equals the raw verifier — PKCE must send the S256 challenge, not the verifier")
	}
}

func TestExchange(t *testing.T) {
	t.Run("returns the raw id_token", func(t *testing.T) {
		f := newFakeIDP(t)
		cfg := baseOIDCConfig(f)
		idToken := f.signIDToken(t, baseClaims(cfg, testNonce))
		f.tokenResp = map[string]any{
			"access_token": "fake-access-token",
			"token_type":   "Bearer",
			"expires_in":   3600,
			"id_token":     idToken,
		}
		flow := newTestFlow(t, cfg)

		raw, err := flow.Exchange(context.Background(), "auth-code", GenerateCodeVerifier())
		if err != nil {
			t.Fatalf("Exchange = %v, want success", err)
		}
		if raw != idToken {
			t.Errorf("Exchange returned a different id_token than the token endpoint sent")
		}
	})

	t.Run("no id_token fails closed", func(t *testing.T) {
		f := newFakeIDP(t)
		cfg := baseOIDCConfig(f)
		f.tokenResp = map[string]any{
			"access_token": "fake-access-token",
			"token_type":   "Bearer",
			"expires_in":   3600,
			// no id_token
		}
		flow := newTestFlow(t, cfg)

		if _, err := flow.Exchange(context.Background(), "auth-code", GenerateCodeVerifier()); !errors.Is(err, ErrNoIDToken) {
			t.Fatalf("Exchange(no id_token) = %v, want ErrNoIDToken", err)
		}
	})

	t.Run("token endpoint error surfaces", func(t *testing.T) {
		f := newFakeIDP(t)
		cfg := baseOIDCConfig(f)
		// tokenResp left nil → /token returns 400, so the exchange fails.
		flow := newTestFlow(t, cfg)

		if _, err := flow.Exchange(context.Background(), "auth-code", GenerateCodeVerifier()); err == nil {
			t.Fatal("Exchange against a failing token endpoint = nil, want an error")
		}
	})
}

// TestAuthCodeURLSendsGoogleHostedDomain covers the account-picker problem a
// Google Workspace deployment hits.
//
// Google's chooser lists every account signed in on that browser, work and
// personal. Picking a personal one produces an ID token with no hd claim, which
// the tenant pin rejects with the same generic 403 every other failure gets, so
// the user is told nothing about which account to use and the picker looks the
// same on the retry. Sending hd narrows the chooser to the domain up front.
//
// hd on the request is a hint and nothing else: Google's own documentation says
// to verify the hd CLAIM on the returned token, which is what the tenant pin
// already does. This must never be read as a reason to relax that check.
func TestAuthCodeURLSendsGoogleHostedDomain(t *testing.T) {
	f := newFakeIDP(t)

	t.Run("tenant_claim hd with one domain: sent", func(t *testing.T) {
		cfg := baseOIDCConfig(f)
		cfg.TenantClaim = "hd"
		cfg.TenantClaims = map[string]string{"corp.example": "default"}
		q := authQuery(t, newTestFlow(t, cfg))
		if got := q.Get("hd"); got != "corp.example" {
			t.Errorf("hd = %q, want corp.example: without it the chooser offers personal accounts that the tenant pin then rejects with a 403 naming nothing", got)
		}
	})

	t.Run("two domains: not sent, because neither one is the answer", func(t *testing.T) {
		cfg := baseOIDCConfig(f)
		cfg.TenantClaim = "hd"
		cfg.TenantClaims = map[string]string{"a.example": "a", "b.example": "b"}
		if got := authQuery(t, newTestFlow(t, cfg)).Get("hd"); got != "" {
			t.Errorf("hd = %q: sending one of several accepted domains would lock out the users of the others", got)
		}
	})

	t.Run("tenant_claim tid: not sent, hd is Google-only", func(t *testing.T) {
		if got := authQuery(t, newTestFlow(t, baseOIDCConfig(f))).Get("hd"); got != "" {
			t.Errorf("hd = %q on an Entra deployment, where the parameter means nothing", got)
		}
	})
}

func authQuery(t *testing.T, flow *Flow) url.Values {
	t.Helper()
	u, err := url.Parse(flow.AuthCodeURL("state", "nonce", GenerateCodeVerifier()))
	if err != nil {
		t.Fatalf("parse AuthCodeURL: %v", err)
	}
	return u.Query()
}
