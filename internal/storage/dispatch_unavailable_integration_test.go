//go:build integration

package storage_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
	"github.com/dexadata/dexaflow/internal/scheduler"
	"github.com/dexadata/dexaflow/internal/storage"
)

// errAPIUnavailable is what the Kubernetes executor returns, classified
// Unavailable, while the apiserver is down (#1606).
var errAPIUnavailable = errors.New("creating pod for task a: the server is currently unable to handle the request")

// outageDispatcher claims an attempt epoch the way the real dispatcher does
// (ResolveTask) and then fails the create as Unavailable while down is set.
// The identities of the dispatches that "created" a pod are kept, so the test
// can report as their agents.
type outageDispatcher struct {
	exec    *storage.ExecutionStore
	down    bool
	claimed []auth.AgentIdentity
	runUUID string
}

func (d *outageDispatcher) Dispatch(ctx context.Context, runID, dagID, _ string, task domain.TaskSpec) (executor.Disposition, error) {
	r, err := d.exec.ResolveTask(ctx, runID, task.TaskID)
	if err != nil {
		return executor.Rejected, err
	}
	d.claimed = append(d.claimed, auth.AgentIdentity{
		TenantID: defaultTenantID(), TaskInstanceID: r.TaskInstanceID, RunID: runID, TaskID: task.TaskID, DagID: dagID,
		TryNumber: r.TryNumber, AttemptEpoch: r.AttemptEpoch, HasAttemptEpoch: true,
	})
	if d.down {
		return executor.Unavailable, errAPIUnavailable
	}
	return executor.Dispatched, nil
}

// outageFixture is one run with a single task "a" (retries: 2) driven by a
// real scheduler over the real store.
type outageFixture struct {
	pg      *storage.Postgres
	exec    *storage.ExecutionStore
	sched   *scheduler.Scheduler
	disp    *outageDispatcher
	runUUID string
	ctx     context.Context
}

func newOutageFixture(t *testing.T, prefix string) *outageFixture {
	t.Helper()
	repo, store, pg, ctx := openInfra(t)
	exec := storage.NewExecutionStore(pg)
	d := &outageDispatcher{exec: exec, down: true}
	sched := scheduler.NewScheduler(store, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Millisecond)
	sched.SetDispatcher(d)
	sched.SetLeading(true)

	suffix := time.Now().UnixNano()
	dagID := fmt.Sprintf("%s_%d", prefix, suffix)
	retries := 2
	spec := domain.DAGSpec{
		SchemaVersion: "1.0", DagID: dagID, DagVersion: "v1", Image: "img:v1",
		Tasks: []domain.TaskSpec{{TaskID: "a", Type: domain.TaskTypePython, Entrypoint: "dag:a", Retries: &retries}},
	}
	hash, err := spec.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RegisterDagVersion(ctx, "default", spec, hash); err != nil {
		t.Fatalf("register version: %v", err)
	}
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: fmt.Sprintf("%s-run-%d", prefix, suffix), State: domain.DagRunStateQueued, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	f := &outageFixture{pg: pg, exec: exec, sched: sched, disp: d, ctx: ctx}
	// materialize -> running -> none->scheduled -> first dispatch (fails).
	f.steps(t, 4)
	f.runUUID = resolveRunUUID(t, store, ctx, dagID)
	d.runUUID = f.runUUID
	return f
}

func (f *outageFixture) steps(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := f.sched.Step(f.ctx); err != nil {
			t.Fatalf("step: %v", err)
		}
	}
}

// row is the dispatch state of task "a".
type outageRow struct {
	state       string
	try         int
	maxTries    int
	attempts    int
	since       *time.Time
	nextAt      *time.Time
	errorPrefix string
}

func (f *outageFixture) row(t *testing.T) outageRow {
	t.Helper()
	var r outageRow
	var msg *string
	if err := f.pg.Pool.QueryRow(f.ctx, `
		SELECT state::text, try_number, max_tries, dispatch_attempts, dispatch_unavailable_since, next_dispatch_at, error_message
		FROM task_instances WHERE dag_run_id = $1::uuid AND task_id = 'a'
		ORDER BY try_number DESC LIMIT 1`, f.runUUID,
	).Scan(&r.state, &r.try, &r.maxTries, &r.attempts, &r.since, &r.nextAt, &msg); err != nil {
		t.Fatalf("read task: %v", err)
	}
	if msg != nil {
		r.errorPrefix = *msg
	}
	return r
}

// releaseHold moves the task's next_dispatch_at to the past, as the clock
// would, so the next tick dispatches it again.
func (f *outageFixture) releaseHold(t *testing.T) {
	t.Helper()
	if _, err := f.pg.Pool.Exec(f.ctx,
		`UPDATE task_instances SET next_dispatch_at = now() - interval '1 second' WHERE dag_run_id = $1::uuid AND task_id = 'a'`,
		f.runUUID); err != nil {
		t.Fatal(err)
	}
}

// TestUnavailableAPIShorterThanTheWindowEndsRunning: the API is down for
// more re-offers than the whole dispatch-attempt budget, then comes back. The
// task never spends a dispatch attempt, keeps the start of its outage window
// across re-offers, is dispatched once the API answers, and its agent's
// RUNNING report applies (#1606).
func TestUnavailableAPIShorterThanTheWindowEndsRunning(t *testing.T) {
	f := newOutageFixture(t, "unavailable_short")
	first := f.row(t)
	if first.state != "scheduled" || first.since == nil || first.attempts != 0 {
		t.Fatalf("after the first unavailable create: %+v, want scheduled, window stamped, 0 attempts", first)
	}
	if first.nextAt == nil || first.nextAt.Sub(*first.since) != 5*time.Second {
		t.Errorf("the first hold must be the 5s base, got next_dispatch_at - since = %v", first.nextAt.Sub(*first.since))
	}
	for i := 0; i < 8; i++ { // more than dispatchMaxAttempts (6)
		f.releaseHold(t)
		f.steps(t, 1)
	}
	held := f.row(t)
	if held.state != "scheduled" || held.attempts != 0 {
		t.Fatalf("an outage inside the window must not spend the dispatch budget: %+v", held)
	}
	if held.since == nil || !held.since.Equal(*first.since) {
		t.Errorf("the window start must be kept across re-offers: first %v, now %v", first.since, held.since)
	}

	f.disp.down = false
	f.releaseHold(t)
	f.steps(t, 1)
	if r := f.row(t); r.state != "queued" {
		t.Fatalf("the task must be dispatched once the API is back, got %+v", r)
	}
	live := f.disp.claimed[len(f.disp.claimed)-1]
	if err := f.exec.ReportState(f.ctx, live, domain.TaskStateRunning, 0, ""); err != nil {
		t.Fatalf("the live dispatch's RUNNING report must apply: %v", err)
	}
	if r := f.row(t); r.state != "running" || r.try != 1 {
		t.Errorf("task = %+v, want running on its first try", r)
	}
	// Every pod an ambiguous create may have left behind is fenced.
	for _, orphan := range f.disp.claimed[:len(f.disp.claimed)-1] {
		wantStale(t, "an orphan pod's RUNNING report", f.exec.ReportState(f.ctx, orphan, domain.TaskStateRunning, 0, ""))
	}
}

// TestUnavailableAPILongerThanTheWindowFailsTheTask: once the task has been
// riding out the outage for longer than the window, the next unavailable
// create fails it as dispatch_failed, terminally: it does not fall back onto
// the user's retries, and the run fails.
func TestUnavailableAPILongerThanTheWindowFailsTheTask(t *testing.T) {
	f := newOutageFixture(t, "unavailable_long")
	// The outage began 16 minutes ago (past the 15 minute window).
	if _, err := f.pg.Pool.Exec(f.ctx,
		`UPDATE task_instances SET dispatch_unavailable_since = now() - interval '16 minutes' WHERE dag_run_id = $1::uuid AND task_id = 'a'`,
		f.runUUID); err != nil {
		t.Fatal(err)
	}
	f.releaseHold(t)
	f.steps(t, 1)
	r := f.row(t)
	if r.state != "failed" || !strings.HasPrefix(r.errorPrefix, "dispatch_failed: Kubernetes API unavailable for 16m") {
		t.Fatalf("task = %+v, want failed with the outage as reason", r)
	}
	if r.attempts != 0 {
		t.Errorf("the window, not the attempt budget, failed the task: attempts = %d", r.attempts)
	}
	f.steps(t, 5)
	r = f.row(t)
	if r.state != "failed" || r.try != 1 {
		t.Errorf("dispatch_failed must be terminal without spending a retry: %+v", r)
	}
	var runState string
	if err := f.pg.Pool.QueryRow(f.ctx, `SELECT state::text FROM dag_runs WHERE id = $1::uuid`, f.runUUID).Scan(&runState); err != nil {
		t.Fatal(err)
	}
	if runState != string(domain.DagRunStateFailed) {
		t.Errorf("run state = %s, want failed", runState)
	}
}

// TestRecordDispatchUnavailableHold pins the hold the query writes: the time
// already spent in the outage, at least minDelay and at most maxDelay, and the
// window start kept from the first failure.
func TestRecordDispatchUnavailableHold(t *testing.T) {
	f := seedStaleHeartbeat(t, "unavailable_hold")
	f.setState(t, "scheduled")
	t0 := time.Now().UTC().Truncate(time.Microsecond)
	for _, step := range []struct {
		at       time.Duration
		wantHold time.Duration
	}{
		{0, 5 * time.Second},                 // first failure: the base
		{12 * time.Second, 12 * time.Second}, // the time spent so far
		{10 * time.Minute, time.Minute},      // capped
	} {
		now := t0.Add(step.at)
		since, nextAt, applied, err := f.sched.RecordDispatchUnavailable(f.ctx, f.runUUID, "t", now, 5*time.Second, time.Minute)
		if err != nil || !applied {
			t.Fatalf("at %v: applied=%v err=%v", step.at, applied, err)
		}
		if !since.Equal(t0) {
			t.Errorf("at %v: since = %v, want the first failure %v", step.at, since, t0)
		}
		if got := nextAt.Sub(now); got != step.wantHold {
			t.Errorf("at %v: hold = %v, want %v", step.at, got, step.wantHold)
		}
	}
	// A task the agent has moved on is left alone.
	f.setState(t, "running")
	if _, _, applied, err := f.sched.RecordDispatchUnavailable(f.ctx, f.runUUID, "t", t0, 5*time.Second, time.Minute); err != nil || applied {
		t.Errorf("a running task must not be re-offered: applied=%v err=%v", applied, err)
	}
	// A clear starts a new dispatch episode: the window is reset with the
	// dispatch-attempt counter, by "clear failed" (ResetFailedTaskInstance)
	// and by a clear of named tasks (ResetTaskInstanceToNone).
	for _, onlyFailed := range []bool{true, false} {
		f.setState(t, "scheduled")
		if _, _, applied, err := f.sched.RecordDispatchUnavailable(f.ctx, f.runUUID, "t", t0, 5*time.Second, time.Minute); err != nil || !applied {
			t.Fatalf("RecordDispatchUnavailable applied=%v err=%v", applied, err)
		}
		f.setState(t, "failed")
		if _, err := f.repo.ClearTaskInstances(f.ctx, "default", f.dagID, f.runID, []string{"t"}, onlyFailed, domain.ClearOptions{}); err != nil {
			t.Fatalf("clear (only failed %v): %v", onlyFailed, err)
		}
		var since *time.Time
		if err := f.pg.Pool.QueryRow(f.ctx,
			`SELECT dispatch_unavailable_since FROM task_instances WHERE dag_run_id = $1::uuid AND task_id = 't' ORDER BY try_number DESC LIMIT 1`,
			f.runUUID).Scan(&since); err != nil {
			t.Fatal(err)
		}
		if since != nil {
			t.Errorf("a clear (only failed %v) must reset the outage window, got %v", onlyFailed, *since)
		}
	}
}

// TestAmbiguousCreateIsFencedByTheEpoch: a create whose response was lost may
// still have made a pod. Re-offering the task bumps the attempt epoch, so the
// orphan pod's agent is refused (should_terminate) before it starts user
// code, the re-dispatch's agent runs the attempt, and only one of them can.
// If the orphan wins the race and reports RUNNING before the re-offer is
// written, the re-offer leaves the row alone and no second dispatch can claim
// it.
func TestAmbiguousCreateIsFencedByTheEpoch(t *testing.T) {
	t.Run("re_offer_first", func(t *testing.T) {
		f := seedStaleHeartbeat(t, "ambiguous_create")
		f.setState(t, "scheduled")
		orphan := f.dispatch(t) // the create reached the apiserver, the answer did not
		if _, _, applied, err := f.sched.RecordDispatchUnavailable(f.ctx, f.runUUID, "t", time.Now(), 5*time.Second, time.Minute); err != nil || !applied {
			t.Fatalf("RecordDispatchUnavailable applied=%v err=%v", applied, err)
		}
		wantStale(t, "the orphan pod's RUNNING report before the re-dispatch", f.exec.ReportState(f.ctx, orphan, domain.TaskStateRunning, 0, ""))
		retry := f.dispatch(t)
		f.transition(t, domain.TaskStateQueued)
		wantStale(t, "the orphan pod's RUNNING report after the re-dispatch", f.exec.ReportState(f.ctx, orphan, domain.TaskStateRunning, 0, ""))
		if err := f.exec.ReportState(f.ctx, retry, domain.TaskStateRunning, 0, ""); err != nil {
			t.Fatalf("the re-dispatch's RUNNING report must apply: %v", err)
		}
	})
	t.Run("orphan_first", func(t *testing.T) {
		f := seedStaleHeartbeat(t, "ambiguous_create_race")
		f.setState(t, "scheduled")
		orphan := f.dispatch(t)
		f.transition(t, domain.TaskStateQueued)
		if err := f.exec.ReportState(f.ctx, orphan, domain.TaskStateRunning, 0, ""); err != nil {
			t.Fatalf("the orphan's RUNNING report on its own epoch applies: %v", err)
		}
		if _, _, applied, err := f.sched.RecordDispatchUnavailable(f.ctx, f.runUUID, "t", time.Now(), 5*time.Second, time.Minute); err != nil || applied {
			t.Fatalf("a running attempt must not be re-offered: applied=%v err=%v", applied, err)
		}
		if _, err := f.exec.ResolveTask(f.ctx, f.runUUID, "t"); !errors.Is(err, storage.ErrNotDispatchable) {
			t.Errorf("no second dispatch may claim a running attempt, got %v", err)
		}
	})
}
