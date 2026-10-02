package main

import (
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSplitMetricParsesTheExpositionFormat pins the hand-rolled scraper. The
// monitor parses three counters with a line scanner instead of pulling
// prometheus/common into the module's direct requirements, so the parsing is
// ours and has to be tested like anything else we wrote.
func TestSplitMetricParsesTheExpositionFormat(t *testing.T) {
	cases := []struct {
		line     string
		wantName string
		wantVal  float64
		wantOK   bool
	}{
		{"leoflow_scheduler_step_downs_total 3", "leoflow_scheduler_step_downs_total", 3, true},
		{`leoflow_scheduler_step_downs_total{reason="lost_lock"} 2`, "leoflow_scheduler_step_downs_total", 2, true},
		{`leoflow_tasks_undispatchable_total{reason="no executor",dag="x"} 1.5`, "leoflow_tasks_undispatchable_total", 1.5, true},
		{"leoflow_dispatch_queue_depth 0", "leoflow_dispatch_queue_depth", 0, true},
		{"# HELP leoflow_x help text", "", 0, false},
		{"nonsense", "", 0, false},
		{"leoflow_x not_a_number", "", 0, false},
		{"", "", 0, false},
	}
	for _, tc := range cases {
		name, val, ok := splitMetric(tc.line)
		if ok != tc.wantOK || name != tc.wantName || val != tc.wantVal {
			t.Errorf("splitMetric(%q) = (%q, %v, %v), want (%q, %v, %v)",
				tc.line, name, val, ok, tc.wantName, tc.wantVal, tc.wantOK)
		}
	}
}

// TestPercentilesUseNearestRank pins the quantile convention, which has to match
// test/load/scheduler_ceiling so the two harnesses' numbers can be compared.
func TestPercentilesUseNearestRank(t *testing.T) {
	p50, p95, p99, mx := pcts([]float64{5, 1, 4, 2, 3})
	if p50 != 3 {
		t.Errorf("p50 = %v, want 3", p50)
	}
	if p95 != 5 || p99 != 5 || mx != 5 {
		t.Errorf("p95/p99/max = %v/%v/%v, want 5/5/5", p95, p99, mx)
	}

	// An empty window must read as zero, not panic: the first samples of a soak
	// have no completed dispatch yet.
	if a, b, c, d := pcts(nil); a != 0 || b != 0 || c != 0 || d != 0 {
		t.Errorf("empty input = %v/%v/%v/%v, want zeros", a, b, c, d)
	}

	// The input must not be reordered: the caller reuses its slice for the
	// per-operator split.
	in := []float64{3, 1, 2}
	pcts(in)
	if in[0] != 3 || in[1] != 1 || in[2] != 2 {
		t.Errorf("pcts sorted its caller's slice: %v", in)
	}
}

// TestDirBytesSumsRegularFilesAndToleratesAMissingTree pins the disk-budget
// reader. It runs against a tree the DAGs are writing into, so a vanished entry
// has to be a skipped entry and never a failure.
func TestDirBytesSumsRegularFilesAndToleratesAMissingTree(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "scratch"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.bin"), make([]byte, 100), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scratch", "b.bin"), make([]byte, 250), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := dirBytes(root); got != 350 {
		t.Errorf("dirBytes = %d, want 350", got)
	}
	if got := dirBytes(filepath.Join(root, "does-not-exist")); got != 0 {
		t.Errorf("dirBytes of a missing tree = %d, want 0", got)
	}
}

// TestHumanBytesRendersTheSummaryColumn pins the one place a reader looks first,
// including the unknown case, which must not render as a number.
func TestHumanBytesRendersTheSummaryColumn(t *testing.T) {
	cases := map[int64]string{
		-1:              "unknown",
		0:               "0.0 B",
		1023:            "1023.0 B",
		1024:            "1.0 KiB",
		1024 * 1024:     "1.0 MiB",
		3 * 1024 * 1024: "3.0 MiB",
	}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

// TestFirstEnvAndEnvOrResolveConfiguration pins the flag fallbacks, which are the
// difference between a soak pointed at its own database and one pointed at the
// developer's.
func TestFirstEnvAndEnvOrResolveConfiguration(t *testing.T) {
	t.Setenv("SOAK_TEST_A", "")
	t.Setenv("SOAK_TEST_B", "second")
	if got := firstEnv("SOAK_TEST_A", "SOAK_TEST_B"); got != "second" {
		t.Errorf("firstEnv skipped an empty value incorrectly: %q", got)
	}
	if got := firstEnv("SOAK_TEST_MISSING"); got != "" {
		t.Errorf("firstEnv of a missing key = %q, want empty", got)
	}
	if got := envOr("SOAK_TEST_MISSING", "fallback"); got != "fallback" {
		t.Errorf("envOr = %q, want the fallback", got)
	}
	if got := envOr("SOAK_TEST_B", "fallback"); got != "second" {
		t.Errorf("envOr overrode a set value: %q", got)
	}
}

// TestCountersReportNaNWhenUnreachable pins that a failed scrape is distinguished
// from a genuine zero. Reading an unreachable /metrics as zero would make the
// leader-churn and undispatchable checks silently unfalsifiable.
func TestCountersReportNaNWhenUnreachable(t *testing.T) {
	c := &collector{
		o:    options{metricsURL: "http://127.0.0.1:1"},
		http: &http.Client{Timeout: time.Second},
	}
	a, b, d := c.counters(t.Context())
	if !math.IsNaN(float64(a)) || !math.IsNaN(float64(b)) || !math.IsNaN(float64(d)) {
		t.Errorf("counters against an unreachable endpoint = %v/%v/%v, want NaN", a, b, d)
	}
}

// TestCountersRejectANon200Scrape pins the difference between "the counter is
// zero" and "the endpoint is not there". Lite serves /metrics on its own
// listener (internal/cli/dev.go devMetricsPort, --port + 1010), never on the API
// port, so a monitor pointed at the API port gets a 404 whose body parses to no
// samples at all. Reading that as zero makes leader_churn and undispatchable_task
// permanently unfalsifiable, which is the exact defect this battery exists to
// avoid.
func TestCountersRejectANon200Scrape(t *testing.T) {
	for _, code := range []int{http.StatusNotFound, http.StatusUnauthorized, http.StatusServiceUnavailable} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
			_, _ = w.Write([]byte("404 page not found\n"))
		}))
		c := &collector{o: options{metricsURL: srv.URL}, http: srv.Client()}
		a, b, d := c.counters(t.Context())
		srv.Close()
		if !math.IsNaN(float64(a)) || !math.IsNaN(float64(b)) || !math.IsNaN(float64(d)) {
			t.Errorf("counters against a %d response = %v/%v/%v, want NaN", code, a, b, d)
		}
	}
}

// TestCountersReadAServedExpositionPage is the other half: a real scrape is read,
// summed across labels, and absent families are a genuine zero.
func TestCountersReadAServedExpositionPage(t *testing.T) {
	const body = `# HELP leoflow_scheduler_step_downs_total Leader step-downs.
# TYPE leoflow_scheduler_step_downs_total counter
leoflow_scheduler_step_downs_total{reason="lost_lock"} 2
leoflow_scheduler_step_downs_total{reason="shutdown"} 1
go_goroutines 42
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c := &collector{o: options{metricsURL: srv.URL}, http: srv.Client()}
	steps, undisp, atCap := c.counters(t.Context())
	if steps != 3 {
		t.Errorf("step downs = %v, want 3 (summed across labels)", steps)
	}
	// An unincremented CounterVec exports no series at all, so absent is zero and
	// must not be NaN: the scrape itself succeeded.
	if undisp != 0 || atCap != 0 {
		t.Errorf("absent families = %v/%v, want 0/0 on a successful scrape", undisp, atCap)
	}
}

// TestMetricsEndpointIsValidatedAtStartup pins the guard that would have caught
// the wrong-port scrape before a weekend of samples was written: the monitor
// refuses to start against an endpoint that does not serve an exposition page.
func TestMetricsEndpointIsValidatedAtStartup(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("leoflow_build_info{version=\"x\"} 1\n"))
	}))
	defer good.Close()
	if err := checkMetricsEndpoint(t.Context(), good.Client(), good.URL); err != nil {
		t.Errorf("a served exposition page was rejected: %v", err)
	}

	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer empty.Close()
	if err := checkMetricsEndpoint(t.Context(), empty.Client(), empty.URL); err == nil {
		t.Error("a 404 was accepted as a metrics endpoint")
	}

	// A 200 that carries no leoflow_ family is the other shape of the same
	// mistake: some other service answering on the port we guessed.
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>hello</html>"))
	}))
	defer foreign.Close()
	if err := checkMetricsEndpoint(t.Context(), foreign.Client(), foreign.URL); err == nil {
		t.Error("a page with no leoflow metric family was accepted")
	}
}

// TestDeriveLiteMetricsURLAppliesLitesOwnOffset pins the derivation against the
// constant it mirrors (internal/cli/dev.go devMetricsPortOffset = 1010). The
// default soak API port 18700 must resolve to 19710 and never to 18700.
func TestDeriveLiteMetricsURLAppliesLitesOwnOffset(t *testing.T) {
	got, err := deriveLiteMetricsURL("http://127.0.0.1:18700")
	if err != nil {
		t.Fatalf("deriving from the default API URL: %v", err)
	}
	if got != "http://127.0.0.1:19710" {
		t.Errorf("derived %q, want http://127.0.0.1:19710", got)
	}
	if _, err := deriveLiteMetricsURL("http://127.0.0.1"); err == nil {
		t.Error("a URL with no port was accepted; the offset cannot be applied to it")
	}
}

// TestDataDirsAreSummedAcrossEveryTreeTheSoakWritesTo pins the disk budget
// against every tree a weekend actually fills. The DuckDB scratch is not the
// only one: Lite writes its task logs under the private soak HOME
// (LEOFLOW_LOGS_DIR, internal/cli/dev.go sharedServerEnv) and the harness writes
// its evidence under --out. A budget that sizes one of the three is not a budget.
func TestDataDirsAreSummedAcrossEveryTreeTheSoakWritesTo(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(a, "scratch.bin"), make([]byte, 100), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b, "task.log"), make([]byte, 250), 0o600); err != nil {
		t.Fatal(err)
	}
	dirs := splitDirs(a + "," + b + ",,  ")
	if len(dirs) != 2 {
		t.Fatalf("splitDirs returned %v, want the two non-empty entries", dirs)
	}
	if got := dirsBytes(dirs); got != 350 {
		t.Errorf("dirsBytes = %d, want 350", got)
	}
	if got := dirsBytes(nil); got != 0 {
		t.Errorf("dirsBytes of no directories = %d, want 0", got)
	}
}

// A control plane since the rename publishes every family under both names.
// The monitor must count it once, from the dexaflow_ series.
func TestCountersDoNotDoubleCountTheLegacyTwins(t *testing.T) {
	const body = `dexaflow_scheduler_step_downs_total{reason="lost_lock"} 2
dexaflow_tasks_undispatchable_total 4
leoflow_scheduler_step_downs_total{reason="lost_lock"} 2
leoflow_tasks_undispatchable_total 4
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c := &collector{o: options{metricsURL: srv.URL}, http: srv.Client()}
	steps, undisp, atCap := c.counters(t.Context())
	if steps != 2 || undisp != 4 || atCap != 0 {
		t.Errorf("counters = %v/%v/%v, want 2/4/0 (each family once)", steps, undisp, atCap)
	}
}
