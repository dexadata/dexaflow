package issuer

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// clockedVerifier is a bearer verifier whose clock the test moves.
type clockedVerifier struct {
	*Verifier
	mu  sync.Mutex
	now time.Time
}

func newClockedVerifier(f *fakeJWKS) *clockedVerifier {
	cv := &clockedVerifier{Verifier: New(context.Background(), bearerConfig(f)), now: testNow}
	cv.Verifier.now = func() time.Time {
		cv.mu.Lock()
		defer cv.mu.Unlock()
		return cv.now
	}
	return cv
}

func (cv *clockedVerifier) advance(d time.Duration) {
	cv.mu.Lock()
	cv.now = cv.now.Add(d)
	cv.mu.Unlock()
}

// forged signs bearer claims with a key the issuer never published, under a
// key id the JWKS does not list: what an anonymous caller can mint at will.
func forged(t *testing.T, claims jwt.MapClaims, kid string) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = kid
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestForgedBearersDownloadKeysAtMostOncePerCooldown locks the fix for the
// JWKS amplification: unknown key ids make the verifier download the JWKS at
// most once per KeyRefreshCooldown, however many forged tokens arrive.
func TestForgedBearersDownloadKeysAtMostOncePerCooldown(t *testing.T) {
	f := newFakeJWKS(t)
	v := newClockedVerifier(f)

	for i := range 20 {
		raw := forged(t, bearerClaims(), "forged-"+strconv.Itoa(i))
		if _, err := v.VerifyBearer(context.Background(), raw); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("forged token %d: err = %v, want ErrInvalidToken", i, err)
		}
	}
	if n := f.calls.Load(); n != 1 {
		t.Fatalf("JWKS downloads for 20 forged tokens = %d, want 1", n)
	}

	v.advance(KeyRefreshCooldown)
	_, _ = v.VerifyBearer(context.Background(), forged(t, bearerClaims(), "forged-late"))
	if n := f.calls.Load(); n != 2 {
		t.Errorf("JWKS downloads after the cooldown = %d, want 2", n)
	}
}

// TestBearerPrecheckDownloadsNothing locks the cheap precheck: a token that
// does not name the trusted issuer and a bearer audience is refused before
// any signature work, so it never reaches the JWKS.
func TestBearerPrecheckDownloadsNothing(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(jwt.MapClaims)
	}{
		{"another issuer", func(c jwt.MapClaims) { c["iss"] = "https://evil.example.com" }},
		{"no issuer", func(c jwt.MapClaims) { delete(c, "iss") }},
		{"another audience", func(c jwt.MapClaims) { c["aud"] = "something-else" }},
		{"no audience", func(c jwt.MapClaims) { delete(c, "aud") }},
		{"the handoff audience too", func(c jwt.MapClaims) { c["aud"] = []string{"leoflow-mcp", "leoflow-engine"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeJWKS(t)
			v := newClockedVerifier(f)
			claims := bearerClaims()
			tc.mutate(claims)

			_, err := v.VerifyBearer(context.Background(), forged(t, claims, "forged"))

			if !errors.Is(err, ErrInvalidToken) {
				t.Errorf("err = %v, want ErrInvalidToken", err)
			}
			if n := f.calls.Load(); n != 0 {
				t.Errorf("JWKS downloads = %d, want 0", n)
			}
		})
	}
	t.Run("malformed", func(t *testing.T) {
		f := newFakeJWKS(t)
		v := newClockedVerifier(f)
		for _, raw := range []string{"", "a.b", "a.b.c", "x.!!.y"} {
			if _, err := v.VerifyBearer(context.Background(), raw); !errors.Is(err, ErrInvalidToken) {
				t.Errorf("%q: err = %v, want ErrInvalidToken", raw, err)
			}
		}
		if n := f.calls.Load(); n != 0 {
			t.Errorf("JWKS downloads = %d, want 0", n)
		}
	})
}

// TestRotatedKeyIsPickedUpAfterTheCooldown locks that the cooldown delays,
// but does not prevent, a key rotation: the new key id verifies once the
// cooldown since the last download has passed.
func TestRotatedKeyIsPickedUpAfterTheCooldown(t *testing.T) {
	f := newFakeJWKS(t)
	v := newClockedVerifier(f)
	if _, err := v.VerifyBearer(context.Background(), f.sign(t, bearerClaims(), f.key)); err != nil {
		t.Fatalf("first bearer: %v", err)
	}
	newKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.key, f.kid = newKey, "k2"
	f.mu.Unlock()

	v.advance(KeyRefreshCooldown)
	if _, err := v.VerifyBearer(context.Background(), f.sign(t, bearerClaims(), newKey)); err != nil {
		t.Fatalf("rotated bearer a cooldown after the first download: %v", err)
	}
	if n := f.calls.Load(); n != 2 {
		t.Errorf("JWKS downloads = %d, want 2 (the first, then the rotation)", n)
	}
}

// TestRotationWithinTheCooldownWaitsForIt documents the trade-off: an unknown
// key id seen within the cooldown of the last download is refused, and
// verifies once the cooldown has passed.
func TestRotationWithinTheCooldownWaitsForIt(t *testing.T) {
	f := newFakeJWKS(t)
	v := newClockedVerifier(f)
	// A forged token spends the download.
	_, _ = v.VerifyBearer(context.Background(), forged(t, bearerClaims(), "forged"))
	newKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.key, f.kid = newKey, "k2"
	f.mu.Unlock()
	raw := f.sign(t, bearerClaims(), newKey)

	if _, err := v.VerifyBearer(context.Background(), raw); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("within the cooldown: err = %v, want ErrInvalidToken", err)
	}
	v.advance(KeyRefreshCooldown)
	if _, err := v.VerifyBearer(context.Background(), raw); err != nil {
		t.Errorf("after the cooldown: %v", err)
	}
}

// TestKeyEndpointOutageIsUnavailable locks L3: when the JWKS cannot be
// downloaded the bearer is refused as ErrKeysUnavailable, not as an invalid
// token, so the API answers 503 instead of telling the client to sign in
// again. The cooldown still holds during the outage.
func TestKeyEndpointOutageIsUnavailable(t *testing.T) {
	f := newFakeJWKS(t)
	f.down.Store(true)
	v := newClockedVerifier(f)
	raw := f.sign(t, bearerClaims(), f.key)

	for i := range 3 {
		_, err := v.VerifyBearer(context.Background(), raw)
		if !errors.Is(err, ErrKeysUnavailable) || errors.Is(err, ErrInvalidToken) {
			t.Fatalf("call %d: err = %v, want ErrKeysUnavailable and not ErrInvalidToken", i, err)
		}
	}
	if n := f.calls.Load(); n != 1 {
		t.Errorf("JWKS downloads during the outage = %d, want 1", n)
	}

	f.down.Store(false)
	v.advance(KeyRefreshCooldown)
	if _, err := v.VerifyBearer(context.Background(), raw); err != nil {
		t.Errorf("after the outage: %v", err)
	}
}

// TestConcurrentFirstBearersShareOneDownload locks that a burst of requests
// on a cold cache waits for one download instead of each starting its own or
// being refused.
func TestConcurrentFirstBearersShareOneDownload(t *testing.T) {
	f := newFakeJWKS(t)
	v := newClockedVerifier(f)
	raw := f.sign(t, bearerClaims(), f.key)

	var wg sync.WaitGroup
	errs := make([]error, 16)
	for i := range errs {
		wg.Go(func() { _, errs[i] = v.VerifyBearer(context.Background(), raw) })
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("request %d: %v", i, err)
		}
	}
	if n := f.calls.Load(); n != 1 {
		t.Errorf("JWKS downloads = %d, want 1", n)
	}
}

// TestHandoffSharesTheCooldown locks that the handoff's verifier uses the same
// key cache, so the handoff route is no second amplification path.
func TestHandoffSharesTheCooldown(t *testing.T) {
	f := newFakeJWKS(t)
	v := newClockedVerifier(f)
	_, _ = v.VerifyBearer(context.Background(), forged(t, bearerClaims(), "forged-1"))
	for i := range 5 {
		claims := goodClaims()
		_, _ = v.Verify(context.Background(), forged(t, claims, "forged-h"+strconv.Itoa(i)))
	}
	if n := f.calls.Load(); n != 1 {
		t.Errorf("JWKS downloads = %d, want 1", n)
	}
}
