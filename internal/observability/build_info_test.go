package observability

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/dexadata/dexaflow/internal/version"
)

// dexaflow_build_info carries the running version of every role (#2299), so a
// replica left on the old version after a rollout shows in Prometheus.
func TestRecordBuildInfoExportsOneSeriesWithTheBuildLabels(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	m.RecordBuildInfo(version.Info{Version: "v0.5.10", GitCommit: "1a2b3c4d", GoVersion: "go1.26.10"}, "scheduler")

	mfs, err := WithLegacyNames(reg).Gather()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"version": "v0.5.10", "commit": "1a2b3c4d", "go_version": "go1.26.10", "role": "scheduler"}
	found := map[string]bool{}
	for _, mf := range mfs {
		name := mf.GetName()
		if name != "dexaflow_build_info" && name != "leoflow_build_info" {
			continue
		}
		found[name] = true
		if n := len(mf.GetMetric()); n != 1 {
			t.Fatalf("%s has %d series, want 1", name, n)
		}
		series := mf.GetMetric()[0]
		if got := series.GetGauge().GetValue(); got != 1 {
			t.Errorf("%s = %v, want 1", name, got)
		}
		got := map[string]string{}
		for _, l := range series.GetLabel() {
			got[l.GetName()] = l.GetValue()
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s label %s = %q, want %q", name, k, got[k], v)
			}
		}
		if len(got) != len(want) {
			t.Errorf("%s labels = %v, want exactly %v", name, got, want)
		}
	}
	if !found["dexaflow_build_info"] || !found["leoflow_build_info"] {
		t.Fatalf("build info families found: %v, want dexaflow_build_info and its leoflow_ twin", found)
	}
}

// A second call (a role recorded again) replaces the series instead of adding
// one, so the process never reports two versions.
func TestRecordBuildInfoKeepsOneSeries(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	m.RecordBuildInfo(version.Info{Version: "dev", GitCommit: "none", GoVersion: "go1.26.10"}, "all")
	m.RecordBuildInfo(version.Info{Version: "v0.5.10", GitCommit: "1a2b3c4d", GoVersion: "go1.26.10"}, "all")
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() == "dexaflow_build_info" && len(mf.GetMetric()) != 1 {
			t.Fatalf("dexaflow_build_info has %d series, want 1", len(mf.GetMetric()))
		}
	}
}

func TestRecordBuildInfoOnNilMetricsIsANoOp(t *testing.T) {
	var m *Metrics
	m.RecordBuildInfo(version.Info{}, "all")
}
