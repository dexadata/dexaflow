package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/issuer"
)

const testServiceToken = "service-token-with-at-least-32-characters"

// fakeServiceStore records calls and answers with canned results.
type fakeServiceStore struct {
	tenantCreated bool
	tenantErr     error
	user          *auth.User
	userCreated   bool
	userErr       error

	gotTenant, gotDisplay                     string
	gotUserTenant, gotEmail, gotProv, gotSubj string
	gotRoles                                  []string
}

func (f *fakeServiceStore) EnsureTenant(_ context.Context, name, displayName string) (bool, error) {
	f.gotTenant, f.gotDisplay = name, displayName
	return f.tenantCreated, f.tenantErr
}

func (f *fakeServiceStore) EnsureIssuerUser(_ context.Context, tenant, email, provider, subject string, roles []string) (*auth.User, bool, error) {
	f.gotUserTenant, f.gotEmail, f.gotProv, f.gotSubj, f.gotRoles = tenant, email, provider, subject, roles
	return f.user, f.userCreated, f.userErr
}

func serviceServer(t *testing.T, store *fakeServiceStore, withIssuer bool) http.Handler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	deps := Dependencies{
		Logger:         discardLogger(),
		Authenticator:  &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"viewer"}}},
		RateLimiter:    auth.NewRateLimiter(100, time.Minute),
		ServiceToken:   testServiceToken,
		ServiceTenants: store,
	}
	if withIssuer {
		deps.TrustedIssuer = fakeTrustedIssuer{issuer.Identity{}}
	}
	return NewServer(deps)
}

func callService(h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestServiceAPIRequiresTheServiceToken: a user session, a wrong token or no
// token never reaches the store (#1283).
func TestServiceAPIRequiresTheServiceToken(t *testing.T) {
	for name, token := range map[string]string{"none": "", "wrong": "not-the-service-token-but-just-as-long", "user jwt": "a.b.c"} {
		t.Run(name, func(t *testing.T) {
			store := &fakeServiceStore{}
			h := serviceServer(t, store, true)

			rec := callService(h, http.MethodPut, "/api/v2/service/tenants/acme", token, `{}`)

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rec.Code)
			}
			if store.gotTenant != "" {
				t.Error("the store was called without the service token")
			}
		})
	}
}

// TestServiceAPIAbsentWithoutAToken keeps the default surface: no service
// token configured, no service routes.
func TestServiceAPIAbsentWithoutAToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewServer(Dependencies{Logger: discardLogger(), RateLimiter: auth.NewRateLimiter(100, time.Minute),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}}})

	rec := callService(h, http.MethodPut, "/api/v2/service/tenants/acme", testServiceToken, `{}`)

	if rec.Code != http.StatusNotFound && rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want the route to be absent", rec.Code)
	}
}

func TestServiceEnsureTenant(t *testing.T) {
	cases := []struct {
		name     string
		store    *fakeServiceStore
		path     string
		body     string
		want     int
		wantBody string
	}{
		{"created", &fakeServiceStore{tenantCreated: true}, "/api/v2/service/tenants/acme", `{"display_name":"Acme Corp"}`, http.StatusCreated, `"created":true`},
		{"already there", &fakeServiceStore{}, "/api/v2/service/tenants/acme", `{}`, http.StatusOK, `"created":false`},
		{"invalid name", &fakeServiceStore{}, "/api/v2/service/tenants/Acme_Corp", `{}`, http.StatusBadRequest, ""},
		{"store failure", &fakeServiceStore{tenantErr: errors.New("db down")}, "/api/v2/service/tenants/acme", `{}`, http.StatusInternalServerError, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := serviceServer(t, tc.store, true)

			rec := callService(h, http.MethodPut, tc.path, testServiceToken, tc.body)

			if rec.Code != tc.want || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Errorf("got %d %s, want %d containing %q", rec.Code, rec.Body.String(), tc.want, tc.wantBody)
			}
		})
	}
}

func TestServiceEnsureTenantPassesTheDisplayName(t *testing.T) {
	store := &fakeServiceStore{tenantCreated: true}
	h := serviceServer(t, store, true)

	callService(h, http.MethodPut, "/api/v2/service/tenants/acme", testServiceToken, `{"display_name":"Acme Corp"}`)

	if store.gotTenant != "acme" || store.gotDisplay != "Acme Corp" {
		t.Errorf("store got (%q, %q), want (acme, Acme Corp)", store.gotTenant, store.gotDisplay)
	}
}

// TestServiceEnsureUserLinksToTheTrustedIssuer: users the service API makes
// are passwordless and linked under the trusted issuer's provider key, so the
// handoff of #1284 can sign them in.
func TestServiceEnsureUserLinksToTheTrustedIssuer(t *testing.T) {
	store := &fakeServiceStore{user: &auth.User{ID: "u-9", TenantID: "acme", Email: "ana@acme.com", Roles: []string{"operator"}}, userCreated: true}
	h := serviceServer(t, store, true)

	rec := callService(h, http.MethodPut, "/api/v2/service/tenants/acme/users/user-42", testServiceToken,
		`{"email":"ana@acme.com","roles":["operator"]}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d (%s), want 201", rec.Code, rec.Body.String())
	}
	if store.gotUserTenant != "acme" || store.gotSubj != "user-42" || store.gotProv != "issuer:portal" ||
		store.gotEmail != "ana@acme.com" || len(store.gotRoles) != 1 || store.gotRoles[0] != "operator" {
		t.Errorf("store got %+v", store)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["id"] != "u-9" || body["created"] != true {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestServiceEnsureUserErrors(t *testing.T) {
	cases := []struct {
		name       string
		store      *fakeServiceStore
		withIssuer bool
		body       string
		want       int
	}{
		{"no trusted issuer", &fakeServiceStore{}, false, `{"email":"a@b.com"}`, http.StatusConflict},
		{"no email", &fakeServiceStore{}, true, `{}`, http.StatusBadRequest},
		{"unknown tenant", &fakeServiceStore{userErr: domain.ErrNotFound}, true, `{"email":"a@b.com"}`, http.StatusNotFound},
		{"unknown role", &fakeServiceStore{userErr: domain.Safef(domain.ErrValidation, "unknown role %q", "boss")}, true, `{"email":"a@b.com","roles":["boss"]}`, http.StatusUnprocessableEntity},
		{"linked elsewhere", &fakeServiceStore{userErr: domain.ErrConflict}, true, `{"email":"a@b.com"}`, http.StatusConflict},
		{"store failure", &fakeServiceStore{userErr: errors.New("db down")}, true, `{"email":"a@b.com"}`, http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := serviceServer(t, tc.store, tc.withIssuer)

			rec := callService(h, http.MethodPut, "/api/v2/service/tenants/acme/users/user-42", testServiceToken, tc.body)

			if rec.Code != tc.want {
				t.Errorf("status = %d (%s), want %d", rec.Code, rec.Body.String(), tc.want)
			}
		})
	}
}
