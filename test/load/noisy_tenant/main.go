// Command noisy_tenant is Load Experiment 10: does one tenant at its limits
// move another tenant's schedule (#2313)?
//
// A quiet tenant runs cron DAGs that fire every minute. The harness first
// records a baseline window with nothing else running, then triggers a wide
// fan-out in a noisy tenant (the noisy_fanout DAG, 5,000 tasks per run, as
// many runs as --noisy-runs) and records a second window while it runs. For
// every scheduled run of the quiet tenant whose slot falls in a window it
// reads two delays from the API: creation (queued_at - run_after), what the
// dexaflow_scheduler_schedule_delay_seconds histogram measures without a
// tenant label, and start (start_date - run_after), which also includes
// dispatch. The verdict compares the p99 of each delay between the windows:
// the noisy window may add at most --max-shift.
//
// It boots nothing: point it at a running control plane where both tenants
// exist, each with an API token, and the DAGs under dags/ are deployed (see
// test/load/README.md, Experiment 10). Usage:
//
//	QUIET_TOKEN=... NOISY_TOKEN=... go run ./test/load/noisy_tenant \
//	  --url https://dexaflow.example --quiet-dags quiet_cron_1,quiet_cron_2 \
//	  --noisy-dag noisy_fanout --noisy-runs 1 --baseline 15m --noise 30m
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
	"os/signal"
	"slices"
	"strings"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "noisy_tenant:", err)
		os.Exit(1)
	}
}

type options struct {
	url        string
	quietToken string
	noisyToken string
	quietDags  []string
	noisyDag   string
	noisyRuns  int
	baseline   time.Duration
	noise      time.Duration
	settle     time.Duration
	maxShift   time.Duration
}

func parseFlags(args []string) (options, error) {
	fs := flag.NewFlagSet("noisy_tenant", flag.ContinueOnError)
	var o options
	var quiet string
	fs.StringVar(&o.url, "url", os.Getenv("DEXAFLOW_URL"), "control plane base URL")
	fs.StringVar(&quiet, "quiet-dags", "quiet_cron", "comma separated cron DAG ids of the quiet tenant")
	fs.StringVar(&o.noisyDag, "noisy-dag", "noisy_fanout", "the fan-out DAG id of the noisy tenant")
	fs.IntVar(&o.noisyRuns, "noisy-runs", 1, "runs of the noisy DAG to trigger at the start of the noise window")
	fs.DurationVar(&o.baseline, "baseline", 15*time.Minute, "length of the window with nothing else running")
	fs.DurationVar(&o.noise, "noise", 30*time.Minute, "length of the window with the noisy tenant running")
	fs.DurationVar(&o.settle, "settle", 2*time.Minute, "wait after each window before its runs are read, so the last slots have started")
	fs.DurationVar(&o.maxShift, "max-shift", 2*time.Second, "most the noise window's p99 may add to the baseline's")
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	o.quietToken, o.noisyToken = os.Getenv("QUIET_TOKEN"), os.Getenv("NOISY_TOKEN")
	switch {
	case o.url == "":
		return options{}, errors.New("--url (or DEXAFLOW_URL) is required")
	case o.quietToken == "" || o.noisyToken == "":
		return options{}, errors.New("QUIET_TOKEN and NOISY_TOKEN must hold an API token of each tenant")
	case o.noisyRuns < 0:
		return options{}, errors.New("--noisy-runs must not be negative")
	}
	for _, d := range strings.Split(quiet, ",") {
		if d = strings.TrimSpace(d); d != "" {
			o.quietDags = append(o.quietDags, d)
		}
	}
	if len(o.quietDags) == 0 {
		return options{}, errors.New("--quiet-dags names no DAG")
	}
	return o, nil
}

func run() error {
	o, err := parseFlags(os.Args[1:])
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	quiet := &client{base: strings.TrimRight(o.url, "/"), token: o.quietToken, http: &http.Client{Timeout: 30 * time.Second}}
	noisy := &client{base: quiet.base, token: o.noisyToken, http: quiet.http}

	baseFrom := time.Now().UTC()
	fmt.Printf("baseline window: %s from %s\n", o.baseline, baseFrom.Format(time.RFC3339))
	if err := sleep(ctx, o.baseline); err != nil {
		return err
	}
	noiseFrom := time.Now().UTC()
	for i := range o.noisyRuns {
		id := fmt.Sprintf("noisy_%d_%d", noiseFrom.Unix(), i)
		if err := noisy.trigger(ctx, o.noisyDag, id); err != nil {
			return fmt.Errorf("triggering %s: %w", o.noisyDag, err)
		}
	}
	fmt.Printf("noise window: %s from %s, %d run(s) of %s triggered\n",
		o.noise, noiseFrom.Format(time.RFC3339), o.noisyRuns, o.noisyDag)
	if err := sleep(ctx, o.noise+o.settle); err != nil {
		return err
	}
	noiseTo := noiseFrom.Add(o.noise)

	var base, noise []dagRun
	for _, dag := range o.quietDags {
		runs, err := quiet.scheduledRuns(ctx, dag)
		if err != nil {
			return fmt.Errorf("listing runs of %s: %w", dag, err)
		}
		base = append(base, inWindow(runs, baseFrom, noiseFrom)...)
		noise = append(noise, inWindow(runs, noiseFrom, noiseTo)...)
	}
	rows := []row{summarize("baseline", base), summarize("noise", noise)}
	v := judge(rows[0], rows[1], o.maxShift)
	fmt.Print(table(rows, v, o.maxShift))
	if !v.pass {
		return errors.New(v.reason)
	}
	return nil
}

// sleep waits d or until ctx ends.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// dagRun is the part of a DAG run the experiment reads.
type dagRun struct {
	RunAfter  time.Time  `json:"run_after"`
	QueuedAt  time.Time  `json:"queued_at"`
	StartDate *time.Time `json:"start_date"`
	RunType   string     `json:"run_type"`
}

// inWindow keeps the scheduled runs whose slot is in [from, to).
func inWindow(runs []dagRun, from, to time.Time) []dagRun {
	var out []dagRun
	for _, r := range runs {
		if r.RunType == "scheduled" && !r.RunAfter.Before(from) && r.RunAfter.Before(to) {
			out = append(out, r)
		}
	}
	return out
}

// row is one window's delays, in seconds.
type row struct {
	name              string
	runs              int
	notStarted        int
	createP50, create float64 // p50 and p99 of queued_at - run_after
	startP50, start   float64 // p50 and p99 of start_date - run_after
}

func summarize(name string, runs []dagRun) row {
	r := row{name: name, runs: len(runs)}
	var create, start []float64
	for _, x := range runs {
		create = append(create, x.QueuedAt.Sub(x.RunAfter).Seconds())
		if x.StartDate == nil {
			r.notStarted++
			continue
		}
		start = append(start, x.StartDate.Sub(x.RunAfter).Seconds())
	}
	slices.Sort(create)
	slices.Sort(start)
	r.createP50, r.create = percentile(create, 0.50), percentile(create, 0.99)
	r.startP50, r.start = percentile(start, 0.50), percentile(start, 0.99)
	return r
}

// percentile is the nearest-rank percentile of sorted values.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(p*float64(len(sorted)) + 0.999999)
	return sorted[min(max(i, 1), len(sorted))-1]
}

type verdict struct {
	pass   bool
	reason string
}

// judge passes when each window has runs, every noise run started, and the
// noise window's p99 of both delays is within maxShift of the baseline's.
func judge(base, noise row, maxShift time.Duration) verdict {
	limit := maxShift.Seconds()
	switch {
	case base.runs == 0 || noise.runs == 0:
		return verdict{reason: "a window holds no scheduled run of the quiet tenant: are the quiet DAGs unpaused and every minute?"}
	case noise.notStarted > 0:
		return verdict{reason: fmt.Sprintf("%d scheduled run(s) of the quiet tenant never started during the noise", noise.notStarted)}
	case noise.create-base.create > limit:
		return verdict{reason: fmt.Sprintf("creation delay p99 moved %.2fs, more than %s", noise.create-base.create, maxShift)}
	case noise.start-base.start > limit:
		return verdict{reason: fmt.Sprintf("start delay p99 moved %.2fs, more than %s", noise.start-base.start, maxShift)}
	}
	return verdict{pass: true, reason: "the quiet tenant's schedule did not move"}
}

// table renders the windows and the verdict.
func table(rows []row, v verdict, maxShift time.Duration) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n%-9s %5s %11s %12s %12s %11s %11s\n", "window", "runs", "not started", "create p50", "create p99", "start p50", "start p99")
	for _, r := range rows {
		fmt.Fprintf(&b, "%-9s %5d %11d %11.2fs %11.2fs %10.2fs %10.2fs\n",
			r.name, r.runs, r.notStarted, r.createP50, r.create, r.startP50, r.start)
	}
	result := "FAIL"
	if v.pass {
		result = "PASS"
	}
	fmt.Fprintf(&b, "\n%s (max shift %s): %s\n", result, maxShift, v.reason)
	return b.String()
}

// client is one tenant's view of the API, by its token.
type client struct {
	base  string
	token string
	http  *http.Client
}

func (c *client) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck // read-only body
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, bytes.TrimSpace(out))
	}
	return out, nil
}

// trigger creates a manual run of dag with the given run id.
func (c *client) trigger(ctx context.Context, dag, runID string) error {
	_, err := c.do(ctx, http.MethodPost, "/api/v2/dags/"+url.PathEscape(dag)+"/dagRuns",
		map[string]any{"dag_run_id": runID, "conf": map[string]any{}})
	return err
}

// scheduledRuns lists every scheduled run of dag, page by page.
func (c *client) scheduledRuns(ctx context.Context, dag string) ([]dagRun, error) {
	const page = 500
	var all []dagRun
	for offset := 0; ; offset += page {
		q := url.Values{"run_type": {"scheduled"}, "limit": {fmt.Sprint(page)}, "offset": {fmt.Sprint(offset)}}
		body, err := c.do(ctx, http.MethodGet, "/api/v2/dags/"+url.PathEscape(dag)+"/dagRuns?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		var res struct {
			DagRuns      []dagRun `json:"dag_runs"`
			TotalEntries int      `json:"total_entries"`
		}
		if err := json.Unmarshal(body, &res); err != nil {
			return nil, fmt.Errorf("decoding runs: %w", err)
		}
		all = append(all, res.DagRuns...)
		if len(res.DagRuns) < page || len(all) >= res.TotalEntries {
			return all, nil
		}
	}
}
