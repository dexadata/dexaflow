package observability

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// schemaSeries returns every dexaflow_schema_ready series as reason -> value,
// and dexaflow_schema_wait_seconds.
func schemaSeries(t *testing.T, reg *prometheus.Registry) (ready map[string]float64, wait float64) {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	ready = map[string]float64{}
	wait = -1
	for _, mf := range mfs {
		switch mf.GetName() {
		case "dexaflow_schema_ready":
			for _, m := range mf.GetMetric() {
				reason := ""
				for _, l := range m.GetLabel() {
					if l.GetName() == "reason" {
						reason = l.GetValue()
					}
				}
				ready[reason] = m.GetGauge().GetValue()
			}
		case "dexaflow_schema_wait_seconds":
			wait = mf.GetMetric()[0].GetGauge().GetValue()
		}
	}
	return ready, wait
}

// ADR 0090 decision 6: dexaflow_schema_ready is one series, 0 with the reason
// while the schema is not ready and 1 once it is, and
// dexaflow_schema_wait_seconds counts from the first not-ready verdict, across
// reason changes, back to 0 when ready.
func TestSchemaStateMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	now := time.Unix(1_000, 0)
	m.schema.now = func() time.Time { return now }

	if ready, wait := schemaSeries(t, reg); len(ready) != 0 || wait != 0 {
		t.Fatalf("before any verdict: ready %v wait %v, want no series and 0", ready, wait)
	}

	m.RecordSchemaState("schema absent")
	now = now.Add(90 * time.Second)
	ready, wait := schemaSeries(t, reg)
	if len(ready) != 1 || ready["schema absent"] != 0 {
		t.Errorf("absent: ready series %v, want only {reason=schema absent} 0", ready)
	}
	if wait != 90 {
		t.Errorf("absent for 90s: wait = %v, want 90", wait)
	}

	m.RecordSchemaState("schema behind")
	now = now.Add(10 * time.Second)
	ready, wait = schemaSeries(t, reg)
	if len(ready) != 1 || ready["schema behind"] != 0 {
		t.Errorf("behind: ready series %v, want only {reason=schema behind} 0", ready)
	}
	if wait != 100 {
		t.Errorf("a new reason restarted the wait: %v, want 100", wait)
	}

	m.RecordSchemaState("")
	ready, wait = schemaSeries(t, reg)
	if len(ready) != 1 || ready[""] != 1 {
		t.Errorf("ready: series %v, want only {} 1", ready)
	}
	if wait != 0 {
		t.Errorf("ready: wait = %v, want 0", wait)
	}

	m.RecordSchemaState("schema absent")
	now = now.Add(5 * time.Second)
	if _, wait = schemaSeries(t, reg); wait != 5 {
		t.Errorf("a second wait carried the first one's start: %v, want 5", wait)
	}
}

func TestRecordSchemaStateOnNilMetricsIsANoOp(t *testing.T) {
	var m *Metrics
	m.RecordSchemaState("schema absent")
}
