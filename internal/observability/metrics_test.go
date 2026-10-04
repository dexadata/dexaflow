package observability

import (
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// touchAll exercises every metric once so the collectors produce a metric
// family on Gather (unused label vectors emit nothing otherwise).
func touchAll(m *Metrics) {
	m.SchedulerLoopDuration.Observe(0.1)
	m.SchedulerDecisions.WithLabelValues("schedule").Inc()
	m.TaskStateTransitions.WithLabelValues("none", "scheduled", "etl").Inc()
	m.TaskDuration.WithLabelValues("etl", "t1", "python").Observe(1)
	m.TaskRetries.WithLabelValues("etl", "t1").Inc()
	m.TaskPodCreationDuration.Observe(1)
	m.TaskColdStart.WithLabelValues("etl").Observe(1)
	m.XComSize.WithLabelValues("etl").Observe(128)
	m.XComPush.WithLabelValues("etl").Inc()
	m.XComPull.WithLabelValues("etl").Inc()
	m.XComRejected.WithLabelValues("too_large").Inc()
	m.HTTPRequests.WithLabelValues("GET", "/api/v2/dags", "200").Inc()
	m.HTTPRequestDuration.WithLabelValues("GET", "/api/v2/dags").Observe(0.01)
	m.AuthFailures.WithLabelValues("bad_password").Inc()
	m.PodsCreated.WithLabelValues("etl", "success").Inc()
	m.PodPendingDuration.Observe(1)
	m.KubernetesAPICalls.WithLabelValues("create_pod", "success").Inc()
}

func TestRecordersIncrementCounters(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	m.RecordHTTPRequest("GET", "/api/v2/dags", 200, 5*time.Millisecond)
	m.RecordSchedulerDecision("panic")    // backs the scheduler resilience metric
	m.RecordUndispatchable("no_executor") // backs the undispatchable signal (#46)
	m.RecordSchedulerWokenTick()          // backs scheduler.eager_promotion's wake rate

	// Each recorder must have incremented its counter to 1.
	for name, want := range map[string]float64{
		"dexaflow_http_requests_total":         1,
		"dexaflow_scheduler_decisions_total":   1,
		"dexaflow_tasks_undispatchable_total":  1,
		"dexaflow_scheduler_woken_ticks_total": 1,
	} {
		if got := counterTotal(t, reg, name); got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
}

// counterTotal sums every sample of the named counter family in reg.
func counterTotal(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, fam := range families {
		if fam.GetName() != name {
			continue
		}
		var sum float64
		for _, met := range fam.GetMetric() {
			sum += met.GetCounter().GetValue()
		}
		return sum
	}
	t.Fatalf("counter %q not found in registry", name)
	return 0
}

func TestRecordTaskDurationObserves(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	m.RecordTaskTransition("running", "success", "etl")
	m.RecordTaskDuration("etl", "hook", "bash", 1.5)

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, fam := range families {
		if fam.GetName() != "dexaflow_task_duration_seconds" {
			continue
		}
		if n := fam.GetMetric()[0].GetHistogram().GetSampleCount(); n != 1 {
			t.Errorf("task duration sample count = %d, want 1", n)
		}
		return
	}
	t.Error("dexaflow_task_duration_seconds not recorded")
}

func TestNewMetricsRegistersAllADR0010Metrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	touchAll(m)

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	got := make(map[string]bool, len(families))
	for _, fam := range families {
		got[fam.GetName()] = true
	}

	want := []string{
		"dexaflow_scheduler_loop_duration_seconds",
		"dexaflow_scheduler_decisions_total",
		"dexaflow_scheduler_woken_ticks_total",
		"dexaflow_task_state_transitions_total",
		"dexaflow_task_duration_seconds",
		"dexaflow_task_retries_total",
		"dexaflow_task_pod_creation_duration_seconds",
		"dexaflow_task_cold_start_seconds",
		"dexaflow_xcom_size_bytes",
		"dexaflow_xcom_push_total",
		"dexaflow_xcom_pull_total",
		"dexaflow_xcom_rejected_total",
		"dexaflow_http_requests_total",
		"dexaflow_http_request_duration_seconds",
		"dexaflow_auth_failures_total",
		"dexaflow_pods_created_total",
		"dexaflow_pod_pending_duration_seconds",
		"dexaflow_kubernetes_api_calls_total",
	}
	var missing []string
	for _, name := range want {
		if !got[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("missing metrics: %v", missing)
	}
}

// Every metric ADR 0010 promises is also scrapeable under its pre-rename name.
func TestEveryRegisteredMetricHasItsLegacyTwin(t *testing.T) {
	reg := prometheus.NewRegistry()
	NewMetrics(reg)
	mfs, err := WithLegacyNames(reg).Gather()
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, mf := range mfs {
		names[mf.GetName()] = true
	}
	for name := range names {
		if suffix, ok := strings.CutPrefix(name, "dexaflow_"); ok && !names["leoflow_"+suffix] {
			t.Errorf("%s has no leoflow_%s twin", name, suffix)
		}
	}
}
