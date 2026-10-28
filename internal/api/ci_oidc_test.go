package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/cioidc"
	"github.com/dexadata/dexaflow/internal/domain"
)

// ciAPISecret is built at run time so the secret scanner does not read a
// test fixture as a leaked key.
var ciAPISecret = "ci-api-test-" + strings.Repeat("0", 28)

// fakeCIVerifier answers every token with its identity or its error, and
// advertises the entries of one tenant.
type fakeCIVerifier struct {
	id  *cioidc.Identity
	err error
}

func (f *fakeCIVerifier) Verify(context.Context, string) (*cioidc.Identity, error) {
	return f.id, f.err
}

func (f *fakeCIVerifier) Advertised(tenant string) []domain.CIOIDCIssuer {
	if tenant != "acme" {
		return nil
	}
	return []domain.CIOIDCIssuer{{Name: "github-acme", Provider: "github", Issuer: "https://token.actions.githubusercontent.com", Audience: "https://dexaflow.acme.example"}}
}

func (f *fakeCIVerifier) Fingerprint(name string) (string, bool) {
	return "fp1", name == "github-acme"
}

// fakeCIRoles holds the grants of acme's deployer, or fails.
type fakeCIRoles struct{ err error }

func (f fakeCIRoles) RolePermissions(_ context.Context, tenant, role string) ([]auth.Permission, error) {
	if f.err != nil {
		return nil, f.err
	}
	if tenant != "acme" || role != "deployer" {
		return nil, auth.ErrRoleNotFound
	}
	return []auth.Permission{{Action: "read", Resource: "dag"}, {Action: "deploy", Resource: "dag"}}, nil
}

// fakeCIExchanges counts the exchanges by issuer and outcome.
type fakeCIExchanges struct {
	mu  sync.Mutex
	got []string
}

func (f *fakeCIExchanges) RecordCIOIDCExchange(issuer, outcome string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, issuer+"/"+outcome)
}

// acmeIdentity is what the verifier answers for acme's production job.
func acmeIdentity() *cioidc.Identity {
	return &cioidc.Identity{Issuer: "github-acme", Tenant: "acme", Subject: "repo:acme/dags:environment:production",
		Role: "deployer", Scopes: []string{auth.ScopeDeploy}, Fingerprint: "fp1"}
}

type ciServer struct {
	srv       http.Handler
	exchanges *fakeCIExchanges
	audit     *fakeAuthAudit
	logs      *bytes.Buffer
}

// newCIServer is the full API with CI OIDC on: the verifier, the session
// authenticator in front of a fake one, and the roles store.
func newCIServer(v *fakeCIVerifier, roles fakeCIRoles) ciServer {
	s := ciServer{exchanges: &fakeCIExchanges{}, audit: &fakeAuthAudit{}, logs: &bytes.Buffer{}}
	s.srv = newTestServer(Dependencies{
		Observability: ObservabilityDeps{
			Logger: slog.New(slog.NewJSONHandler(s.logs, nil)),
		},
		Auth: AuthDeps{
			Authenticator: auth.NewCISessionAuthenticator(&fakeAuthn{authErr: auth.ErrInvalidToken}, ciAPISecret, v, roles),
			RateLimiter:   auth.NewRateLimiter(100, time.Minute),
			TokenTTLSecs:  900,
			JWTSecret:     ciAPISecret,
			AuthAudit:     s.audit,
		},
		Issuers: IssuerDeps{
			CIOIDC:           v,
			CIOIDCRoles:      roles,
			CIOIDCSessionTTL: 20 * time.Minute,
			CIOIDCExchanges:  s.exchanges,
		},
	})
	return s
}

func postCIToken(srv http.Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, ciTokenPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "192.0.2.10:1234"
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

const providerToken = "eyJhbGciOiJSUzI1NiJ9.provider-payload.provider-signature"

func TestCITokenExchangeIssuesASessionThatDeploys(t *testing.T) {
	s := newCIServer(&fakeCIVerifier{id: acmeIdentity()}, fakeCIRoles{})

	rec := postCIToken(s.srv, `{"token":"`+providerToken+`"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d %s, want 200", rec.Code, rec.Body.String())
	}
	var got ciTokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.AccessToken, auth.CISessionPrefix) || got.TokenType != "Bearer" || got.ExpiresIn != 1200 ||
		got.Tenant != "acme" || got.Role != "deployer" || got.Issuer != "github-acme" || len(got.Scopes) != 1 || got.Scopes[0] != auth.ScopeDeploy {
		t.Errorf("response = %+v", got)
	}
	if want := []string{"github-acme/issued"}; fmt.Sprint(s.exchanges.got) != fmt.Sprint(want) {
		t.Errorf("metric = %v, want %v", s.exchanges.got, want)
	}
	if !s.audit.has("ci_oidc.exchange", "success") {
		t.Errorf("audit = %+v, want a ci_oidc.exchange success", s.audit.events)
	}

	// The session is a deploy principal of acme: it reads the deploy
	// defaults, which list acme's entries.
	dd := getDeployDefaults(s.srv, got.AccessToken)
	if dd.Code != http.StatusOK {
		t.Fatalf("deployDefaults with the session = %d %s, want 200", dd.Code, dd.Body.String())
	}
	var d domain.DeployDefaults
	if err := json.Unmarshal(dd.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.CIOIDC == nil || len(d.CIOIDC.Issuers) != 1 || d.CIOIDC.Issuers[0].Name != "github-acme" {
		t.Errorf("ci_oidc = %+v, want github-acme", d.CIOIDC)
	}
}

// TestCITokenRefusals maps each verifier and store outcome to its status
// and metric outcome.
func TestCITokenRefusals(t *testing.T) {
	named := func(err error) error {
		return &cioidc.Error{Issuer: "github-acme", Tenant: "acme", Subject: "repo:mallory/dags:ref:refs/heads/main", Err: err}
	}
	cases := []struct {
		name    string
		v       *fakeCIVerifier
		roles   fakeCIRoles
		status  int
		outcome string
	}{
		{"unknown issuer", &fakeCIVerifier{err: &cioidc.Error{Err: cioidc.ErrUnknownIssuer}}, fakeCIRoles{}, http.StatusUnauthorized, "unknown/unknown_issuer"},
		{"invalid token", &fakeCIVerifier{err: named(cioidc.ErrInvalidToken)}, fakeCIRoles{}, http.StatusUnauthorized, "github-acme/invalid_token"},
		{"no rule", &fakeCIVerifier{err: named(cioidc.ErrNoMatchingRule)}, fakeCIRoles{}, http.StatusForbidden, "github-acme/no_rule"},
		{"keys unavailable", &fakeCIVerifier{err: named(cioidc.ErrKeysUnavailable)}, fakeCIRoles{}, http.StatusServiceUnavailable, "github-acme/keys_unavailable"},
		{"role missing", &fakeCIVerifier{id: func() *cioidc.Identity { i := acmeIdentity(); i.Role = "ghost"; return i }()}, fakeCIRoles{}, http.StatusForbidden, "github-acme/role_missing"},
		{"store down", &fakeCIVerifier{id: acmeIdentity()}, fakeCIRoles{err: errors.New("database down")}, http.StatusServiceUnavailable, "github-acme/error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newCIServer(tc.v, tc.roles)

			rec := postCIToken(s.srv, `{"token":"`+providerToken+`"}`)

			if rec.Code != tc.status {
				t.Errorf("status = %d %s, want %d", rec.Code, rec.Body.String(), tc.status)
			}
			if strings.Contains(rec.Body.String(), auth.CISessionPrefix) {
				t.Errorf("a refusal carried a session: %s", rec.Body.String())
			}
			if fmt.Sprint(s.exchanges.got) != "["+tc.outcome+"]" {
				t.Errorf("metric = %v, want [%s]", s.exchanges.got, tc.outcome)
			}
		})
	}
}

// TestCITokenLogsNoTokenContents: neither the log, nor the audit, nor a
// response carries the provider's token or the session.
func TestCITokenLogsNoTokenContents(t *testing.T) {
	for _, v := range []*fakeCIVerifier{
		{id: acmeIdentity()},
		{err: &cioidc.Error{Issuer: "github-acme", Tenant: "acme", Subject: "repo:acme/dags:ref:refs/heads/x", Err: cioidc.ErrNoMatchingRule}},
	} {
		s := newCIServer(v, fakeCIRoles{})
		rec := postCIToken(s.srv, `{"token":"`+providerToken+`"}`)

		var got ciTokenResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &got) //nolint:errcheck // a refusal is a problem body
		for _, secret := range []string{"provider-payload", "provider-signature", got.AccessToken} {
			if secret == "" {
				continue
			}
			if strings.Contains(s.logs.String(), secret) {
				t.Errorf("the log carries %q:\n%s", secret, s.logs.String())
			}
			if strings.Contains(fmt.Sprint(s.audit.events), secret) {
				t.Errorf("the audit carries %q: %+v", secret, s.audit.events)
			}
		}
		if !strings.Contains(s.logs.String(), `"issuer":"github-acme"`) {
			t.Errorf("the log does not name the entry:\n%s", s.logs.String())
		}
	}
}

func TestCITokenRefusesABadBody(t *testing.T) {
	s := newCIServer(&fakeCIVerifier{id: acmeIdentity()}, fakeCIRoles{})
	for name, body := range map[string]string{
		"not JSON":      `token`,
		"no token":      `{}`,
		"empty token":   `{"token":""}`,
		"unknown field": `{"token":"x","tenant":"globex"}`,
		"too large":     `{"token":"` + strings.Repeat("a", ciTokenMaxBody) + `"}`,
	} {
		if rec := postCIToken(s.srv, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d %s, want 400", name, rec.Code, rec.Body.String())
		}
	}
	if len(s.exchanges.got) != 0 {
		t.Errorf("bad bodies were counted as exchanges: %v", s.exchanges.got)
	}
}

// TestCITokenIsOffWithoutIssuers: no verifier, no route.
func TestCITokenIsOffWithoutIssuers(t *testing.T) {
	srv := newTestServer(Dependencies{Observability: ObservabilityDeps{Logger: discardLogger()}, Auth: AuthDeps{Authenticator: &fakeAuthn{}, RateLimiter: auth.NewRateLimiter(5, time.Minute)}})
	if rec := postCIToken(srv, `{"token":"x"}`); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	admin := &auth.User{ID: "u1", TenantID: "acme", Roles: []string{"admin"}}
	rec := getDeployDefaults(deployDefaultsServer(admin, domain.DeployDefaults{}), "tok")
	if !strings.Contains(rec.Body.String(), `"ci_oidc":null`) {
		t.Errorf("deployDefaults without issuers = %s, want ci_oidc null", rec.Body.String())
	}
}

// TestCITokenRateLimitsFailures: refusals spend a per-address budget, and
// a caller over it is answered 429 without a verification.
func TestCITokenRateLimitsFailures(t *testing.T) {
	s := newCIServer(&fakeCIVerifier{err: &cioidc.Error{Err: cioidc.ErrUnknownIssuer}}, fakeCIRoles{})
	for i := range ciTokenFailuresPerMinute {
		if rec := postCIToken(s.srv, `{"token":"x"}`); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d, want 401", i+1, rec.Code)
		}
	}
	if rec := postCIToken(s.srv, `{"token":"x"}`); rec.Code != http.StatusTooManyRequests {
		t.Errorf("over the budget = %d, want 429", rec.Code)
	}

	ok := newCIServer(&fakeCIVerifier{id: acmeIdentity()}, fakeCIRoles{})
	for i := range ciTokenFailuresPerMinute + 5 {
		if rec := postCIToken(ok.srv, `{"token":"x"}`); rec.Code != http.StatusOK {
			t.Fatalf("success %d = %d, want 200: successes spend no budget", i+1, rec.Code)
		}
	}
}

// TestCISessionIsNotABrowserSession: a session in the cookie is ignored.
func TestCISessionIsNotABrowserSession(t *testing.T) {
	s := newCIServer(&fakeCIVerifier{id: acmeIdentity()}, fakeCIRoles{})
	var got ciTokenResponse
	if err := json.Unmarshal(postCIToken(s.srv, `{"token":"x"}`).Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v2/deployDefaults", http.NoBody)
	req.AddCookie(&http.Cookie{Name: authTokenCookie, Value: got.AccessToken})
	rec := httptest.NewRecorder()
	s.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("session as a cookie = %d, want 401", rec.Code)
	}
}

// TestDeployDefaultsListsOnlyTheCallersTenant: a user of another tenant
// sees no CI issuer of acme.
func TestDeployDefaultsListsOnlyTheCallersTenant(t *testing.T) {
	for tenant, want := range map[string]string{"acme": `"name":"github-acme"`, "globex": `"ci_oidc":null`} {
		srv := newTestServer(Dependencies{
			Observability: ObservabilityDeps{
				Logger: discardLogger(),
			},
			Auth: AuthDeps{
				Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: tenant, Roles: []string{"admin"}}},
				RateLimiter:   auth.NewRateLimiter(5, time.Minute),
			},
			Issuers: IssuerDeps{
				CIOIDC: &fakeCIVerifier{},
			},
		})
		rec := getDeployDefaults(srv, "tok")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: %d %s, want %s", tenant, rec.Code, rec.Body.String(), want)
		}
		if tenant == "globex" && strings.Contains(rec.Body.String(), "acme") {
			t.Errorf("globex sees acme's entry: %s", rec.Body.String())
		}
	}
}
