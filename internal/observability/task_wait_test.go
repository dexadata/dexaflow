package observability

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestObserveTaskQueueWaitRecordsOneSamplePerCall(t *testing.T) {
	m := NewMetrics(prometheus.NewRegistry())
	m.ObserveTaskQueueWait("acme", 3)
	m.ObserveTaskQueueWait("acme", 7)
	m.ObserveTaskQueueWait("other", 1)
	if got := histogramCount(t, m.TaskQueueWait.WithLabelValues("acme")); got != 2 {
		t.Errorf("acme samples = %d, want 2", got)
	}
	if got := histogramCount(t, m.TaskQueueWait.WithLabelValues("other")); got != 1 {
		t.Errorf("other samples = %d, want 1", got)
	}
}

// SetOldestWaiting replaces the whole set: a tenant that stopped waiting has no
// series left, and nil clears every one.
func TestSetOldestWaitingReplacesTheSeries(t *testing.T) {
	m := NewMetrics(prometheus.NewRegistry())
	m.SetOldestWaiting([]WaitingAge{{"acme", "queued", 40}, {"acme", "scheduled", 5}, {"other", "queued", 2}})
	if got := len(collect(t, m.TaskOldestWaiting)); got != 3 {
		t.Fatalf("series = %d, want 3", got)
	}
	var out dto.Metric
	if err := m.TaskOldestWaiting.WithLabelValues("acme", "queued").Write(&out); err != nil {
		t.Fatal(err)
	}
	if got := out.GetGauge().GetValue(); got != 40 {
		t.Errorf("acme queued = %v, want 40", got)
	}
	m.SetOldestWaiting([]WaitingAge{{"acme", "queued", 55}})
	if got := len(collect(t, m.TaskOldestWaiting)); got != 1 {
		t.Fatalf("series after the second set = %d, want 1", got)
	}
	m.SetOldestWaiting(nil)
	if got := len(collect(t, m.TaskOldestWaiting)); got != 0 {
		t.Fatalf("series after a nil set = %d, want 0", got)
	}
}

func TestTaskWaitRecordersOnNilMetricsAreNoOps(t *testing.T) {
	var m *Metrics
	m.ObserveTaskQueueWait("acme", 1)
	m.SetOldestWaiting([]WaitingAge{{"acme", "queued", 1}})
}

func collect(t *testing.T, c prometheus.Collector) []prometheus.Metric {
	t.Helper()
	ch := make(chan prometheus.Metric, 64)
	c.Collect(ch)
	close(ch)
	var out []prometheus.Metric
	for m := range ch {
		out = append(out, m)
	}
	return out
}
