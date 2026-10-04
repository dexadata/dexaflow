package agentrpc

import (
	"context"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// epochFencingStore answers like the storage fence (ADR 0051 amendment): a
// report applies only for the row's current epoch, a token with no epoch read
// as epoch 0, and a heartbeat with no epoch is not fenced on it.
type epochFencingStore struct {
	fakeStore
	current int
}

func (s *epochFencingStore) fenced(id auth.AgentIdentity) bool {
	epoch := 0
	if id.HasAttemptEpoch {
		epoch = id.AttemptEpoch
	}
	return epoch != s.current
}

func (s *epochFencingStore) ReportState(ctx context.Context, id auth.AgentIdentity, st domain.TaskState, exit int, msg string) error {
	if s.fenced(id) {
		return ErrStaleReport
	}
	return s.fakeStore.ReportState(ctx, id, st, exit, msg)
}

// RecordHeartbeat matches a claim-less token on the try alone, like the SQL.
func (s *epochFencingStore) RecordHeartbeat(ctx context.Context, id auth.AgentIdentity) error {
	if id.HasAttemptEpoch && s.fenced(id) {
		return ErrStaleReport
	}
	return s.fakeStore.RecordHeartbeat(ctx, id)
}

func (s *epochFencingStore) Reschedule(ctx context.Context, id auth.AgentIdentity, at time.Time) error {
	if s.fenced(id) {
		return ErrStaleReport
	}
	return s.fakeStore.Reschedule(ctx, id, at)
}

// countingLegacyRecorder counts legacy-token uses.
type countingLegacyRecorder struct{ n int }

func (c *countingLegacyRecorder) RecordLegacyAttemptToken() { c.n++ }

func ctxForIdentity(t *testing.T, a *auth.JWTAuthenticator, id auth.AgentIdentity) context.Context {
	t.Helper()
	token, err := a.IssueAgentToken(id, time.Hour)
	if err != nil {
		t.Fatalf("IssueAgentToken: %v", err)
	}
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
}

func epochTokenIdentity(epoch int) auth.AgentIdentity {
	id := testIdentity()
	id.AttemptEpoch, id.HasAttemptEpoch = epoch, true
	return id
}

// TestStaleEpochGetsShouldTerminate: an agent whose token names a superseded
// epoch of the same try is told to terminate on its report, its heartbeat and
// its reschedule, and the live epoch is not (#911).
func TestStaleEpochGetsShouldTerminate(t *testing.T) {
	store := &epochFencingStore{current: 2}
	srv, a := newServer(store)
	stale := ctxForIdentity(t, a, epochTokenIdentity(1))
	live := ctxForIdentity(t, a, epochTokenIdentity(2))

	rep, err := srv.ReportState(stale, &agentv1.ReportStateRequest{State: agentv1.TaskState_TASK_STATE_RUNNING})
	if err != nil || !rep.GetShouldTerminate() {
		t.Errorf("stale report: should_terminate=%v err=%v, want true", rep.GetShouldTerminate(), err)
	}
	hb, err := srv.Heartbeat(stale, &agentv1.HeartbeatRequest{})
	if err != nil || !hb.GetShouldTerminate() {
		t.Errorf("stale heartbeat: should_terminate=%v err=%v, want true", hb.GetShouldTerminate(), err)
	}
	rs, err := srv.ReportState(stale, &agentv1.ReportStateRequest{
		State: agentv1.TaskState_TASK_STATE_UP_FOR_RESCHEDULE, RescheduleAt: timestamppb.New(time.Now().Add(time.Minute)),
	})
	if err != nil || !rs.GetShouldTerminate() {
		t.Errorf("stale reschedule: should_terminate=%v err=%v, want true", rs.GetShouldTerminate(), err)
	}

	rep, err = srv.ReportState(live, &agentv1.ReportStateRequest{State: agentv1.TaskState_TASK_STATE_RUNNING})
	if err != nil || rep.GetShouldTerminate() {
		t.Errorf("live report: should_terminate=%v err=%v, want false", rep.GetShouldTerminate(), err)
	}
	hb, err = srv.Heartbeat(live, &agentv1.HeartbeatRequest{})
	if err != nil || hb.GetShouldTerminate() {
		t.Errorf("live heartbeat: should_terminate=%v err=%v, want false", hb.GetShouldTerminate(), err)
	}

	// A claim-less token (pre-upgrade, or stripped by an old replica mid-rollout)
	// keeps heartbeating, so the attempt is not killed for the missing claim, but
	// its report is still fenced as epoch 0.
	legacy := ctxForIdentity(t, a, testIdentity())
	hb, err = srv.Heartbeat(legacy, &agentv1.HeartbeatRequest{})
	if err != nil || hb.GetShouldTerminate() {
		t.Errorf("claim-less heartbeat: should_terminate=%v err=%v, want false", hb.GetShouldTerminate(), err)
	}
	rep, err = srv.ReportState(legacy, &agentv1.ReportStateRequest{State: agentv1.TaskState_TASK_STATE_RUNNING})
	if err != nil || !rep.GetShouldTerminate() {
		t.Errorf("claim-less report on an epoch-2 row: should_terminate=%v err=%v, want true", rep.GetShouldTerminate(), err)
	}
}

// TestLegacyAttemptTokenIsMetered: a task token without the epoch claim is
// accepted under the epoch-0 rule and counted, so operators can see when the
// last legacy credential is gone before the next release rejects them. A
// token with the claim, and a warm-worker credential, are not counted.
func TestLegacyAttemptTokenIsMetered(t *testing.T) {
	store := &epochFencingStore{current: 0}
	srv, a := newServer(store)
	rec := &countingLegacyRecorder{}
	srv.SetLegacyTokenRecorder(rec)

	if _, err := srv.Heartbeat(ctxForIdentity(t, a, testIdentity()), &agentv1.HeartbeatRequest{}); err != nil {
		t.Fatalf("legacy heartbeat: %v", err)
	}
	if rec.n != 1 {
		t.Errorf("a legacy task token must be metered once per call, got %d", rec.n)
	}
	if _, err := srv.Heartbeat(ctxForIdentity(t, a, epochTokenIdentity(0)), &agentv1.HeartbeatRequest{}); err != nil {
		t.Fatalf("epoch heartbeat: %v", err)
	}
	if rec.n != 1 {
		t.Errorf("a token carrying the claim (even epoch 0) is not legacy, count=%d", rec.n)
	}
}
