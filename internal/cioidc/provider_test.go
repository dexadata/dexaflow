package cioidc

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// fakeProvider is a CI provider on a loopback server: it publishes an
// OpenID discovery document at /.well-known/openid-configuration and one RSA
// key at /jwks, the way GitHub Actions, GitLab and Google do.
type fakeProvider struct {
	srv *httptest.Server
	key *rsa.PrivateKey
	kid string
	// jwksCalls and discoveryCalls count the downloads.
	jwksCalls, discoveryCalls atomic.Int32
	// down makes both endpoints answer 503, like a provider outage.
	down atomic.Bool
	// docIssuer, when set, is the issuer the discovery document claims
	// instead of the server's own URL.
	docIssuer string
}

func newFakeProvider(t *testing.T) *fakeProvider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeProvider{key: key, kid: "k1"}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		p.discoveryCalls.Add(1)
		if p.down.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		iss := p.srv.URL
		if p.docIssuer != "" {
			iss = p.docIssuer
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": iss, "jwks_uri": p.srv.URL + "/jwks"}) //nolint:errcheck // test server
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		p.jwksCalls.Add(1)
		if p.down.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{ //nolint:errcheck // test server
			"kty": "RSA", "use": "sig", "alg": "RS256", "kid": p.kid,
			"n": base64.RawURLEncoding.EncodeToString(p.key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(p.key.E)).Bytes()),
		}}})
	})
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	return p
}

// sign signs claims with key (the provider's own when nil) as RS256.
func (p *fakeProvider) sign(t *testing.T, claims jwt.MapClaims, key *rsa.PrivateKey) string {
	t.Helper()
	if key == nil {
		key = p.key
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = p.kid
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// unsigned returns claims as a token with alg none and no signature.
func unsigned(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	s, err := jwt.NewWithClaims(jwt.SigningMethodNone, claims).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

var testNow = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

// githubClaims are the claims of a GitHub Actions job of acme/dags on main in
// the production environment, valid for five minutes as GitHub's are.
func githubClaims(iss string) jwt.MapClaims {
	return jwt.MapClaims{
		"iss":         iss,
		"aud":         "https://dexaflow.acme.example",
		"sub":         "repo:acme/dags:environment:production",
		"repository":  "acme/dags",
		"ref":         "refs/heads/main",
		"environment": "production",
		"iat":         testNow.Add(-time.Minute).Unix(),
		"nbf":         testNow.Add(-time.Minute).Unix(),
		"exp":         testNow.Add(4 * time.Minute).Unix(),
	}
}

// acmeIssuer trusts p as GitHub for tenant acme, with one rule for the
// production deploys of acme/dags.
func acmeIssuer(p *fakeProvider) Issuer {
	return Issuer{
		Name:     "github-acme",
		Provider: ProviderGitHub,
		Issuer:   p.srv.URL,
		Audience: "https://dexaflow.acme.example",
		Tenant:   "acme",
		Rules: []Rule{{
			Claims: map[string]string{"repository": "acme/dags", "ref": "refs/heads/main", "environment": "production"},
			Role:   "deployer",
		}},
	}
}

func newTestVerifier(issuers ...Issuer) *Verifier {
	v := New(issuers)
	v.now = func() time.Time { return testNow }
	return v
}

// tamper replaces the payload of a signed token, keeping its header and
// signature.
func tamper(t *testing.T, raw string, claims jwt.MapClaims) string {
	t.Helper()
	parts := strings.Split(raw, ".")
	body, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return parts[0] + "." + base64.RawURLEncoding.EncodeToString(body) + "." + parts[2]
}
