package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
)

// clientIPServer builds a minimal server with the given trusted-proxy list and a
// probe route that echoes the resolved client IP.
func clientIPServer(trusted []string) *gin.Engine {
	r := NewServer(Dependencies{
		Logger:         discardLogger(),
		RateLimiter:    auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:    []string{"*"},
		DevNoAuth:      true,
		TrustedProxies: trusted,
	})
	r.GET("/_clientip", func(c *gin.Context) { c.String(http.StatusOK, c.ClientIP()) })
	return r
}

func clientIP(t *testing.T, r *gin.Engine, remoteAddr, xff string) string {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/_clientip", http.NoBody)
	req.RemoteAddr = remoteAddr
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Body.String()
}

// TestTrustNoProxiesByDefault: with no trusted proxies configured, an
// X-Forwarded-For header is attacker-controlled and must be ignored — ClientIP
// resolves to the direct peer. This is what stops a spoofed XFF from evading or
// poisoning the login rate-limiter and the audit log (audit H1).
func TestTrustNoProxiesByDefault(t *testing.T) {
	r := clientIPServer(nil)
	if got := clientIP(t, r, "203.0.113.9:4444", "1.2.3.4"); got != "203.0.113.9" {
		t.Errorf("with no trusted proxies, ClientIP must be the direct peer 203.0.113.9, got %q", got)
	}
}

// TestTrustedProxyHonorsForwardedFor: when the direct peer IS a configured
// trusted proxy, the left-most X-Forwarded-For entry is the real client and
// ClientIP resolves to it — the intended path for a Pro deployment behind a
// known ingress.
func TestTrustedProxyHonorsForwardedFor(t *testing.T) {
	r := clientIPServer([]string{"203.0.113.9/32"})
	if got := clientIP(t, r, "203.0.113.9:4444", "1.2.3.4"); got != "1.2.3.4" {
		t.Errorf("behind a trusted proxy, ClientIP must be the forwarded client 1.2.3.4, got %q", got)
	}
}
