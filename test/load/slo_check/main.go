// Command slo_check judges the service level objectives of the production
// profile (operate/service-level-objectives, #2314) from scrapes of the
// control plane's /metrics, for a soak or a failure injection run that has no
// Prometheus.
//
// Give it the scrapes in time order. Each objective's counters are summed as
// increases between consecutive scrapes, with a counter that went down (the
// process restarted) counted from zero, as Prometheus' increase() does: so a
// run that kills the control plane needs a scrape every few seconds, or the
// counts of the killed process since its last scrape are lost.
//
// Usage (see test/load/README.md):
//
//	while sleep 15; do curl -s localhost:9090/metrics > "scrapes/$(date +%s).prom"; done &
//	... run the soak ...
//	go run ./test/load/slo_check scrapes/*.prom
//
// It prints one row per objective and exits 1 when one is broken. An
// objective with no event in the window is reported as not measured, never as
// met.
package main

import (
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

// lostDecisions are the reaper decisions that fail an attempt for a reason
// the platform caused, the ones DexaflowTasksLost counts.
var lostDecisions = map[string]bool{"agent_lost": true, "pod_lost": true, "dispatch_lost": true, "warm_worker_lost": true}

// scrape is one /metrics reading: every counter and histogram sample by
// series key.
type scrape map[string]float64

// objective is one row of the verdict.
type objective struct {
	Name      string
	Target    float64 // the share of good events the objective asks for
	Good, All float64
}

// Ratio is the share of good events, NaN with none.
func (o objective) Ratio() float64 {
	if o.All == 0 {
		return math.NaN()
	}
	return o.Good / o.All
}

// Verdict is met, broken or not measured.
func (o objective) Verdict() string {
	r := o.Ratio()
	switch {
	case math.IsNaN(r):
		return "not measured"
	case r >= o.Target:
		return "met"
	default:
		return "BROKEN"
	}
}

func main() {
	punctuality := flag.Float64("schedule-punctuality", 0.99, "share of scheduled runs created within 60 s of their slot")
	taskStart := flag.Float64("task-start", 0.99, "share of attempts running within 120 s of queued")
	platform := flag.Float64("platform-failures", 0.001, "highest share of attempts failed by the platform")
	api := flag.Float64("api-availability", 0.999, "share of API requests answered without a 5xx")
	flag.Parse()
	if flag.NArg() < 2 {
		fmt.Fprintln(os.Stderr, "slo_check: give at least two scrapes, in time order")
		os.Exit(2)
	}
	scrapes := make([]scrape, 0, flag.NArg())
	for _, path := range flag.Args() {
		s, err := parseFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "slo_check: %s: %v\n", path, err)
			os.Exit(2)
		}
		scrapes = append(scrapes, s)
	}
	rows := judge(increases(scrapes), *punctuality, *taskStart, *platform, *api)
	if err := report(os.Stdout, rows); err != nil {
		fmt.Fprintf(os.Stderr, "slo_check: %v\n", err)
		os.Exit(2)
	}
	for _, o := range rows {
		if o.Verdict() == "BROKEN" {
			os.Exit(1)
		}
	}
}

func parseFile(path string) (scrape, error) {
	f, err := os.Open(path) //nolint:gosec // operator-given scrape files; reading them is the point.
	if err != nil {
		return nil, err
	}
	s, err := parse(f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return s, err
}

// report writes one row per objective.
func report(out io.Writer, rows []objective) error {
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	lines := make([]string, 0, 1+len(rows))
	lines = append(lines, "objective\ttarget\tmeasured\tevents\tverdict")
	for _, o := range rows {
		lines = append(lines, fmt.Sprintf("%s\t%.4g\t%.5f\t%.0f\t%s", o.Name, o.Target, o.Ratio(), o.All, o.Verdict()))
	}
	if _, err := io.WriteString(w, strings.Join(lines, "\n")+"\n"); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}
	_, err := io.WriteString(out, b.String())
	return err
}

// parse reads one Prometheus text exposition into series keys: the metric
// name (with _bucket{le} and _count for a histogram) and its labels.
func parse(r io.Reader) (scrape, error) {
	p := expfmt.NewTextParser(model.UTF8Validation)
	families, err := p.TextToMetricFamilies(r)
	if err != nil {
		return nil, err
	}
	s := scrape{}
	for name, mf := range families {
		for _, m := range mf.GetMetric() {
			labels := labelString(m.GetLabel())
			switch mf.GetType() {
			case dto.MetricType_COUNTER:
				s[name+labels] = m.GetCounter().GetValue()
			case dto.MetricType_HISTOGRAM:
				h := m.GetHistogram()
				s[name+"_count"+labels] = float64(h.GetSampleCount())
				for _, b := range h.GetBucket() {
					s[fmt.Sprintf("%s_bucket%s|le=%g", name, labels, b.GetUpperBound())] = float64(b.GetCumulativeCount())
				}
			case dto.MetricType_GAUGE, dto.MetricType_SUMMARY, dto.MetricType_UNTYPED, dto.MetricType_GAUGE_HISTOGRAM:
				// No objective reads a gauge or a summary.
			}
		}
	}
	return s, nil
}

func labelString(ls []*dto.LabelPair) string {
	parts := make([]string, 0, len(ls))
	for _, l := range ls {
		parts = append(parts, l.GetName()+"="+l.GetValue())
	}
	sort.Strings(parts)
	return "{" + strings.Join(parts, ",") + "}"
}

// increases sums each series' increase over consecutive scrapes; a value
// below the previous one is a restart and counts from zero. A series absent
// from a scrape (a restarted process that has not seen it yet) keeps its
// previous value as the base.
func increases(scrapes []scrape) scrape {
	out := scrape{}
	last := scrape{}
	for k, v := range scrapes[0] {
		last[k] = v
		out[k] = 0
	}
	for _, s := range scrapes[1:] {
		for k, v := range s {
			prev, seen := last[k]
			switch {
			case !seen:
				out[k] += v
			case v >= prev:
				out[k] += v - prev
			default:
				out[k] += v
			}
			last[k] = v
		}
	}
	return out
}

// judge sums the increases into the five objectives the page states, leaving
// out leadership, which the soak's leader_churn check judges, and outcome
// durability, which its success_archived check judges.
func judge(inc scrape, punctuality, taskStart, platform, api float64) []objective {
	sched := objective{Name: "schedule punctuality", Target: punctuality}
	start := objective{Name: "task start", Target: taskStart}
	plat := objective{Name: "platform-caused failures", Target: 1 - platform}
	avail := objective{Name: "API availability", Target: api}
	var lost float64
	for k, v := range inc {
		name, rest, _ := strings.Cut(k, "{")
		switch {
		case name == "dexaflow_scheduler_schedule_delay_seconds_count":
			sched.All += v
		case name == "dexaflow_scheduler_schedule_delay_seconds_bucket" && strings.HasSuffix(rest, "|le=60"):
			sched.Good += v
		case name == "dexaflow_task_queue_wait_seconds_count":
			start.All += v
		case name == "dexaflow_task_queue_wait_seconds_bucket" && strings.HasSuffix(rest, "|le=120"):
			start.Good += v
		case name == "dexaflow_http_requests_total":
			avail.All += v
			if !strings.Contains(rest, "status=5") {
				avail.Good += v
			}
		case name == "dexaflow_task_attempts_total":
			plat.All += v
		case name == "dexaflow_scheduler_decisions_total" && lostDecisions[label(rest, "decision_type")]:
			lost += v
		}
	}
	plat.Good = plat.All - lost
	return []objective{sched, start, plat, avail}
}

// label is one label's value out of a series key's label part.
func label(rest, name string) string {
	for _, part := range strings.Split(strings.TrimSuffix(rest, "}"), ",") {
		if k, v, ok := strings.Cut(part, "="); ok && k == name {
			return v
		}
	}
	return ""
}
