package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/gin-gonic/gin"
)

// tenantlessAuthn authenticates every token as a principal that carries no
// tenant, the shape of a token minted without a tenant_id claim.
type tenantlessAuthn struct{}

func (tenantlessAuthn) IssueToken(context.Context, auth.Credentials) (string, error) {
	return "", nil
}
func (tenantlessAuthn) Authenticate(context.Context, string) (*auth.User, error) {
	return &auth.User{ID: "u1", Roles: []string{"admin"}}, nil
}

// TestTenantOfFailsClosedWithoutAPrincipal: a request with no tenant on its
// principal must not be served from the default tenant. tenantOf returns the
// empty name, which no tenant can have, so every tenant-scoped lookup misses.
func TestTenantOfFailsClosedWithoutAPrincipal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := map[string]*auth.User{
		"no principal":         nil,
		"principal, no tenant": {ID: "u1"},
	}
	for name, user := range cases {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		if user != nil {
			c.Set(contextKeyUser, user)
		}
		if got := tenantOf(c); got != "" {
			t.Errorf("%s: tenantOf = %q, want \"\" (never the default tenant)", name, got)
		}
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set(contextKeyUser, &auth.User{ID: "u1", TenantID: "acme"})
	if got := tenantOf(c); got != "acme" {
		t.Errorf("tenantOf = %q, want the principal's tenant acme", got)
	}
}

// TestJWTAuthRejectsATenantlessPrincipal: a token that authenticates but names
// no tenant is refused at the door, instead of reaching a handler that would
// have served it the default tenant's data.
func TestJWTAuthRejectsATenantlessPrincipal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(JWTAuth(tenantlessAuthn{}))
	r.GET("/api/v2/dags", func(c *gin.Context) { c.Status(http.StatusOK) })
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v2/dags", http.NoBody)
	req.Header.Set("Authorization", "Bearer any")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("tenantless principal: status = %d, want 401", rec.Code)
	}
}

// TestDevBypassAuthKeepsTheDefaultTenant: the loopback-only dev mode names the
// default tenant explicitly, so it does not depend on the old fallback.
func TestDevBypassAuthKeepsTheDefaultTenant(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(DevBypassAuth())
	var got string
	r.GET("/api/v2/dags", func(c *gin.Context) { got = tenantOf(c); c.Status(http.StatusOK) })
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v2/dags", http.NoBody))
	if got != "default" {
		t.Errorf("dev mode tenant = %q, want default", got)
	}
}

// mixedAuthn: the bearer "tenantless" authenticates to a principal with no
// tenant, the cookie "good" to a tenanted one.
type mixedAuthn struct{}

func (mixedAuthn) IssueToken(context.Context, auth.Credentials) (string, error) { return "", nil }
func (mixedAuthn) Authenticate(_ context.Context, token string) (*auth.User, error) {
	switch token {
	case "tenantless":
		return &auth.User{ID: "u1"}, nil
	case "good":
		return &auth.User{ID: "u1", TenantID: "default"}, nil
	}
	return nil, auth.ErrInvalidToken
}

// TestJWTAuthTenantlessBearerFallsBackToTheCookie: refusing a tenantless bearer
// keeps the bearer-then-cookie fallback the SPA refresh depends on.
func TestJWTAuthTenantlessBearerFallsBackToTheCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(JWTAuth(mixedAuthn{}))
	r.GET("/api/v2/dags", func(c *gin.Context) { c.String(http.StatusOK, tenantOf(c)) })
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v2/dags", http.NoBody)
	req.Header.Set("Authorization", "Bearer tenantless")
	req.AddCookie(&http.Cookie{Name: authTokenCookie, Value: "good"})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "default" {
		t.Errorf("got %d %q, want 200 from the cookie's tenant", rec.Code, rec.Body.String())
	}
}

// TestShellSessionRejectsATenantlessPrincipal: the SPA shell gate agrees with
// JWTAuth, so it does not render a shell whose every data call would 401.
func TestShellSessionRejectsATenantlessPrincipal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for token, want := range map[string]bool{"tenantless": false, "good": true} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
		c.Request.Header.Set("Authorization", "Bearer "+token)
		if got := shellSessionValid(c, mixedAuthn{}); got != want {
			t.Errorf("token %s: shellSessionValid = %v, want %v", token, got, want)
		}
	}
}

// TestJWTAuthWithTheRealAuthenticator: end to end through the JWT
// authenticator, a dev token without a tenant claim is a 401 and one with a
// tenant is served in that tenant.
func TestJWTAuthWithTheRealAuthenticator(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const secret = "test-secret-test-secret-test-secret-test"
	r := gin.New()
	r.Use(JWTAuth(auth.NewJWTAuthenticator(nil, secret, time.Hour)))
	r.GET("/api/v2/dags", func(c *gin.Context) { c.String(http.StatusOK, tenantOf(c)) })
	for _, tc := range []struct {
		tenant   string
		wantCode int
	}{{"", http.StatusUnauthorized}, {"default", http.StatusOK}} {
		tok, err := auth.MintUserToken(secret, time.Hour, auth.User{ID: auth.DevTokenSubject, TenantID: tc.tenant, Roles: []string{"admin"}})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v2/dags", http.NoBody)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != tc.wantCode {
			t.Errorf("tenant %q: status = %d, want %d", tc.tenant, rec.Code, tc.wantCode)
		}
	}
}
