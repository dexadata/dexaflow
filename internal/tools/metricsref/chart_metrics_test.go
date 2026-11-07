package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The chart's Grafana dashboards and alert rules query metrics by name. A
// renamed or removed metric leaves them empty without any error: a dashboard
// panel shows "No data" and an alert never fires. These tests hold every name
// and label they query to what the server registers (#1577).

var (
	metricRef      = regexp.MustCompile(`\bdexaflow_[a-z0-9_]+`)
	metricSelector = regexp.MustCompile(`\b(dexaflow_[a-z0-9_]+)\{([^}]*)\}`)
	selectorLabel  = regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_]*)\s*(?:=~|!~|!=|=)`)
	byClause       = regexp.MustCompile(`\bby \(([^)]*)\)`)
)

// targetLabels are added by the scrape, not by the server.
var targetLabels = []string{"namespace", "pod", "instance", "job", "service", "container"}

// registered maps each series name a query can use to its family: a histogram
// also exports _bucket (with le), _sum and _count.
func registered(t *testing.T) map[string]family {
	t.Helper()
	fams, err := families()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]family{}
	for _, f := range fams {
		out[f.name] = f
		if f.kind == "histogram" || f.kind == "summary" {
			out[f.name+"_sum"] = f
			out[f.name+"_count"] = f
		}
		if f.kind == "histogram" {
			b := f
			b.labels = append(slices.Clone(f.labels), "le")
			out[f.name+"_bucket"] = b
		}
	}
	return out
}

// checkExpr reports every metric expr names that the server does not export,
// and every label it selects or groups by that none of its metrics carry.
func checkExpr(errorf func(string, ...any), where, expr string, reg map[string]family) {
	labels := slices.Clone(targetLabels)
	for _, name := range metricRef.FindAllString(expr, -1) {
		f, ok := reg[name]
		if !ok {
			errorf("%s: %s is not a metric the server registers", where, name)
			continue
		}
		labels = append(labels, f.labels...)
	}
	for _, m := range metricSelector.FindAllStringSubmatch(expr, -1) {
		f, ok := reg[m[1]]
		if !ok {
			continue
		}
		for _, l := range selectorLabel.FindAllStringSubmatch(m[2], -1) {
			if !slices.Contains(f.labels, l[1]) && !slices.Contains(targetLabels, l[1]) {
				errorf("%s: %s has no label %q (it has %v)", where, m[1], l[1], f.labels)
			}
		}
	}
	for _, m := range byClause.FindAllStringSubmatch(expr, -1) {
		for _, l := range strings.Split(m[1], ",") {
			if l = strings.TrimSpace(l); l != "" && !slices.Contains(labels, l) {
				errorf("%s: groups by %q, which none of its metrics carry", where, l)
			}
		}
	}
}

// TestDashboardsQueryRegisteredMetrics walks every panel query of every
// dashboard the chart ships.
func TestDashboardsQueryRegisteredMetrics(t *testing.T) {
	reg := registered(t)
	paths, err := filepath.Glob(filepath.Join(repoRoot, "helm", "dexaflow", "dashboards", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no dashboard under helm/dexaflow/dashboards")
	}
	for _, p := range paths {
		raw, err := os.ReadFile(p) //nolint:gosec // a fixed chart file under the repository root
		if err != nil {
			t.Fatal(err)
		}
		var d struct {
			UID    string `json:"uid"`
			Panels []struct {
				Title   string `json:"title"`
				Targets []struct {
					Expr string `json:"expr"`
				} `json:"targets"`
			} `json:"panels"`
		}
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if d.UID == "" {
			t.Errorf("%s: no uid, so every import creates a new dashboard", p)
		}
		queries := 0
		for _, panel := range d.Panels {
			for _, tg := range panel.Targets {
				queries++
				checkExpr(t.Errorf, filepath.Base(p)+" "+panel.Title, tg.Expr, reg)
			}
		}
		if queries == 0 {
			t.Errorf("%s: no panel queries", p)
		}
	}
}

// TestAlertRulesQueryRegisteredMetrics checks the metric names of the chart's
// PrometheusRule. Its selectors are Helm templates, so only names and the
// grouping labels are checked here; promtool runs the rules themselves.
func TestAlertRulesQueryRegisteredMetrics(t *testing.T) {
	reg := registered(t)
	raw, err := os.ReadFile(filepath.Join(repoRoot, "helm", "dexaflow", "templates", "prometheusrule.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	exprs := 0
	for line := range strings.SplitSeq(string(raw), "\n") {
		if !metricRef.MatchString(line) || strings.Contains(line, "description:") || strings.Contains(line, "summary:") {
			continue
		}
		exprs++
		for _, name := range metricRef.FindAllString(line, -1) {
			if _, ok := reg[name]; !ok {
				t.Errorf("prometheusrule.yaml: %s is not a metric the server registers: %s", name, strings.TrimSpace(line))
			}
		}
	}
	if exprs == 0 {
		t.Fatal("no metric found in prometheusrule.yaml")
	}
}

// TestCheckExprCatchesDrift proves the checks fail on the drift they exist for.
func TestCheckExprCatchesDrift(t *testing.T) {
	reg := registered(t)
	cases := map[string]string{
		"renamed metric": `sum(rate(dexaflow_http_request_total[5m]))`,
		"wrong label":    `sum(rate(dexaflow_http_requests_total{code="500"}[5m]))`,
		"wrong group":    `sum by (tenant) (rate(dexaflow_http_requests_total[5m]))`,
	}
	for name, expr := range cases {
		var got []string
		checkExpr(func(f string, a ...any) { got = append(got, fmt.Sprintf(f, a...)) }, name, expr, reg)
		if len(got) == 0 {
			t.Errorf("%s: %s passed the check", name, expr)
		}
	}
	var got []string
	checkExpr(func(f string, a ...any) { got = append(got, fmt.Sprintf(f, a...)) }, "valid",
		`histogram_quantile(0.99, sum by (le, tenant) (rate(dexaflow_task_queue_wait_seconds_bucket{namespace=~"$namespace"}[5m])))`, reg)
	if len(got) != 0 {
		t.Errorf("a valid query failed the check: %v", got)
	}
}
