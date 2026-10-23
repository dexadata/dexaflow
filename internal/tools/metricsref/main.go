// Command metricsref regenerates the table of the metrics reference page
// (website/content/reference/metrics.md) from the collectors the server
// registers, so the page lists every metric family it exports.
//
// The page stays hand-written prose around one generated block, the text
// between
//
//	<!-- BEGIN GENERATED metricsref -->
//	<!-- END GENERATED metricsref -->
//
// whose body is replaced on every run. Names, help text and labels come from
// each collector's descriptors; the type from the collector itself, or, for a
// custom collector, from the name (a _total suffix is a counter).
//
// Usage, from the repository root:
//
//	go run ./internal/tools/metricsref          # rewrite the page
//	go run ./internal/tools/metricsref -check   # fail when the page is stale
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/dexadata/dexaflow/internal/observability"
	"github.com/dexadata/dexaflow/internal/storage"
)

// pagePath is the reference page, relative to the repository root.
const pagePath = "website/content/reference/metrics.md"

const (
	beginMarker = "<!-- BEGIN GENERATED metricsref -->"
	endMarker   = "<!-- END GENERATED metricsref -->"
)

func main() {
	root := flag.String("root", ".", "repository root")
	check := flag.Bool("check", false, "fail instead of writing when the page is stale")
	flag.Parse()
	if err := run(*root, *check); err != nil {
		fmt.Fprintln(os.Stderr, "metricsref:", err)
		os.Exit(1)
	}
}

// run regenerates the page under root, or with check reports a stale page
// without writing it.
func run(root string, check bool) error {
	path := filepath.Join(root, pagePath)
	page, err := os.ReadFile(path) //nolint:gosec // path is the fixed reference page under the repository root
	if err != nil {
		return fmt.Errorf("reading the page: %w", err)
	}
	fams, err := families()
	if err != nil {
		return err
	}
	want, err := render(page, fams)
	if err != nil {
		return err
	}
	if bytes.Equal(want, page) {
		return nil
	}
	if check {
		return fmt.Errorf("%s is stale: run `make gen-metrics-reference` and commit the result", pagePath)
	}
	if werr := os.WriteFile(path, want, 0o644); werr != nil { //nolint:gosec // a docs page, world-readable like the rest of the tree
		return fmt.Errorf("writing the page: %w", werr)
	}
	return nil
}

// family is one exported metric family.
type family struct {
	name, kind, help string
	labels           []string
}

// recorder is a prometheus.Registerer that keeps what it is given.
type recorder struct {
	collectors []prometheus.Collector
}

// Register records c.
func (r *recorder) Register(c prometheus.Collector) error {
	r.collectors = append(r.collectors, c)
	return nil
}

// MustRegister records cs.
func (r *recorder) MustRegister(cs ...prometheus.Collector) {
	r.collectors = append(r.collectors, cs...)
}

// Unregister is never called by the constructors it records.
func (r *recorder) Unregister(prometheus.Collector) bool { return false }

// families returns every metric family the server registers, sorted by name:
// the observability metrics and the database pool collector.
func families() ([]family, error) {
	rec := &recorder{}
	observability.NewMetrics(rec)
	rec.MustRegister(storage.NewPoolCollector(nil))
	seen := map[string]bool{}
	var out []family
	for _, c := range rec.collectors {
		kind := collectorKind(c)
		for _, d := range describe(c) {
			f, err := parseDesc(d)
			if err != nil {
				return nil, err
			}
			if seen[f.name] {
				return nil, fmt.Errorf("metric %s is registered twice", f.name)
			}
			seen[f.name] = true
			f.kind = kind
			if f.kind == "" {
				f.kind = kindFromName(f.name)
			}
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

// describe returns the descriptors c announces.
func describe(c prometheus.Collector) []*prometheus.Desc {
	ch := make(chan *prometheus.Desc)
	go func() {
		c.Describe(ch)
		close(ch)
	}()
	var ds []*prometheus.Desc
	for d := range ch {
		ds = append(ds, d)
	}
	return ds
}

// collectorKind names the Prometheus type of c, or "" for a custom collector
// whose type it cannot tell without data.
func collectorKind(c prometheus.Collector) string {
	switch c.(type) {
	case *prometheus.CounterVec:
		return "counter"
	case *prometheus.GaugeVec:
		return "gauge"
	case *prometheus.HistogramVec:
		return "histogram"
	case *prometheus.SummaryVec:
		return "summary"
	}
	m, ok := c.(prometheus.Metric)
	if !ok {
		return ""
	}
	var pb dto.Metric
	if err := m.Write(&pb); err != nil {
		return ""
	}
	switch {
	case pb.Counter != nil:
		return "counter"
	case pb.Gauge != nil:
		return "gauge"
	case pb.Histogram != nil:
		return "histogram"
	case pb.Summary != nil:
		return "summary"
	}
	return ""
}

// kindFromName follows the Prometheus naming convention: a counter ends in
// _total, anything else a custom collector exports is a gauge.
func kindFromName(name string) string {
	if strings.HasSuffix(name, "_total") {
		return "counter"
	}
	return "gauge"
}

var (
	descName   = regexp.MustCompile(`fqName: "([^"]*)"`)
	descHelp   = regexp.MustCompile(`help: "((?:[^"\\]|\\.)*)"`)
	descLabels = regexp.MustCompile(`variableLabels: \{([^}]*)\}`)
)

// parseDesc reads a descriptor's name, help and variable labels from its
// String form, the only public view of them client_golang offers.
func parseDesc(d *prometheus.Desc) (family, error) {
	s := d.String()
	n := descName.FindStringSubmatch(s)
	h := descHelp.FindStringSubmatch(s)
	if n == nil || h == nil {
		return family{}, fmt.Errorf("cannot read descriptor %s", s)
	}
	f := family{name: n[1], help: strings.ReplaceAll(h[1], `\"`, `"`)}
	if l := descLabels.FindStringSubmatch(s); len(l) == 2 && strings.TrimSpace(l[1]) != "" {
		for _, label := range strings.Split(l[1], ",") {
			f.labels = append(f.labels, strings.TrimSpace(label))
		}
	}
	return f, nil
}

// render returns page with its generated block replaced by the table of fams.
func render(page []byte, fams []family) ([]byte, error) {
	s := string(page)
	b := strings.Index(s, beginMarker)
	e := strings.Index(s, endMarker)
	if b < 0 || e < b {
		return nil, errors.New("the page has no generated block (" + beginMarker + " ... " + endMarker + ")")
	}
	var t strings.Builder
	t.WriteString(beginMarker + "\n")
	t.WriteString("| Metric | Type | Labels | Meaning |\n|---|---|---|---|\n")
	for _, f := range fams {
		labels := "_(none)_"
		if len(f.labels) > 0 {
			labels = "`" + strings.Join(f.labels, "`, `") + "`"
		}
		fmt.Fprintf(&t, "| `%s` | %s | %s | %s |\n", f.name, f.kind, labels, cell(f.help))
	}
	return []byte(s[:b] + t.String() + s[e:]), nil
}

// cell makes help text safe inside a Markdown table cell.
func cell(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "|", `\|`), "\n", " ")
}
