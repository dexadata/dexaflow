package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
)

// recordingAlerter signals each AlertRunFailed call over a buffered channel so a
// test can observe the scheduler's fire-and-forget dispatch deterministically.
type recordingAlerter struct{ ch chan RunState }

func (r *recordingAlerter) AlertRunFailed(_ context.Context, run RunState) bool {
	r.ch <- run
	return true
}

// blockingAlerter signals when a dispatch starts and then holds the concurrency
// slot until released, so a test can prove the scheduler bounds concurrency.
type blockingAlerter struct {
	started chan RunState
	release chan struct{}
}

func (b *blockingAlerter) AlertRunFailed(_ context.Context, run RunState) bool {
	b.started <- run
	<-b.release
	return true
}

func exhaustedFailedRun(id string) RunState {
	return RunState{
		RunID: id, DagID: "etl", State: domain.DagRunStateRunning, Tasks: linearTasks(),
		States:   map[string]domain.TaskState{"a": domain.TaskStateFailed, "b": domain.TaskStateUpstreamFailed},
		Tries:    map[string]int{"a": 3, "b": 1},
		MaxTries: map[string]int{"a": 3, "b": 3},
		Alerts:   &domain.AlertsConfig{OnFailure: []domain.AlertRule{{Type: "slack", Conn: "slack"}}},
	}
}

// With the concurrency bound at 1, a second simultaneously-failing run is dropped
// (best-effort) rather than spawning an unbounded second dispatch goroutine.
func TestStepBoundsAlertConcurrency(t *testing.T) {
	al := &blockingAlerter{started: make(chan RunState, 4), release: make(chan struct{})}
	defer close(al.release)
	store := newFakeStore(exhaustedFailedRun("r1"), exhaustedFailedRun("r2"))
	s := newScheduler(store)
	s.SetAlerter(al)
	s.SetAlertConcurrency(1)
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-al.started:
	case <-time.After(2 * time.Second):
		t.Fatal("expected one alert to start")
	}
	select {
	case <-al.started:
		t.Fatal("the second alert must be dropped while the slot is held, not spawned")
	case <-time.After(150 * time.Millisecond):
	}
}

// When the dispatch semaphore is saturated the dropped alert is not just logged —
// it is recorded as result="dropped" so operators can alert on a burst (#435).
func TestStepRecordsDroppedAlertWhenSaturated(t *testing.T) {
	al := &blockingAlerter{started: make(chan RunState, 4), release: make(chan struct{})}
	defer close(al.release)
	rec := &capturingRecorder{}
	store := newFakeStore(exhaustedFailedRun("r1"), exhaustedFailedRun("r2"))
	s := newScheduler(store)
	s.SetAlerter(al)
	s.SetRecorder(rec)
	s.SetAlertConcurrency(1)
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	// One dispatch acquired the slot and blocks; the other run's alert is dropped.
	select {
	case <-al.started:
	case <-time.After(2 * time.Second):
		t.Fatal("expected one alert to start")
	}
	// The drop is recorded synchronously in the finalize path (etl DAG, slack rule).
	if got := rec.alertCount("etl/slack/dropped"); got != 1 {
		t.Fatalf("dropped-alert records = %d, want 1 (%v)", got, rec.alerts)
	}
}

// Dedup per failure episode (#431): a run whose failed state is re-ticked without
// a clear alerts only once — the second MarkRunAlerted loses the CAS.
func TestStepDedupsAlertPerEpisode(t *testing.T) {
	store := newFakeStore(exhaustedFailedRun("r1"))
	al := &recordingAlerter{ch: make(chan RunState, 2)}
	s := newScheduler(store)
	s.SetAlerter(al)
	// First tick: the run finalizes failed and claims the alert (fires once).
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-al.ch:
	case <-time.After(2 * time.Second):
		t.Fatal("expected the first failure to alert")
	}
	// The page is sent from a dispatch goroutine that stamps delivery
	// (MarkRunAlertDelivered) only after the send returns. Wait for that stamp
	// before re-ticking: the invariant under test is "a re-tick of an
	// already-DELIVERED episode must not re-alert", so racing the async stamp
	// would be testing a different (unstamped) state — the #609 flake.
	deadline := time.Now().Add(2 * time.Second)
	for !store.wasDelivered("r1") {
		if time.Now().After(deadline) {
			t.Fatal("first alert was not stamped delivered within 2s")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Second tick: the fake still returns the run as active (re-tick of the same
	// failed episode, no clear). The episode is stamped delivered, so no second page.
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-al.ch:
		t.Fatal("a re-tick of the same failed episode must not re-alert (#431)")
	case <-time.After(150 * time.Millisecond):
	}
}

// Fail-open (#431): if the dedup CAS errors, the alert still fires — a missed page
// is worse than a rare duplicate.
func TestStepAlertsWhenDedupErrors(t *testing.T) {
	store := newFakeStore(exhaustedFailedRun("r1"))
	store.markAlertedErr = true
	al := &recordingAlerter{ch: make(chan RunState, 1)}
	s := newScheduler(store)
	s.SetAlerter(al)
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-al.ch:
	case <-time.After(2 * time.Second):
		t.Fatal("a dedup error must fail open and still alert")
	}
}

// A run that finalizes failed with on_failure rules fires the alerter exactly once.
func TestStepAlertsOnFailedFinalize(t *testing.T) {
	store := newFakeStore(RunState{
		RunID: "r1", DagID: "etl", State: domain.DagRunStateRunning, Tasks: linearTasks(),
		States:   map[string]domain.TaskState{"a": domain.TaskStateFailed, "b": domain.TaskStateUpstreamFailed},
		Tries:    map[string]int{"a": 3, "b": 1},
		MaxTries: map[string]int{"a": 3, "b": 3},
		Alerts:   &domain.AlertsConfig{OnFailure: []domain.AlertRule{{Type: "slack", Conn: "slack"}}},
	})
	al := &recordingAlerter{ch: make(chan RunState, 1)}
	s := newScheduler(store)
	s.SetAlerter(al)
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-al.ch:
		if got.RunID != "r1" {
			t.Errorf("alerted run = %q, want r1", got.RunID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected an on-failure alert to fire")
	}
}

// A successful run never alerts, even with on_failure rules configured.
func TestStepNoAlertOnSuccess(t *testing.T) {
	store := newFakeStore(RunState{
		RunID: "r1", State: domain.DagRunStateRunning, Tasks: linearTasks(),
		States: map[string]domain.TaskState{"a": domain.TaskStateSuccess, "b": domain.TaskStateSuccess},
		Alerts: &domain.AlertsConfig{OnFailure: []domain.AlertRule{{Type: "slack", Conn: "slack"}}},
	})
	al := &recordingAlerter{ch: make(chan RunState, 1)}
	s := newScheduler(store)
	s.SetAlerter(al)
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-al.ch:
		t.Fatal("no alert expected on a successful run")
	case <-time.After(150 * time.Millisecond):
	}
}

// A failed run with no alert rules does not fire the alerter (nothing to send).
func TestStepNoAlertWhenNoRules(t *testing.T) {
	store := newFakeStore(RunState{
		RunID: "r1", State: domain.DagRunStateRunning, Tasks: linearTasks(),
		States:   map[string]domain.TaskState{"a": domain.TaskStateFailed, "b": domain.TaskStateUpstreamFailed},
		Tries:    map[string]int{"a": 3, "b": 1},
		MaxTries: map[string]int{"a": 3, "b": 3},
	})
	al := &recordingAlerter{ch: make(chan RunState, 1)}
	s := newScheduler(store)
	s.SetAlerter(al)
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-al.ch:
		t.Fatal("no alert expected without on_failure rules")
	case <-time.After(150 * time.Millisecond):
	}
}

// failingAlerter reports that delivery did not complete, the way the real
// dispatcher does when a channel returns 500 or a rule's connection cannot be
// resolved.
type failingAlerter struct{ calls chan RunState }

func (f *failingAlerter) AlertRunFailed(_ context.Context, run RunState) bool {
	f.calls <- run
	return false
}

// A send that fails must leave the episode CLAIMABLE. Before the delivery split,
// the claim was taken before sending, so a 500 marked the run alerted and the
// page was lost for good — measured on v0.1.2-rc.1: a receiver answering 500 left
// the run marked with no retry, and a burst of 15 failures marked all 15 while
// delivering none.
func TestStepDoesNotMarkDeliveredWhenSendFails(t *testing.T) {
	store := newFakeStore(exhaustedFailedRun("r1"))
	al := &failingAlerter{calls: make(chan RunState, 4)}
	s := newScheduler(store)
	s.SetAlerter(al)
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-al.calls:
	case <-time.After(2 * time.Second):
		t.Fatal("expected the alerter to be called")
	}
	// The goroutine stamps (or does not) after the alerter returns; give it a beat.
	waitFor(t, func() bool { return store.attemptsFor("r1") == 1 })
	if store.wasDelivered("r1") {
		t.Fatal("a failed send must NOT be recorded as delivered — that is what loses the page")
	}
}

// The counterpart: a successful send IS recorded, so the next tick does not
// re-page for the same episode.
func TestStepMarksDeliveredWhenSendSucceeds(t *testing.T) {
	store := newFakeStore(exhaustedFailedRun("r1"))
	al := &recordingAlerter{ch: make(chan RunState, 2)}
	s := newScheduler(store)
	s.SetAlerter(al)
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-al.ch:
	case <-time.After(2 * time.Second):
		t.Fatal("expected the alerter to be called")
	}
	waitFor(t, func() bool { return store.wasDelivered("r1") })
}

// A failed episode is retried, but not forever: a run stays failed for good, so
// without a ceiling a dead endpoint would be hit once per tick for the life of
// the run — the alert path DoSing the endpoint it is trying to reach.
func TestStepStopsRetryingAfterAttemptBudget(t *testing.T) {
	store := newFakeStore(exhaustedFailedRun("r1"))
	al := &failingAlerter{calls: make(chan RunState, 32)}
	s := newScheduler(store)
	s.SetAlerter(al)
	// Tick well past the budget. The fake does not simulate backoff, so every
	// tick that is allowed to claim will claim — which makes the ceiling the only
	// thing that can stop it.
	for i := 0; i < alertMaxAttempts+4; i++ {
		if err := s.Step(context.Background()); err != nil {
			t.Fatal(err)
		}
		waitFor(t, func() bool { return len(al.calls) > 0 || store.attemptsFor("r1") >= alertMaxAttempts })
		for len(al.calls) > 0 {
			<-al.calls
		}
	}
	if got := store.attemptsFor("r1"); got != alertMaxAttempts {
		t.Fatalf("attempts = %d, want exactly %d — the budget must cap retries", got, alertMaxAttempts)
	}
	if store.wasDelivered("r1") {
		t.Fatal("nothing was ever delivered; the episode must not be marked paged")
	}
}

// waitFor polls a condition briefly. The send runs in a goroutine detached from
// the tick, so an assertion about its effect cannot be made synchronously.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within 2s")
}
