package retention

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"
)

// fakeStore serves canned batches and records every call the janitor makes.
type fakeStore struct {
	tenants []string
	// runBatches is consumed one entry per DeleteFinishedRuns call, per tenant.
	runBatches map[string][]Counts
	// auditBatches is consumed one entry per audit delete call, keyed by tenant
	// ("" is the tenant-less system rows).
	auditBatches map[string][]int64
	eligible     Counts

	runCalls   []runCall
	auditCalls []auditCall
	countCalls int
	purges     []purgeCall
}

type purgeCall struct {
	tenant string
	rows   int64
	cutoff time.Time
}

type runCall struct {
	tenant  string
	cutoff  time.Time
	maxRuns int
	limit   int
}

type auditCall struct {
	tenant string
	cutoff time.Time
	limit  int
}

func (f *fakeStore) TenantIDs(context.Context) ([]string, error) { return f.tenants, nil }

func (f *fakeStore) DeleteFinishedRuns(_ context.Context, tenant string, cutoff time.Time, maxRuns, rowLimit int) (Counts, error) {
	f.runCalls = append(f.runCalls, runCall{tenant, cutoff, maxRuns, rowLimit})
	q := f.runBatches[tenant]
	if len(q) == 0 {
		return Counts{}, nil
	}
	f.runBatches[tenant] = q[1:]
	return q[0], nil
}

func (f *fakeStore) DeleteAuditLog(_ context.Context, tenant string, cutoff time.Time, limit int) (int64, error) {
	f.auditCalls = append(f.auditCalls, auditCall{tenant, cutoff, limit})
	q := f.auditBatches[tenant]
	if len(q) == 0 {
		return 0, nil
	}
	f.auditBatches[tenant] = q[1:]
	return q[0], nil
}

func (f *fakeStore) RecordRetentionPurge(_ context.Context, tenant string, rows int64, cutoff time.Time) error {
	f.purges = append(f.purges, purgeCall{tenant, rows, cutoff})
	return nil
}

func (f *fakeStore) CountEligible(context.Context, *time.Time, *time.Time) (Counts, error) {
	f.countCalls++
	return f.eligible, nil
}

// fakeRecorder captures the metrics the janitor emits.
type fakeRecorder struct {
	deleted  map[string]int64
	eligible map[string]int64
	cycles   int
}

func newFakeRecorder() *fakeRecorder {
	return &fakeRecorder{deleted: map[string]int64{}, eligible: map[string]int64{}}
}

func (r *fakeRecorder) RecordRetentionDeleted(table string, n int64)  { r.deleted[table] += n }
func (r *fakeRecorder) RecordRetentionEligible(table string, n int64) { r.eligible[table] = n }
func (r *fakeRecorder) ObserveRetentionCycle(time.Duration)           { r.cycles++ }

var fixedNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func newTestJanitor(store Store, cfg Config, rec Recorder) (*Janitor, *[]time.Duration) {
	j := New(store, cfg, rec, slog.New(slog.NewTextHandler(io.Discard, nil)))
	j.now = func() time.Time { return fixedNow }
	var pauses []time.Duration
	j.sleep = func(_ context.Context, d time.Duration) error {
		pauses = append(pauses, d)
		return nil
	}
	return j, &pauses
}

func baseConfig() Config {
	return Config{BatchSize: 1000, BatchPause: 100 * time.Millisecond, MaxRowsPerCycle: 100000}
}

// With no class configured the janitor does not touch the store at all.
func TestRunOnceWithNothingConfiguredTouchesNothing(t *testing.T) {
	store := &fakeStore{tenants: []string{"t1"}}
	j, _ := newTestJanitor(store, baseConfig(), newFakeRecorder())
	got, err := j.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if got.Total() != 0 || len(store.runCalls) != 0 || len(store.auditCalls) != 0 || store.countCalls != 0 {
		t.Fatalf("store touched with retention off: counts=%+v runs=%v audit=%v count=%d", got, store.runCalls, store.auditCalls, store.countCalls)
	}
}

// Finished runs are removed in batches, one batch per tenant in turn, until a
// batch removes nothing; the cutoff is the configured window back from now,
// each batch is bounded, and the janitor pauses between batches.
func TestRunOnceDeletesRunsInPausedBatchesPerTenant(t *testing.T) {
	store := &fakeStore{
		tenants: []string{"t1", "t2"},
		runBatches: map[string][]Counts{
			"t1": {{DagRuns: 100, TaskInstances: 900}, {DagRuns: 3, TaskInstances: 9}},
			"t2": {{DagRuns: 1, TaskInstances: 2, TaskStateHistory: 4}},
		},
	}
	cfg := baseConfig()
	cfg.DagRunsDays = 30
	rec := newFakeRecorder()
	j, pauses := newTestJanitor(store, cfg, rec)

	got, err := j.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if got.DagRuns != 104 || got.TaskInstances != 911 || got.TaskStateHistory != 4 {
		t.Fatalf("counts = %+v", got)
	}
	wantCutoff := fixedNow.Add(-30 * 24 * time.Hour)
	// t1: two batches with rows, then an empty one; t2: one, then an empty one;
	// the tenants take turns.
	if len(store.runCalls) != 5 {
		t.Fatalf("run delete calls = %d (%+v), want 5", len(store.runCalls), store.runCalls)
	}
	for _, c := range store.runCalls {
		if !c.cutoff.Equal(wantCutoff) {
			t.Errorf("cutoff = %v, want %v", c.cutoff, wantCutoff)
		}
		if c.maxRuns != maxRunsPerBatch || c.limit != 1000 {
			t.Errorf("batch bounds = (%d runs, %d rows), want (%d, 1000)", c.maxRuns, c.limit, maxRunsPerBatch)
		}
	}
	order := make([]string, 0, len(store.runCalls))
	for _, c := range store.runCalls {
		order = append(order, c.tenant)
	}
	if got := fmt.Sprint(order); got != "[t1 t2 t1 t2 t1]" {
		t.Errorf("tenant order = %s, want the tenants in turn", got)
	}
	if len(*pauses) != 3 {
		t.Errorf("pauses = %v, want one after each non-empty batch", *pauses)
	}
	if rec.deleted[TableDagRuns] != 104 || rec.deleted[TableTaskInstances] != 911 {
		t.Errorf("deleted metrics = %v", rec.deleted)
	}
	if rec.cycles != 1 {
		t.Errorf("cycle observations = %d, want 1", rec.cycles)
	}
	if len(store.auditCalls) != 0 {
		t.Errorf("audit log touched with only runs configured: %+v", store.auditCalls)
	}
	if len(store.purges) != 0 {
		t.Errorf("audit purge recorded without deleting audit rows: %+v", store.purges)
	}
}

// A batch that only removed child rows (a run with more children than one
// batch allows) is not the end: the janitor keeps going until a batch removes
// nothing at all.
func TestRunOnceContinuesThroughChildOnlyBatches(t *testing.T) {
	store := &fakeStore{
		tenants:    []string{"t1"},
		runBatches: map[string][]Counts{"t1": {{TaskInstances: 600, TaskStateHistory: 400}, {DagRuns: 1, TaskInstances: 3}}},
	}
	cfg := baseConfig()
	cfg.DagRunsDays = 30
	j, _ := newTestJanitor(store, cfg, newFakeRecorder())
	got, err := j.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(store.runCalls) != 3 || got.DagRuns != 1 || got.TaskInstances != 603 {
		t.Fatalf("calls = %d, counts = %+v; want 3 calls deleting the whole run", len(store.runCalls), got)
	}
}

// A small batch size also bounds the runs per transaction.
func TestRunOnceRunBatchNeverExceedsBatchSize(t *testing.T) {
	store := &fakeStore{tenants: []string{"t1"}}
	cfg := baseConfig()
	cfg.DagRunsDays = 1
	cfg.BatchSize = 10
	j, _ := newTestJanitor(store, cfg, newFakeRecorder())
	if _, err := j.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.runCalls[0].maxRuns != 10 {
		t.Fatalf("maxRuns = %d, want 10", store.runCalls[0].maxRuns)
	}
}

// Audit rows are removed per tenant and for the tenant-less system rows, until
// a batch comes back short, and each purge leaves its own audit record.
func TestRunOnceDeletesAuditLogPerTenantAndSystemRows(t *testing.T) {
	store := &fakeStore{
		tenants: []string{"t1"},
		auditBatches: map[string][]int64{
			"t1": {1000, 1000, 7},
			"":   {2},
		},
	}
	cfg := baseConfig()
	cfg.AuditLogDays = 365
	j, _ := newTestJanitor(store, cfg, newFakeRecorder())
	got, err := j.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.AuditLog != 2009 {
		t.Fatalf("audit deleted = %d, want 2009", got.AuditLog)
	}
	if len(store.auditCalls) != 4 {
		t.Fatalf("audit calls = %+v, want 3 for t1 and 1 for system rows", store.auditCalls)
	}
	if store.auditCalls[1].tenant != "" {
		t.Errorf("second call tenant = %q, want the system rows in turn", store.auditCalls[1].tenant)
	}
	want := fixedNow.Add(-365 * 24 * time.Hour)
	purged := map[string]int64{}
	for _, p := range store.purges {
		purged[p.tenant] += p.rows
		if !p.cutoff.Equal(want) {
			t.Errorf("purge cutoff = %v, want %v", p.cutoff, want)
		}
	}
	if len(store.purges) != 2 || purged["t1"] != 2007 || purged[""] != 2 {
		t.Errorf("purge records = %+v, want one per scope with its row count", store.purges)
	}
	if !store.auditCalls[0].cutoff.Equal(want) {
		t.Errorf("audit cutoff = %v, want %v", store.auditCalls[0].cutoff, want)
	}
	if len(store.runCalls) != 0 {
		t.Errorf("runs touched with only the audit log configured")
	}
}

// The per-cycle cap stops the janitor from starting another batch once
// reached, and shrinks the last audit batch to what is left of the budget.
func TestRunOnceStopsAtTheCycleCap(t *testing.T) {
	store := &fakeStore{
		tenants: []string{"t1", "t2"},
		runBatches: map[string][]Counts{
			"t1": {{DagRuns: 100, TaskInstances: 400}, {DagRuns: 100, TaskInstances: 400}},
			"t2": {{DagRuns: 100}},
		},
		auditBatches: map[string][]int64{"t1": {1000}},
	}
	cfg := baseConfig()
	cfg.DagRunsDays = 7
	cfg.AuditLogDays = 7
	cfg.MaxRowsPerCycle = 600
	j, _ := newTestJanitor(store, cfg, newFakeRecorder())
	got, err := j.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(store.runCalls) != 2 || store.runCalls[1].tenant != "t2" {
		t.Fatalf("run calls = %+v, want t1 then t2 under budget and nothing after", store.runCalls)
	}
	if store.runCalls[1].limit != 100 {
		t.Errorf("second batch row limit = %d, want the 100 rows left of the budget", store.runCalls[1].limit)
	}
	if got.Total() != 600 {
		t.Errorf("total = %d, want 600", got.Total())
	}
	if len(store.auditCalls) != 0 {
		t.Errorf("audit batch started past the cap: %+v", store.auditCalls)
	}

	store2 := &fakeStore{tenants: []string{"t1"}, auditBatches: map[string][]int64{"t1": {250}}}
	cfg2 := baseConfig()
	cfg2.AuditLogDays = 7
	cfg2.MaxRowsPerCycle = 250
	j2, _ := newTestJanitor(store2, cfg2, newFakeRecorder())
	if _, err := j2.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store2.auditCalls) != 1 || store2.auditCalls[0].limit != 250 {
		t.Fatalf("audit calls = %+v, want one batch shrunk to the 250-row budget", store2.auditCalls)
	}
}

// With a run backlog larger than the cycle cap, the audit log still gets its
// turn in every cycle instead of waiting for the runs to drain.
func TestRunOnceGivesTheAuditLogATurnUnderTheCap(t *testing.T) {
	batches := make([]Counts, 50)
	for i := range batches {
		batches[i] = Counts{DagRuns: 10, TaskInstances: 90}
	}
	store := &fakeStore{
		tenants:      []string{"t1"},
		runBatches:   map[string][]Counts{"t1": batches},
		auditBatches: map[string][]int64{"t1": {100}},
	}
	cfg := baseConfig()
	cfg.DagRunsDays = 30
	cfg.AuditLogDays = 30
	cfg.MaxRowsPerCycle = 500
	j, _ := newTestJanitor(store, cfg, newFakeRecorder())
	if _, err := j.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.auditCalls) == 0 {
		t.Fatal("the audit log got no batch while the run backlog used the cap")
	}
}

// A cycle that deletes resets the dry-run gauges, so a stale count does not
// linger after dry_run is turned off.
func TestRunOnceResetsEligibleGaugesOutsideDryRun(t *testing.T) {
	store := &fakeStore{tenants: []string{"t1"}}
	cfg := baseConfig()
	cfg.DagRunsDays = 30
	rec := newFakeRecorder()
	rec.eligible[TableDagRuns] = 12
	j, _ := newTestJanitor(store, cfg, rec)
	if _, err := j.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v, ok := rec.eligible[TableDagRuns]; !ok || v != 0 {
		t.Fatalf("eligible gauge = %v (set %v), want reset to 0", v, ok)
	}
}

// Dry run only counts: no delete is issued and the eligible gauges are set.
func TestRunOnceDryRunOnlyCounts(t *testing.T) {
	store := &fakeStore{
		tenants:    []string{"t1"},
		runBatches: map[string][]Counts{"t1": {{DagRuns: 1}}},
		eligible:   Counts{DagRuns: 12, TaskInstances: 40, AuditLog: 7},
	}
	cfg := baseConfig()
	cfg.DagRunsDays = 30
	cfg.AuditLogDays = 30
	cfg.DryRun = true
	rec := newFakeRecorder()
	j, _ := newTestJanitor(store, cfg, rec)
	got, err := j.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(store.runCalls) != 0 || len(store.auditCalls) != 0 {
		t.Fatalf("dry run deleted: runs=%+v audit=%+v", store.runCalls, store.auditCalls)
	}
	if store.countCalls != 1 || got.DagRuns != 12 {
		t.Fatalf("count calls = %d, counts = %+v", store.countCalls, got)
	}
	if rec.eligible[TableDagRuns] != 12 || rec.eligible[TableAuditLog] != 7 || len(rec.deleted) != 0 {
		t.Errorf("metrics eligible=%v deleted=%v", rec.eligible, rec.deleted)
	}
	if len(store.purges) != 0 {
		t.Errorf("dry run recorded a purge: %+v", store.purges)
	}
}

// Losing leadership mid-cycle stops the janitor before its next batch.
func TestRunOnceStopsWhenLeadershipIsLost(t *testing.T) {
	store := &fakeStore{
		tenants:    []string{"t1"},
		runBatches: map[string][]Counts{"t1": {{DagRuns: 100}, {DagRuns: 100}, {DagRuns: 100}}},
	}
	cfg := baseConfig()
	cfg.DagRunsDays = 30
	j, _ := newTestJanitor(store, cfg, newFakeRecorder())
	calls := 0
	j.SetLeading(func() bool {
		calls++
		return calls <= 1
	})
	_, err := j.RunOnce(context.Background())
	if !errors.Is(err, ErrNotLeading) {
		t.Fatalf("RunOnce err = %v, want ErrNotLeading", err)
	}
	if len(store.runCalls) != 1 {
		t.Fatalf("run calls = %d, want 1 (stopped after leadership was lost)", len(store.runCalls))
	}
}

// A canceled context ends the cycle with the context's error.
func TestRunOnceHonorsCancellation(t *testing.T) {
	store := &fakeStore{tenants: []string{"t1"}, runBatches: map[string][]Counts{"t1": {{DagRuns: 5}, {DagRuns: 5}}}}
	cfg := baseConfig()
	cfg.DagRunsDays = 30
	j, _ := newTestJanitor(store, cfg, newFakeRecorder())
	ctx, cancel := context.WithCancel(context.Background())
	j.sleep = func(context.Context, time.Duration) error {
		cancel()
		return context.Canceled
	}
	if _, err := j.RunOnce(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(store.runCalls) != 1 {
		t.Fatalf("run calls = %d, want 1", len(store.runCalls))
	}
}
