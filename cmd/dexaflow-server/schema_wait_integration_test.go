//go:build integration

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4/database"
	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/migrator"
	"github.com/dexadata/dexaflow/internal/observability"
	"github.com/dexadata/dexaflow/internal/storage"
	"github.com/dexadata/dexaflow/migrations"
)

// scratchDatabase creates an empty database next to DATABASE_URL's and drops
// it when the test ends.
func scratchDatabase(t *testing.T) string {
	t.Helper()
	raw := os.Getenv("DATABASE_URL")
	if raw == "" {
		t.Skip("DATABASE_URL must point at a Postgres for integration tests")
	}
	admin, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	name := "dexaflow_schema_wait_" + hex.EncodeToString(b)
	conn, err := pgx.Connect(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(t.Context(), "CREATE DATABASE "+name); err != nil {
		t.Fatalf("CREATE DATABASE: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = conn.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		_ = conn.Close(ctx)
	})
	admin.Path = "/" + name
	return admin.String()
}

func get(addr, path string) (int, string, error) {
	resp, err := http.Get("http://" + addr + path) //nolint:noctx // a short-lived probe in a test
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), nil
}

// eventually polls until cond holds or thirty seconds pass.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	// The boot under test shares the CI runner with the rest of the -race
	// integration suite, and a 10s deadline there was missed.
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// bootRun is one run of the boot entry point: done closes when it returns.
type bootRun struct {
	done chan struct{}
	pg   *storage.Postgres
	err  error
}

// bootAgainst runs the real boot entry point, openVerifiedPostgres, against
// dsn in the "all" role and returns where it serves and how it ended.
func bootAgainst(t *testing.T, dsn string) (httpAddr, metricsAddr string, reg *prometheus.Registry, run *bootRun) {
	t.Helper()
	return bootWith(t, config.DatabaseSection{URL: dsn})
}

// bootAs is bootAgainst for a release whose installation id is
// installationID (database.installation_id).
func bootAs(t *testing.T, dsn, installationID string) (httpAddr, metricsAddr string, reg *prometheus.Registry, run *bootRun) {
	t.Helper()
	return bootWith(t, config.DatabaseSection{URL: dsn, InstallationID: installationID})
}

// bootWith is bootAgainst with a whole database section.
func bootWith(t *testing.T, db config.DatabaseSection) (httpAddr, metricsAddr string, reg *prometheus.Registry, run *bootRun) {
	t.Helper()
	cfg := &config.ServerConfig{Database: db}
	cfg.Server.HTTPAddr = freeAddr(t)
	cfg.Server.MetricsAddr = freeAddr(t)
	reg = prometheus.NewRegistry()
	tel := &observability.Telemetry{Logger: discardLogger(), Registry: reg, Metrics: observability.NewMetrics(reg)}
	ctx, cancel := context.WithCancel(t.Context())
	run = &bootRun{done: make(chan struct{})}
	go func() {
		defer close(run.done)
		run.pg, run.err = openVerifiedPostgres(ctx, cfg, tel)
	}()
	t.Cleanup(func() {
		cancel()
		<-run.done
		if run.pg != nil {
			run.pg.Close()
		}
	})
	return cfg.Server.HTTPAddr, cfg.Server.MetricsAddr, reg, run
}

func schemaReadyGauge(t *testing.T, reg *prometheus.Registry) (reason string, value float64) {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() == "dexaflow_schema_ready" && len(mf.GetMetric()) == 1 {
			m := mf.GetMetric()[0]
			for _, l := range m.GetLabel() {
				reason = l.GetValue()
			}
			return reason, m.GetGauge().GetValue()
		}
	}
	return "", -1
}

// ADR 0090 decision 1 against a real Postgres: a server booted on an empty
// database stays up, alive and not ready, says why on /readyz on both ports
// and in dexaflow_schema_ready, and finishes booting without a restart once
// the migrate wrapper brings the schema current.
func TestBootWaitsForAnEmptyDatabaseThenBoots(t *testing.T) {
	dsn := scratchDatabase(t)
	httpAddr, metricsAddr, reg, run := bootAgainst(t, dsn)

	// The listeners answer 503 "checking the database schema" before the
	// first probe has run, so wait for its verdict, not for the first 503.
	eventually(t, "the absent reason on /readyz and in the gauge", func() bool {
		code, body, err := get(metricsAddr, "/readyz")
		reason, _ := schemaReadyGauge(t, reg)
		return err == nil && code == http.StatusServiceUnavailable &&
			strings.Contains(body, "schema absent") && reason == storage.SchemaReasonAbsent
	})
	for _, addr := range []string{httpAddr, metricsAddr} {
		if code, _, err := get(addr, "/healthz"); err != nil || code != http.StatusOK {
			t.Errorf("%s/healthz = %d %v, want 200 while waiting", addr, code, err)
		}
		code, body, err := get(addr, "/readyz")
		if err != nil || code != http.StatusServiceUnavailable || !strings.Contains(body, "schema absent") {
			t.Errorf("%s/readyz = %d %q %v, want 503 naming the absent schema", addr, code, body, err)
		}
	}
	if code, _, _ := get(httpAddr, "/api/v2/dags"); code != http.StatusServiceUnavailable {
		t.Errorf("/api/v2/dags = %d while waiting, want 503: no API before the schema is current", code)
	}
	if _, body, _ := get(metricsAddr, "/metrics"); !strings.Contains(body, `dexaflow_schema_ready{reason="schema absent"} 0`) {
		t.Errorf("/metrics while waiting lacks the not-ready gauge:\n%s", body)
	}
	if _, body, _ := get(metricsAddr, "/metrics"); !strings.Contains(body, `dexaflow_db_pool_max_conns{pool="health"} 2`) {
		t.Errorf("/metrics while waiting lacks the database pool series (#1049):\n%s", body)
	}

	var out, errOut strings.Builder
	if code := migrator.Main(t.Context(), []string{"-database=" + dsn, "up"}, &out, &errOut, "test"); code != 0 {
		t.Fatalf("migrate up: exit %d: %s", code, errOut.String())
	}

	select {
	case <-run.done:
		b := run
		if b.err != nil {
			t.Fatalf("boot after migrating: %v", b.err)
		}
		if reason, v := schemaReadyGauge(t, reg); reason != "" || v != 1 {
			t.Errorf("dexaflow_schema_ready = {reason=%q} %v after boot, want {} 1", reason, v)
		}
		if err := b.pg.SchemaReady(t.Context()); err != nil {
			t.Errorf("readiness of the booted pool: %v", err)
		}
		// A pod that is serving keeps the metric current through readiness.
		if _, err := b.pg.Pool.Exec(t.Context(), "DROP TABLE schema_migrations"); err != nil {
			t.Fatal(err)
		}
		_ = b.pg.SchemaReady(t.Context())
		if reason, v := schemaReadyGauge(t, reg); reason != storage.SchemaReasonAbsent || v != 0 {
			t.Errorf("dexaflow_schema_ready = {reason=%q} %v once readiness sees no schema, want {reason=schema absent} 0", reason, v)
		}
	case <-time.After(45 * time.Second):
		t.Fatal("boot did not finish within the 30s backoff cap after the schema became current")
	}
	if _, _, err := get(metricsAddr, "/readyz"); err == nil {
		t.Error("the wait listener still answers after boot moved on")
	}
}

// A schema dirty below this binary's latest may be a migration in flight, so
// boot waits and says dirty; so does one dirty at the latest while the
// migrator holds its lock; one dirty at the latest with no migrator stops
// boot.
func TestBootWaitsOnADirtySchemaAndRefusesOneDirtyAtItsLatest(t *testing.T) {
	latest, err := migrations.Latest()
	if err != nil {
		t.Fatal(err)
	}
	dsn := scratchDatabase(t)
	conn, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(t.Context(), "CREATE TABLE schema_migrations (version bigint NOT NULL, dirty boolean NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(t.Context(), "INSERT INTO schema_migrations VALUES ($1, true)", int64(latest)-1); err != nil {
		t.Fatal(err)
	}

	_, metricsAddr, reg, run := bootAgainst(t, dsn)
	want := "schema dirty at v" + strconv.FormatUint(uint64(latest-1), 10)
	eventually(t, "the dirty reason on /readyz", func() bool {
		code, body, err := get(metricsAddr, "/readyz")
		return err == nil && code == http.StatusServiceUnavailable && strings.Contains(body, want)
	})
	time.Sleep(1500 * time.Millisecond) // past the first re-check
	select {
	case <-run.done:
		t.Fatalf("boot ended on a dirty schema it should wait through: %v", run.err)
	default:
	}
	if reason, v := schemaReadyGauge(t, reg); reason != storage.SchemaReasonDirty || v != 0 {
		t.Errorf("dexaflow_schema_ready = {reason=%q} %v, want {reason=schema dirty} 0", reason, v)
	}

	// The migration reaches this binary's latest and marks it dirty while it
	// applies it, holding the migrator's lock: boot keeps waiting.
	drv, err := database.Open(migrator.DriverURL(dsn))
	if err != nil {
		t.Fatal(err)
	}
	defer drv.Close()
	if err := drv.Lock(); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(t.Context(), "UPDATE schema_migrations SET version = $1", int64(latest)); err != nil {
		t.Fatal(err)
	}
	wantLatest := "schema dirty at v" + strconv.FormatUint(uint64(latest), 10)
	eventually(t, "the dirty reason at the latest on /readyz", func() bool {
		code, body, err := get(metricsAddr, "/readyz")
		return err == nil && code == http.StatusServiceUnavailable && strings.Contains(body, wantLatest)
	})
	select {
	case <-run.done:
		t.Fatalf("boot ended while a migration still held the lock: %v", run.err)
	default:
	}

	// The migration stops there and lets go of the lock, leaving the latest
	// dirty: the one state no wait fixes.
	if err := drv.Unlock(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-run.done:
		if !errors.Is(run.err, domain.ErrSchemaNotCurrent) {
			t.Fatalf("boot on a schema dirty at its latest = %v, want the dirty-schema error", run.err)
		}
	case <-time.After(45 * time.Second):
		t.Fatal("boot kept waiting on a schema dirty at this binary's latest")
	}
}

// ADR 0090 decisions 3 and 5 against a real Postgres: a server whose database
// carries another installation id waits, not ready, with the reason database
// replaced (and no ids) on /readyz and the metric; it boots once the right
// database is back; and once serving, a readiness probe that finds the id
// changed stops the process with errDatabaseReplaced.
func TestBootWaitsOnAReplacedDatabaseAndAServingOneStops(t *testing.T) {
	dsn := scratchDatabase(t)
	var out, errOut strings.Builder
	if code := migrator.Main(t.Context(), []string{"-database=" + dsn, "up"}, &out, &errOut, "test"); code != 0 {
		t.Fatalf("migrate up: exit %d: %s", code, errOut.String())
	}
	conn, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	setID := func(id string) {
		t.Helper()
		if _, err := conn.Exec(t.Context(), `INSERT INTO installation (id) VALUES ($1)
ON CONFLICT (singleton) DO UPDATE SET id = EXCLUDED.id`, id); err != nil {
			t.Fatal(err)
		}
	}
	setID("someone-else")

	_, metricsAddr, reg, run := bootAs(t, dsn, "this-release")
	eventually(t, "the replaced reason on /readyz", func() bool {
		code, body, err := get(metricsAddr, "/readyz")
		return err == nil && code == http.StatusServiceUnavailable && strings.Contains(body, "database replaced: another installation id")
	})
	_, body, _ := get(metricsAddr, "/readyz")
	if strings.Contains(body, "someone-else") || strings.Contains(body, "this-release") || !strings.Contains(body, "installation.id") {
		t.Errorf("/readyz = %q, want how to accept the database and no installation ids", body)
	}
	if reason, v := schemaReadyGauge(t, reg); reason != storage.SchemaReasonReplaced || v != 0 {
		t.Errorf("dexaflow_schema_ready = {reason=%q} %v, want {reason=database replaced} 0", reason, v)
	}

	setID("this-release")
	select {
	case <-run.done:
		if run.err != nil {
			t.Fatalf("boot on the restored database: %v", run.err)
		}
	case <-time.After(45 * time.Second):
		t.Fatal("boot did not finish once the right database was back")
	}

	ctx, release := stopOnReplaced(t.Context(), run.pg)
	defer release()
	if err := run.pg.SchemaReady(ctx); err != nil {
		t.Fatalf("readiness on this release's database: %v", err)
	}
	setID("someone-else")
	if err := run.pg.SchemaReady(ctx); !errors.Is(err, domain.ErrDatabaseReplaced) {
		t.Fatalf("readiness after the swap = %v, want domain.ErrDatabaseReplaced", err)
	}
	select {
	case <-ctx.Done():
		if !errors.Is(serveExit(ctx, nil), errDatabaseReplaced) {
			t.Errorf("the server stopped with %v, want errDatabaseReplaced", context.Cause(ctx))
		}
	default:
		t.Fatal("a readiness probe that found the database replaced did not stop the server")
	}
}

// migrations.onBoot (ADR 0090 decision 4) against a real Postgres: a server
// booted on an empty database migrates it itself and boots, with no Job.
func TestBootMigratesItselfWithOnBoot(t *testing.T) {
	dsn := scratchDatabase(t)
	_, _, reg, run := bootWith(t, config.DatabaseSection{URL: dsn, MigrateOnBoot: true})
	select {
	case <-run.done:
		if run.err != nil {
			t.Fatalf("boot with onBoot on an empty database: %v", run.err)
		}
	case <-time.After(45 * time.Second):
		t.Fatal("boot with onBoot did not finish on an empty database")
	}
	if err := run.pg.SchemaReady(t.Context()); err != nil {
		t.Errorf("readiness after onBoot: %v", err)
	}
	if reason, v := schemaReadyGauge(t, reg); reason != "" || v != 1 {
		t.Errorf("dexaflow_schema_ready = {reason=%q} %v, want {} 1", reason, v)
	}
}

// onBoot never migrates a replaced database: an emptied database under an
// established release stays empty and the server waits on it.
func TestBootWithOnBootLeavesAReplacedDatabaseAlone(t *testing.T) {
	dsn := scratchDatabase(t)
	_, metricsAddr, _, run := bootWith(t, config.DatabaseSection{URL: dsn, MigrateOnBoot: true, InstallationID: "this-release"})
	eventually(t, "the replaced reason on /readyz", func() bool {
		code, body, err := get(metricsAddr, "/readyz")
		return err == nil && code == http.StatusServiceUnavailable && strings.Contains(body, "database replaced: no schema")
	})
	select {
	case <-run.done:
		t.Fatalf("boot ended on a replaced database: %v", run.err)
	default:
	}
	conn, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	var tables int
	if err := conn.QueryRow(t.Context(), "SELECT count(*) FROM pg_tables WHERE schemaname = 'public'").Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 0 {
		t.Errorf("onBoot created %d tables in a replaced database", tables)
	}
}
