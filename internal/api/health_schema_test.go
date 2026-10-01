package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/dexadata/dexaflow/internal/domain"
)

// fakeSchemaHealthCheck implements HealthChecker plus the optional SchemaChecker,
// standing in for *storage.Postgres — the one dependency that carries a schema.
// schemaErr is whatever the storage-layer check would return (an absent
// schema_migrations, a version behind the binary, a dirty migration).
type fakeSchemaHealthCheck struct {
	pingErr     error
	schemaErr   error
	schemaCalls int
	hadDeadline bool
}

func (f *fakeSchemaHealthCheck) Ping(context.Context) error { return f.pingErr }

func (f *fakeSchemaHealthCheck) SchemaReady(ctx context.Context) error {
	f.schemaCalls++
	_, f.hadDeadline = ctx.Deadline()
	return f.schemaErr
}

// TestReadinessHandlerAssertsSchemaInvariant pins the #1023 contract: readiness
// asserts the same invariant boot does, on every probe, not only once at startup.
//
// A Postgres Ping succeeds whenever the *connection* is healthy and says nothing
// about the schema, so a database emptied, restored from an older backup, failed
// over to a lagging replica, or simply repointed under a RUNNING pod kept /readyz
// at 200 while the scheduler could not read dag_runs and /auth/token answered 503.
// Kubernetes routes traffic on that 200 and calls a rollout successful on it, so
// the pod has to report itself not-ready instead.
func TestReadinessHandlerAssertsSchemaInvariant(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// readyz drives the handler over one dependency map and returns the recorder.
	readyz := func(t *testing.T, checks map[string]HealthChecker) *httptest.ResponseRecorder {
		t.Helper()
		r := gin.New()
		r.GET("/readyz", readinessHandler(checks))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/readyz", http.NoBody))
		return rec
	}

	t.Run("current schema → 200 ready", func(t *testing.T) {
		pg := &fakeSchemaHealthCheck{}
		rec := readyz(t, map[string]HealthChecker{"postgres": pg})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if pg.schemaCalls != 1 {
			t.Errorf("schema checked %d times, want exactly 1 per probe", pg.schemaCalls)
		}
	})

	t.Run("schema_migrations absent → 503, not ready", func(t *testing.T) {
		// The exact shape of the boot-time failure (#1023): a healthy connection to
		// a database where migrations never ran.
		pg := &fakeSchemaHealthCheck{schemaErr: errors.New(
			"database schema is not current: schema_migrations is absent — migrations have not run; schema v26 is required")}
		rec := readyz(t, map[string]HealthChecker{"postgres": pg})
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503 — an empty database must never report ready", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "postgres") {
			t.Errorf("body must name the unready dependency, got %q", rec.Body.String())
		}
	})

	t.Run("schema behind the binary → 503, not ready", func(t *testing.T) {
		pg := &fakeSchemaHealthCheck{schemaErr: errors.New(
			"database schema is not current: schema is at v23 but this binary requires v26")}
		rec := readyz(t, map[string]HealthChecker{"postgres": pg})
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503 — a database behind the binary must not report ready", rec.Code)
		}
	})

	t.Run("the body leaks no connection detail", func(t *testing.T) {
		// /readyz is unauthenticated (probes carry no token), so the raw error —
		// which reaches here from a DSN-aware layer — must not go in the response
		// (audit H2). It is logged server-side instead.
		pg := &fakeSchemaHealthCheck{schemaErr: errors.New(
			"reading database schema version: dial postgres://leoflow:hunter2@pg.internal.svc:5432/leoflow: connection refused")}
		rec := readyz(t, map[string]HealthChecker{"postgres": pg})
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
		body := rec.Body.String()
		for _, secret := range []string{"hunter2", "pg.internal.svc", "postgres://", "5432"} {
			if strings.Contains(body, secret) {
				t.Errorf("body leaked %q from the raw dependency error: %q", secret, body)
			}
		}
	})

	t.Run("the schema probe is bounded, so a slow database cannot hang it", func(t *testing.T) {
		// The kubelet allows the probe probes.readiness.timeoutSeconds (3s in the
		// chart). A query with no deadline of its own leaves the handler blocked on
		// a wedged database for as long as the query takes.
		pg := &fakeSchemaHealthCheck{}
		readyz(t, map[string]HealthChecker{"postgres": pg})
		if !pg.hadDeadline {
			t.Error("schema check ran with no deadline; a slow database would hang the probe")
		}
	})

	t.Run("a dependency with no schema is unaffected", func(t *testing.T) {
		// Redis implements HealthChecker only. The schema assertion is an optional
		// capability, so a non-schema dependency keeps its Ping-only contract.
		rec := readyz(t, map[string]HealthChecker{
			"postgres": &fakeSchemaHealthCheck{},
			"redis":    &fakeHealthCheck{},
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
	})

	t.Run("an unreachable database is reported without querying it", func(t *testing.T) {
		// Ping first: with the connection down there is nothing to learn from a
		// schema query, and it would only burn the probe's budget waiting.
		pg := &fakeSchemaHealthCheck{pingErr: errors.New("connection refused")}
		rec := readyz(t, map[string]HealthChecker{"postgres": pg})
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
		if pg.schemaCalls != 0 {
			t.Errorf("schema queried %d times on a dependency that failed Ping, want 0", pg.schemaCalls)
		}
	})
}

// TestReadinessDistinguishesSchemaFromUnavailable pins the classification the
// detail string makes. The schema check reaches the handler through one return
// value, but it answers two different questions: "the schema is wrong" and "I
// could not read the schema". Treating every non-nil return as a schema verdict
// reports `postgres schema not current` for a database that is merely slow —
// the 2s bound alone produces it under pool exhaustion or a stalled connection —
// and sends whoever is on call into migration-land for a connectivity problem.
//
// Both stay 503 (a probe that cannot read the schema must not report ready) and
// both still leak nothing; only the operator-facing detail differs.
func TestReadinessDistinguishesSchemaFromUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)

	readyz := func(t *testing.T, schemaErr error) *httptest.ResponseRecorder {
		t.Helper()
		r := gin.New()
		r.GET("/readyz", readinessHandler(map[string]HealthChecker{
			"postgres": &fakeSchemaHealthCheck{schemaErr: schemaErr},
		}))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/readyz", http.NoBody))
		return rec
	}

	t.Run("a timed-out schema read reports a deadline, not a schema verdict", func(t *testing.T) {
		// Exactly what the handler's own budget produces against a slow
		// database: the storage layer wraps the context error.
		//
		// This used to assert "postgres unavailable". It now asserts the more
		// specific deadline wording, because a shared probe budget made
		// "unavailable" ambiguous in a way that matters: under one budget the
		// check that fails is not necessarily the one that consumed it, so a
		// timeout has to be reported AS a timeout, naming the dependency that
		// spent the budget. The property this case exists for is unchanged and
		// still asserted — a slow database must not be reported as a migration
		// problem, and the answer must still name postgres.
		rec := readyz(t, fmt.Errorf("reading database schema version: %w", context.DeadlineExceeded))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
		body := rec.Body.String()
		if strings.Contains(body, "schema not current") {
			t.Errorf("a slow database was reported as a migration problem: %q", body)
		}
		if !strings.Contains(body, "budget") {
			t.Errorf("body = %q, want it to report that the probe budget ran out", body)
		}
		if !strings.Contains(body, "postgres") {
			t.Errorf("body = %q, want it to name postgres", body)
		}
	})

	t.Run("a real schema verdict still says schema not current", func(t *testing.T) {
		rec := readyz(t, fmt.Errorf("%w: schema_migrations is absent", domain.ErrSchemaNotCurrent))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "postgres schema not current") {
			t.Errorf("body = %q, want it to name the schema verdict", rec.Body.String())
		}
	})

	t.Run("neither classification leaks connection detail", func(t *testing.T) {
		for _, err := range []error{
			fmt.Errorf("reading database schema version: dial postgres://leoflow:hunter2@pg.internal.svc:5432/leoflow: %w", context.DeadlineExceeded),
			fmt.Errorf("%w: schema at v23, want v26 (postgres://leoflow:hunter2@pg.internal.svc:5432/leoflow)", domain.ErrSchemaNotCurrent),
		} {
			body := readyz(t, err).Body.String()
			for _, secret := range []string{"hunter2", "pg.internal.svc", "postgres://", "5432"} {
				if strings.Contains(body, secret) {
					t.Errorf("body leaked %q: %q", secret, body)
				}
			}
		}
	})
}
