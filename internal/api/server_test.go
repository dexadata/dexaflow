package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
)

type fakeAuthn struct {
	token    string
	user     *auth.User
	issueErr error
	authErr  error
	gotCreds auth.Credentials
}

func (f *fakeAuthn) IssueToken(_ context.Context, c auth.Credentials) (string, error) {
	f.gotCreds = c
	return f.token, f.issueErr
}

func (f *fakeAuthn) Authenticate(context.Context, string) (*auth.User, error) {
	return f.user, f.authErr
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testServer(authn auth.Authenticator) *gin.Engine {
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: authn,
		RateLimiter:   auth.NewRateLimiter(5, time.Minute),
		HealthChecks:  map[string]HealthChecker{},
		CORSOrigins:   []string{"*"},
		TokenTTLSecs:  3600,
	})
}

func do(srv *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestHealthz(t *testing.T) {
	rec := do(testServer(&fakeAuthn{}), http.MethodGet, "/healthz", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz = %d, want 200", rec.Code)
	}
}

func TestDocsAndOpenAPI(t *testing.T) {
	srv := testServer(&fakeAuthn{})
	if rec := do(srv, http.MethodGet, "/docs", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "api-reference") {
		t.Errorf("/docs = %d, body missing scalar", rec.Code)
	}
	rec := do(srv, http.MethodGet, "/openapi.json", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("/openapi.json = %d, want 200", rec.Code)
	}
	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Errorf("/openapi.json is not valid JSON: %v", err)
	}
	if _, ok := doc["paths"]; !ok {
		t.Error("/openapi.json missing paths")
	}
}

func TestAuthTokenSuccess(t *testing.T) {
	rec := do(testServer(&fakeAuthn{token: "jwt-123"}), http.MethodPost, "/auth/token", `{"username":"a@b.c","password":"pw"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("auth/token = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var resp tokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.AccessToken != "jwt-123" || resp.TokenType != "bearer" || resp.ExpiresIn != 3600 {
		t.Errorf("unexpected token response: %+v", resp)
	}
}

func TestAuthTokenBadCredentials(t *testing.T) {
	rec := do(testServer(&fakeAuthn{issueErr: auth.ErrInvalidCredentials}), http.MethodPost, "/auth/token", `{"username":"a","password":"x"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("auth/token bad creds = %d, want 401", rec.Code)
	}
}

func TestProtectedRouteRequiresToken(t *testing.T) {
	srv := gin.New()
	srv.Use(JWTAuth(&fakeAuthn{authErr: auth.ErrInvalidToken}))
	srv.GET("/api/v2/dags", RequirePermission("read", "dag"), func(c *gin.Context) { c.Status(http.StatusOK) })

	if rec := do(srv, http.MethodGet, "/api/v2/dags", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", rec.Code)
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v2/dags", http.NoBody)
	req.Header.Set("Authorization", "Bearer x")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("invalid token = %d, want 401", rec.Code)
	}
}

func TestRequirePermissionForbidden(t *testing.T) {
	srv := gin.New()
	srv.Use(JWTAuth(&fakeAuthn{user: &auth.User{ID: "u1", Roles: []string{"viewer"}}}))
	srv.GET("/api/v2/dags", RequirePermission("write", "dag"), func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v2/dags", http.NoBody)
	req.Header.Set("Authorization", "Bearer good")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("viewer write:dag = %d, want 403", rec.Code)
	}
}

func TestAuthTokenTrimsUsernameNotPassword(t *testing.T) {
	f := &fakeAuthn{token: "jwt"}
	// Username has surrounding whitespace (autofill/paste); password has a
	// trailing space that MUST be preserved (trimming passwords is unsafe).
	rec := do(testServer(f), http.MethodPost, "/auth/token",
		`{"username":"  admin@leoflow.local  ","password":"pw "}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("auth/token = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if f.gotCreds.Username != "admin@leoflow.local" {
		t.Errorf("username = %q, want trimmed 'admin@leoflow.local'", f.gotCreds.Username)
	}
	if f.gotCreds.Password != "pw " {
		t.Errorf("password = %q, want preserved 'pw ' (passwords must not be trimmed)", f.gotCreds.Password)
	}
}
