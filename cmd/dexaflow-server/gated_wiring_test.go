package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/dexadata/dexaflow/internal/api"
	"github.com/dexadata/dexaflow/internal/config"
)

// The wiring of the gated performance settings, driven through the helpers
// main calls (M-09 of the 2026-10-05 audit, #1616). The perf-gates-on E2E leg
// runs them end to end; these pin each helper on every CI run.

// observability.metrics.drop_legacy_names reaches the metrics listener: off,
// every dexaflow_ family is also served under its leoflow_ name; on, only once.
func TestDropLegacyNamesReachesTheMetricsListener(t *testing.T) {
	for _, drop := range []bool{false, true} {
		cfg := &config.ServerConfig{}
		cfg.Observability.Metrics.DropLegacyNames = drop
		reg := prometheus.NewRegistry()
		c := prometheus.NewCounter(prometheus.CounterOpts{Name: "dexaflow_wiring_probe_total", Help: "probe"})
		reg.MustRegister(c)
		c.Inc()
		h := api.ObservabilityHandler(reg, nil, observabilityOptions(cfg)...)
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", http.NoBody)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		body := rec.Body.String()
		if !strings.Contains(body, "dexaflow_wiring_probe_total 1") {
			t.Fatalf("drop_legacy_names=%v: the dexaflow_ family is missing:\n%s", drop, body)
		}
		if legacy := strings.Contains(body, "leoflow_wiring_probe_total 1"); legacy == drop {
			t.Errorf("drop_legacy_names=%v: leoflow_ family served = %v, want %v", drop, legacy, !drop)
		}
	}
}

// fakeS3 answers every GET with the given status and S3 error code and counts
// the GETs, which is all the segmented layout's boot probe sends.
func fakeS3(t *testing.T, status int, code string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var gets atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if r.Method == http.MethodGet {
			gets.Add(1)
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>`+code+`</Code><Message>m</Message></Error>`)
	}))
	t.Cleanup(srv.Close)
	return srv, &gets
}

// logs.sink.layout reaches the S3 sink buildLogSink returns: segmented makes
// the boot probe a missing key, accepted when the store answers NoSuchKey and
// refused, naming s3:ListBucket, when it answers AccessDenied; single never
// probes.
func TestBuildLogSinkCarriesTheObjectLayout(t *testing.T) {
	for _, tc := range []struct {
		name, layout, code string
		status             int
		wantErr            string
		wantProbe          bool
	}{
		{name: "segmented on a store that answers NoSuchKey", layout: "segmented", status: http.StatusNotFound, code: "NoSuchKey", wantProbe: true},
		{name: "segmented without s3:ListBucket", layout: "segmented", status: http.StatusForbidden, code: "AccessDenied", wantErr: "s3:ListBucket", wantProbe: true},
		{name: "single", layout: "single", status: http.StatusForbidden, code: "AccessDenied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, gets := fakeS3(t, tc.status, tc.code)
			cfg := &config.ServerConfig{}
			cfg.Logs.Backend = "s3"
			cfg.Logs.Sink.Bucket = "logs"
			cfg.Logs.Sink.Region = "us-east-1"
			cfg.Logs.Sink.Endpoint = srv.URL
			cfg.Logs.Sink.ForcePathStyle = true
			cfg.Logs.Sink.AccessKeyID, cfg.Logs.Sink.SecretAccessKey = "k", "s"
			cfg.Logs.Sink.Layout = tc.layout
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			sink, err := buildLogSink(ctx, cfg, slog.New(slog.DiscardHandler))
			if tc.wantErr == "" && (err != nil || sink == nil) {
				t.Fatalf("buildLogSink: sink %v, err %v", sink, err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("buildLogSink err = %v, want one naming %s", err, tc.wantErr)
			}
			if probed := gets.Load() > 0; probed != tc.wantProbe {
				t.Errorf("layout %s: boot probe sent = %v, want %v", tc.layout, probed, tc.wantProbe)
			}
		})
	}
}
