package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
)

// TestStructuredLoggerSurfacesErrorDetail is the observability guard the escaped
// 400/500 regressions exposed: a failing request must log its CAUSE and the right
// level (5xx=ERROR, 4xx=WARN), not just a bare status code.
func TestStructuredLoggerSurfacesErrorDetail(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	r := gin.New()
	r.Use(StructuredLogger(logger))
	r.GET("/bad", func(c *gin.Context) {
		AbortProblem(c, http.StatusBadRequest, "bad request", "cannot unmarshal array into Go struct field clearRequest.task_ids of type string")
	})
	r.GET("/boom", func(c *gin.Context) {
		AbortProblem(c, http.StatusInternalServerError, "server error", "scanning task instances: db exploded")
	})
	r.GET("/ok", func(c *gin.Context) { c.Status(http.StatusOK) })

	do := func(p string) map[string]any {
		buf.Reset()
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, p, http.NoBody))
		var m map[string]any
		if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
			t.Fatalf("log line not JSON: %q", buf.String())
		}
		return m
	}
	if m := do("/bad"); m["level"] != "WARN" || !strings.Contains(m["detail"].(string), "task_ids") {
		t.Errorf("4xx must log WARN + cause, got %v", m)
	}
	if m := do("/boom"); m["level"] != "ERROR" || !strings.Contains(m["detail"].(string), "db exploded") {
		t.Errorf("5xx must log ERROR + cause, got %v", m)
	}
	if m := do("/ok"); m["level"] != "INFO" {
		t.Errorf("2xx stays INFO, got %v", m)
	}
}

// tokenAuthn authenticates exactly one token value; anything else is invalid. It
// lets a test distinguish a stale bearer from a valid session cookie.
type tokenAuthn struct{ valid string }

func (tokenAuthn) IssueToken(context.Context, auth.Credentials) (string, error) { return "", nil }
func (a tokenAuthn) Authenticate(_ context.Context, token string) (*auth.User, error) {
	if token == a.valid {
		return &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}, nil
	}
	return nil, auth.ErrInvalidToken
}

// TestJWTAuthFallsBackToCookieWhenBearerInvalid covers the SPA-refresh login bug:
// the Airflow UI loses its in-memory token on refresh and may send a stale/empty
// bearer while a valid session lives in the _token cookie. JWTAuth must try the
// bearer AND fall back to the cookie, accepting whichever validates — otherwise an
// invalid bearer short-circuits to 401 and the UI bounces to login ("flash").
func TestJWTAuthFallsBackToCookieWhenBearerInvalid(t *testing.T) {
	gin.SetMode(gin.TestMode)
	run := func(setup func(*http.Request)) int {
		r := gin.New()
		r.Use(JWTAuth(tokenAuthn{valid: "good"}))
		r.GET("/ui/auth/me", func(c *gin.Context) { c.Status(http.StatusOK) })
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/ui/auth/me", http.NoBody)
		setup(req)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code
	}
	cases := []struct {
		name  string
		setup func(*http.Request)
		want  int
	}{
		{"stale bearer + valid cookie -> cookie wins", func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer stale-or-undefined")
			r.AddCookie(&http.Cookie{Name: authTokenCookie, Value: "good"})
		}, http.StatusOK},
		{"valid bearer", func(r *http.Request) { r.Header.Set("Authorization", "Bearer good") }, http.StatusOK},
		{"valid cookie only", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: authTokenCookie, Value: "good"}) }, http.StatusOK},
		{"invalid bearer, no cookie", func(r *http.Request) { r.Header.Set("Authorization", "Bearer bad") }, http.StatusUnauthorized},
		{"nothing", func(*http.Request) {}, http.StatusUnauthorized},
	}
	for _, c := range cases {
		if got := run(c.setup); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

// TestDevBypassAuthInjectsAdmin verifies the dev-only auth bypass: a protected
// data-plane route is reachable with NO token, and the request carries an admin
// user (so RBAC checks pass). This middleware must only ever be wired under the
// explicit dev opt-in.
func TestDevBypassAuthInjectsAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(DevBypassAuth())
	r.GET("/api/v2/protected", func(c *gin.Context) {
		u, ok := UserFromContext(c)
		if !ok {
			c.Status(http.StatusInternalServerError)
			return
		}
		c.JSON(http.StatusOK, gin.H{"email": u.Email, "roles": u.Roles})
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v2/protected", http.NoBody)
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (no token needed under dev bypass)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "admin") {
		t.Errorf("expected an admin user, got %s", rec.Body.String())
	}
}
