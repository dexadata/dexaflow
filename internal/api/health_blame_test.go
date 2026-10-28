package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// slowChecker burns a fixed slice of the probe budget, then answers. It respects
// the context, so once the budget is gone it returns ctx.Err() the way a real
// pgx call does.
type slowChecker struct {
	ping   time.Duration
	schema time.Duration
}

func burn(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *slowChecker) Ping(ctx context.Context) error { return burn(ctx, s.ping) }

type slowSchemaChecker struct{ slowChecker }

func (s *slowSchemaChecker) SchemaReady(ctx context.Context) error { return burn(ctx, s.schema) }

// TestUnreadyDetailBlamesTheBudgetNotTheBystander is the regression for the
// defect a shared probe budget introduces and a per-step allowance did not have.
//
// With one budget across every dependency, the check that FAILS is whichever one
// happened to be running when the budget ran out — not the one that consumed it.
// A slow-but-working Postgres eating 1.8s of a 2s budget leaves Redis 200ms, so
// Redis times out and the 503 reads "redis unavailable" while Redis is perfectly
// healthy. Map iteration order is randomized, so which dependency gets labeled
// also changes run to run.
//
// That is the same failure #1023 and #1040 exist to eliminate — an operator
// pointed at the wrong dependency — moved one step later, so the fix is not
// worth having without this.
func TestUnreadyDetailBlamesTheBudgetNotTheBystander(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Postgres answers, slowly, inside the budget. Redis is healthy and instant.
	// The only way this probe fails is the budget, and Postgres is what spent it.
	checks := map[string]HealthChecker{
		"postgres": &slowSchemaChecker{slowChecker{ping: 10 * time.Millisecond, schema: defaultProbeBudget - 100*time.Millisecond}},
		"redis":    &slowChecker{ping: 300 * time.Millisecond},
	}

	// Repeated, because the bug is a coin flip on map order: a single run passes
	// against the broken code roughly one time in ten.
	for i := range 10 {
		r := gin.New()
		r.GET("/readyz", readinessHandler(checks, defaultProbeBudget))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/readyz", http.NoBody))

		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("run %d: readyz = %d, want 503 (the budget cannot cover both)", i, rec.Code)
		}
		var body struct {
			Detail string `json:"detail"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("run %d: decoding the problem document: %v (%s)", i, err, rec.Body.String())
		}
		if strings.Contains(body.Detail, "redis unavailable") {
			t.Fatalf("run %d: detail %q blames redis, which answered in 300ms; postgres spent the budget", i, body.Detail)
		}
		if !strings.Contains(body.Detail, "budget") {
			t.Errorf("run %d: detail %q does not say the budget ran out, so the operator cannot tell a timeout from a real outage", i, body.Detail)
		}
		if !strings.Contains(body.Detail, "postgres") {
			t.Errorf("run %d: detail %q does not name postgres, the dependency that consumed the budget", i, body.Detail)
		}
	}
}

// TestReadinessChecksDependenciesInAStableOrder pins the determinism the blame
// fix rests on: the same set of dependencies must be probed in the same order
// every time, or two probes a second apart can reach different verdicts about
// which dependency is at fault.
func TestReadinessChecksDependenciesInAStableOrder(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var first []string
	for i := range 20 {
		var seen []string
		checks := map[string]HealthChecker{
			"postgres": recordingChecker{&seen, "postgres"},
			"redis":    recordingChecker{&seen, "redis"},
			"alpha":    recordingChecker{&seen, "alpha"},
			"zulu":     recordingChecker{&seen, "zulu"},
		}
		r := gin.New()
		r.GET("/readyz", readinessHandler(checks, defaultProbeBudget))
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/readyz", http.NoBody))
		if i == 0 {
			first = seen
			continue
		}
		if strings.Join(seen, ",") != strings.Join(first, ",") {
			t.Fatalf("run %d probed %v, run 0 probed %v — map iteration order is leaking into the verdict", i, seen, first)
		}
	}
}

type recordingChecker struct {
	into *[]string
	name string
}

func (r recordingChecker) Ping(context.Context) error { *r.into = append(*r.into, r.name); return nil }
