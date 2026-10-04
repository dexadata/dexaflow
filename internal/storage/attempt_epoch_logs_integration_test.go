//go:build integration

// Package storage_test: logs and history of a try with several executions
// (ADR 0051 amendment, PR A5, #863).
//
// An infra re-place keeps the try, so one try can have several executions,
// each with its own attempt epoch. Each writes its own log object, the
// Airflow-compatible tries endpoint shows the try once (the latest
// execution's state), and reading the try's log serves every execution's
// stream in order.
package storage_test

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/logs"
	"github.com/dexadata/dexaflow/internal/storage"
)

func (f *staleHeartbeatFixture) logRef(try, epoch int) logs.Ref {
	var tenant string
	if err := f.pg.Pool.QueryRow(f.ctx,
		"SELECT tenant_id::text FROM task_instances WHERE id=$1::uuid", f.tiID).Scan(&tenant); err != nil {
		panic(err)
	}
	return logs.Ref{TenantID: tenant, DagID: f.dagID, RunID: f.runUUID, TaskID: "t", TryNumber: try, AttemptEpoch: epoch}
}

func writeLog(t *testing.T, sink logs.Sink, ref logs.Ref, msg string) {
	t.Helper()
	w, err := sink.Open(ref)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.WriteEvent(logs.Event{Time: time.Now().UTC(), Level: "info", Stream: "stdout", Message: msg}); err != nil {
		t.Fatalf("WriteEvent: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// infraReplaceAndRedispatch fails the running execution as agent_lost,
// re-places it off-budget (same try) and dispatches the replacement to
// running, returning the replacement's epoch.
func (f *staleHeartbeatFixture) infraReplaceAndRedispatch(t *testing.T) int {
	t.Helper()
	if ok, err := f.sched.MarkTaskAgentLost(f.ctx, f.tiID, f.tryNumber(t), f.attemptEpoch(t)); err != nil || !ok {
		t.Fatalf("MarkTaskAgentLost ok=%v err=%v", ok, err)
	}
	if ok, err := f.sched.ResetForInfraReplace(f.ctx, f.runUUID, "t"); err != nil || !ok {
		t.Fatalf("ResetForInfraReplace ok=%v err=%v", ok, err)
	}
	f.setState(t, "scheduled")
	id := f.dispatch(t)
	f.transition(t, domain.TaskStateQueued)
	f.transition(t, domain.TaskStateRunning)
	return id.AttemptEpoch
}

func streamMessages(t *testing.T, rc io.ReadCloser) []string {
	t.Helper()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	_ = rc.Close()
	var out []string
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		ev := logs.DecodeLine(line)
		if ev.Stream != "system" {
			out = append(out, ev.Message)
		}
	}
	return out
}

// TestTryWithSeveralExecutionsKeepsEveryLogAndShowsOnce is the #863 contract:
// a pre-upgrade execution (epoch 0) and two post-upgrade infra re-places of
// try 1 each keep their log; the try's log holds all three streams in order;
// the tries endpoint shows try 1 once, with the latest execution's state.
func TestTryWithSeveralExecutionsKeepsEveryLogAndShowsOnce(t *testing.T) {
	f := seedStaleHeartbeat(t, "epoch_logs")
	sink := logs.NewDiskSink(t.TempDir())
	reader := storage.NewLogReader(f.pg, sink, nil)

	writeLog(t, sink, f.logRef(1, 0), "legacy execution")
	e1 := f.infraReplaceAndRedispatch(t)
	writeLog(t, sink, f.logRef(1, e1), "first re-place")
	e2 := f.infraReplaceAndRedispatch(t)
	writeLog(t, sink, f.logRef(1, e2), "second re-place")
	if f.tryNumber(t) != 1 {
		t.Fatalf("precondition: infra re-places keep try 1")
	}

	rc, err := reader.ReadLogs(f.ctx, "default", f.dagID, f.runID, "t", 1)
	if err != nil {
		t.Fatalf("ReadLogs: %v", err)
	}
	if got := strings.Join(streamMessages(t, rc), "|"); got != "legacy execution|first re-place|second re-place" {
		t.Fatalf("the try's log must hold every execution's stream in order, got %q", got)
	}

	attempts, err := f.repo.ListTaskInstanceAttempts(f.ctx, "default", f.dagID, f.runID, "t")
	if err != nil {
		t.Fatalf("ListTaskInstanceAttempts: %v", err)
	}
	if len(attempts) != 1 || attempts[0].TryNumber != 1 || attempts[0].State != domain.TaskStateRunning {
		t.Fatalf("the tries endpoint must show try 1 once, with the latest execution's state; got %+v", attempts)
	}

	// The try ends in an application failure and is retried. Its archived row
	// is the latest execution, and its log is still served whole; try 2 starts
	// its own log.
	f.setState(t, "up_for_retry")
	if ok, err := f.sched.ResetForRetry(f.ctx, f.runUUID, "t"); err != nil || !ok {
		t.Fatalf("ResetForRetry ok=%v err=%v", ok, err)
	}
	var archived int
	if err := f.pg.Pool.QueryRow(f.ctx,
		"SELECT attempt_epoch FROM task_instance_history WHERE task_instance_id=$1::uuid AND try_number=1", f.tiID).Scan(&archived); err != nil {
		t.Fatalf("select archived epoch: %v", err)
	}
	if archived != e2 {
		t.Errorf("try 1's history row must be its latest execution (epoch %d), got epoch %d", e2, archived)
	}
	attempts, err = f.repo.ListTaskInstanceAttempts(f.ctx, "default", f.dagID, f.runID, "t")
	if err != nil {
		t.Fatalf("ListTaskInstanceAttempts: %v", err)
	}
	if len(attempts) != 2 || attempts[0].State != domain.TaskStateUpForRetry {
		t.Fatalf("want try 1 (up_for_retry) and try 2, got %+v", attempts)
	}

	f.setState(t, "scheduled")
	try2 := f.dispatch(t)
	writeLog(t, sink, f.logRef(2, try2.AttemptEpoch), "try two")
	rc, err = reader.ReadLogs(f.ctx, "default", f.dagID, f.runID, "t", 1)
	if err != nil {
		t.Fatalf("ReadLogs(try 1): %v", err)
	}
	if got := strings.Join(streamMessages(t, rc), "|"); got != "legacy execution|first re-place|second re-place" {
		t.Fatalf("an archived try's log must still hold every execution, got %q", got)
	}
	rc, err = reader.ReadLogs(f.ctx, "default", f.dagID, f.runID, "t", 2)
	if err != nil {
		t.Fatalf("ReadLogs(try 2): %v", err)
	}
	if got := strings.Join(streamMessages(t, rc), "|"); got != "try two" {
		t.Fatalf("try 2's log must hold only its own execution, got %q", got)
	}
}

// TestLegacyLogIsServedAsBefore: a try that ran entirely before the upgrade
// has one epoch-0 log, which is still found and served.
func TestLegacyLogIsServedAsBefore(t *testing.T) {
	f := seedStaleHeartbeat(t, "epoch_logs_legacy")
	sink := logs.NewDiskSink(t.TempDir())
	writeLog(t, sink, f.logRef(1, 0), "before the upgrade")
	rc, err := storage.NewLogReader(f.pg, sink, nil).ReadLogs(f.ctx, "default", f.dagID, f.runID, "t", 1)
	if err != nil {
		t.Fatalf("ReadLogs: %v", err)
	}
	if got := strings.Join(streamMessages(t, rc), "|"); got != "before the upgrade" {
		t.Fatalf("got %q", got)
	}
}
