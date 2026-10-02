package observability

import (
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"google.golang.org/protobuf/proto"
)

// metricPrefix is the name prefix of every metric the control plane registers.
const metricPrefix = "dexaflow_"

// legacyMetricPrefix is the metric prefix before the rename.
const legacyMetricPrefix = "leoflow_"

// legacyNamesGatherer publishes every dexaflow_* family a second time under its
// pre-rename leoflow_* name. See WithLegacyNames.
type legacyNamesGatherer struct {
	inner prometheus.Gatherer
}

// WithLegacyNames wraps g so each dexaflow_* metric family is also exposed as
// leoflow_*, with identical help, type, labels and values. Dashboards, alerts
// and recording rules written before the rename keep working against the same
// scrape; other families pass through once.
func WithLegacyNames(g prometheus.Gatherer) prometheus.Gatherer {
	return legacyNamesGatherer{inner: g}
}

// Gather implements prometheus.Gatherer.
func (l legacyNamesGatherer) Gather() ([]*dto.MetricFamily, error) {
	mfs, err := l.inner.Gather()
	out := make([]*dto.MetricFamily, 0, 2*len(mfs))
	for _, mf := range mfs {
		out = append(out, mf)
		if suffix, ok := strings.CutPrefix(mf.GetName(), metricPrefix); ok {
			twin, ok := proto.Clone(mf).(*dto.MetricFamily)
			if !ok {
				continue
			}
			twin.Name = proto.String(legacyMetricPrefix + suffix)
			out = append(out, twin)
		}
	}
	return out, err
}
