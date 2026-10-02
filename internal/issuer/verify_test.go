package issuer

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
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// fakeJWKS publishes one RSA key, the way an operator's issuer would.
type fakeJWKS struct {
	srv   *httptest.Server
	key   *rsa.PrivateKey
	kid   string
	calls atomic.Int32
}

func newFakeJWKS(t *testing.T) *fakeJWKS {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeJWKS{key: key, kid: "k1"}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f.calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "use": "sig", "alg": "RS256", "kid": f.kid,
			"n": base64.RawURLEncoding.EncodeToString(f.key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(f.key.E)).Bytes()),
		}}})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeJWKS) sign(t *testing.T, claims jwt.MapClaims, key *rsa.PrivateKey) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = f.kid
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

var testNow = time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

func testConfig(f *fakeJWKS) Config {
	return Config{
		Name:           "portal",
		Issuer:         "https://portal.example.com",
		JWKSURL:        f.srv.URL,
		Audience:       "leoflow-engine",
		TenantClaim:    "tenant_id",
		AllowedTenants: []string{"acme", "globex"},
	}
}

func goodClaims() jwt.MapClaims {
	return jwt.MapClaims{
		"iss":       "https://portal.example.com",
		"aud":       "leoflow-engine",
		"sub":       "user-42",
		"email":     "ana@acme.com",
		"tenant_id": "acme",
		"iat":       testNow.Add(-time.Minute).Unix(),
		"exp":       testNow.Add(4 * time.Minute).Unix(),
	}
}

func newTestVerifier(f *fakeJWKS, cfg Config) *Verifier {
	v := New(context.Background(), cfg)
	v.now = func() time.Time { return testNow }
	return v
}

func TestVerifyAcceptsATokenFromTheTrustedIssuer(t *testing.T) {
	f := newFakeJWKS(t)
	v := newTestVerifier(f, testConfig(f))
	raw := f.sign(t, goodClaims(), f.key)

	id, err := v.Verify(context.Background(), raw)

	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	want := Identity{Subject: "user-42", Email: "ana@acme.com", Tenant: "acme"}
	if *id != want {
		t.Errorf("identity = %+v, want %+v", *id, want)
	}
}

// TestVerifyRejections is the fail-closed table: every way a token can be
// wrong ends in an error, and the error names the class so the caller can log
// a specific reason without exposing it to the browser.
func TestVerifyRejections(t *testing.T) {
	f := newFakeJWKS(t)
	foreign, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		mutate func(jwt.MapClaims)
		key    *rsa.PrivateKey
		want   error
	}{
		{"signed by another key", nil, foreign, ErrInvalidToken},
		{"other issuer", func(c jwt.MapClaims) { c["iss"] = "https://evil.example.com" }, nil, ErrInvalidToken},
		{"other audience", func(c jwt.MapClaims) { c["aud"] = "someone-else" }, nil, ErrInvalidToken},
		{"expired", func(c jwt.MapClaims) { c["exp"] = testNow.Add(-time.Second).Unix() }, nil, ErrInvalidToken},
		{"no issued-at", func(c jwt.MapClaims) { delete(c, "iat") }, nil, ErrLifetime},
		{"lives too long", func(c jwt.MapClaims) { c["exp"] = testNow.Add(time.Hour).Unix() }, nil, ErrLifetime},
		{"no subject", func(c jwt.MapClaims) { delete(c, "sub") }, nil, ErrInvalidToken},
		{"no tenant", func(c jwt.MapClaims) { delete(c, "tenant_id") }, nil, ErrTenantNotAllowed},
		{"tenant not allowed", func(c jwt.MapClaims) { c["tenant_id"] = "initech" }, nil, ErrTenantNotAllowed},
		{"tenant not a string", func(c jwt.MapClaims) { c["tenant_id"] = []string{"acme"} }, nil, ErrTenantNotAllowed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := newTestVerifier(f, testConfig(f))
			claims := goodClaims()
			if tc.mutate != nil {
				tc.mutate(claims)
			}
			key := f.key
			if tc.key != nil {
				key = tc.key
			}

			_, err := v.Verify(context.Background(), f.sign(t, claims, key))

			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestVerifyAnyTenantWithWildcard covers the multi-tenant operator, who signs
// for every tenant and lists "*" instead of each one.
func TestVerifyAnyTenantWithWildcard(t *testing.T) {
	f := newFakeJWKS(t)
	cfg := testConfig(f)
	cfg.AllowedTenants = []string{"*"}
	v := newTestVerifier(f, cfg)
	claims := goodClaims()
	claims["tenant_id"] = "initech"

	id, err := v.Verify(context.Background(), f.sign(t, claims, f.key))

	if err != nil || id.Tenant != "initech" {
		t.Fatalf("Verify = %+v, %v; want tenant initech accepted under *", id, err)
	}
}

// TestVerifyFetchesKeysLazily locks that building a Verifier never calls the
// issuer: a JWKS outage must not stop the control plane from booting, only the
// sign-ins that need it.
func TestVerifyFetchesKeysLazily(t *testing.T) {
	f := newFakeJWKS(t)

	_ = newTestVerifier(f, testConfig(f))

	if n := f.calls.Load(); n != 0 {
		t.Errorf("JWKS fetched %d times at construction, want 0", n)
	}
}

func TestProviderKeyIsNamespaced(t *testing.T) {
	v := &Verifier{cfg: Config{Name: "portal"}}

	got := v.Provider()

	if got != "issuer:portal" {
		t.Errorf("Provider() = %q, want issuer:portal", got)
	}
}

// TestVerifyRejectsATokenIssuedInTheFuture closes the gap the lifetime bound
// alone leaves: a token whose iat and exp both sit far in the future has a
// short exp - iat and an exp that has not passed, so without this check it
// would replay for as long as the issuer cared to post-date it.
func TestVerifyRejectsATokenIssuedInTheFuture(t *testing.T) {
	f := newFakeJWKS(t)
	v := newTestVerifier(f, testConfig(f))
	claims := goodClaims()
	claims["iat"] = testNow.Add(365 * 24 * time.Hour).Unix()
	claims["exp"] = testNow.Add(365*24*time.Hour + 5*time.Minute).Unix()

	_, err := v.Verify(context.Background(), f.sign(t, claims, f.key))

	if !errors.Is(err, ErrLifetime) {
		t.Errorf("err = %v, want ErrLifetime for a post-dated token", err)
	}
}

// TestVerifyToleratesSmallClockSkew keeps a token minted by a clock a few
// seconds ahead of ours usable.
func TestVerifyToleratesSmallClockSkew(t *testing.T) {
	f := newFakeJWKS(t)
	v := newTestVerifier(f, testConfig(f))
	claims := goodClaims()
	claims["iat"] = testNow.Add(30 * time.Second).Unix()

	if _, err := v.Verify(context.Background(), f.sign(t, claims, f.key)); err != nil {
		t.Errorf("Verify with iat 30s ahead = %v, want accepted", err)
	}
}
