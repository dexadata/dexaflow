package observability

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// The retention janitor's recorder counts deleted rows per table, sets the
// dry-run eligible gauge and observes the cycle duration.
func TestRetentionRecorder(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	m.RecordRetentionDeleted("dag_runs", 3)
	m.RecordRetentionDeleted("dag_runs", 2)
	m.RecordRetentionEligible("audit_log", 7)
	m.ObserveRetentionCycle(2 * time.Second)
	if got := counterTotal(t, reg, "dexaflow_retention_rows_deleted_total"); got != 5 {
		t.Errorf("rows deleted = %v, want 5", got)
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, f := range families {
		seen[f.GetName()] = true
		if f.GetName() == "dexaflow_retention_rows_eligible" {
			if v := f.GetMetric()[0].GetGauge().GetValue(); v != 7 {
				t.Errorf("eligible gauge = %v, want 7", v)
			}
		}
	}
	for _, name := range []string{"dexaflow_retention_rows_eligible", "dexaflow_retention_cycle_duration_seconds"} {
		if !seen[name] {
			t.Errorf("%s not registered", name)
		}
	}
}
