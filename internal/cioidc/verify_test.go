package cioidc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestVerifyMapsTheMatchingRuleToItsRole(t *testing.T) {
	p := newFakeProvider(t)
	v := newTestVerifier(acmeIssuer(p))

	id, err := v.Verify(context.Background(), p.sign(t, githubClaims(p.srv.URL), nil))

	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	want := Identity{
		Issuer: "github-acme", Tenant: "acme", Subject: "repo:acme/dags:environment:production",
		Role: "deployer", Scopes: []string{"dexaflow:deploy"}, Fingerprint: v.entries[0].fingerprint,
	}
	if !reflect.DeepEqual(*id, want) {
		t.Errorf("identity = %+v, want %+v", *id, want)
	}
}

// TestVerifyTakesTheTenantFromTheIssuerEntry is the tenant isolation rule
// of ADR 0092: claims that look like a tenant never move the principal.
func TestVerifyTakesTheTenantFromTheIssuerEntry(t *testing.T) {
	p := newFakeProvider(t)
	v := newTestVerifier(acmeIssuer(p))
	for _, claim := range []string{"tenant_id", "tenant", "tid", "namespace", "dexaflow_tenant"} {
		claims := githubClaims(p.srv.URL)
		claims[claim] = "globex"

		id, err := v.Verify(context.Background(), p.sign(t, claims, nil))

		if err != nil {
			t.Fatalf("%s: Verify: %v", claim, err)
		}
		if id.Tenant != "acme" {
			t.Errorf("a token with %s=globex resolved to tenant %q, want acme (the issuer entry's)", claim, id.Tenant)
		}
	}
}

// TestVerifyAudienceSelectsTheEntry: two tenants trust the same provider
// with different audiences; each token lands in the tenant of its audience,
// and a rule of one tenant never admits the other tenant's repository.
func TestVerifyAudienceSelectsTheEntry(t *testing.T) {
	p := newFakeProvider(t)
	globex := acmeIssuer(p)
	globex.Name, globex.Audience, globex.Tenant = "github-globex", "https://dexaflow.globex.example", "globex"
	globex.Rules = []Rule{{Claims: map[string]string{"repository": "globex/dags"}, Role: "deployer"}}
	v := newTestVerifier(acmeIssuer(p), globex)

	claims := githubClaims(p.srv.URL)
	claims["aud"] = "https://dexaflow.globex.example"
	claims["repository"] = "globex/dags"
	id, err := v.Verify(context.Background(), p.sign(t, claims, nil))
	if err != nil || id.Tenant != "globex" || id.Issuer != "github-globex" {
		t.Fatalf("globex token: id %+v, err %v; want tenant globex through github-globex", id, err)
	}

	// acme's repository with globex's audience matches no globex rule.
	claims["repository"] = "acme/dags"
	if _, err := v.Verify(context.Background(), p.sign(t, claims, nil)); !errors.Is(err, ErrNoMatchingRule) {
		t.Errorf("acme repository for the globex audience: err = %v, want ErrNoMatchingRule", err)
	}
}

// TestVerifyRejections is the fail-closed table: every token that is not
// one the configured provider minted for this audience, now, is refused.
func TestVerifyRejections(t *testing.T) {
	p := newFakeProvider(t)
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
		{"other issuer", func(c jwt.MapClaims) { c["iss"] = "https://token.example.com" }, nil, ErrUnknownIssuer},
		{"other audience", func(c jwt.MapClaims) { c["aud"] = "https://dexaflow.other.example" }, nil, ErrUnknownIssuer},
		{"no audience", func(c jwt.MapClaims) { delete(c, "aud") }, nil, ErrUnknownIssuer},
		{"expired", func(c jwt.MapClaims) { c["exp"] = testNow.Add(-2 * time.Minute).Unix() }, nil, ErrInvalidToken},
		{"no expiry", func(c jwt.MapClaims) { delete(c, "exp") }, nil, ErrInvalidToken},
		{"not yet valid", func(c jwt.MapClaims) { c["nbf"] = testNow.Add(2 * time.Minute).Unix() }, nil, ErrInvalidToken},
		{"no issued-at", func(c jwt.MapClaims) { delete(c, "iat") }, nil, ErrInvalidToken},
		{"issued in the future", func(c jwt.MapClaims) {
			c["iat"] = testNow.Add(2 * time.Minute).Unix()
			c["exp"] = testNow.Add(5 * time.Minute).Unix()
		}, nil, ErrInvalidToken},
		{"longer than the lifetime cap", func(c jwt.MapClaims) { c["exp"] = testNow.Add(2 * time.Hour).Unix() }, nil, ErrInvalidToken},
		{"no subject", func(c jwt.MapClaims) { delete(c, "sub") }, nil, ErrInvalidToken},
		{"other repository", func(c jwt.MapClaims) { c["repository"] = "mallory/dags" }, nil, ErrNoMatchingRule},
		{"other ref", func(c jwt.MapClaims) { c["ref"] = "refs/heads/feature" }, nil, ErrNoMatchingRule},
		{"no environment", func(c jwt.MapClaims) { delete(c, "environment") }, nil, ErrNoMatchingRule},
		{"environment not a string", func(c jwt.MapClaims) { c["environment"] = []string{"production"} }, nil, ErrNoMatchingRule},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := newTestVerifier(acmeIssuer(p))
			claims := githubClaims(p.srv.URL)
			if tc.mutate != nil {
				tc.mutate(claims)
			}

			_, err := v.Verify(context.Background(), p.sign(t, claims, tc.key))

			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestVerifyToleratesClockSkew: a minute of difference between the
// provider's clock and ours does not refuse a token.
func TestVerifyToleratesClockSkew(t *testing.T) {
	p := newFakeProvider(t)
	v := newTestVerifier(acmeIssuer(p))
	claims := githubClaims(p.srv.URL)
	claims["nbf"] = testNow.Add(50 * time.Second).Unix()
	claims["exp"] = testNow.Add(-50 * time.Second).Unix()
	claims["iat"] = testNow.Add(-4 * time.Minute).Unix()

	if _, err := v.Verify(context.Background(), p.sign(t, claims, nil)); err != nil {
		t.Errorf("a token 50 seconds past exp, nbf 50 seconds ahead: %v, want accepted within the skew", err)
	}
}

// TestVerifyRefusesUnsignedTokens: alg none, and a token whose payload was
// changed after signing, are refused, and the unsigned one before any key
// is downloaded.
func TestVerifyRefusesUnsignedTokens(t *testing.T) {
	p := newFakeProvider(t)
	v := newTestVerifier(acmeIssuer(p))

	_, err := v.Verify(context.Background(), unsigned(t, githubClaims(p.srv.URL)))
	if !errors.Is(err, ErrInvalidToken) {
		t.Errorf("alg none: err = %v, want ErrInvalidToken", err)
	}
	if n := p.jwksCalls.Load() + p.discoveryCalls.Load(); n != 0 {
		t.Errorf("alg none cost %d downloads, want 0", n)
	}
	// The algorithm is read before the issuer: an unsigned token is an
	// invalid token, whichever issuer it claims.
	stranger := githubClaims("https://token.example.com")
	if _, err := v.Verify(context.Background(), unsigned(t, stranger)); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("alg none for an unknown issuer: err = %v, want ErrInvalidToken", err)
	}

	claims := githubClaims(p.srv.URL)
	raw := p.sign(t, claims, nil)
	claims["repository"] = "acme/other"
	if _, err := v.Verify(context.Background(), tamper(t, raw, claims)); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("tampered payload: err = %v, want ErrInvalidToken", err)
	}
}

// TestVerifyRefusesHMAC: an HS256 token, which anyone who knows the
// audience could sign with a guessed secret, is refused before any key is
// downloaded.
func TestVerifyRefusesHMAC(t *testing.T) {
	p := newFakeProvider(t)
	v := newTestVerifier(acmeIssuer(p))
	raw, err := jwt.NewWithClaims(jwt.SigningMethodHS256, githubClaims(p.srv.URL)).SignedString([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := v.Verify(context.Background(), raw); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("HS256: err = %v, want ErrInvalidToken", err)
	}
	if n := p.jwksCalls.Load() + p.discoveryCalls.Load(); n != 0 {
		t.Errorf("HS256 cost %d downloads, want 0", n)
	}
}

// TestVerifyUnknownIssuerCostsNoDownload: a token for an issuer or audience
// nobody configured is refused from its unverified payload.
func TestVerifyUnknownIssuerCostsNoDownload(t *testing.T) {
	p := newFakeProvider(t)
	v := newTestVerifier(acmeIssuer(p))
	claims := githubClaims(p.srv.URL)
	claims["aud"] = "someone-else"

	_, err := v.Verify(context.Background(), p.sign(t, claims, nil))

	if !errors.Is(err, ErrUnknownIssuer) {
		t.Errorf("err = %v, want ErrUnknownIssuer", err)
	}
	if n := p.jwksCalls.Load() + p.discoveryCalls.Load(); n != 0 {
		t.Errorf("an unknown audience cost %d downloads, want 0", n)
	}
}

// TestVerifyFirstMatchingRuleWins: rules are tried in order, and the
// principal gets the role and the scopes of the first that matches, never
// a union.
func TestVerifyFirstMatchingRuleWins(t *testing.T) {
	p := newFakeProvider(t)
	is := acmeIssuer(p)
	is.Rules = []Rule{
		{Claims: map[string]string{"repository": "acme/dags", "ref": "refs/heads/release-*"}, Role: "release-deployer"},
		{Claims: map[string]string{"repository": "acme/*"}, Role: "deployer", Scopes: []string{"dexaflow:deploy", "dexaflow:run"}},
		{Claims: map[string]string{"repository": "acme/dags"}, Role: "op"},
	}
	v := newTestVerifier(is)

	id, err := v.Verify(context.Background(), p.sign(t, githubClaims(p.srv.URL), nil))
	if err != nil {
		t.Fatal(err)
	}
	if id.Role != "deployer" || !reflect.DeepEqual(id.Scopes, []string{"dexaflow:deploy", "dexaflow:run"}) {
		t.Errorf("main: role %q scopes %v, want the second rule's deployer with deploy and run", id.Role, id.Scopes)
	}

	claims := githubClaims(p.srv.URL)
	claims["ref"] = "refs/heads/release-1.2"
	id, err = v.Verify(context.Background(), p.sign(t, claims, nil))
	if err != nil {
		t.Fatal(err)
	}
	if id.Role != "release-deployer" || !reflect.DeepEqual(id.Scopes, []string{"dexaflow:deploy"}) {
		t.Errorf("release branch: role %q scopes %v, want the first rule's release-deployer with deploy only", id.Role, id.Scopes)
	}
}

// TestVerifyPatternsDoNotCrossSlashes: refs/heads/* matches a branch, not a
// branch under a folder, so a pattern grants what it reads as.
func TestVerifyPatternsDoNotCrossSlashes(t *testing.T) {
	p := newFakeProvider(t)
	is := acmeIssuer(p)
	is.Rules = []Rule{{Claims: map[string]string{"repository": "acme/dags", "ref": "refs/heads/*"}, Role: "deployer"}}
	v := newTestVerifier(is)
	claims := githubClaims(p.srv.URL)
	claims["ref"] = "refs/heads/team/x"

	if _, err := v.Verify(context.Background(), p.sign(t, claims, nil)); !errors.Is(err, ErrNoMatchingRule) {
		t.Errorf("refs/heads/team/x against refs/heads/*: err = %v, want ErrNoMatchingRule", err)
	}
}

// TestVerifyDiscoversTheKeys: with no jwks_url, the keys come from the
// issuer's discovery document, fetched once and cached.
func TestVerifyDiscoversTheKeys(t *testing.T) {
	p := newFakeProvider(t)
	v := newTestVerifier(acmeIssuer(p))

	for i := range 3 {
		if _, err := v.Verify(context.Background(), p.sign(t, githubClaims(p.srv.URL), nil)); err != nil {
			t.Fatalf("Verify %d: %v", i+1, err)
		}
	}
	if d, k := p.discoveryCalls.Load(), p.jwksCalls.Load(); d != 1 || k != 1 {
		t.Errorf("downloads: discovery %d, jwks %d, want 1 and 1", d, k)
	}
}

// TestVerifyUsesTheConfiguredJWKSURL: a jwks_url skips discovery.
func TestVerifyUsesTheConfiguredJWKSURL(t *testing.T) {
	p := newFakeProvider(t)
	is := acmeIssuer(p)
	is.JWKSURL = p.srv.URL + "/jwks"
	v := newTestVerifier(is)

	if _, err := v.Verify(context.Background(), p.sign(t, githubClaims(p.srv.URL), nil)); err != nil {
		t.Fatal(err)
	}
	if n := p.discoveryCalls.Load(); n != 0 {
		t.Errorf("discovery fetched %d times with a jwks_url, want 0", n)
	}
}

// TestVerifyRefusesADiscoveryDocumentOfAnotherIssuer: a document that names
// another issuer is not this provider's, and its keys are not trusted.
func TestVerifyRefusesADiscoveryDocumentOfAnotherIssuer(t *testing.T) {
	p := newFakeProvider(t)
	p.docIssuer = "https://evil.example.com"
	v := newTestVerifier(acmeIssuer(p))

	_, err := v.Verify(context.Background(), p.sign(t, githubClaims(p.srv.URL), nil))

	if !errors.Is(err, ErrKeysUnavailable) {
		t.Errorf("err = %v, want ErrKeysUnavailable", err)
	}
	if n := p.jwksCalls.Load(); n != 0 {
		t.Errorf("jwks fetched %d times from a document of another issuer, want 0", n)
	}
}

// TestVerifyProviderOutageIsNotARefusal: keys that cannot be downloaded say
// nothing about the token, so the error is ErrKeysUnavailable (503), not
// ErrInvalidToken (401).
func TestVerifyProviderOutageIsNotARefusal(t *testing.T) {
	p := newFakeProvider(t)
	p.down.Store(true)
	for _, jwksURL := range []string{"", "/jwks"} {
		is := acmeIssuer(p)
		if jwksURL != "" {
			is.JWKSURL = p.srv.URL + jwksURL
		}
		v := newTestVerifier(is)

		_, err := v.Verify(context.Background(), p.sign(t, githubClaims(p.srv.URL), nil))

		if !errors.Is(err, ErrKeysUnavailable) || errors.Is(err, ErrInvalidToken) {
			t.Errorf("jwks_url %q: err = %v, want ErrKeysUnavailable only", jwksURL, err)
		}
	}
}

// TestVerifyErrorNamesTheEntry: a refusal after the entry is known names it
// and its tenant, for the metric and the log; an unknown issuer names none.
func TestVerifyErrorNamesTheEntry(t *testing.T) {
	p := newFakeProvider(t)
	v := newTestVerifier(acmeIssuer(p))
	claims := githubClaims(p.srv.URL)
	claims["repository"] = "mallory/dags"

	_, err := v.Verify(context.Background(), p.sign(t, claims, nil))

	var e *Error
	if !errors.As(err, &e) || e.Issuer != "github-acme" || e.Tenant != "acme" || e.Subject != "repo:acme/dags:environment:production" {
		t.Errorf("err = %#v, want an *Error naming github-acme, acme and the subject", err)
	}
	claims["aud"] = "nobody"
	_, err = v.Verify(context.Background(), p.sign(t, claims, nil))
	if !errors.As(err, &e) || e.Issuer != "" {
		t.Errorf("unknown issuer: err = %#v, want an *Error naming no entry", err)
	}
}

func TestFingerprintChangesWithTheEntry(t *testing.T) {
	p := newFakeProvider(t)
	a := New([]Issuer{acmeIssuer(p)})
	changed := acmeIssuer(p)
	changed.Rules[0].Role = "op"
	b := New([]Issuer{changed})

	fa, okA := a.Fingerprint("github-acme")
	fb, okB := b.Fingerprint("github-acme")
	if !okA || !okB || fa == "" || fa == fb {
		t.Errorf("fingerprints %q (%v) and %q (%v): want two different, non-empty values", fa, okA, fb, okB)
	}
	if again, _ := New([]Issuer{acmeIssuer(p)}).Fingerprint("github-acme"); again != fa {
		t.Errorf("the same entry fingerprints as %q then %q, want stable", fa, again)
	}
	if _, ok := a.Fingerprint("nobody"); ok {
		t.Error("Fingerprint of an unknown entry reported ok")
	}
}

// TestAdvertisedListsOnlyTheTenantsEntries: what deployDefaults serves a
// user is the entries of that user's tenant, and nothing of another's.
func TestAdvertisedListsOnlyTheTenantsEntries(t *testing.T) {
	p := newFakeProvider(t)
	globex := acmeIssuer(p)
	globex.Name, globex.Audience, globex.Tenant = "github-globex", "globex-aud", "globex"
	v := New([]Issuer{acmeIssuer(p), globex})

	got := v.Advertised("acme")

	if len(got) != 1 || got[0].Name != "github-acme" || got[0].Audience != "https://dexaflow.acme.example" ||
		got[0].Provider != "github" || got[0].Issuer != p.srv.URL {
		t.Errorf("Advertised(acme) = %+v, want github-acme alone", got)
	}
	if got := v.Advertised("initech"); len(got) != 0 {
		t.Errorf("Advertised(initech) = %+v, want none", got)
	}
}
