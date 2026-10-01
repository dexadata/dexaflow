package api

import (
	"net/http"
	"testing"
	"time"

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
