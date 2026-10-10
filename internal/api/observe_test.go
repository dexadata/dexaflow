package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
)

type fakeMetrics struct {
	calls      int
	lastMethod string
	lastPath   string
	lastStatus int
}

func (f *fakeMetrics) RecordHTTPRequest(method, path string, status int, _ time.Duration) {
	f.calls++
	f.lastMethod = method
	f.lastPath = path
	f.lastStatus = status
}

func TestObserveRecordsHTTPMetrics(t *testing.T) {
	m := &fakeMetrics{}
	srv := NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{},
		RateLimiter:   auth.NewRateLimiter(5, time.Minute),
		Metrics:       m,
		CORSOrigins:   []string{"*"},
	})
	do(srv, http.MethodGet, "/healthz", "")
	if m.calls != 1 {
		t.Fatalf("RecordHTTPRequest called %d times, want 1", m.calls)
	}
	if m.lastMethod != http.MethodGet || m.lastPath != "/healthz" || m.lastStatus != http.StatusOK {
		t.Errorf("recorded (%s %s %d), want (GET /healthz 200)", m.lastMethod, m.lastPath, m.lastStatus)
	}
}

// TestObserveRecordsARedirectedRefusalAsTheRefusal: a refusal answered with a
// redirect (the trusted-issuer handoff with an external sign-in URL) is
// recorded under the status it stands for, so a 4xx or 5xx alert on the route
// still fires and a refusal does not count as a successful 303.
func TestObserveRecordsARedirectedRefusalAsTheRefusal(t *testing.T) {
	m := &fakeMetrics{}
	r := gin.New()
	r.Use(Observe(m, nil))
	r.POST("/x", func(c *gin.Context) {
		c.Set(contextKeyRefusalStatus, http.StatusForbidden)
		c.Redirect(http.StatusSeeOther, "https://signin.example.com/?error=user_not_linked")
	})
	r.POST("/ok", func(c *gin.Context) { c.Redirect(http.StatusSeeOther, "/home") })
	do(r, http.MethodPost, "/x", "")
	if m.lastStatus != http.StatusForbidden {
		t.Errorf("recorded %d for a redirected refusal, want 403", m.lastStatus)
	}
	do(r, http.MethodPost, "/ok", "")
	if m.lastStatus != http.StatusSeeOther {
		t.Errorf("recorded %d for a plain redirect, want 303", m.lastStatus)
	}
}
