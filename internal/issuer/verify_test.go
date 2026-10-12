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
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// fakeJWKS publishes one RSA key, the way an operator's issuer would.
type fakeJWKS struct {
	srv   *httptest.Server
	calls atomic.Int32
	// down makes the endpoint answer 503, like an issuer outage.
	down atomic.Bool

	mu  sync.Mutex // guards key and kid, which rotate swaps
	key *rsa.PrivateKey
	kid string
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
		if f.down.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
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
	f.mu.Lock()
	kid := f.kid
	f.mu.Unlock()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = kid
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

// jtiSeq gives every test token its own jti, as a real issuer would.
var jtiSeq atomic.Int64

func goodClaims() jwt.MapClaims {
	return jwt.MapClaims{
		"jti":       "t-" + strconv.FormatInt(jtiSeq.Add(1), 10),
		"iss":       "https://portal.example.com",
		"aud":       "leoflow-engine",
		"sub":       "user-42",
		"email":     "ana@acme.com",
		"tenant_id": "acme",
		"iat":       testNow.Add(-30 * time.Second).Unix(),
		"exp":       testNow.Add(60 * time.Second).Unix(),
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
	// A handoff token carries no scopes: the session it opens is unscoped.
	want := Identity{Subject: "user-42", Email: "ana@acme.com", Tenant: "acme"}
	if !reflect.DeepEqual(*id, want) {
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
		{"no jti", func(c jwt.MapClaims) { delete(c, "jti") }, nil, ErrInvalidToken},
		{"longer than the default", func(c jwt.MapClaims) { c["exp"] = testNow.Add(2 * time.Minute).Unix() }, nil, ErrLifetime},
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

// TestVerifyRefusesAReplayedToken locks the one-session rule: the same token
// posted twice opens one session, and the second post is ErrReplayed.
func TestVerifyRefusesAReplayedToken(t *testing.T) {
	f := newFakeJWKS(t)
	v := newTestVerifier(f, testConfig(f))
	raw := f.sign(t, goodClaims(), f.key)

	if _, err := v.Verify(context.Background(), raw); err != nil {
		t.Fatalf("first Verify: %v", err)
	}
	_, err := v.Verify(context.Background(), raw)

	if !errors.Is(err, ErrReplayed) {
		t.Errorf("second Verify = %v, want ErrReplayed", err)
	}
}

// TestVerifyKeepsTheJTIOfARefusedToken locks that a token refused for another
// reason does not use up its jti, so a correct token is not blocked by an
// earlier bad one that happened to share it.
func TestVerifyKeepsTheJTIOfARefusedToken(t *testing.T) {
	f := newFakeJWKS(t)
	v := newTestVerifier(f, testConfig(f))
	claims := goodClaims()
	claims["tenant_id"] = "initech"
	if _, err := v.Verify(context.Background(), f.sign(t, claims, f.key)); !errors.Is(err, ErrTenantNotAllowed) {
		t.Fatalf("Verify = %v, want ErrTenantNotAllowed", err)
	}
	claims["tenant_id"] = "acme"

	if _, err := v.Verify(context.Background(), f.sign(t, claims, f.key)); err != nil {
		t.Errorf("Verify with the same jti after a refusal = %v, want accepted", err)
	}
}

// TestUsedIDsForgetExpiredTokens locks that the used-jti set does not grow
// forever: once full, expired entries make room, and live ones never do.
func TestUsedIDsForgetExpiredTokens(t *testing.T) {
	var u usedIDs
	for i := range maxUsedIDs {
		if !u.claim(strconv.Itoa(i), testNow.Add(time.Minute), testNow) {
			t.Fatalf("claim %d refused before the set was full", i)
		}
	}
	if u.claim("one-more", testNow.Add(time.Minute), testNow) {
		t.Fatal("claim on a full set of live tokens = true, want refused")
	}

	later := testNow.Add(2 * time.Minute)

	if !u.claim("one-more", later.Add(time.Minute), later) {
		t.Error("claim after every entry expired = false, want accepted")
	}
	if len(u.exp) != 1 {
		t.Errorf("set holds %d entries after pruning, want 1", len(u.exp))
	}
}
