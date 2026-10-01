package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
)

// TestAuthOutageIsNot401 is the regression for #1087.
//
// JWTAuth inspected only `err == nil` and answered 401 "invalid token" for every
// failure, so a database the control plane could not reach was reported as a
// statement about the caller's credentials. Measured against a real stopped
// Postgres during the v0.4.6 validation: 401 after 76 seconds, on a token minted
// seconds earlier. Signature verification is local and takes about a
// millisecond, so the duration alone proves it was not the token.
//
// 401 is not merely the wrong number. A well-behaved client reads it as
// "re-authenticate", so it retries against /auth/token — which needs the same
// database — turning a read outage into a retry storm on the dependency that is
// already down. And a wall of 401s reads to whoever is on call as an auth
// incident, sending them to rotate credentials during a database outage.
func TestAuthOutageIsNot401(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dbDown := errors.New("failed to connect to `user=leoflow database=leoflow`: dial tcp 10.0.0.1:5432: i/o timeout")

	run := func(t *testing.T, authErr error) (*httptest.ResponseRecorder, string) {
		t.Helper()
		var logBuf bytes.Buffer
		r := gin.New()
		r.Use(StructuredLogger(slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))))
		r.Use(JWTAuth(&fakeAuthn{authErr: authErr}))
		r.GET("/api/v2/dags", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{}) })

		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v2/dags", http.NoBody)
		req.Header.Set("Authorization", "Bearer whatever")
		r.ServeHTTP(rec, req)
		return rec, logBuf.String()
	}

	t.Run("a backend failure is 503, not 401", func(t *testing.T) {
		rec, _ := run(t, dbDown)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503 — the token was never judged, the store was unreachable", rec.Code)
		}
		if strings.Contains(rec.Body.String(), "invalid token") {
			t.Errorf("body blames the caller's token for an outage: %s", rec.Body.String())
		}
	})

	t.Run("the body stays clean and the log keeps the cause", func(t *testing.T) {
		rec, logs := run(t, dbDown)
		// #961's split: the tenant learns nothing about the driver; the operator
		// keeps everything, correlatable by request_id.
		for _, leak := range []string{"dial tcp", "10.0.0.1", "user=leoflow", "i/o timeout"} {
			if strings.Contains(rec.Body.String(), leak) {
				t.Errorf("body leaks %q: %s", leak, rec.Body.String())
			}
		}
		if !strings.Contains(logs, "dial tcp") {
			t.Errorf("the log must carry the cause the body withheld; got %s", logs)
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("body is not problem+json: %v", err)
		}
		if body["status"].(float64) != http.StatusServiceUnavailable {
			t.Errorf("problem body status = %v, want 503", body["status"])
		}
	})

	t.Run("a genuinely invalid token is still 401", func(t *testing.T) {
		// The 401 path must survive: narrowing it to real rejections is the fix,
		// removing it would be a different bug.
		rec, _ := run(t, auth.ErrInvalidToken)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401 for a token that was judged and rejected", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "invalid token") {
			t.Errorf("body = %s, want the invalid-token detail", rec.Body.String())
		}
	})

	t.Run("a missing token is still 401 and never 503", func(t *testing.T) {
		r := gin.New()
		r.Use(JWTAuth(&fakeAuthn{authErr: dbDown}))
		r.GET("/api/v2/dags", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{}) })
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v2/dags", http.NoBody))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401 — no credential was presented, so nothing needed the store", rec.Code)
		}
	})
}
