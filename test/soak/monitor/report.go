package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// verdict is the machine-readable bottom line. It is rewritten on every sample,
// not only at the end, so a harness that is killed mid-run still leaves a
// current verdict on disk. That is the whole point: the interesting failure is
// the one that takes the harness with it.
type verdict struct {
	Verdict      string         `json:"verdict"`
	Label        string         `json:"label"`
	StartedAt    time.Time      `json:"started_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	ElapsedS     float64        `json:"elapsed_s"`
	Samples      int            `json:"samples"`
	Violations   int            `json:"violations"`
	ByCheck      map[string]int `json:"violations_by_check"`
	StopReason   string         `json:"stop_reason"`
	StoppedEarly bool           `json:"stopped_early"`
	Complete     bool           `json:"complete"`
	Thresholds   map[string]any `json:"thresholds"`
	LastSample   *sample        `json:"last_sample,omitempty"`
	FirstSample  *sample        `json:"first_sample,omitempty"`
}

// reporter owns every output file. Appends are flushed and synced per write so
// a SIGKILL loses at most the record being written.
type reporter struct {
	o           options
	samplesF    *os.File
	violationsF *os.File
	started     time.Time
	n           int
	totalViol   int
	byCheck     map[string]int
	first       *sample
	last        *sample
	// trend keeps a compact history for the summary's shape table: one entry per
	// sample is fine for a 72h run at 10s (about 26k rows, a few MB in memory),
	// but the summary only prints bucketed aggregates.
	trend []trendPoint
}

type trendPoint struct {
	elapsedS   float64
	tickProbe  float64
	baseline   float64
	totalRuns  int64
	activeRuns int
	p95        float64
}

func newReporter(o options) (*reporter, error) {
	sf, err := os.OpenFile(filepath.Join(o.outDir, "samples.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, fmt.Errorf("opening samples.jsonl: %w", err)
	}
	vf, err := os.OpenFile(filepath.Join(o.outDir, "violations.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		_ = sf.Close() //nolint:errcheck // already returning an error
		return nil, fmt.Errorf("opening violations.jsonl: %w", err)
	}
	return &reporter{
		o: o, samplesF: sf, violationsF: vf,
		started: time.Now(), byCheck: map[string]int{},
	}, nil
}

// Close closes the append handles.
func (r *reporter) Close() {
	_ = r.samplesF.Close()    //nolint:errcheck // best-effort close; every record was already synced
	_ = r.violationsF.Close() //nolint:errcheck // best-effort close; every record was already synced
}

// WriteSample persists one sample plus any violations it produced, then
// refreshes summary.md and verdict.json.
func (r *reporter) WriteSample(s sample, vs []violation) {
	r.n++
	if r.first == nil {
		cp := s
		r.first = &cp
	}
	cp := s
	r.last = &cp
	r.trend = append(r.trend, trendPoint{
		elapsedS: s.ElapsedS, tickProbe: s.TickProbeMS, baseline: s.BaselineProbeMS,
		totalRuns: s.TotalRuns, activeRuns: s.ActiveRuns, p95: s.DispatchP95,
	})

	appendJSON(r.samplesF, s)
	for _, v := range vs {
		r.totalViol++
		r.byCheck[v.Check]++
		appendJSON(r.violationsF, v)
		fmt.Printf("  VIOLATION [%s] %s\n", v.Check, v.Detail)
	}
	r.writeSummary("running", false)
}

// Finish writes the terminal summary and verdict and returns it.
func (r *reporter) Finish(stopReason string, byCheck map[string]int) verdict {
	for k, v := range byCheck {
		if _, ok := r.byCheck[k]; !ok {
			r.byCheck[k] = v
		}
	}
	return r.writeSummary(stopReason, true)
}

func (r *reporter) writeSummary(stopReason string, complete bool) verdict {
	v := verdict{
		Verdict:    "PASS",
		Label:      r.o.label,
		StartedAt:  r.started,
		UpdatedAt:  time.Now(),
		ElapsedS:   time.Since(r.started).Seconds(),
		Samples:    r.n,
		Violations: r.totalViol,
		ByCheck:    r.byCheck,
		StopReason: stopReason,
		Complete:   complete,
		Thresholds: map[string]any{
			"queued_wedge_s":    r.o.queuedWedge.Seconds(),
			"scheduled_wedge_s": r.o.scheduledWedge.Seconds(),
			"run_wedge_s":       r.o.runWedge.Seconds(),
			"recovery_budget_s": r.o.recoveryBudget.Seconds(),
			"interval_s":        r.o.interval.Seconds(),
			"duration_s":        r.o.duration.Seconds(),
			"max_db_bytes":      r.o.maxDBBytes,
			"max_data_bytes":    r.o.maxDataBytes,
			"min_free_bytes":    r.o.minFreeBytes,
		},
		FirstSample: r.first,
		LastSample:  r.last,
	}
	if r.totalViol > 0 {
		v.Verdict = "FAIL"
	}
	if r.n == 0 {
		v.Verdict = "INCONCLUSIVE"
	}
	// An early stop is not a violation, but it is also not the run that was
	// asked for, so it is annotated identically for all three budgets and the
	// exit code stays non-zero. Reporting a 40-minute truncation of a 48 h
	// request as a clean PASS is how an unattended battery lies by omission.
	if complete && isEarlyStop(stopReason) {
		v.Verdict += " (stopped early on a budget)"
		v.StoppedEarly = true
	}
	writeAtomic(filepath.Join(r.o.outDir, "verdict.json"), mustJSONIndent(v))
	writeAtomic(filepath.Join(r.o.outDir, "summary.md"), []byte(r.renderMarkdown(v)))
	return v
}

func (r *reporter) renderMarkdown(v verdict) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Soak report: %s\n\n", r.o.label)
	fmt.Fprintf(&b, "**Verdict: %s** (%d violations across %d samples)\n\n", v.Verdict, v.Violations, v.Samples)
	fmt.Fprintf(&b, "| | |\n|---|---|\n")
	fmt.Fprintf(&b, "| started | %s |\n", r.started.Format(time.RFC3339))
	fmt.Fprintf(&b, "| updated | %s |\n", v.UpdatedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "| elapsed | %s |\n", time.Duration(v.ElapsedS*float64(time.Second)).Round(time.Second))
	fmt.Fprintf(&b, "| stop reason | `%s` |\n", v.StopReason)
	if v.StoppedEarly {
		fmt.Fprintf(&b, "| stopped early | **yes**, a budget was reached before the wall-clock ceiling |\n")
	}
	fmt.Fprintf(&b, "| complete | %v |\n\n", v.Complete)

	if len(r.byCheck) > 0 {
		b.WriteString("## Violations by check\n\n| check | count |\n|---|---|\n")
		for _, k := range sortedKeys(r.byCheck) {
			fmt.Fprintf(&b, "| `%s` | %d |\n", k, r.byCheck[k])
		}
		b.WriteString("\nFull evidence, one JSON object per violation, is in `violations.jsonl`.\n\n")
	} else {
		b.WriteString("## Violations by check\n\nNone.\n\n")
	}

	if r.last != nil {
		s := *r.last
		b.WriteString("## Latest sample\n\n| signal | value |\n|---|---|\n")
		fmt.Fprintf(&b, "| scheduler health | `%s` (via %s) |\n", s.SchedulerHealth, s.HealthSource)
		fmt.Fprintf(&b, "| active runs | %d |\n", s.ActiveRuns)
		fmt.Fprintf(&b, "| task instances scheduled / queued / running | %d / %d / %d |\n", s.ScheduledTI, s.QueuedTIs, s.RunningTIs)
		fmt.Fprintf(&b, "| up_for_retry / up_for_reschedule | %d / %d |\n", s.RetryTIs, s.ReschedTIs)
		fmt.Fprintf(&b, "| oldest queued / scheduled | %.0fs / %.0fs |\n", s.OldestQueuedS, s.OldestScheduledS)
		fmt.Fprintf(&b, "| tick probe (ActiveRuns wall time) | %.2f ms over %d runs |\n", s.TickProbeMS, s.TickProbeRuns)
		fmt.Fprintf(&b, "| baseline probe (`SELECT 1`, same sample) | %.2f ms |\n", s.BaselineProbeMS)
		fmt.Fprintf(&b, "| dispatch latency p50 / p95 / p99 | %.0f / %.0f / %.0f ms (n=%d) |\n", s.DispatchP50, s.DispatchP95, s.DispatchP99, s.DispatchN)
		fmt.Fprintf(&b, "| runs total / success / failed | %d / %d / %d |\n", s.TotalRuns, s.RunsSuccess, s.RunsFailed)
		fmt.Fprintf(&b, "| task instances / archived attempts / xcom rows | %d / %d / %d |\n", s.TotalTIs, s.TotalHistory, s.TotalXCom)
		fmt.Fprintf(&b, "| audit rows | %d |\n", s.TotalAudit)
		fmt.Fprintf(&b, "| off-budget infra re-placements (recorded, not asserted) | %d |\n", s.InfraReplacements)
		fmt.Fprintf(&b, "| database size (metadatabase / whole cluster) | %s / %s |\n", humanBytes(s.DBBytes), humanBytes(s.DBClusterBytes))
		fmt.Fprintf(&b, "| DAG scratch data | %s |\n", humanBytes(s.DataBytes))
		fmt.Fprintf(&b, "| filesystem free | %s |\n\n", humanBytes(s.FreeBytes))

		if len(s.DispatchByType) > 0 {
			b.WriteString("### Dispatch latency by task type (rolling window)\n\n")
			b.WriteString("| operator | n | p50 ms | p95 ms | max ms |\n|---|---|---|---|---|\n")
			ops := make([]string, 0, len(s.DispatchByType))
			for k := range s.DispatchByType {
				ops = append(ops, k)
			}
			sort.Strings(ops)
			for _, op := range ops {
				l := s.DispatchByType[op]
				fmt.Fprintf(&b, "| `%s` | %d | %.0f | %.0f | %.0f |\n", op, l.N, l.P50, l.P95, l.Max)
			}
			b.WriteString("\n")
		}
		if len(s.RunsCreated) > 0 {
			b.WriteString("### Runs created in the cadence window\n\n| dag | runs | window min |\n|---|---|---|\n")
			for _, k := range sortedKeysInt(s.RunsCreated) {
				fmt.Fprintf(&b, "| `%s` | %d | %.1f |\n", k, s.RunsCreated[k], s.WindowMin)
			}
			b.WriteString("\n")
		}
	}

	b.WriteString(r.renderShape())
	b.WriteString("\n---\n\nGenerated by `test/soak/monitor`. Raw samples: `samples.jsonl`.\n")
	return b.String()
}

// renderShape is the scalability answer: how the tick probe moved as the
// database grew. It buckets the run into ten slices and prints the median probe
// cost against the active and total run counts in each, so the reader can see
// whether the cost tracks the active set (flat as history grows) or the history
// (rising). That distinction is the ceiling question, answered without paying
// for a big N.
func (r *reporter) renderShape() string {
	if len(r.trend) < 4 {
		return "## Tick-cost shape\n\nNot enough samples yet.\n"
	}
	const buckets = 10
	var b strings.Builder
	b.WriteString("## Tick-cost shape (does a tick cost grow with ACTIVE runs or with TOTAL history?)\n\n")
	b.WriteString("| elapsed | samples | active runs (med) | total runs (med) | tick probe p50 ms | tick probe p95 ms | baseline `SELECT 1` p50 ms | dispatch p95 ms |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|\n")
	size := (len(r.trend) + buckets - 1) / buckets
	for i := 0; i < len(r.trend); i += size {
		end := i + size
		if end > len(r.trend) {
			end = len(r.trend)
		}
		chunk := r.trend[i:end]
		probes := make([]float64, 0, len(chunk))
		bases := make([]float64, 0, len(chunk))
		disp := make([]float64, 0, len(chunk))
		actives := make([]float64, 0, len(chunk))
		totals := make([]float64, 0, len(chunk))
		for _, p := range chunk {
			probes = append(probes, p.tickProbe)
			bases = append(bases, p.baseline)
			disp = append(disp, p.p95)
			actives = append(actives, float64(p.activeRuns))
			totals = append(totals, float64(p.totalRuns))
		}
		p50, p95, _, _ := pcts(probes)
		bp50, _, _, _ := pcts(bases)
		dp95, _, _, _ := pcts(disp)
		am, _, _, _ := pcts(actives)
		tm, _, _, _ := pcts(totals)
		fmt.Fprintf(&b, "| %.0fs | %d | %.0f | %.0f | %.2f | %.2f | %.2f | %.0f |\n",
			chunk[len(chunk)-1].elapsedS, len(chunk), am, tm, p50, p95, bp50, dp95)
	}
	b.WriteString("\nRead it this way: if the probe column is flat while `total runs` grows by an\n")
	b.WriteString("order of magnitude, a tick costs what the ACTIVE set costs and the ceiling is\n")
	b.WriteString("set by concurrency, not by retention. If the probe tracks `total runs`, the\n")
	b.WriteString("ceiling arrives with age and the fix is retention, not capacity.\n\n")
	b.WriteString("Read the baseline column before either conclusion: it is a trivial `SELECT 1`\n")
	b.WriteString("taken in the same sample. A bucket where BOTH columns rose says the database,\n")
	b.WriteString("the loopback or the laptop got slower, not that a tick did. Only a bucket where\n")
	b.WriteString("the probe rose and the baseline did not is evidence about `ActiveRuns` itself.\n")
	b.WriteString("Note also that `active runs` co-varies with elapsed time in this workload, so a\n")
	b.WriteString("single run cannot separate the active-set axis from the history axis on its own.\n")
	return b.String()
}

func appendJSON(f *os.File, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return
	}
	// Sync per record: a soak that is killed must not lose the last minute of
	// evidence, and at one record per ten seconds the cost is irrelevant.
	_ = f.Sync() //nolint:errcheck // a failed sync is reported by the next write
}

func writeAtomic(path string, b []byte) {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o640); err != nil {
		return
	}
	_ = os.Rename(tmp, path) //nolint:errcheck // the previous snapshot stays in place if the rename fails
}

func mustJSONIndent(v any) []byte {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return []byte("{}")
	}
	return append(b, '\n')
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeysInt(m map[string]int) []string { return sortedKeys(m) }

func humanBytes(n int64) string {
	if n < 0 {
		return "unknown"
	}
	f := float64(n)
	for _, u := range []string{"B", "KiB", "MiB", "GiB", "TiB"} {
		if math.Abs(f) < 1024 {
			return fmt.Sprintf("%.1f %s", f, u)
		}
		f /= 1024
	}
	return fmt.Sprintf("%.1f PiB", f)
}
