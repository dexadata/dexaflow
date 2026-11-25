// Command slo_check judges the service level objectives of the production
// profile (operate/service-level-objectives, #2314) from scrapes of the
// control plane's /metrics, for a soak or a failure injection run that has no
// Prometheus.
//
// Scrape every control plane process separately, each into a directory of
// its own, one file per scrape named by its Unix time in seconds; the file's
// modification time stands in for a name that is not a number. Give the
// directories as arguments:
//
//	mkdir -p scrapes/server
//	while sleep 15; do curl -s localhost:9090/metrics > "scrapes/server/$(date +%s).prom"; done &
//	... run the soak ...
//	go run ./test/load/slo_check scrapes/*/
//
// Within a directory, each objective's counters are summed as increases
// between consecutive scrapes. A counter that went down is a restart: the new
// process's values count from zero, as Prometheus' increase() does. The
// control plane exports no process start time, so a restarted process whose
// counters climbed past the old values before the next scrape is undercounted,
// and a process killed between two scrapes loses its counts since the last
// one: keep the interval short in a run that kills it. A scrape that does not
// parse, or holds no dexaflow_ series (an empty file from a refused
// connection), is skipped with a warning. The directories' increases are then
// added up.
//
// Leadership is judged on the union of the directories: a stretch between two
// scrapes of one process in which its leader tick count grew had a leader,
// and every other stretch of the run, a stretch with no scrape included, did
// not. The run's total time without a leader must stay under the monthly
// budget, 15 minutes.
//
// It prints one row per objective and exits 1 when one is broken. An
// objective with no event in the run is reported as not measured, never as
// met.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

// lostDecisions are the reaper decisions that fail an attempt for a reason
// the platform caused, the ones DexaflowTasksLost counts.
var lostDecisions = map[string]bool{"agent_lost": true, "pod_lost": true, "dispatch_lost": true, "warm_worker_lost": true}

// probePaths are not API traffic: the kubelet's probes and the scrape.
var probePaths = map[string]bool{"/healthz": true, "/readyz": true, "/metrics": true}

var serverError = regexp.MustCompile(`^5\d\d$`)

const leaderTicks = "dexaflow_scheduler_loop_duration_seconds_count{}"

// scrape is one /metrics reading: every counter and histogram sample by
// series key.
type scrape map[string]float64

// fromDexaflow reports whether the scrape holds a control plane series.
func (s scrape) fromDexaflow() bool {
	for k := range s {
		if strings.HasPrefix(k, "dexaflow_") {
			return true
		}
	}
	return false
}

// timedScrape is a scrape and when it was taken.
type timedScrape struct {
	at time.Time
	s  scrape
}

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
	api := flag.Float64("api-availability", 0.999, "share of API requests answered without a 5xx, probes left out")
	leaderless := flag.Duration("leaderless", 15*time.Minute, "longest total time without a leader over the run")
	flag.Parse()
	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "slo_check: give one directory of scrapes per control plane process")
		os.Exit(2)
	}
	sources := make([][]timedScrape, 0, flag.NArg())
	for _, dir := range flag.Args() {
		src, err := loadDir(dir, os.Stderr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "slo_check: %s: %v\n", dir, err)
			os.Exit(2)
		}
		if len(src) < 2 {
			fmt.Fprintf(os.Stderr, "slo_check: %s: fewer than two scrapes parse\n", dir)
			os.Exit(2)
		}
		sources = append(sources, src)
	}
	total := scrape{}
	for _, src := range sources {
		for k, v := range increases(src) {
			total[k] += v
		}
	}
	rows := judge(total, *punctuality, *taskStart, *platform, *api)
	without, span := leaderlessTime(sources)
	if err := report(os.Stdout, rows, without, span, *leaderless); err != nil {
		fmt.Fprintf(os.Stderr, "slo_check: %v\n", err)
		os.Exit(2)
	}
	broken := without > *leaderless
	for _, o := range rows {
		broken = broken || o.Verdict() == "BROKEN"
	}
	if broken {
		os.Exit(1)
	}
}

// loadDir reads one process's scrapes in time order, warning about and
// skipping the ones that do not parse.
func loadDir(dir string, warn io.Writer) ([]timedScrape, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]timedScrape, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		at, err := scrapeTime(path, e)
		if err != nil {
			return nil, err
		}
		s, err := parseFile(path)
		if err == nil && !s.fromDexaflow() {
			err = errors.New("no dexaflow_ series (the process did not answer)")
		}
		if err != nil {
			if _, werr := fmt.Fprintf(warn, "slo_check: skipping %s: %v\n", path, err); werr != nil {
				return nil, werr
			}
			continue
		}
		out = append(out, timedScrape{at: at, s: s})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].at.Before(out[j].at) })
	return out, nil
}

// scrapeTime is the Unix time a file's name gives, or its modification time.
func scrapeTime(path string, e os.DirEntry) (time.Time, error) {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if sec, err := strconv.ParseInt(base, 10, 64); err == nil {
		return time.Unix(sec, 0), nil
	}
	info, err := e.Info()
	if err != nil {
		return time.Time{}, err
	}
	return info.ModTime(), nil
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

// report writes one row per objective, then leadership.
func report(out io.Writer, rows []objective, without, span, budget time.Duration) error {
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	lines := make([]string, 0, 2+len(rows))
	lines = append(lines, "objective\ttarget\tmeasured\tevents\tverdict")
	for _, o := range rows {
		lines = append(lines, fmt.Sprintf("%s\t%.4g\t%.5f\t%.0f\t%s", o.Name, o.Target, o.Ratio(), o.All, o.Verdict()))
	}
	verdict := "met"
	switch {
	case span == 0:
		verdict = "not measured"
	case without > budget:
		verdict = "BROKEN"
	}
	lines = append(lines, fmt.Sprintf("leadership\tunder %s\t%s without a leader\tover %s\t%s", budget, without.Round(time.Second), span.Round(time.Second), verdict))
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
			case dto.MetricType_GAUGE:
				// A gauge marks the scrape as the control plane's.
				s[name+labels] = 0
			case dto.MetricType_SUMMARY, dto.MetricType_UNTYPED, dto.MetricType_GAUGE_HISTOGRAM:
				// No objective reads a summary.
			}
		}
	}
	// The leader's tick count summed over its labels, for leadership.
	for k, v := range s {
		if strings.HasPrefix(k, "dexaflow_scheduler_loop_duration_seconds_count{") && k != leaderTicks {
			s[leaderTicks] += v
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

// step is what one series gained from prev to cur, one process's
// consecutive scrapes: everything when it went down (a restart).
func step(prev, cur scrape, k string) float64 {
	v := cur[k]
	p, seen := prev[k]
	switch {
	case !seen:
		return v
	case v >= p:
		return v - p
	default:
		return v
	}
}

// increases sums each series' increase over one process's consecutive
// scrapes.
func increases(src []timedScrape) scrape {
	out := scrape{}
	for i := 1; i < len(src); i++ {
		for k := range src[i].s {
			out[k] += step(src[i-1].s, src[i].s, k)
		}
	}
	return out
}

// leaderlessTime is the time of the run, from its first scrape to its last
// over every process, that no stretch with a growing leader tick count
// covers, and the run's span.
func leaderlessTime(sources [][]timedScrape) (without, span time.Duration) {
	type interval struct{ from, to time.Time }
	var covered []interval
	var first, last time.Time
	for _, src := range sources {
		for i, ts := range src {
			if first.IsZero() || ts.at.Before(first) {
				first = ts.at
			}
			if ts.at.After(last) {
				last = ts.at
			}
			if i > 0 && step(src[i-1].s, ts.s, leaderTicks) > 0 {
				covered = append(covered, interval{src[i-1].at, ts.at})
			}
		}
	}
	span = last.Sub(first)
	sort.Slice(covered, func(i, j int) bool { return covered[i].from.Before(covered[j].from) })
	var led time.Duration
	var cur interval
	for _, c := range covered {
		switch {
		case cur.to.IsZero():
			cur = c
		case !c.from.After(cur.to):
			if c.to.After(cur.to) {
				cur.to = c.to
			}
		default:
			led += cur.to.Sub(cur.from)
			cur = c
		}
	}
	if !cur.to.IsZero() {
		led += cur.to.Sub(cur.from)
	}
	return span - led, span
}

// judge sums the increases into the four ratio objectives the page states.
// Leadership is judged by leaderlessTime, and outcome durability by the soak
// monitor's success_archived check.
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
		case name == "dexaflow_http_requests_total" && !probePaths[label(rest, "path")]:
			avail.All += v
			if !serverError.MatchString(label(rest, "status")) {
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
	rest, _, _ = strings.Cut(rest, "|")
	for _, part := range strings.Split(strings.TrimSuffix(rest, "}"), ",") {
		if k, v, ok := strings.Cut(part, "="); ok && k == name {
			return v
		}
	}
	return ""
}
