// Command monitor is the soak battery's continuous invariant checker.
//
// It is the answer to "a soak that only reports it did not crash is worthless".
// The workload (test/soak/dags) generates load; this program decides, every
// sample, whether the control plane is still behaving, and writes the evidence
// down as it goes rather than at the end, because the interesting failure is the
// one that kills the harness.
//
// It follows the test/load convention: a small `package main` driving the REAL
// control-plane code (storage.SchedulerStore) against a REAL Postgres. Unlike
// the load experiments it does not seed anything and it never writes: every
// query it issues is a SELECT, and the one product call it makes,
// SchedulerStore.ActiveRuns, is the read half of a scheduler tick and mutates
// nothing. A monitor that could perturb the system it measures would be worse
// than no monitor.
//
// Outputs, all under --out, all written continuously:
//
//	samples.jsonl     one JSON object per sample (machine-readable)
//	violations.jsonl  one JSON object per invariant violation, with evidence
//	summary.md        a human summary, rewritten atomically every sample
//	verdict.json      the final verdict (also written every sample, so a killed
//	                  harness still leaves a current one behind)
//
// Exit codes: 0 = every invariant held; 1 = at least one violation; 2 = the
// monitor itself could not run (bad flags, unreachable database).
//
// Usage (see test/soak/README.md for the full runbook):
//
//	go run ./test/soak/monitor \
//	  --db "$SOAK_DATABASE_URL" --api http://127.0.0.1:18700 \
//	  --out ./soak-out --duration 30m
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	code, err := run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "soak monitor: %v\n", err)
	}
	os.Exit(code)
}

// options is the whole configuration surface, kept in one struct so the design
// document and the flag list cannot drift apart.
type options struct {
	dbURL      string
	apiURL     string
	metricsURL string
	outDir     string
	duration   time.Duration
	interval   time.Duration

	latencyWindow time.Duration

	queuedWedge    time.Duration
	scheduledWedge time.Duration
	runWedge       time.Duration
	recoveryBudget time.Duration
	healthTolerate time.Duration

	maxDBBytes   int64
	maxDataBytes int64
	minFreeBytes int64
	dataDir      string

	faultsFile string
	label      string
}

// stopReasonFor says why the loop must stop after sample s, or "" to go on.
func stopReasonFor(ck *checker, s sample, rep *reporter) string {
	// The subject left. Keeping the loop alive would spend the remaining
	// hours describing an absence, which is what turned one aborted weekend
	// into 2332 meaningless violations.
	if ck.Gone() {
		return "control_plane_gone"
	}
	// The datastore left (#1257): stop instead of sampling into the void for
	// the rest of the run, and say when it happened.
	if ck.DatastoreGone() {
		rep.MarkAborted(fmt.Sprintf("datastore unreachable at %.1fh", ck.DatastoreLostAtS()/3600))
		return "datastore_unreachable"
	}
	return ck.stopCondition(s)
}

func run() (int, error) {
	o := options{}
	flag.StringVar(&o.dbURL, "db", firstEnv("SOAK_DATABASE_URL", "DATABASE_URL", "LEOFLOW_DATABASE_URL"), "Postgres URL of the soak metadatabase")
	flag.StringVar(&o.apiURL, "api", envOr("SOAK_API_URL", "http://127.0.0.1:18700"), "base URL of the Lite control plane")
	flag.StringVar(&o.metricsURL, "metrics", os.Getenv("SOAK_METRICS_URL"), "base URL of the control plane's METRICS listener (empty derives it from --api with Lite's +1010 offset; it is never the API port)")
	flag.StringVar(&o.outDir, "out", "soak-out", "directory for samples.jsonl, violations.jsonl, summary.md and verdict.json")
	flag.DurationVar(&o.duration, "duration", 30*time.Minute, "wall-clock ceiling; the monitor stops itself at this point no matter what")
	flag.DurationVar(&o.interval, "interval", 10*time.Second, "sampling interval")
	flag.DurationVar(&o.latencyWindow, "latency-window", 10*time.Minute, "rolling window for the dispatch-latency percentiles")
	flag.DurationVar(&o.queuedWedge, "queued-wedge", 180*time.Second, "a task instance in `queued` older than this is a wedge (default matches the dispatch-lost threshold)")
	flag.DurationVar(&o.scheduledWedge, "scheduled-wedge", 300*time.Second, "a task instance in `scheduled` older than this is a wedge (generous: max_active_tasks legitimately holds ready siblings here)")
	// 90m, not 30m. soak_token's body runs for forty minutes by design, so a
	// thirty-minute threshold reported the battery's own longest DAG as wedged on
	// every sample it was alive: 1392 violations in one weekend run, describing a
	// task that was working exactly as written. The threshold has to clear the
	// longest DAG by a margin, or it measures the fixture rather than the system.
	flag.DurationVar(&o.runWedge, "run-wedge", 90*time.Minute, "a dag run in `running` older than this is a wedge")
	flag.DurationVar(&o.recoveryBudget, "recovery-budget", 2*time.Minute, "after a declared fault window closes, how long the control plane gets to be healthy and drained again")
	flag.DurationVar(&o.healthTolerate, "health-tolerance", 0, "grace applied to the scheduler-health check outside fault windows (0 = any unhealthy sample outside a window is a violation)")
	flag.Int64Var(&o.maxDBBytes, "max-db-bytes", 8<<30, "stop cleanly when the soak database exceeds this size")
	flag.Int64Var(&o.maxDataBytes, "max-data-bytes", 8<<30, "stop cleanly when --data-dir exceeds this size")
	flag.Int64Var(&o.minFreeBytes, "min-free-bytes", 10<<30, "stop cleanly when the filesystem holding --out drops below this much free space")
	flag.StringVar(&o.dataDir, "data-dir", "", "comma-separated list of directories the soak writes into (DuckDB scratch, Lite's task logs, the evidence dir); summed every sample, empty disables the check")
	flag.StringVar(&o.faultsFile, "faults", "", "JSONL file of declared fault windows written by the harness (empty = no faults expected)")
	flag.StringVar(&o.label, "label", "soak", "label recorded in every sample, e.g. warm-on / warm-off")
	flag.Parse()

	if err := o.validate(); err != nil {
		return 2, err
	}

	if err := os.MkdirAll(o.outDir, 0o750); err != nil {
		return 2, fmt.Errorf("creating --out: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	col, err := newCollector(ctx, o)
	if err != nil {
		return 2, err
	}
	defer col.Close()

	// Refuse to start with a check that cannot fire. leader_churn and
	// undispatchable_task are read only from the metrics listener, so an
	// unscrapable endpoint is not a degraded run, it is a silently weaker one.
	if merr := checkMetricsEndpoint(ctx, col.http, o.metricsURL); merr != nil {
		return 2, fmt.Errorf("the metrics listener must be scrapable or two invariants cannot fire: %w", merr)
	}

	rep, err := newReporter(o)
	if err != nil {
		return 2, err
	}
	defer rep.Close()

	ck := newChecker(o)

	started := time.Now()
	deadline := started.Add(o.duration)
	ticker := time.NewTicker(o.interval)
	defer ticker.Stop()

	fmt.Printf("soak monitor: label=%s interval=%s duration=%s out=%s\n", o.label, o.interval, o.duration, o.outDir)
	fmt.Printf("soak monitor: api=%s metrics=%s\n", o.apiURL, o.metricsURL)
	fmt.Printf("soak monitor: thresholds queued=%s scheduled=%s run=%s recovery=%s\n",
		o.queuedWedge, o.scheduledWedge, o.runWedge, o.recoveryBudget)

	stopReason := "duration_reached"
	for {
		s, serr := col.Sample(ctx, started)
		if serr != nil {
			// A sampling error is itself evidence (the database went away), not a
			// reason to stop: the whole point is to keep observing through an
			// outage. It becomes a violation only through the health check.
			s.SampleError = serr.Error()
		}
		windows := ck.loadFaultWindows()
		s.InFaultWindow = ck.inFaultWindow(s.At, windows)
		vs := ck.Check(s)
		s.Violations = len(vs)
		rep.WriteSample(s, vs)

		if r := stopReasonFor(ck, s, rep); r != "" {
			stopReason = r
			break
		}
		if !time.Now().Before(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			stopReason = "signal"
			goto done
		case <-ticker.C:
		}
	}
done:
	v := rep.Finish(stopReason, ck.Totals())
	fmt.Printf("\nsoak monitor: stop_reason=%s samples=%d violations=%d verdict=%s\n",
		stopReason, v.Samples, v.Violations, v.Verdict)
	fmt.Printf("soak monitor: evidence in %s\n", o.outDir)
	// A vanished control plane is a HARNESS failure (2), not a product one (1).
	// Reporting it as a failing soak would put a red verdict on the product for
	// something that happened to the experiment, and the operator reading it on
	// Monday would have no way to tell the two apart.
	if stopReason == "control_plane_gone" {
		fmt.Println("soak monitor: the control plane disappeared; this is a harness failure, not a product verdict")
		return 2, nil
	}
	if stopReason == "datastore_unreachable" {
		fmt.Println("soak monitor: the metadatabase stopped answering; the run is ABORTED, not a product verdict")
		return 2, nil
	}
	if v.Verdict != "PASS" {
		return 1, nil
	}
	return 0, nil
}

// validate rejects a configuration that would produce a run nobody can trust: no
// database, no ceiling, or a metrics URL that cannot be derived. It is a method
// so the rules live next to the struct they constrain and `run` stays readable.
func (o *options) validate() error {
	if o.dbURL == "" {
		return errors.New("no database URL: pass --db or set $SOAK_DATABASE_URL")
	}
	if o.interval <= 0 {
		return errors.New("--interval must be positive")
	}
	// A soak left unattended must have a ceiling. Refusing an unbounded run is
	// the cheapest guard against an experiment quietly becoming a resident.
	if o.duration <= 0 {
		return errors.New("--duration must be positive: an unattended run needs a wall-clock ceiling")
	}
	if o.duration > 72*time.Hour {
		return fmt.Errorf("--duration %s exceeds the 72h ceiling; run several bounded soaks instead", o.duration)
	}
	if o.metricsURL == "" {
		derived, err := deriveLiteMetricsURL(o.apiURL)
		if err != nil {
			return err
		}
		o.metricsURL = derived
	}
	return nil
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
