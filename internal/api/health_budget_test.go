package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// deadlineRecorder records the deadline every health call it receives was given,
// so a test can assert what the handler BUDGETED rather than how long it took.
type deadlineRecorder struct {
	deadlines []time.Time
	unbounded int
}

func (d *deadlineRecorder) note(ctx context.Context) {
	if dl, ok := ctx.Deadline(); ok {
		d.deadlines = append(d.deadlines, dl)
		return
	}
	d.unbounded++
}

func (d *deadlineRecorder) Ping(ctx context.Context) error { d.note(ctx); return nil }

// schemaDeadlineRecorder is the schema-carrying dependency (*storage.Postgres);
// deadlineRecorder alone is a Ping-only one (Redis). They are separate types
// because SchemaChecker is discovered by a runtime type assertion on the value:
// one type with an unused flag would make every fake a SchemaChecker and the
// test would silently stop covering the Ping-only path.
type schemaDeadlineRecorder struct{ deadlineRecorder }

func (d *schemaDeadlineRecorder) SchemaReady(ctx context.Context) error { d.note(ctx); return nil }

// TestReadinessProbeIsOneBudgetNotOnePerStep pins #1040: the readiness handler
// spends ONE deadline across everything it touches.
//
// The bound used to be applied per dependency, and only to the schema query —
// Ping inherited the request context and was not bounded at all. With Postgres
// and Redis both registered the handler's worst case was Ping(pg) + 2s +
// Ping(redis), each Ping limited only by whatever the kubelet allowed. A probe
// that overruns the kubelet's timeout reports nothing, and a probe that reports
// nothing writes no log line naming the dependency, which is the entire
// diagnostic #1023 exists to provide.
//
// Asserted on the deadline rather than on elapsed time: a duration assertion
// passes on the broken code whenever the fakes are fast, which is always.
func TestReadinessProbeIsOneBudgetNotOnePerStep(t *testing.T) {
	gin.SetMode(gin.TestMode)

	pg := &schemaDeadlineRecorder{}
	redis := &deadlineRecorder{}
	checks := map[string]HealthChecker{"postgres": pg, "redis": redis}

	before := time.Now()
	r := gin.New()
	r.GET("/readyz", readinessHandler(checks, defaultProbeBudget))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/readyz", http.NoBody))
	after := time.Now()

	if rec.Code != http.StatusOK {
		t.Fatalf("readyz = %d, want 200", rec.Code)
	}

	all := make([]time.Time, 0, len(pg.deadlines)+len(redis.deadlines))
	all = append(all, pg.deadlines...)
	all = append(all, redis.deadlines...)

	// Every step bounded. Ping used to arrive here with no deadline at all.
	if n := pg.unbounded + redis.unbounded; n != 0 {
		t.Errorf("%d health call(s) ran with no deadline; the whole probe must be bounded", n)
	}
	// postgres contributes Ping + SchemaReady, redis contributes Ping.
	if len(all) != 3 {
		t.Fatalf("recorded %d deadlines, want 3 (pg ping, pg schema, redis ping)", len(all))
	}
	// One budget: every step must see the SAME instant. A per-step allowance
	// computed as now+defaultProbeBudget drifts between calls, so equality here is what
	// separates "one deadline shared" from "a fresh one each time".
	for i, dl := range all[1:] {
		if !dl.Equal(all[0]) {
			t.Errorf("deadline %d is %v, want the shared %v — each step got its own allowance", i+1, dl, all[0])
		}
	}
	// And it is the probe budget, derived once from the request.
	if lo, hi := before.Add(defaultProbeBudget), after.Add(defaultProbeBudget); all[0].Before(lo) || all[0].After(hi) {
		t.Errorf("shared deadline %v is not request-start + defaultProbeBudget (%v..%v)", all[0], lo, hi)
	}
}

// TestMonitorHealthSharesTheProbeBudget keeps /api/v2/monitor/health on the same
// budget as /readyz. The two read the same checks map and must not come to
// disagree about how long a dependency may take, for the same reason they must
// not disagree about whether it works — that disagreement was half of #1023's
// blast radius.
func TestMonitorHealthSharesTheProbeBudget(t *testing.T) {
	gin.SetMode(gin.TestMode)

	pg := &schemaDeadlineRecorder{}
	r := gin.New()
	r.GET("/health", monitorHealthHandler(map[string]HealthChecker{"postgres": pg}, nil, defaultProbeBudget))
	rec := httptest.NewRecorder()
	before := time.Now()
	r.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health", http.NoBody))
	after := time.Now()

	if pg.unbounded != 0 {
		t.Errorf("%d monitor health call(s) ran with no deadline", pg.unbounded)
	}
	if len(pg.deadlines) != 2 {
		t.Fatalf("recorded %d deadlines, want 2 (ping, schema)", len(pg.deadlines))
	}
	if !pg.deadlines[1].Equal(pg.deadlines[0]) {
		t.Errorf("ping and schema got different deadlines (%v vs %v)", pg.deadlines[0], pg.deadlines[1])
	}
	// The same request-start + defaultProbeBudget window /readyz is held to. A looser
	// lower bound accepts a hardcoded 1.5s here and calls it defaultProbeBudget.
	if lo, hi := before.Add(defaultProbeBudget), after.Add(defaultProbeBudget); pg.deadlines[0].Before(lo) || pg.deadlines[0].After(hi) {
		t.Errorf("deadline %v is not request-start + defaultProbeBudget (%v..%v)", pg.deadlines[0], lo, hi)
	}
}

// readyzDeadline serves /readyz from h once and returns how far past the
// request the dependency check's deadline was set.
func readyzDeadline(t *testing.T, h http.Handler, pg *schemaDeadlineRecorder) time.Duration {
	t.Helper()
	before := time.Now()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/readyz", http.NoBody))
	if rec.Code != http.StatusOK || len(pg.deadlines) == 0 {
		t.Fatalf("readyz = %d with %d deadlines, want 200 with a bounded check", rec.Code, len(pg.deadlines))
	}
	return pg.deadlines[len(pg.deadlines)-1].Sub(before)
}

// TestProbeBudgetIsConfigurable pins #1050: server.probe_budget, which the
// chart renders from probes.readiness.timeoutSeconds, is the deadline of both
// /readyz surfaces, and 0 keeps the 2s every earlier release used.
func TestProbeBudgetIsConfigurable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	near := func(got, want time.Duration) bool { return got > want-time.Second/2 && got < want+time.Second/2 }

	for _, tc := range []struct {
		budget, want time.Duration
	}{{9 * time.Second, 9 * time.Second}, {0, defaultProbeBudget}} {
		pg := &schemaDeadlineRecorder{}
		r := gin.New()
		r.GET("/readyz", readinessHandler(map[string]HealthChecker{"postgres": pg}, tc.budget))
		if got := readyzDeadline(t, r, pg); !near(got, tc.want) {
			t.Errorf("API /readyz with budget %s: deadline %s after the request, want %s", tc.budget, got, tc.want)
		}

		pg = &schemaDeadlineRecorder{}
		obs := ObservabilityHandler(nil, map[string]HealthChecker{"postgres": pg}, WithProbeBudget(tc.budget))
		if got := readyzDeadline(t, obs, pg); !near(got, tc.want) {
			t.Errorf("metrics /readyz with budget %s: deadline %s after the request, want %s", tc.budget, got, tc.want)
		}
	}
}
