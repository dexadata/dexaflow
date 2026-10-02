package observability

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics are named dexaflow_* since the rename. Dashboards and alerts written
// against the leoflow_* names keep working because the scrape endpoint also
// publishes every dexaflow_* family under its leoflow_* name, same values.
func TestWithLegacyNamesPublishesBothNames(t *testing.T) {
	reg := prometheus.NewRegistry()
	c := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "dexaflow_widgets_total", Help: "Widgets.",
	}, []string{"kind"})
	reg.MustRegister(c)
	c.WithLabelValues("a").Add(3)

	mfs, err := WithLegacyNames(reg).Gather()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]float64{}
	for _, mf := range mfs {
		if mf.GetHelp() != "Widgets." {
			t.Errorf("%s help = %q, want the original help", mf.GetName(), mf.GetHelp())
		}
		for _, m := range mf.GetMetric() {
			if len(m.GetLabel()) != 1 || m.GetLabel()[0].GetValue() != "a" {
				t.Errorf("%s labels = %v, want kind=a", mf.GetName(), m.GetLabel())
			}
			got[mf.GetName()] = m.GetCounter().GetValue()
		}
	}
	want := map[string]float64{"dexaflow_widgets_total": 3, "leoflow_widgets_total": 3}
	if len(got) != len(want) || got["dexaflow_widgets_total"] != 3 || got["leoflow_widgets_total"] != 3 {
		t.Fatalf("gathered %v, want %v", got, want)
	}
}

// Families that are not ours (go_*, process_*) are passed through once.
func TestWithLegacyNamesLeavesOtherFamiliesAlone(t *testing.T) {
	reg := prometheus.NewRegistry()
	g := prometheus.NewGauge(prometheus.GaugeOpts{Name: "go_like_gauge", Help: "Other."})
	reg.MustRegister(g)
	g.Set(1)
	mfs, err := WithLegacyNames(reg).Gather()
	if err != nil {
		t.Fatal(err)
	}
	if len(mfs) != 1 || mfs[0].GetName() != "go_like_gauge" {
		t.Fatalf("got %d families, first %q; want only go_like_gauge", len(mfs), mfs[0].GetName())
	}
}

// The copy must not alias the original: a scrape that mutates one family (as
// the text encoder does not, but a future consumer might) must not change the
// other.
func TestWithLegacyNamesCopiesInsteadOfAliasing(t *testing.T) {
	reg := prometheus.NewRegistry()
	g := prometheus.NewGauge(prometheus.GaugeOpts{Name: "dexaflow_g", Help: "G."})
	reg.MustRegister(g)
	g.Set(5)
	mfs, err := WithLegacyNames(reg).Gather()
	if err != nil {
		t.Fatal(err)
	}
	if len(mfs) != 2 {
		t.Fatalf("got %d families, want 2", len(mfs))
	}
	v := 9.0
	mfs[0].Metric[0].Gauge.Value = &v
	if mfs[1].Metric[0].Gauge.GetValue() != 5 {
		t.Fatal("the legacy family shares memory with the current one")
	}
}

// Every metric the control plane registers uses the current prefix, so each
// one gets its legacy twin.
func TestRegisteredMetricsUseTheDexaflowPrefix(t *testing.T) {
	reg := prometheus.NewRegistry()
	NewMetrics(reg)
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if strings.HasPrefix(mf.GetName(), "leoflow_") {
			t.Errorf("metric %q is registered under the legacy prefix", mf.GetName())
		}
	}
}
