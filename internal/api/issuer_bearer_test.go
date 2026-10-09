package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/issuer"
)

// fakeBearerIssuer accepts the bearer "good" as a token of tenant acme. Every
// other token is refused the way issuer.Verifier refuses one.
type fakeBearerIssuer struct{ calls int }

func (*fakeBearerIssuer) Provider() string { return "issuer:portal" }

func (f *fakeBearerIssuer) VerifyBearer(_ context.Context, raw string) (*issuer.Identity, error) {
	f.calls++
	if raw != "good" {
		return nil, fmt.Errorf("%w: bad signature", issuer.ErrInvalidToken)
	}
	// Read only, as the real verifier reads a token without a scope claim.
	return &issuer.Identity{Subject: "user-42", Email: "ana@acme.com", Tenant: "acme", Scopes: []string{auth.ScopeRead}}, nil
}

// tenantDagRepo records the tenant a DAG listing was scoped to.
type tenantDagRepo struct {
	fakeDagRepo
	tenant string
}

func (r *tenantDagRepo) ListDags(_ context.Context, tenant string, _, _ int) ([]domain.DAG, int, error) {
	r.tenant = tenant
	return []domain.DAG{{DagID: "etl"}}, 1, nil
}

type bearerFixture struct {
	srv    http.Handler
	issuer *fakeBearerIssuer
	users  *fakeIssuerUsers
	audit  *fakeAuthAudit
	dags   *tenantDagRepo
}

// linkedReader is the user row a valid bearer resolves to: tenant acme, with
// only the permission to read DAGs.
func linkedReader() *auth.User {
	return &auth.User{ID: "u-1", TenantID: "acme", Email: "ana@acme.com", Roles: []string{"Viewer"},
		Permissions: []auth.Permission{{Action: "read", Resource: "dag"}}}
}

func newBearerFixture(t *testing.T, users *fakeIssuerUsers, withIssuer bool) *bearerFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	f := &bearerFixture{issuer: &fakeBearerIssuer{}, users: users, audit: &fakeAuthAudit{}, dags: &tenantDagRepo{}}
	deps := Dependencies{
		Logger:             discardLogger(),
		Authenticator:      &fakeAuthn{authErr: auth.ErrInvalidToken},
		RateLimiter:        auth.NewRateLimiter(5, time.Minute),
		TokenTTLSecs:       900,
		Dags:               f.dags,
		TrustedIssuerUsers: users,
		AuthAudit:          f.audit,
	}
	if withIssuer {
		deps.TrustedIssuerBearer = f.issuer
	}
	f.srv = NewServer(deps)
	return f
}

func (f *bearerFixture) get(path, bearer string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, http.NoBody)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	return rec
}

// TestIssuerBearerReadsDagsOfItsTenant is the main path of #1468: a trusted
// issuer's bearer reads /api/v2/dags, scoped to the linked user's tenant.
func TestIssuerBearerReadsDagsOfItsTenant(t *testing.T) {
	f := newBearerFixture(t, &fakeIssuerUsers{user: linkedReader(), active: true}, true)

	rec := f.get("/api/v2/dags", "good")

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v2/dags = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	if f.dags.tenant != "acme" {
		t.Errorf("listing scoped to tenant %q, want acme", f.dags.tenant)
	}
	if f.users.provider != "issuer:portal" || f.users.subject != "user-42" {
		t.Errorf("user looked up by (%q, %q), want (issuer:portal, user-42)", f.users.provider, f.users.subject)
	}
}

// TestIssuerBearerTakesPermissionsFromTheUserRow locks that the token grants
// nothing: a linked user without the permission is refused by RBAC.
func TestIssuerBearerTakesPermissionsFromTheUserRow(t *testing.T) {
	user := linkedReader()
	user.Permissions = nil
	f := newBearerFixture(t, &fakeIssuerUsers{user: user, active: true}, true)

	rec := f.get("/api/v2/dags", "good")

	if rec.Code != http.StatusForbidden {
		t.Errorf("GET /api/v2/dags without read:dag = %d, want 403", rec.Code)
	}
}

// TestIssuerBearerRefusals covers every way a bearer that is not an engine
// token fails: each one is 401, like a bad engine token, and the ones whose
// token verified leave an audit record of why.
func TestIssuerBearerRefusals(t *testing.T) {
	otherTenant := linkedReader()
	otherTenant.TenantID = "globex"
	cases := []struct {
		name      string
		users     *fakeIssuerUsers
		token     string
		wantAudit string
	}{
		{"token refused by the issuer", &fakeIssuerUsers{user: linkedReader(), active: true}, "forged", ""},
		{"unknown subject", &fakeIssuerUsers{}, "good", "user_not_linked"},
		{"disabled user", &fakeIssuerUsers{user: linkedReader(), active: false}, "good", "user_inactive"},
		{"user linked in another tenant", &fakeIssuerUsers{user: otherTenant, active: true}, "good", "tenant_mismatch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newBearerFixture(t, tc.users, true)

			rec := f.get("/api/v2/dags", tc.token)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			if f.dags.tenant != "" {
				t.Errorf("handler ran for tenant %q, want refused before it", f.dags.tenant)
			}
			if tc.wantAudit == "" {
				if len(f.audit.events) != 0 {
					t.Errorf("audit = %+v, want none for a token the issuer refused", f.audit.events)
				}
				return
			}
			if len(f.audit.events) != 1 {
				t.Fatalf("audit = %+v, want one issuer.bearer.failure", f.audit.events)
			}
			e := f.audit.events[0]
			if e.action != "issuer.bearer.failure" || e.outcome != "failure" || e.extra["reason"] != tc.wantAudit || e.tenant != "acme" {
				t.Errorf("audit = %+v, want issuer.bearer.failure in acme with reason %s", e, tc.wantAudit)
			}
		})
	}
}

// TestIssuerBearerIsOffWithoutAVerifier locks the default: without bearer
// audiences the server never consults the issuer for a bearer.
func TestIssuerBearerIsOffWithoutAVerifier(t *testing.T) {
	f := newBearerFixture(t, &fakeIssuerUsers{user: linkedReader(), active: true}, false)

	rec := f.get("/api/v2/dags", "good")

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 with the bearer mode off", rec.Code)
	}
}

// TestIssuerBearerStoreOutageIs503 keeps the rule of #1087: a lookup that
// failed says nothing about the token, so the client is not told to sign in
// again.
func TestIssuerBearerStoreOutageIs503(t *testing.T) {
	f := newBearerFixture(t, &fakeIssuerUsers{err: errors.New("connection refused")}, true)

	rec := f.get("/api/v2/dags", "good")

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

// TestEngineOutageDoesNotFallBackToTheIssuer keeps an outage an outage: when
// the engine could not check its own token, the issuer is not asked to vouch
// for it instead, and the answer stays 503.
func TestEngineOutageDoesNotFallBackToTheIssuer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	bearer := &fakeBearerIssuer{}
	srv := NewServer(Dependencies{
		Logger:              discardLogger(),
		Authenticator:       &fakeAuthn{authErr: errors.New("database unreachable")},
		RateLimiter:         auth.NewRateLimiter(5, time.Minute),
		Dags:                &tenantDagRepo{},
		TrustedIssuerBearer: bearer,
		TrustedIssuerUsers:  &fakeIssuerUsers{user: linkedReader(), active: true},
	})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v2/dags", http.NoBody)
	req.Header.Set("Authorization", "Bearer good")
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	if bearer.calls != 0 {
		t.Errorf("issuer consulted %d times during an engine outage, want 0", bearer.calls)
	}
}

// TestIssuerBearerIsNotReadFromTheCookie locks that only the Authorization
// header carries an issuer bearer. The session cookie is the engine's own.
func TestIssuerBearerIsNotReadFromTheCookie(t *testing.T) {
	f := newBearerFixture(t, &fakeIssuerUsers{user: linkedReader(), active: true}, true)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v2/dags", http.NoBody)
	req.AddCookie(&http.Cookie{Name: authTokenCookie, Value: "good"})
	rec := httptest.NewRecorder()

	f.srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for an issuer token in the cookie", rec.Code)
	}
	if f.issuer.calls != 0 {
		t.Errorf("issuer consulted %d times for a cookie, want 0", f.issuer.calls)
	}
}

// TestIssuerBearerIgnoresTheCookieBehindABadHeader covers the fallback order:
// a stale header falls back to the cookie as an engine token only, so an
// issuer token planted in the cookie still gets nothing.
func TestIssuerBearerIgnoresTheCookieBehindABadHeader(t *testing.T) {
	f := newBearerFixture(t, &fakeIssuerUsers{user: linkedReader(), active: true}, true)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v2/dags", http.NoBody)
	req.Header.Set("Authorization", "Bearer stale")
	req.AddCookie(&http.Cookie{Name: authTokenCookie, Value: "good"})
	rec := httptest.NewRecorder()

	f.srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if f.issuer.calls != 1 {
		t.Errorf("issuer consulted %d times, want once, for the header only", f.issuer.calls)
	}
}

// TestEngineTokenNeverReachesTheIssuer locks the order: an engine token that
// authenticates is used as is, with no JWKS verification behind it.
func TestEngineTokenNeverReachesTheIssuer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	bearer := &fakeBearerIssuer{}
	dags := &tenantDagRepo{}
	srv := NewServer(Dependencies{
		Logger:              discardLogger(),
		Authenticator:       &fakeAuthn{user: &auth.User{ID: "u-9", TenantID: "initech", Roles: []string{"admin"}}},
		RateLimiter:         auth.NewRateLimiter(5, time.Minute),
		Dags:                dags,
		TrustedIssuerBearer: bearer,
		TrustedIssuerUsers:  &fakeIssuerUsers{},
	})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v2/dags", http.NoBody)
	req.Header.Set("Authorization", "Bearer engine-token")
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || dags.tenant != "initech" {
		t.Fatalf("status = %d, tenant = %q; want 200 for initech", rec.Code, dags.tenant)
	}
	if bearer.calls != 0 {
		t.Errorf("issuer consulted %d times for a valid engine token, want 0", bearer.calls)
	}
}
