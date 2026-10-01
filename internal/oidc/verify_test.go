package oidc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/dexadata/dexaflow/internal/config"
)

// ─────────────────────────── fake IdP ───────────────────────────

// fakeIDP is an in-process OpenID Provider: it serves discovery + JWKS and signs
// ID tokens with an in-test RSA key. It is the minimal harness needed to drive
// the verifier against a real signature and a real discovery document, ported
// down from the api-layer OIDC flow tests so internal/oidc exercises verify.go
// and flow.go directly.
type fakeIDP struct {
	srv    *httptest.Server
	key    *rsa.PrivateKey
	kid    string
	issuer string

	// tokenResp, when set, is the JSON body the /token endpoint returns; it lets a
	// flow test drive Exchange against a controlled response.
	tokenResp map[string]any
}

func newFakeIDP(t *testing.T) *fakeIDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa keygen: %v", err)
	}
	f := &fakeIDP{key: key, kid: "test-key-1"}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", f.handleDiscovery)
	mux.HandleFunc("/jwks", f.handleJWKS)
	mux.HandleFunc("/token", f.handleToken)
	f.srv = httptest.NewServer(mux)
	f.issuer = f.srv.URL
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIDP) handleToken(w http.ResponseWriter, _ *http.Request) {
	if f.tokenResp == nil {
		http.Error(w, "no token response staged", http.StatusBadRequest)
		return
	}
	writeJSON(w, f.tokenResp)
}

func (f *fakeIDP) handleDiscovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"issuer":                                f.issuer,
		"authorization_endpoint":                f.issuer + "/authorize",
		"token_endpoint":                        f.issuer + "/token",
		"jwks_uri":                              f.issuer + "/jwks",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
}

func (f *fakeIDP) handleJWKS(w http.ResponseWriter, _ *http.Request) {
	pub := f.key.PublicKey
	writeJSON(w, map[string]any{"keys": []map[string]any{{
		"kty": "RSA",
		"use": "sig",
		"alg": "RS256",
		"kid": f.kid,
		"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}}})
}

// signIDToken signs claims with the IdP's published key under its published kid.
func (f *fakeIDP) signIDToken(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	return signWith(t, claims, f.key, f.kid)
}

// signWith signs claims with an arbitrary key/kid, so a test can mint a token
// that does not match the JWKS.
func signWith(t *testing.T, claims jwt.MapClaims, key *rsa.PrivateKey, kid string) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = kid
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatalf("sign id token: %v", err)
	}
	return s
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// ─────────────────────────── harness ───────────────────────────

const testNonce = "nonce-xyz"

func baseOIDCConfig(f *fakeIDP) config.OIDCSection {
	return config.OIDCSection{
		Issuer:           f.issuer,
		ClientID:         "client-abc",
		ClientSecret:     "client-secret",
		RedirectURL:      "https://app.example/api/v2/auth/oidc/callback",
		Scopes:           []string{"openid", "email", "profile", "groups"},
		GroupsClaim:      "groups",
		TenantClaim:      "tid",
		TenantClaims:     map[string]string{"tenant-guid": "default"},
		ClockSkewSeconds: 60,
	}
}

func baseClaims(cfg config.OIDCSection, nonce string) jwt.MapClaims {
	now := time.Now()
	return jwt.MapClaims{
		"iss":            cfg.Issuer,
		"aud":            cfg.ClientID,
		"sub":            "subject-123",
		"exp":            now.Add(time.Hour).Unix(),
		"iat":            now.Unix(),
		"nonce":          nonce,
		"email":          "alice@corp.example",
		"email_verified": true,
		"tid":            "tenant-guid",
		"groups":         []string{"data-eng"},
	}
}

// newTestVerifier discovers the fake IdP and builds a verifier, exercising the
// real NewVerifier → newVerifierWithProvider construction path.
func newTestVerifier(t *testing.T, cfg config.OIDCSection) *Verifier {
	t.Helper()
	v, err := NewVerifier(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return v
}

// ─────────────────────────── happy path ───────────────────────────

func TestVerifyHappyPath(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	v := newTestVerifier(t, cfg)

	id, err := v.Verify(context.Background(), f.signIDToken(t, baseClaims(cfg, testNonce)), testNonce)
	if err != nil {
		t.Fatalf("Verify(valid token) = %v, want success", err)
	}
	if id.Provider != cfg.Issuer {
		t.Errorf("Provider = %q, want %q", id.Provider, cfg.Issuer)
	}
	if id.Subject != "subject-123" {
		t.Errorf("Subject = %q, want subject-123", id.Subject)
	}
	if id.Email != "alice@corp.example" {
		t.Errorf("Email = %q, want alice@corp.example", id.Email)
	}
	if !id.EmailVerified {
		t.Error("EmailVerified = false, want true on a returned identity")
	}
	if id.Tenant != "default" {
		t.Errorf("Tenant = %q, want default (mapped from tid tenant-guid)", id.Tenant)
	}
	if len(id.Groups) != 1 || id.Groups[0] != "data-eng" {
		t.Errorf("Groups = %v, want [data-eng]", id.Groups)
	}
}

// ─────────────────────────── H1a: issuer pin ───────────────────────────

func TestVerifyIssuerMismatch(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	v := newTestVerifier(t, cfg)

	claims := baseClaims(cfg, testNonce)
	claims["iss"] = "https://evil.example"
	_, err := v.Verify(context.Background(), f.signIDToken(t, claims), testNonce)
	if !errors.Is(err, ErrIssuerMismatch) {
		t.Fatalf("Verify(iss mismatch) = %v, want ErrIssuerMismatch", err)
	}
}

// ─────────────────────────── H4: nonce binding ───────────────────────────

func TestVerifyNonceMismatch(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	v := newTestVerifier(t, cfg)

	t.Run("nonce differs from expected", func(t *testing.T) {
		claims := baseClaims(cfg, "some-other-nonce")
		if _, err := v.Verify(context.Background(), f.signIDToken(t, claims), testNonce); !errors.Is(err, ErrNonceMismatch) {
			t.Fatalf("Verify(nonce mismatch) = %v, want ErrNonceMismatch", err)
		}
	})

	t.Run("empty expected nonce is rejected", func(t *testing.T) {
		claims := baseClaims(cfg, testNonce)
		if _, err := v.Verify(context.Background(), f.signIDToken(t, claims), ""); !errors.Is(err, ErrNonceMismatch) {
			t.Fatalf("Verify(empty expected nonce) = %v, want ErrNonceMismatch", err)
		}
	})
}

// ─────────────────────────── H4: azp ───────────────────────────

func TestVerifyAzpMismatch(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	v := newTestVerifier(t, cfg)

	t.Run("azp present and not this client", func(t *testing.T) {
		claims := baseClaims(cfg, testNonce)
		claims["azp"] = "some-other-client"
		if _, err := v.Verify(context.Background(), f.signIDToken(t, claims), testNonce); !errors.Is(err, ErrAzpMismatch) {
			t.Fatalf("Verify(azp mismatch) = %v, want ErrAzpMismatch", err)
		}
	})

	t.Run("azp present and equal to client id is accepted", func(t *testing.T) {
		claims := baseClaims(cfg, testNonce)
		claims["azp"] = cfg.ClientID
		if _, err := v.Verify(context.Background(), f.signIDToken(t, claims), testNonce); err != nil {
			t.Fatalf("Verify(azp == client_id) = %v, want success", err)
		}
	})
}

// ─────────────────────────── H4: clock skew ───────────────────────────

func TestVerifyClockSkew(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f) // 60s skew

	t.Run("expired beyond skew", func(t *testing.T) {
		v := newTestVerifier(t, cfg)
		claims := baseClaims(cfg, testNonce)
		claims["exp"] = time.Now().Add(-5 * time.Minute).Unix()
		if _, err := v.Verify(context.Background(), f.signIDToken(t, claims), testNonce); !errors.Is(err, ErrTokenExpired) {
			t.Fatalf("Verify(expired) = %v, want ErrTokenExpired", err)
		}
	})

	t.Run("missing exp is rejected", func(t *testing.T) {
		v := newTestVerifier(t, cfg)
		claims := baseClaims(cfg, testNonce)
		delete(claims, "exp") // a token with no expiry has no validity window
		if _, err := v.Verify(context.Background(), f.signIDToken(t, claims), testNonce); !errors.Is(err, ErrMissingExpiry) {
			t.Fatalf("Verify(no exp) = %v, want ErrMissingExpiry", err)
		}
	})

	t.Run("within skew boundary is accepted", func(t *testing.T) {
		v := newTestVerifier(t, cfg)
		claims := baseClaims(cfg, testNonce)
		// Expired 30s ago, inside the 60s skew → still valid.
		claims["exp"] = time.Now().Add(-30 * time.Second).Unix()
		if _, err := v.Verify(context.Background(), f.signIDToken(t, claims), testNonce); err != nil {
			t.Fatalf("Verify(within skew) = %v, want success", err)
		}
	})

	t.Run("iat too far in the future", func(t *testing.T) {
		v := newTestVerifier(t, cfg)
		claims := baseClaims(cfg, testNonce)
		claims["iat"] = time.Now().Add(5 * time.Minute).Unix()
		if _, err := v.Verify(context.Background(), f.signIDToken(t, claims), testNonce); !errors.Is(err, ErrTokenExpired) {
			t.Fatalf("Verify(future iat) = %v, want ErrTokenExpired", err)
		}
	})

	t.Run("nbf in the future beyond skew", func(t *testing.T) {
		v := newTestVerifier(t, cfg)
		claims := baseClaims(cfg, testNonce)
		claims["nbf"] = time.Now().Add(5 * time.Minute).Unix()
		if _, err := v.Verify(context.Background(), f.signIDToken(t, claims), testNonce); !errors.Is(err, ErrTokenExpired) {
			t.Fatalf("Verify(future nbf) = %v, want ErrTokenExpired", err)
		}
	})

	t.Run("nbf within skew is accepted", func(t *testing.T) {
		v := newTestVerifier(t, cfg)
		claims := baseClaims(cfg, testNonce)
		// Not-before is 30s ahead, inside the 60s skew → accepted.
		claims["nbf"] = time.Now().Add(30 * time.Second).Unix()
		if _, err := v.Verify(context.Background(), f.signIDToken(t, claims), testNonce); err != nil {
			t.Fatalf("Verify(nbf within skew) = %v, want success", err)
		}
	})
}

// ─────────────────────────── subject presence ───────────────────────────

func TestVerifyNoSubject(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	v := newTestVerifier(t, cfg)

	claims := baseClaims(cfg, testNonce)
	claims["sub"] = ""
	if _, err := v.Verify(context.Background(), f.signIDToken(t, claims), testNonce); !errors.Is(err, ErrNoSubject) {
		t.Fatalf("Verify(empty sub) = %v, want ErrNoSubject", err)
	}
}

// ─────────────────────────── D6c: email_verified ───────────────────────────

func TestVerifyEmailNotVerified(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	v := newTestVerifier(t, cfg)

	t.Run("email_verified false", func(t *testing.T) {
		claims := baseClaims(cfg, testNonce)
		claims["email_verified"] = false
		if _, err := v.Verify(context.Background(), f.signIDToken(t, claims), testNonce); !errors.Is(err, ErrEmailNotVerified) {
			t.Fatalf("Verify(email_verified=false) = %v, want ErrEmailNotVerified", err)
		}
	})

	t.Run("email_verified absent (treated as false)", func(t *testing.T) {
		claims := baseClaims(cfg, testNonce)
		delete(claims, "email_verified")
		if _, err := v.Verify(context.Background(), f.signIDToken(t, claims), testNonce); !errors.Is(err, ErrEmailNotVerified) {
			t.Fatalf("Verify(email_verified absent) = %v, want ErrEmailNotVerified", err)
		}
	})
}

// ─────────────────────────── D6b/d: tenant pin ───────────────────────────

func TestVerifyTenantNotAllowed(t *testing.T) {
	f := newFakeIDP(t)

	t.Run("TenantClaim unset", func(t *testing.T) {
		cfg := baseOIDCConfig(f)
		cfg.TenantClaim = ""
		v := newTestVerifier(t, cfg)
		if _, err := v.Verify(context.Background(), f.signIDToken(t, baseClaims(cfg, testNonce)), testNonce); !errors.Is(err, ErrTenantNotAllowed) {
			t.Fatalf("Verify(TenantClaim unset) = %v, want ErrTenantNotAllowed", err)
		}
	})

	t.Run("tid claim absent", func(t *testing.T) {
		cfg := baseOIDCConfig(f)
		v := newTestVerifier(t, cfg)
		claims := baseClaims(cfg, testNonce)
		delete(claims, "tid")
		if _, err := v.Verify(context.Background(), f.signIDToken(t, claims), testNonce); !errors.Is(err, ErrTenantNotAllowed) {
			t.Fatalf("Verify(tid absent) = %v, want ErrTenantNotAllowed", err)
		}
	})

	t.Run("tid value not in TenantClaims map", func(t *testing.T) {
		cfg := baseOIDCConfig(f)
		v := newTestVerifier(t, cfg)
		claims := baseClaims(cfg, testNonce)
		claims["tid"] = "unmapped-tenant"
		if _, err := v.Verify(context.Background(), f.signIDToken(t, claims), testNonce); !errors.Is(err, ErrTenantNotAllowed) {
			t.Fatalf("Verify(tid unmapped) = %v, want ErrTenantNotAllowed", err)
		}
	})
}

// ─────────────────────────── email-domain allowlist ───────────────────────────

func TestVerifyEmailDomainAllowlist(t *testing.T) {
	f := newFakeIDP(t)

	t.Run("domain in allowlist is accepted", func(t *testing.T) {
		cfg := baseOIDCConfig(f)
		cfg.AllowedEmailDomains = []string{"corp.example"}
		v := newTestVerifier(t, cfg)
		if _, err := v.Verify(context.Background(), f.signIDToken(t, baseClaims(cfg, testNonce)), testNonce); err != nil {
			t.Fatalf("Verify(domain in allowlist) = %v, want success", err)
		}
	})

	t.Run("domain not in allowlist is rejected", func(t *testing.T) {
		cfg := baseOIDCConfig(f)
		cfg.AllowedEmailDomains = []string{"trusted.example"}
		v := newTestVerifier(t, cfg)
		if _, err := v.Verify(context.Background(), f.signIDToken(t, baseClaims(cfg, testNonce)), testNonce); !errors.Is(err, ErrEmailDomainNotAllowed) {
			t.Fatalf("Verify(domain not in allowlist) = %v, want ErrEmailDomainNotAllowed", err)
		}
	})

	t.Run("empty allowlist imposes no restriction", func(t *testing.T) {
		cfg := baseOIDCConfig(f) // AllowedEmailDomains empty
		v := newTestVerifier(t, cfg)
		if _, err := v.Verify(context.Background(), f.signIDToken(t, baseClaims(cfg, testNonce)), testNonce); err != nil {
			t.Fatalf("Verify(empty allowlist) = %v, want success", err)
		}
	})

	t.Run("unverified email fails before the domain check", func(t *testing.T) {
		cfg := baseOIDCConfig(f)
		cfg.AllowedEmailDomains = []string{"corp.example"} // domain WOULD pass
		v := newTestVerifier(t, cfg)
		claims := baseClaims(cfg, testNonce)
		claims["email_verified"] = false
		// The domain would be allowed, so a domain error here would prove the
		// order is wrong; email_verified must be the failing check.
		if _, err := v.Verify(context.Background(), f.signIDToken(t, claims), testNonce); !errors.Is(err, ErrEmailNotVerified) {
			t.Fatalf("Verify(unverified) = %v, want ErrEmailNotVerified (before domain check)", err)
		}
	})
}

// ─────────────────────────── signature ───────────────────────────

func TestVerifyRejectsForeignSignature(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	v := newTestVerifier(t, cfg)

	// Sign with a key that is NOT in the JWKS, under the published kid so go-oidc
	// selects the real key and the signature fails to verify.
	foreign, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa keygen: %v", err)
	}
	raw := signWith(t, baseClaims(cfg, testNonce), foreign, f.kid)
	if _, verr := v.Verify(context.Background(), raw, testNonce); verr == nil {
		t.Fatal("Verify(foreign signature) = nil, want a verification error")
	}
}

// ─────────────────────────── extractGroups ───────────────────────────

func TestVerifyExtractGroups(t *testing.T) {
	f := newFakeIDP(t)

	verifyGroups := func(t *testing.T, cfg config.OIDCSection, groupsVal any, present bool) []string {
		t.Helper()
		v := newTestVerifier(t, cfg)
		claims := baseClaims(cfg, testNonce)
		if present {
			claims["groups"] = groupsVal
		} else {
			delete(claims, "groups")
		}
		// When the configured claim name differs, stage it explicitly.
		if cfg.GroupsClaim != "groups" && cfg.GroupsClaim != "" && present {
			delete(claims, "groups")
			claims[cfg.GroupsClaim] = groupsVal
		}
		id, err := v.Verify(context.Background(), f.signIDToken(t, claims), testNonce)
		if err != nil {
			t.Fatalf("Verify = %v, want success", err)
		}
		return id.Groups
	}

	t.Run("array claim", func(t *testing.T) {
		got := verifyGroups(t, baseOIDCConfig(f), []string{"a", "b"}, true)
		if len(got) != 2 || got[0] != "a" || got[1] != "b" {
			t.Errorf("groups = %v, want [a b]", got)
		}
	})

	t.Run("single string claim", func(t *testing.T) {
		got := verifyGroups(t, baseOIDCConfig(f), "solo", true)
		if len(got) != 1 || got[0] != "solo" {
			t.Errorf("groups = %v, want [solo]", got)
		}
	})

	t.Run("absent claim yields nil", func(t *testing.T) {
		if got := verifyGroups(t, baseOIDCConfig(f), nil, false); got != nil {
			t.Errorf("groups = %v, want nil", got)
		}
	})

	t.Run("non-string entries are skipped", func(t *testing.T) {
		got := verifyGroups(t, baseOIDCConfig(f), []any{"a", 123, "", "b"}, true)
		if len(got) != 2 || got[0] != "a" || got[1] != "b" {
			t.Errorf("groups = %v, want [a b] (non-string and empty skipped)", got)
		}
	})

	t.Run("empty single string yields nil", func(t *testing.T) {
		if got := verifyGroups(t, baseOIDCConfig(f), "", true); got != nil {
			t.Errorf("groups = %v, want nil (empty string)", got)
		}
	})

	t.Run("non-array non-string claim yields nil", func(t *testing.T) {
		if got := verifyGroups(t, baseOIDCConfig(f), 42, true); got != nil {
			t.Errorf("groups = %v, want nil (numeric claim ignored)", got)
		}
	})

	t.Run("configurable claim name", func(t *testing.T) {
		cfg := baseOIDCConfig(f)
		cfg.GroupsClaim = "roles"
		got := verifyGroups(t, cfg, []string{"x"}, true)
		if len(got) != 1 || got[0] != "x" {
			t.Errorf("groups = %v, want [x] (read from custom claim 'roles')", got)
		}
	})

	t.Run("empty claim name yields nil", func(t *testing.T) {
		cfg := baseOIDCConfig(f)
		cfg.GroupsClaim = ""
		got := verifyGroups(t, cfg, []string{"ignored"}, true)
		if got != nil {
			t.Errorf("groups = %v, want nil (groups_claim unset)", got)
		}
	})
}

// TestVerifyGroupOverage: an Azure Entra group-overage token omits the groups
// claim and points to it under _claim_names. It must fail closed (ErrGroupOverage),
// not be silently treated as "no groups" — which would demote a heavily-grouped,
// often-privileged user to default-deny with no signal.
func TestVerifyGroupOverage(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	v := newTestVerifier(t, cfg)
	claims := baseClaims(cfg, testNonce)
	delete(claims, "groups")
	// A well-formed Entra overage token: _claim_names points groups at a source,
	// _claim_sources describes it (a Graph endpoint we deliberately never call).
	claims["_claim_names"] = map[string]any{"groups": "src1"}
	claims["_claim_sources"] = map[string]any{"src1": map[string]any{"endpoint": "https://graph.microsoft.com/v1.0/users/x/getMemberObjects"}}

	_, err := v.Verify(context.Background(), f.signIDToken(t, claims), testNonce)
	if !errors.Is(err, ErrGroupOverage) {
		t.Fatalf("Verify with group overage = %v, want ErrGroupOverage", err)
	}
}

// verifyWithClaims signs a token with the base claims plus a mutation and
// requires it to verify.
func verifyWithClaims(t *testing.T, f *fakeIDP, cfg config.OIDCSection, mut func(jwt.MapClaims)) *VerifiedIdentity {
	t.Helper()
	claims := baseClaims(cfg, testNonce)
	mut(claims)
	id, err := newTestVerifier(t, cfg).Verify(context.Background(), f.signIDToken(t, claims), testNonce)
	if err != nil {
		t.Fatalf("Verify = %v, want success", err)
	}
	return id
}

// verifyExpectingError is its counterpart for the arms that must fail closed.
func verifyExpectingError(t *testing.T, f *fakeIDP, cfg config.OIDCSection, mut func(jwt.MapClaims)) (*VerifiedIdentity, error) {
	t.Helper()
	claims := baseClaims(cfg, testNonce)
	mut(claims)
	return newTestVerifier(t, cfg).Verify(context.Background(), f.signIDToken(t, claims), testNonce)
}

// TestResolveTenantAcceptsAnArrayClaim covers a shape the pin silently could not
// read.
//
// The claim was cast to string and nothing else, which is right for hd (Google)
// and tid (Entra) but not for aud, which OpenID Connect defines as a string OR
// an array of strings. An operator behind a single-tenant IdP has no domain
// claim to pin on and reaches for aud (see the single-tenant discussion in
// ADR 0057). Cognito emits it as one string, so it works there; an IdP emitting
// the array form rejected EVERY login, audited tenant_not_allowed, which reads
// as "this tenant is not on the allowlist" and sends the operator to inspect a
// map that is correct.
func TestResolveTenantAcceptsAnArrayClaim(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	cfg.TenantClaim = "aud_list"
	cfg.TenantClaims = map[string]string{"client-abc": "default"}

	t.Run("a single-element array resolves", func(t *testing.T) {
		id := verifyWithClaims(t, f, cfg, func(c jwt.MapClaims) { c["aud_list"] = []any{"client-abc"} })
		if id.Tenant != "default" {
			t.Errorf("tenant = %q, want default: an array of one names exactly one tenant", id.Tenant)
		}
	})

	t.Run("a string still resolves", func(t *testing.T) {
		id := verifyWithClaims(t, f, cfg, func(c jwt.MapClaims) { c["aud_list"] = "client-abc" })
		if id.Tenant != "default" {
			t.Errorf("tenant = %q, want default", id.Tenant)
		}
	})

	t.Run("an array with one MATCHING element resolves even when others are present", func(t *testing.T) {
		id := verifyWithClaims(t, f, cfg, func(c jwt.MapClaims) {
			c["aud_list"] = []any{"some-other-client", "client-abc"}
		})
		if id.Tenant != "default" {
			t.Errorf("tenant = %q, want default: unmapped values are not claims about our tenants", id.Tenant)
		}
	})

	t.Run("two matching elements are refused, not picked between", func(t *testing.T) {
		amb := cfg
		amb.TenantClaims = map[string]string{"client-abc": "default", "client-xyz": "other"}
		_, err := verifyExpectingError(t, f, amb, func(c jwt.MapClaims) {
			c["aud_list"] = []any{"client-abc", "client-xyz"}
		})
		if !errors.Is(err, ErrTenantAmbiguous) {
			t.Errorf("err = %v, want ErrTenantAmbiguous: a token naming two accepted tenants identifies neither, and picking the first would be arbitrary", err)
		}
	})

	t.Run("a shape that is neither is refused with its own reason", func(t *testing.T) {
		_, err := verifyExpectingError(t, f, cfg, func(c jwt.MapClaims) { c["aud_list"] = 42 })
		if !errors.Is(err, ErrTenantClaimShape) {
			t.Errorf("err = %v, want ErrTenantClaimShape: the log must separate a claim we cannot read from a tenant that is not allowed", err)
		}
	})

	t.Run("an array with no match is still not allowed", func(t *testing.T) {
		_, err := verifyExpectingError(t, f, cfg, func(c jwt.MapClaims) { c["aud_list"] = []any{"nope"} })
		if !errors.Is(err, ErrTenantNotAllowed) {
			t.Errorf("err = %v, want ErrTenantNotAllowed", err)
		}
	})
}
