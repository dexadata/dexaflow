package observability

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// schemaState backs dexaflow_schema_ready and dexaflow_schema_wait_seconds
// (ADR 0090 decision 6): the reason of the current verdict and when the
// process started waiting.
type schemaState struct {
	ready *prometheus.GaugeVec
	now   func() time.Time

	mu    sync.Mutex
	since time.Time // zero while the schema is ready
}

func newSchemaState(f promauto.Factory) *schemaState {
	s := &schemaState{now: time.Now}
	s.ready = f.NewGaugeVec(prometheus.GaugeOpts{
		Name: "dexaflow_schema_ready",
		Help: "1 when the database schema is one this binary can serve, 0 while it waits for it, with the reason " +
			"(schema absent, schema behind, schema dirty, schema unreadable). Alert on 0 for 5 minutes.",
	}, []string{"reason"})
	f.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "dexaflow_schema_wait_seconds",
		Help: "Seconds since this process found its database schema not ready, 0 when it is ready.",
	}, s.waitSeconds)
	return s
}

func (s *schemaState) waitSeconds() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.since.IsZero() {
		return 0
	}
	return s.now().Sub(s.since).Seconds()
}

// RecordSchemaState records the schema verdict: the reason the schema is not
// ready, or "" once it is. One series is kept, so a dashboard reads the
// current reason, not every reason ever seen. No-op on a nil receiver.
func (m *Metrics) RecordSchemaState(reason string) {
	if m == nil {
		return
	}
	s := m.schema
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ready.Reset()
	if reason == "" {
		s.since = time.Time{}
		s.ready.WithLabelValues("").Set(1)
		return
	}
	if s.since.IsZero() {
		s.since = s.now()
	}
	s.ready.WithLabelValues(reason).Set(0)
}
