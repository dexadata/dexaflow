package observability

import (
	"strings"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// newSampler builds the trace sampler. ratio is the share of new root traces
// kept; a request inside an already sampled trace follows its parent. With
// skipProbes, health probes and static assets are dropped before recording.
// ratio 1 without skipProbes matches the SDK default sampler.
func newSampler(ratio float64, skipProbes bool) sdktrace.Sampler {
	s := sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))
	if skipProbes {
		return probeSkipSampler{next: s}
	}
	return s
}

// probeSkipSampler drops spans whose http.route is a probe or static asset and
// delegates every other decision.
type probeSkipSampler struct {
	next sdktrace.Sampler
}

// ShouldSample implements sdktrace.Sampler.
func (s probeSkipSampler) ShouldSample(p sdktrace.SamplingParameters) sdktrace.SamplingResult {
	for _, kv := range p.Attributes {
		if kv.Key == "http.route" && isProbeRoute(kv.Value.AsString()) {
			return sdktrace.SamplingResult{
				Decision:   sdktrace.Drop,
				Tracestate: trace.SpanContextFromContext(p.ParentContext).TraceState(),
			}
		}
	}
	return s.next.ShouldSample(p)
}

// Description implements sdktrace.Sampler.
func (s probeSkipSampler) Description() string {
	return "ProbeSkip{" + s.next.Description() + "}"
}

func isProbeRoute(route string) bool {
	return route == "/healthz" || route == "/readyz" || strings.HasPrefix(route, "/static/")
}
