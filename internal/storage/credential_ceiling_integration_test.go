//go:build integration

// Package storage_test: the credential-ceiling mark (#1461). An attempt that
// outlived auth.max_attempt_credential_lifetime is failed as a task failure
// (last_failure_kind NULL, so the retry policy applies), never as an infra loss
// that the planner would re-place with a fresh credential.
package storage_test

import (
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

// TestAgentLostCandidateCarriesStartedAt: the reaper judges the ceiling from
// the attempt's running-since stamp, so the candidate list must carry it.
func TestAgentLostCandidateCarriesStartedAt(t *testing.T) {
	f := seedStaleHeartbeat(t, "ceiling_started")
	cands, err := f.sched.ListAgentLostCandidates(f.ctx)
	if err != nil {
		t.Fatalf("ListAgentLostCandidates: %v", err)
	}
	c := findAgentLostCandidate(cands, f.runUUID)
	if c == nil {
		t.Fatalf("the stale-heartbeat TI must be a candidate, got %+v", cands)
	}
	if c.StartedAt.IsZero() {
		t.Errorf("candidate StartedAt must be the running transition's stamp, got zero")
	}
}

// TestMarkTaskCredentialCeiling: the mark fails the running attempt with the
// credential_ceiling reason as a task failure, even with provisional infra marks
// on (Kubernetes): it is not an infra guess, so it is neither provisional nor
// re-placeable. It is fenced on (try, epoch) and idempotent like the infra marks.
func TestMarkTaskCredentialCeiling(t *testing.T) {
	f := seedStaleHeartbeat(t, "ceiling_mark")
	f.sched.SetProvisionalInfraMarks(true)
	try, epoch := f.tryNumber(t), f.attemptEpoch(t)

	if ok, err := f.sched.MarkTaskCredentialCeiling(f.ctx, f.tiID, try, epoch+1); err != nil || ok {
		t.Fatalf("a mark computed for another epoch must be a no-op: ok=%v err=%v", ok, err)
	}
	if ok, err := f.sched.MarkTaskCredentialCeiling(f.ctx, f.tiID, try+1, epoch); err != nil || ok {
		t.Fatalf("a mark computed for another try must be a no-op: ok=%v err=%v", ok, err)
	}
	if ok, err := f.sched.MarkTaskCredentialCeiling(f.ctx, f.tiID, try, epoch); err != nil || !ok {
		t.Fatalf("MarkTaskCredentialCeiling ok=%v err=%v", ok, err)
	}

	var (
		state, msg string
		kind       *string
	)
	if err := f.pg.Pool.QueryRow(f.ctx,
		"SELECT state::text, COALESCE(error_message,''), last_failure_kind FROM task_instances WHERE id=$1::uuid",
		f.tiID).Scan(&state, &msg, &kind); err != nil {
		t.Fatalf("select ti: %v", err)
	}
	if state != string(domain.TaskStateFailed) {
		t.Errorf("state = %q, want failed", state)
	}
	if !strings.HasPrefix(msg, "credential_ceiling:") || !strings.Contains(msg, "auth.max_attempt_credential_lifetime") {
		t.Errorf("error_message = %q, want the credential_ceiling reason naming the setting", msg)
	}
	if kind != nil {
		t.Errorf("last_failure_kind = %q, want NULL (a task failure, subject to retries)", *kind)
	}
	if at := f.confirmedAt(t); at != nil {
		t.Errorf("a task failure carries no infra confirmation, got %v", at)
	}

	runs, err := f.sched.ActiveRuns(f.ctx)
	if err != nil {
		t.Fatalf("ActiveRuns: %v", err)
	}
	for _, r := range runs {
		if r.RunID == f.runUUID && (r.InfraFailed["t"] || r.InfraProvisional["t"]) {
			t.Errorf("the planner must not see a credential-ceiling failure as infra: failed=%v provisional=%v",
				r.InfraFailed["t"], r.InfraProvisional["t"])
		}
	}
	rows, err := f.sched.ListProvisionalInfraFailures(f.ctx)
	if err != nil {
		t.Fatalf("ListProvisionalInfraFailures: %v", err)
	}
	for _, r := range rows {
		if r.TaskInstanceID == f.tiID {
			t.Errorf("a credential-ceiling failure must not wait on the reconciler's confirmation")
		}
	}

	if ok, err := f.sched.MarkTaskCredentialCeiling(f.ctx, f.tiID, try, epoch); err != nil || ok {
		t.Errorf("a second mark on a failed TI must be a no-op: ok=%v err=%v", ok, err)
	}
}
