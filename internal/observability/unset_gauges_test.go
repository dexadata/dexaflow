package observability

import (
	"reflect"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// TestNoGaugeIsDeclaredWithoutAWriter pins the removal of gauges that nothing in
// the tree ever set. A GaugeVec without a writer exports nothing and a plain
// Gauge exports a constant 0, which reads as a real measurement (no pods running)
// on a dashboard. A gauge comes back together with the code that sets it.
func TestNoGaugeIsDeclaredWithoutAWriter(t *testing.T) {
	typ := reflect.TypeOf(Metrics{})
	for _, name := range []string{"SchedulerLeader", "ActiveDAGRuns", "QueuedTasks", "PodsRunning"} {
		if _, ok := typ.FieldByName(name); ok {
			t.Errorf("Metrics.%s is declared but never set anywhere", name)
		}
	}
}

// TestFreshScrapeHasNoConstantZeroPodsGauge pins the visible effect: a fresh
// registry no longer serves dexaflow_pods_running 0.
func TestFreshScrapeHasNoConstantZeroPodsGauge(t *testing.T) {
	reg := prometheus.NewRegistry()
	NewMetrics(reg)
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() == "dexaflow_pods_running" {
			t.Fatal("dexaflow_pods_running is still served although nothing sets it")
		}
	}
}
