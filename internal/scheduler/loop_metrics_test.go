package scheduler

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/observability"
)

func loopSamples(t *testing.T, m *observability.Metrics) uint64 {
	t.Helper()
	var out dto.Metric
	if err := m.SchedulerLoopDuration.(prometheus.Metric).Write(&out); err != nil {
		t.Fatal(err)
	}
	return out.GetHistogram().GetSampleCount()
}

// Every leader tick observes dexaflow_scheduler_loop_duration_seconds (#1186,
// audit R-31). Before, nothing in the loop observed it, so the histogram a
// dashboard reads was always empty and looked healthy.
func TestTickObservesLoopDurationOnTheLeader(t *testing.T) {
	m := observability.NewMetrics(prometheus.NewRegistry())
	s := newScheduler(newFakeStore())
	s.SetRecorder(m)

	s.tick(context.Background())
	s.tick(context.Background())
	if got := loopSamples(t, m); got != 2 {
		t.Fatalf("loop duration samples after two leader ticks = %d, want 2", got)
	}

	// A follower's tick does no scheduling work; counting it would dilute the
	// leader's loop latency with near-zero samples.
	s.SetLeading(false)
	s.tick(context.Background())
	if got := loopSamples(t, m); got != 2 {
		t.Errorf("a follower tick must not be observed, samples = %d, want 2", got)
	}
}

// A tick whose step fails is still a loop iteration and is still observed.
func TestTickObservesLoopDurationWhenTheStepFails(t *testing.T) {
	m := observability.NewMetrics(prometheus.NewRegistry())
	store := newFakeStore()
	store.activeRunsErr = context.DeadlineExceeded
	s := newScheduler(store)
	s.SetRecorder(m)

	s.tick(context.Background())
	if got := loopSamples(t, m); got != 1 {
		t.Errorf("a failed tick must be observed, samples = %d, want 1", got)
	}
}

// An applied retry reset counts dexaflow_task_retries_total for its task; a
// reset that lost its race (the task was no longer up_for_retry) does not.
func TestRetryResetCountsTaskRetries(t *testing.T) {
	m := observability.NewMetrics(prometheus.NewRegistry())
	store := retriableRun(domain.TaskStateUpForRetry, 1)
	s := newScheduler(store)
	s.SetRecorder(m)

	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := counterValue(t, m.TaskRetries.WithLabelValues("etl", "a")); got != 1 {
		t.Errorf("dexaflow_task_retries_total{dag_id=etl,task_id=a} = %v, want 1", got)
	}

	m = observability.NewMetrics(prometheus.NewRegistry())
	store = retriableRun(domain.TaskStateUpForRetry, 1)
	store.retryNoop = true
	s = newScheduler(store)
	s.SetRecorder(m)
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := counterValue(t, m.TaskRetries.WithLabelValues("etl", "a")); got != 0 {
		t.Errorf("a retry reset that did not apply must not count, got %v", got)
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
