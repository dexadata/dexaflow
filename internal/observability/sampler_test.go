package observability

import (
	"context"
	"math"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func sampleRoot(s sdktrace.Sampler, traceID byte, route string) sdktrace.SamplingDecision {
	var id trace.TraceID
	for i := range id {
		id[i] = traceID
	}
	return s.ShouldSample(sdktrace.SamplingParameters{
		ParentContext: context.Background(),
		TraceID:       id,
		Name:          "GET " + route,
		Kind:          trace.SpanKindInternal,
		Attributes:    []attribute.KeyValue{attribute.String("http.route", route)},
	}).Decision
}

// TestSamplerDefaultSamplesEverything pins the gate's default: a ratio of 1
// without the probe skip records every span, probes included, as the SDK
// default sampler did.
func TestSamplerDefaultSamplesEverything(t *testing.T) {
	s := newSampler(1, false)
	for _, route := range []string{"/api/v2/dags", "/healthz", "/readyz", "/static/*filepath"} {
		for _, id := range []byte{0x00, 0x7f, 0xff} {
			if got := sampleRoot(s, id, route); got != sdktrace.RecordAndSample {
				t.Errorf("route %s trace %#x: decision %v, want RecordAndSample", route, id, got)
			}
		}
	}
}

// TestSamplerRatioZeroDropsRoots pins that the ratio reaches the sampler.
func TestSamplerRatioZeroDropsRoots(t *testing.T) {
	s := newSampler(0, false)
	if got := sampleRoot(s, 0x01, "/api/v2/dags"); got != sdktrace.Drop {
		t.Errorf("decision %v, want Drop", got)
	}
}

// TestSamplerHonorsSampledParent pins ParentBased: a request that arrives
// inside a sampled trace stays in it whatever the local ratio.
func TestSamplerHonorsSampledParent(t *testing.T) {
	s := newSampler(0, false)
	parent := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1},
		SpanID:     trace.SpanID{1},
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})
	res := s.ShouldSample(sdktrace.SamplingParameters{
		ParentContext: trace.ContextWithRemoteSpanContext(context.Background(), parent),
		TraceID:       trace.TraceID{1},
		Name:          "GET /api/v2/dags",
	})
	if res.Decision != sdktrace.RecordAndSample {
		t.Errorf("decision %v, want RecordAndSample", res.Decision)
	}
}

// TestSamplerSkipsProbesWhenEnabled pins the probe gate: health probes and
// static assets are dropped, everything else still follows the ratio.
func TestSamplerSkipsProbesWhenEnabled(t *testing.T) {
	s := newSampler(1, true)
	for _, route := range []string{"/healthz", "/readyz", "/static/*filepath"} {
		if got := sampleRoot(s, 0x01, route); got != sdktrace.Drop {
			t.Errorf("route %s: decision %v, want Drop", route, got)
		}
	}
	for _, route := range []string{"/api/v2/dags", "/ui/dags", "/healthzz", "/staticx"} {
		if got := sampleRoot(s, 0x01, route); got != sdktrace.RecordAndSample {
			t.Errorf("route %s: decision %v, want RecordAndSample", route, got)
		}
	}
}

// TestSetupRejectsSampleRatioOutOfRange pins boot validation: a ratio outside
// [0, 1], NaN included, is a typo, not a request to sample "a lot".
func TestSetupRejectsSampleRatioOutOfRange(t *testing.T) {
	t.Cleanup(resetGlobalTracerProvider)
	for _, ratio := range []float64{-0.1, 1.5, math.NaN()} {
		_, _, err := Setup(context.Background(), Config{
			ServiceName:  "leoflow-test",
			OTelEnabled:  true,
			OTelEndpoint: "localhost:4317",
			SampleRatio:  ratio,
		})
		if err == nil {
			t.Errorf("ratio %v: Setup() succeeded, want an error", ratio)
		}
	}
}
