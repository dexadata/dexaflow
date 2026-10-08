package issuer

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func bearerConfig(f *fakeJWKS) Config {
	cfg := testConfig(f)
	cfg.BearerAudiences = []string{"leoflow-mcp"}
	return cfg
}

// bearerClaims is a token an MCP gateway would mint: the bearer audience, a
// few minutes of life, and no jti, because it is reused on every request.
func bearerClaims() jwt.MapClaims {
	c := goodClaims()
	delete(c, "jti")
	c["aud"] = "leoflow-mcp"
	c["exp"] = testNow.Add(5 * time.Minute).Unix()
	return c
}

func TestVerifyBearerAcceptsATokenForABearerAudience(t *testing.T) {
	f := newFakeJWKS(t)
	v := newTestVerifier(f, bearerConfig(f))
	raw := f.sign(t, bearerClaims(), f.key)

	id, err := v.VerifyBearer(context.Background(), raw)

	if err != nil {
		t.Fatalf("VerifyBearer: %v", err)
	}
	want := Identity{Subject: "user-42", Email: "ana@acme.com", Tenant: "acme"}
	if *id != want {
		t.Errorf("identity = %+v, want %+v", *id, want)
	}
}

// TestVerifyBearerAcceptsTheSameTokenAgain locks the difference from the
// handoff: a bearer is sent on every request of a client, so there is no
// one-use rule.
func TestVerifyBearerAcceptsTheSameTokenAgain(t *testing.T) {
	f := newFakeJWKS(t)
	v := newTestVerifier(f, bearerConfig(f))
	claims := bearerClaims()
	claims["jti"] = "reused"
	raw := f.sign(t, claims, f.key)

	for i := range 3 {
		if _, err := v.VerifyBearer(context.Background(), raw); err != nil {
			t.Fatalf("VerifyBearer call %d: %v", i+1, err)
		}
	}
}

// TestVerifyBearerRejections is the fail-closed table for the bearer mode.
func TestVerifyBearerRejections(t *testing.T) {
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
		{"handoff audience", func(c jwt.MapClaims) { c["aud"] = "leoflow-engine" }, nil, ErrInvalidToken},
		{"handoff and bearer audience", func(c jwt.MapClaims) { c["aud"] = []string{"leoflow-mcp", "leoflow-engine"} }, nil, ErrInvalidToken},
		{"no audience", func(c jwt.MapClaims) { delete(c, "aud") }, nil, ErrInvalidToken},
		{"expired", func(c jwt.MapClaims) { c["exp"] = testNow.Add(-time.Second).Unix() }, nil, ErrInvalidToken},
		{"no subject", func(c jwt.MapClaims) { delete(c, "sub") }, nil, ErrInvalidToken},
		{"no issued-at", func(c jwt.MapClaims) { delete(c, "iat") }, nil, ErrLifetime},
		{"longer than the default", func(c jwt.MapClaims) { c["exp"] = testNow.Add(16 * time.Minute).Unix() }, nil, ErrLifetime},
		{"issued in the future", func(c jwt.MapClaims) {
			c["iat"] = testNow.Add(time.Hour).Unix()
			c["exp"] = testNow.Add(time.Hour + time.Minute).Unix()
		}, nil, ErrLifetime},
		{"no tenant", func(c jwt.MapClaims) { delete(c, "tenant_id") }, nil, ErrTenantNotAllowed},
		{"tenant not allowed", func(c jwt.MapClaims) { c["tenant_id"] = "initech" }, nil, ErrTenantNotAllowed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := newTestVerifier(f, bearerConfig(f))
			claims := bearerClaims()
			if tc.mutate != nil {
				tc.mutate(claims)
			}
			key := f.key
			if tc.key != nil {
				key = tc.key
			}

			_, err := v.VerifyBearer(context.Background(), f.sign(t, claims, key))

			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestVerifyBearerHonorsTheConfiguredLifetime locks that BearerMaxLifetime,
// not the handoff's MaxLifetime, bounds a bearer.
func TestVerifyBearerHonorsTheConfiguredLifetime(t *testing.T) {
	f := newFakeJWKS(t)
	cfg := bearerConfig(f)
	cfg.BearerMaxLifetime = 30 * time.Minute
	v := newTestVerifier(f, cfg)
	claims := bearerClaims()
	claims["exp"] = testNow.Add(29 * time.Minute).Unix()

	if _, err := v.VerifyBearer(context.Background(), f.sign(t, claims, f.key)); err != nil {
		t.Errorf("29-minute bearer under a 30-minute cap = %v, want accepted", err)
	}
	claims["exp"] = testNow.Add(31 * time.Minute).Unix()
	if _, err := v.VerifyBearer(context.Background(), f.sign(t, claims, f.key)); !errors.Is(err, ErrLifetime) {
		t.Errorf("31-minute bearer under a 30-minute cap = %v, want ErrLifetime", err)
	}
}

// TestVerifyBearerIsOffWithoutAudiences locks the default: no bearer
// audience configured means no token is accepted as a bearer, not even one a
// handoff would take.
func TestVerifyBearerIsOffWithoutAudiences(t *testing.T) {
	f := newFakeJWKS(t)
	v := newTestVerifier(f, testConfig(f))

	_, err := v.VerifyBearer(context.Background(), f.sign(t, goodClaims(), f.key))

	if !errors.Is(err, ErrBearerDisabled) {
		t.Errorf("err = %v, want ErrBearerDisabled", err)
	}
	if v.BearerEnabled() {
		t.Error("BearerEnabled() = true without bearer audiences")
	}
}

// TestHandoffRefusesABearerToken keeps the two modes apart in the other
// direction: a bearer posted to the handoff opens no session.
func TestHandoffRefusesABearerToken(t *testing.T) {
	f := newFakeJWKS(t)
	v := newTestVerifier(f, bearerConfig(f))
	claims := bearerClaims()
	claims["jti"] = "j"
	claims["exp"] = testNow.Add(time.Minute).Unix()

	_, err := v.Verify(context.Background(), f.sign(t, claims, f.key))

	if !errors.Is(err, ErrInvalidToken) {
		t.Errorf("handoff Verify of a bearer = %v, want ErrInvalidToken", err)
	}
}

// TestVerifyBearerRefusesAnEngineToken locks that an HS256 token (what the
// engine itself signs) is refused by the algorithm list, before any key is
// fetched, so a stale engine token never costs a JWKS round trip.
func TestVerifyBearerRefusesAnEngineToken(t *testing.T) {
	f := newFakeJWKS(t)
	v := newTestVerifier(f, bearerConfig(f))
	raw, err := jwt.NewWithClaims(jwt.SigningMethodHS256, bearerClaims()).SignedString([]byte("engine-secret"))
	if err != nil {
		t.Fatal(err)
	}

	_, err = v.VerifyBearer(context.Background(), raw)

	if !errors.Is(err, ErrInvalidToken) {
		t.Errorf("err = %v, want ErrInvalidToken", err)
	}
	if n := f.calls.Load(); n != 0 {
		t.Errorf("JWKS fetched %d times for an HS256 token, want 0", n)
	}
}
