package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/dexadata/dexaflow/internal/api"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/observability"
	"github.com/dexadata/dexaflow/internal/storage"
)

// Boot waits for its schema instead of exiting (ADR 0090 decision 1). A crash
// loop hides the pod that could say what is wrong, and a database that lost
// its schema under a release (an emptied volume, a restore, a repointed
// database.url) only needs migrating, which a pod that stays up can see.

// schemaBackoffFirst and schemaBackoffCap bound the re-check: 1s doubling to
// 30s, so a migration Job that finishes is noticed within 30 seconds and a
// database nobody fixes is read twice a minute.
const (
	schemaBackoffFirst = time.Second
	schemaBackoffCap   = 30 * time.Second
)

// errStoppedWaitingForSchema is a shutdown that arrived while boot waited for
// the schema: a clean stop, not a boot failure.
var errStoppedWaitingForSchema = errors.New("stopped while waiting for the database schema")

// stoppedWhileWaiting turns a shutdown during the schema wait into a clean
// return of run, and passes every other boot error through.
func stoppedWhileWaiting(err error, logger *slog.Logger) error {
	if errors.Is(err, errStoppedWaitingForSchema) {
		logger.Info("shutting down")
		return nil
	}
	return err
}

// schemaProber reads the boot gate's verdict; *storage.Postgres implements it.
type schemaProber interface {
	SchemaWaitState(ctx context.Context) (storage.SchemaWait, error)
}

// schemaRecorder feeds dexaflow_schema_ready; *observability.Metrics
// implements it.
type schemaRecorder interface {
	RecordSchemaState(reason string)
}

// schemaWaiter re-reads the schema until it is current, and holds what the
// gate's /readyz says meanwhile.
type schemaWaiter struct {
	probe  schemaProber
	record schemaRecorder
	logger *slog.Logger
	after  func(time.Duration) <-chan time.Time
	// hint is what to do about each reason (recoveryHint.forClass).
	hint   func(class string) string
	detail atomic.Pointer[string]
}

func newSchemaWaiter(probe schemaProber, record schemaRecorder, logger *slog.Logger) *schemaWaiter {
	w := &schemaWaiter{probe: probe, record: record, logger: logger, after: time.After, hint: recoveryHint{}.forClass}
	w.setDetail("checking the database schema")
	return w
}

func (w *schemaWaiter) setDetail(s string) { w.detail.Store(&s) }

// Detail is the gate's readiness answer: the reason and what to do.
func (w *schemaWaiter) Detail() string { return *w.detail.Load() }

// wait returns nil once the schema is one this binary can serve, the verdict's
// error for a schema it must never start on (dirty at its latest), and
// errStoppedWaitingForSchema when ctx ends first. It logs one line per state
// change, not per attempt, so a long wait does not bury the reason.
func (w *schemaWaiter) wait(ctx context.Context) error {
	delay := schemaBackoffFirst
	var last string
	started := time.Now()
	for {
		verdict, err := w.probe.SchemaWaitState(ctx)
		if err != nil {
			return err
		}
		if verdict.Ready() {
			w.record.RecordSchemaState("")
			if last != "" {
				w.logger.InfoContext(ctx, "database schema is current; starting", "waited", time.Since(started).Round(time.Second).String())
			}
			w.setDetail("starting")
			return nil
		}
		w.record.RecordSchemaState(verdict.Class)
		action := w.hint(verdict.Class)
		w.setDetail(verdict.Reason + ": " + action)
		if verdict.Reason != last {
			attrs := []any{"reason", verdict.Reason, "action", action}
			if verdict.Err != nil {
				attrs = append(attrs, "error", verdict.Err.Error())
			}
			if verdict.Detail != "" {
				attrs = append(attrs, "detail", verdict.Detail)
			}
			w.logger.WarnContext(ctx, "database schema is not ready; serving only the probes and metrics and checking again", attrs...)
			last = verdict.Reason
		}
		select {
		case <-ctx.Done():
			return errStoppedWaitingForSchema
		case <-w.after(delay):
		}
		delay = min(delay*2, schemaBackoffCap)
	}
}

// awaitSchema binds the probe listeners, waits until the schema is current
// and closes them, so the rest of boot runs as before and binds the real
// listeners on the same ports.
func awaitSchema(ctx context.Context, cfg *config.ServerConfig, tel *observability.Telemetry, probe schemaProber) error {
	w := newSchemaWaiter(probe, tel.Metrics, tel.Logger)
	w.hint = newRecoveryHint(cfg).forClass
	gate, err := startSchemaGate(ctx, cfg, tel, w.Detail)
	if err != nil {
		return err
	}
	defer gate.Close()
	if wantsMigrateOnBoot(cfg) {
		// Behind the gate, so the probes answer while a long migration runs
		// and the startup probe does not restart the pod into a dirty schema.
		w.setDetail("applying the embedded migrations (migrations.onBoot)")
		migrateOnBoot(ctx, probe, embeddedMigrate(cfg.Database.URL, tel.Logger), tel.Logger)
	}
	return w.wait(ctx)
}

// schemaGate is the listeners a waiting process serves on.
type schemaGate struct {
	servers   []*http.Server
	listeners []net.Listener
}

// startSchemaGate binds the metrics port in every role, and the API port (with
// its TLS, since the probes use the same scheme) when the role serves the API,
// and serves api.SchemaWaitHandler on them.
func startSchemaGate(ctx context.Context, cfg *config.ServerConfig, tel *observability.Telemetry, detail func() string) (*schemaGate, error) {
	g := &schemaGate{}
	metrics := newHTTPServer(cfg.Server.MetricsAddr, api.SchemaWaitHandler(tel.Registry, detail, observabilityOptions(cfg)...), cfg)
	servers := []*http.Server{metrics}
	if cfg.Server.ServesAPI() {
		apiSrv := newHTTPServer(cfg.Server.HTTPAddr, api.SchemaWaitHandler(nil, detail), cfg)
		if err := configureAPITLS(apiSrv, cfg.Server, tel.Metrics); err != nil {
			return nil, err
		}
		servers = append(servers, apiSrv)
	}
	var lc net.ListenConfig
	for _, srv := range servers {
		lis, err := lc.Listen(ctx, "tcp", srv.Addr)
		if err != nil {
			g.Close()
			return nil, fmt.Errorf("listening on %s: %w", srv.Addr, err)
		}
		g.servers = append(g.servers, srv)
		g.listeners = append(g.listeners, lis)
		go func() {
			var serr error
			if srv.TLSConfig != nil {
				serr = srv.ServeTLS(lis, "", "")
			} else {
				serr = srv.Serve(lis)
			}
			if serr != nil && !errors.Is(serr, http.ErrServerClosed) {
				tel.Logger.Error("schema wait listener stopped", "addr", srv.Addr, "error", serr)
			}
		}()
	}
	return g, nil
}

// addrs are the bound addresses, for tests that bind port 0.
func (g *schemaGate) addrs() []string {
	out := make([]string, 0, len(g.listeners))
	for _, l := range g.listeners {
		out = append(out, l.Addr().String())
	}
	return out
}

// Close stops serving and frees the ports at once.
func (g *schemaGate) Close() {
	for i, srv := range g.servers {
		_ = srv.Close()            //nolint:errcheck // closing a listener we own; nothing to recover
		_ = g.listeners[i].Close() //nolint:errcheck // already closed by srv.Close in the usual case
	}
}
