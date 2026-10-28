package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
)

// TestAuthOutageResponsesCarryRetryAfter is the regression for #1100.
//
// #1097 answers 503 instead of 401 when the store could not be reached to check
// a credential, so a client is not told to sign in again against the database
// that is down. A bare 503 still leaves the client guessing how long to wait, so
// a well-behaved one retries at once and the outage becomes a retry loop. Every
// 503 the API gives for that outage names a wait in Retry-After.
func TestAuthOutageResponsesCarryRetryAfter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dbDown := errors.New("dial tcp 10.0.0.1:5432: i/o timeout")
	want := strconv.Itoa(outageRetryAfterSeconds)

	cases := map[string]func(t *testing.T) *httptest.ResponseRecorder{
		"a protected route": func(*testing.T) *httptest.ResponseRecorder {
			r := gin.New()
			r.Use(JWTAuth(&fakeAuthn{authErr: dbDown}))
			r.GET("/api/v2/dags", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{}) })
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v2/dags", http.NoBody)
			req.Header.Set("Authorization", "Bearer whatever")
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			return rec
		},
		"the password login": func(*testing.T) *httptest.ResponseRecorder {
			return do(authnServer(dbDownAuthn{}), http.MethodPost, "/auth/token", `{"username":"a@b.c","password":"x"}`)
		},
		"the token renewal": func(*testing.T) *httptest.ResponseRecorder {
			return postRenew(renewServer(&fakeRenewer{err: dbDown}), "still-valid-token")
		},
		"the trusted issuer handoff": func(t *testing.T) *httptest.ResponseRecorder {
			h := issuerSessionServerWith(t, issuerSessionOpts{users: activeAcmeUser()})
			return postSession(h, url.Values{"token": {"keys-down"}})
		},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			rec := run(t)
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503", rec.Code)
			}
			if got := rec.Header().Get("Retry-After"); got != want {
				t.Errorf("Retry-After = %q, want %q: a 503 with no wait invites a tight retry loop", got, want)
			}
		})
	}
}

// TestAuthRefusalsCarryNoRetryAfter keeps Retry-After to outages: a 401 is a
// verdict on the credential, and retrying it after any wait gets the same 401.
func TestAuthRefusalsCarryNoRetryAfter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := map[string]*httptest.ResponseRecorder{
		"an invalid token": func() *httptest.ResponseRecorder {
			r := gin.New()
			r.Use(JWTAuth(&fakeAuthn{authErr: auth.ErrInvalidToken}))
			r.GET("/api/v2/dags", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{}) })
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v2/dags", http.NoBody)
			req.Header.Set("Authorization", "Bearer whatever")
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			return rec
		}(),
		"wrong credentials": do(authnServer(credAuthn{}), http.MethodPost, "/auth/token", `{"username":"admin@leoflow.local","password":"wrong"}`),
		"a refused renewal": postRenew(renewServer(&fakeRenewer{err: auth.ErrInvalidToken}), "bad-token"),
	}
	for name, rec := range cases {
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", name, rec.Code)
		}
		if got := rec.Header().Get("Retry-After"); got != "" {
			t.Errorf("%s: Retry-After = %q on a 401, want none", name, got)
		}
	}
}
