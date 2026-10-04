package observability

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// Config configures observability setup.
type Config struct {
	ServiceName  string
	LogLevel     string
	LogFormat    string
	OTelEnabled  bool
	OTelEndpoint string
	// SampleRatio is the share of new root traces kept, in [0, 1].
	SampleRatio float64
	// SkipProbeSpans drops spans for /healthz, /readyz and /static/*.
	SkipProbeSpans bool
}

// Telemetry bundles the configured observability primitives.
type Telemetry struct {
	Logger   *slog.Logger
	Metrics  *Metrics
	Registry *prometheus.Registry
	Tracer   trace.Tracer
}

// Setup builds logging, metrics, and tracing from cfg and returns the telemetry
// bundle plus a shutdown function the caller must defer.
func Setup(ctx context.Context, cfg Config) (*Telemetry, func(), error) {
	logger := NewLogger(cfg.LogLevel, cfg.LogFormat)
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(reg)

	shutdown := func() {}
	switch {
	case cfg.OTelEnabled && cfg.OTelEndpoint == "":
		// Empty endpoint with enabled=true is a misconfig: the OTLP/gRPC
		// exporter would silently fall back to localhost:4317 (the SDK
		// default), producing the "dial tcp [::1]:4317" log spam #319
		// reports. Fail loud at boot — operators must opt into a real
		// endpoint when they opt into OTel.
		return nil, nil, errors.New(
			"observability.otel.enabled=true but observability.otel.endpoint is empty: " +
				"set the OTLP/gRPC endpoint explicitly (e.g. otel-collector:4317) " +
				"or set enabled=false to skip tracing (#319)")
	case cfg.OTelEnabled && (cfg.SampleRatio < 0 || cfg.SampleRatio > 1):
		return nil, nil, fmt.Errorf(
			"observability.otel.sample_ratio must be between 0 and 1 (got %v)", cfg.SampleRatio)
	case cfg.OTelEnabled:
		tp, err := newTracerProvider(ctx, cfg.OTelEndpoint, cfg.ServiceName,
			newSampler(cfg.SampleRatio, cfg.SkipProbeSpans))
		if err != nil {
			return nil, nil, err
		}
		otel.SetTracerProvider(tp)
		// shutdown runs after Setup's ctx may already be done, so it uses a
		// fresh bounded context rather than deriving from the startup ctx.
		shutdown = func() { //nolint:contextcheck // intentional fresh context for graceful shutdown
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := tp.Shutdown(sctx); err != nil {
				logger.Error("otel tracer shutdown", "error", err)
			}
		}
	default:
		// Disabled: install the noop provider explicitly so a polluted
		// global (test bleed, a stray OTEL_EXPORTER_* env var that
		// auto-instantiates a real exporter, or any future package that
		// SetTracerProvider's at init) cannot leave a real exporter live
		// behind our back. The downstream effect we are blocking: the api
		// handler middleware (internal/api/observe.go) calls
		// otel.Tracer("leoflow") which resolves through this global; with
		// a real provider behind it, every request creates a recording
		// span and the batcher tries to flush to localhost:4317 (#319).
		otel.SetTracerProvider(noop.NewTracerProvider())
	}

	return &Telemetry{
		Logger:   logger,
		Metrics:  metrics,
		Registry: reg,
		Tracer:   otel.Tracer(cfg.ServiceName),
	}, shutdown, nil
}
