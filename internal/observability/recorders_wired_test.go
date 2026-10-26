package observability

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func histogramCount(t *testing.T, h prometheus.Observer) uint64 {
	t.Helper()
	m, ok := h.(prometheus.Metric)
	if !ok {
		t.Fatalf("%T is not a metric", h)
	}
	var out dto.Metric
	if err := m.Write(&out); err != nil {
		t.Fatal(err)
	}
	return out.GetHistogram().GetSampleCount()
}

// Every recorder method the scheduler, the dispatch pool and the executor call
// for #1186 moves its collector.
func TestSchedulerAndDispatchRecordersMoveTheirCollectors(t *testing.T) {
	m := NewMetrics(prometheus.NewRegistry())

	m.ObserveSchedulerLoop(0)
	if got := histogramCount(t, m.SchedulerLoopDuration); got != 1 {
		t.Errorf("dexaflow_scheduler_loop_duration_seconds samples = %d, want 1", got)
	}
	m.ObserveScheduleDelay(0)
	if got := histogramCount(t, m.ScheduleDelay); got != 1 {
		t.Errorf("dexaflow_scheduler_schedule_delay_seconds samples = %d, want 1", got)
	}
	m.RecordTaskRetry("etl", "extract")
	if got := counterValue(t, m.TaskRetries.WithLabelValues("etl", "extract")); got != 1 {
		t.Errorf("dexaflow_task_retries_total = %v, want 1", got)
	}
	m.RecordDispatchLatencySeconds(0.2)
	if got := histogramCount(t, m.DispatchLatency); got != 1 {
		t.Errorf("dexaflow_dispatch_latency_seconds samples = %d, want 1", got)
	}
	m.ObservePodCreation("etl", "success", 0)
	if got := counterValue(t, m.PodsCreated.WithLabelValues("etl", "success")); got != 1 {
		t.Errorf("dexaflow_pods_created_total = %v, want 1", got)
	}
	if got := histogramCount(t, m.TaskPodCreationDuration); got != 1 {
		t.Errorf("dexaflow_task_pod_creation_duration_seconds samples = %d, want 1", got)
	}
}

func counterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	var out dto.Metric
	if err := c.Write(&out); err != nil {
		t.Fatal(err)
	}
	return out.GetCounter().GetValue()
}
