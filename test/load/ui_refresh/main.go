// Command ui_refresh is Load Experiment 4: what one auto-refresh cycle of the
// UI costs the control plane, and so how many viewers an interval allows
// (#1196).
//
// The embedded UI polls: on a page whose data holds an active run, it
// refetches a fixed set of queries every ui.autoRefreshIntervalSeconds. The
// harness replays that set (a profile, see profiles.go) against a running
// control plane, as V viewers that each run cycles back to back, and measures
// the cycles per second the control plane sustains and the latency of one
// cycle. With --db it also reads the database transactions one cycle costs.
// A viewer at interval I issues one cycle every I seconds, so the sustained
// rate times I is the number of viewers one control plane serves at I.
//
// It boots nothing: point it at a running api or all role process and a
// DAG of that installation. Usage (see test/load/README.md, Experiment 4):
//
//	DEXAFLOW_PASS=admin go run ./test/load/ui_refresh \
//	  --url http://localhost:8080 --dag my_dag --viewers 1,10,50
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "ui_refresh: %v\n", err)
		os.Exit(1)
	}
}

type options struct {
	baseURL, user, pass, dag, run, profile, dbURL string
	viewers, intervals                            []int
	window                                        time.Duration
	parallel                                      int
}

func parseFlags() (options, error) {
	var o options
	var viewers, intervals string
	flag.StringVar(&o.baseURL, "url", firstEnv("DEXAFLOW_URL", "LEOFLOW_URL"), "control plane URL (falls back to $DEXAFLOW_URL, $LEOFLOW_URL)")
	flag.StringVar(&o.user, "user", "admin@dexaflow.local", "user to sign in as")
	flag.StringVar(&o.pass, "pass", firstEnv("DEXAFLOW_PASS", "LEOFLOW_PASS"), "password (falls back to $DEXAFLOW_PASS, $LEOFLOW_PASS)")
	flag.StringVar(&o.dag, "dag", "", "DAG whose pages are replayed (required)")
	flag.StringVar(&o.run, "run", "", "run of the run profile (default: the DAG's newest run)")
	flag.StringVar(&o.profile, "profile", "dag", "page to replay: "+strings.Join(profileNames(), ", "))
	flag.StringVar(&viewers, "viewers", "1,10,50", "comma-separated concurrent viewer counts")
	flag.StringVar(&intervals, "intervals", "1,5,10,30", "comma-separated refresh intervals, in seconds, for the capacity table")
	flag.DurationVar(&o.window, "window", 10*time.Second, "measured window per viewer count")
	flag.IntVar(&o.parallel, "parallel", 6, "requests one viewer has in flight at once (a browser opens 6 connections per host)")
	flag.StringVar(&o.dbURL, "db", firstEnv("DATABASE_URL", "DEXAFLOW_DATABASE_URL", "LEOFLOW_DATABASE_URL"), "the control plane's database, to count transactions per cycle (optional)")
	flag.Parse()
	var err error
	if o.viewers, err = parseInts(viewers); err != nil {
		return o, fmt.Errorf("--viewers: %w", err)
	}
	if o.intervals, err = parseInts(intervals); err != nil {
		return o, fmt.Errorf("--intervals: %w", err)
	}
	switch {
	case o.baseURL == "":
		return o, errors.New("no control plane: pass --url or set DEXAFLOW_URL")
	case o.dag == "":
		return o, errors.New("--dag is required")
	case o.window <= 0 || o.parallel <= 0:
		return o, errors.New("--window and --parallel must be positive")
	}
	o.baseURL = strings.TrimRight(o.baseURL, "/")
	return o, nil
}

func run() error {
	o, err := parseFlags()
	if err != nil {
		return err
	}
	ctx := context.Background()
	maxViewers := 0
	for _, v := range o.viewers {
		maxViewers = max(maxViewers, v)
	}
	c := &client{base: o.baseURL, http: &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{MaxIdleConnsPerHost: maxViewers * o.parallel, MaxConnsPerHost: maxViewers * o.parallel},
	}}
	if signErr := c.signIn(ctx, o.user, o.pass); signErr != nil {
		return signErr
	}
	t, err := c.target(ctx, o.dag, o.run)
	if err != nil {
		return err
	}
	reqs, err := profileRequests(o.profile, t)
	if err != nil {
		return err
	}
	for _, r := range reqs {
		if getErr := c.get(ctx, r); getErr != nil {
			return fmt.Errorf("profile request: %w", getErr)
		}
	}
	var pool *pgxpool.Pool
	if o.dbURL != "" {
		if pool, err = pgxpool.New(ctx, o.dbURL); err != nil {
			return fmt.Errorf("connect: %w", err)
		}
		defer pool.Close()
	}
	fmt.Printf("Profile `%s` on DAG `%s`: %d requests per cycle, %d in flight per viewer, %s per row.\n\n",
		o.profile, t.dag, len(reqs), o.parallel, o.window)
	var results []result
	for _, v := range o.viewers {
		r, err := measure(ctx, c, pool, reqs, v, o.parallel, o.window)
		if err != nil {
			return fmt.Errorf("%d viewers: %w", v, err)
		}
		results = append(results, r)
	}
	printResults(results, len(reqs), o.intervals)
	return nil
}

type result struct {
	viewers   int
	cycles    int
	elapsed   time.Duration
	cycleMs   []float64 // sorted
	dbXacts   float64   // per cycle; -1 when not measured
	errorRate float64
}

func (r result) cyclesPerSec() float64 { return float64(r.cycles) / r.elapsed.Seconds() }

// measure runs viewers concurrent viewers for window, each issuing one cycle
// after the other, and times every cycle.
func measure(ctx context.Context, c *client, pool *pgxpool.Pool, reqs []string, viewers, parallel int, window time.Duration) (result, error) {
	before, err := dbXacts(ctx, pool)
	if err != nil {
		return result{}, err
	}
	var (
		mu       sync.Mutex
		cycles   []float64
		failures atomic.Int64
		total    atomic.Int64
		wg       sync.WaitGroup
	)
	start := time.Now()
	deadline := start.Add(window)
	for range viewers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				t0 := time.Now()
				failed := cycle(ctx, c, reqs, parallel)
				total.Add(int64(len(reqs)))
				failures.Add(int64(failed))
				d := time.Since(t0)
				mu.Lock()
				cycles = append(cycles, float64(d.Microseconds())/1000)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)
	r := result{viewers: viewers, cycles: len(cycles), elapsed: elapsed, cycleMs: cycles, dbXacts: -1}
	sort.Float64s(r.cycleMs)
	if n := total.Load(); n > 0 {
		r.errorRate = float64(failures.Load()) / float64(n)
	}
	if pool != nil && r.cycles > 0 {
		// Postgres publishes a backend's counters when it goes idle, at most
		// a second late.
		time.Sleep(1500 * time.Millisecond)
		after, err := dbXacts(ctx, pool)
		if err != nil {
			return r, err
		}
		r.dbXacts = float64(after-before) / float64(r.cycles)
	}
	return r, nil
}

// cycle issues every request of one refresh, at most parallel at once, and
// returns how many failed.
func cycle(ctx context.Context, c *client, reqs []string, parallel int) int {
	sem := make(chan struct{}, parallel)
	var failed atomic.Int64
	var wg sync.WaitGroup
	for _, r := range reqs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if c.get(ctx, r) != nil {
				failed.Add(1)
			}
		}()
	}
	wg.Wait()
	return int(failed.Load())
}

// dbXacts is the database's committed and rolled back transactions so far,
// or 0 without a pool.
func dbXacts(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	if pool == nil {
		return 0, nil
	}
	var n int64
	err := pool.QueryRow(ctx, `SELECT xact_commit + xact_rollback FROM pg_stat_database WHERE datname = current_database()`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("read pg_stat_database: %w", err)
	}
	return n, nil
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(p * float64(len(sorted)-1))
	return sorted[i]
}

func printResults(results []result, perCycle int, intervals []int) {
	fmt.Println("| Viewers | Cycles/s | Requests/s | Cycle p50 ms | Cycle p95 ms | Cycle p99 ms | DB transactions per cycle | Errors |")
	fmt.Println("|---:|---:|---:|---:|---:|---:|---:|---:|")
	best := 0.0
	for _, r := range results {
		x := "n/a"
		if r.dbXacts >= 0 {
			x = fmt.Sprintf("%.1f", r.dbXacts)
		}
		fmt.Printf("| %d | %.1f | %.0f | %.1f | %.1f | %.1f | %s | %.2f%% |\n",
			r.viewers, r.cyclesPerSec(), r.cyclesPerSec()*float64(perCycle),
			percentile(r.cycleMs, 0.5), percentile(r.cycleMs, 0.95), percentile(r.cycleMs, 0.99), x, 100*r.errorRate)
		best = max(best, r.cyclesPerSec())
	}
	fmt.Printf("\nThe highest sustained rate, %.1f cycles/s, as viewers on this page at each interval:\n\n", best)
	fmt.Println("| Interval s | Viewers |")
	fmt.Println("|---:|---:|")
	for _, i := range intervals {
		fmt.Printf("| %d | %.0f |\n", i, best*float64(i))
	}
}

type client struct {
	base  string
	http  *http.Client
	token string
}

func (c *client) signIn(ctx context.Context, user, pass string) error {
	body, _ := json.Marshal(map[string]string{"username": user, "password": pass}) //nolint:errcheck // strings always marshal
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/auth/token", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("sign in: %w", err)
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // a read body's close error says nothing
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("sign in as %s: status %d", user, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return fmt.Errorf("sign in: %w", err)
	}
	if tok.AccessToken == "" {
		return errors.New("sign in: the response holds no access token")
	}
	c.token = tok.AccessToken
	return nil
}

func (c *client) do(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	return c.http.Do(req)
}

// get issues one request and reads its whole body, as the browser does.
func (c *client) get(ctx context.Context, path string) error {
	resp, err := c.do(ctx, path)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // a read body's close error says nothing
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", path, resp.StatusCode)
	}
	return nil
}

// target reads the DAG's ten newest runs, as the grid shows them.
func (c *client) target(ctx context.Context, dag, run string) (target, error) {
	resp, err := c.do(ctx, "/ui/grid/runs/"+url.PathEscape(dag)+"?limit=10&order_by=-run_after")
	if err != nil {
		return target{}, err
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // a read body's close error says nothing
	if resp.StatusCode != http.StatusOK {
		return target{}, fmt.Errorf("DAG %s: grid runs status %d", dag, resp.StatusCode)
	}
	var runs []struct {
		RunID string `json:"run_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&runs); err != nil {
		return target{}, fmt.Errorf("DAG %s: grid runs: %w", dag, err)
	}
	if len(runs) == 0 {
		return target{}, fmt.Errorf("DAG %s has no runs", dag)
	}
	t := target{dag: dag, run: run}
	for _, r := range runs {
		t.runIDs = append(t.runIDs, r.RunID)
	}
	if t.run == "" {
		t.run = t.runIDs[0]
	}
	return t, nil
}

func parseInts(s string) ([]int, error) {
	var out []int
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f == "" {
			continue
		}
		n, err := strconv.Atoi(f)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("%q is not a positive integer", f)
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, errors.New("empty list")
	}
	return out, nil
}

func firstEnv(names ...string) string {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v
		}
	}
	return ""
}
