package agentrpc

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// fakeLiveness is a scripted liveness predicate: it returns (live, err) and
// records how many times it was consulted so a test can prove the gate ran (or
// did not) on a given path.
type fakeLiveness struct {
	live  bool
	err   error
	calls int
}

func (f *fakeLiveness) IsTaskInstanceLive(_ context.Context, _, _ string, _ int) (bool, error) {
	f.calls++
	return f.live, f.err
}

// livenessEvent captures one RecordSecretLivenessDenial call — identity + kind +
// mode, never secret names or values.
type livenessEvent struct {
	tenant, dagID, runID, taskID, kind, mode string
	tryNumber                                int
}

type fakeLivenessAuditor struct {
	events []livenessEvent
}

func (f *fakeLivenessAuditor) RecordSecretLivenessDenial(_ context.Context, tenant, dagID, runID, taskID string, tryNumber int, kind, mode string) error {
	f.events = append(f.events, livenessEvent{tenant, dagID, runID, taskID, kind, mode, tryNumber})
	return nil
}

// TestSecretLivenessObserveDeliversWhenNotLive is the observe-mode contract and
// the strongest guard against a pipeline-breaking bug: a NOT-live TI does NOT
// deny in observe mode — the secrets are still delivered — and the
// would-have-denied is recorded on the audit surface. Observe mode must never
// change behavior.
func TestSecretLivenessObserveDeliversWhenNotLive(t *testing.T) {
	srv, a := newServer(&fakeStore{})
	sec := &fakeSecrets{vars: map[string]string{"FOO": "bar"}, conns: map[string]string{"pg": "postgres://u:p@h/db"}}
	audit := &fakeLivenessAuditor{}
	srv.SetSecrets(sec, true)
	srv.SetLivenessGate(&fakeLiveness{live: false}, LivenessObserve)
	srv.SetSecretLivenessAuditor(audit)
	ctx := ctxWithToken(t, a)

	vresp, err := srv.GetVariables(ctx, &agentv1.GetVariablesRequest{})
	if err != nil {
		t.Fatalf("observe mode must not deny a not-live TI: %v", err)
	}
	if vresp.Variables["FOO"] != "bar" {
		t.Errorf("observe mode must still deliver: got %v", vresp.Variables)
	}
	if _, err := srv.GetConnections(ctx, &agentv1.GetConnectionsRequest{}); err != nil {
		t.Fatalf("observe mode must not deny connections: %v", err)
	}
	if len(audit.events) != 2 {
		t.Fatalf("recorded %d liveness events, want 2 (one per kind): %+v", len(audit.events), audit.events)
	}
	for _, e := range audit.events {
		if e.mode != "observe" {
			t.Errorf("observe-mode event mode = %q, want observe", e.mode)
		}
		if e.tenant != "acme" || e.runID != "run-1" || e.taskID != "extract" {
			t.Errorf("liveness event identity = %+v, want tenant=acme run=run-1 task=extract", e)
		}
	}
}

// TestSecretLivenessEnforceDeniesWhenNotLive is the security side: in enforce
// mode a positive not-live result denies with PermissionDenied and records the
// denial.
func TestSecretLivenessEnforceDeniesWhenNotLive(t *testing.T) {
	srv, a := newServer(&fakeStore{})
	sec := &fakeSecrets{vars: map[string]string{"FOO": "bar"}, conns: map[string]string{"pg": "postgres://u:p@h/db"}}
	audit := &fakeLivenessAuditor{}
	srv.SetSecrets(sec, true)
	srv.SetLivenessGate(&fakeLiveness{live: false}, LivenessEnforce)
	srv.SetSecretLivenessAuditor(audit)
	ctx := ctxWithToken(t, a)

	if _, err := srv.GetVariables(ctx, &agentv1.GetVariablesRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("enforce mode + not-live GetVariables = %v, want PermissionDenied", err)
	}
	if _, err := srv.GetConnections(ctx, &agentv1.GetConnectionsRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("enforce mode + not-live GetConnections = %v, want PermissionDenied", err)
	}
	if len(audit.events) != 2 {
		t.Fatalf("recorded %d liveness events, want 2: %+v", len(audit.events), audit.events)
	}
	for _, e := range audit.events {
		if e.mode != "enforce" {
			t.Errorf("enforce-mode event mode = %q, want enforce", e.mode)
		}
	}
}

// TestSecretLivenessLiveAlwaysResolves is the availability invariant on both
// modes: a LIVE TI always resolves, never a false-deny.
func TestSecretLivenessLiveAlwaysResolves(t *testing.T) {
	for _, mode := range []string{LivenessObserve, LivenessEnforce} {
		t.Run(mode, func(t *testing.T) {
			srv, a := newServer(&fakeStore{})
			sec := &fakeSecrets{vars: map[string]string{"FOO": "bar"}}
			audit := &fakeLivenessAuditor{}
			srv.SetSecrets(sec, true)
			srv.SetLivenessGate(&fakeLiveness{live: true}, mode)
			srv.SetSecretLivenessAuditor(audit)

			vresp, err := srv.GetVariables(ctxWithToken(t, a), &agentv1.GetVariablesRequest{})
			if err != nil {
				t.Fatalf("%s mode + live TI must resolve: %v", mode, err)
			}
			if vresp.Variables["FOO"] != "bar" {
				t.Errorf("live TI must deliver: got %v", vresp.Variables)
			}
			if len(audit.events) != 0 {
				t.Errorf("a live TI must record no liveness event: %+v", audit.events)
			}
		})
	}
}

// TestSecretLivenessTransientErrorDoesNotDeny is the transient-error rule: an
// inconclusive liveness read (a DB blip) must NOT deny and must NOT warn-as-not-
// live, in EITHER mode. Denying on an errored check would break a live pipeline
// on a transient blip; the short token TTL already bounds a real one.
func TestSecretLivenessTransientErrorDoesNotDeny(t *testing.T) {
	for _, mode := range []string{LivenessObserve, LivenessEnforce} {
		t.Run(mode, func(t *testing.T) {
			srv, a := newServer(&fakeStore{})
			sec := &fakeSecrets{vars: map[string]string{"FOO": "bar"}}
			audit := &fakeLivenessAuditor{}
			srv.SetSecrets(sec, true)
			srv.SetLivenessGate(&fakeLiveness{err: errors.New("db blip")}, mode)
			srv.SetSecretLivenessAuditor(audit)

			vresp, err := srv.GetVariables(ctxWithToken(t, a), &agentv1.GetVariablesRequest{})
			if err != nil {
				t.Fatalf("%s mode: an inconclusive liveness read must not deny: %v", mode, err)
			}
			if vresp.Variables["FOO"] != "bar" {
				t.Errorf("%s mode: must still deliver on an inconclusive read: got %v", mode, vresp.Variables)
			}
			if len(audit.events) != 0 {
				t.Errorf("an inconclusive read must not record a not-live event: %+v", audit.events)
			}
		})
	}
}

// TestSecretLivenessNoCheckerDelivers proves the gate is opt-in: with no
// liveness checker configured (the pre-E2 wiring), delivery is unchanged.
func TestSecretLivenessNoCheckerDelivers(t *testing.T) {
	srv, a := newServer(&fakeStore{})
	sec := &fakeSecrets{vars: map[string]string{"FOO": "bar"}}
	srv.SetSecrets(sec, true) // no SetLivenessGate

	vresp, err := srv.GetVariables(ctxWithToken(t, a), &agentv1.GetVariablesRequest{})
	if err != nil {
		t.Fatalf("no checker configured must deliver: %v", err)
	}
	if vresp.Variables["FOO"] != "bar" {
		t.Errorf("no checker must deliver the full set: got %v", vresp.Variables)
	}
}

// TestSecretLivenessGateIsSecretPathOnly locks D2: the liveness gate wraps the
// secret RPCs only, never the shared identify(). Heartbeat and ReportState are
// designed to run for a superseded TI so they can return the terminate signal;
// gating them on liveness would break that. Even with a not-live checker in
// enforce mode, Heartbeat and Register must not consult it and must not be
// denied by it.
func TestSecretLivenessGateIsSecretPathOnly(t *testing.T) {
	live := &fakeLiveness{live: false}
	srv, a := newServer(&fakeStore{})
	srv.SetSecrets(&fakeSecrets{}, true)
	srv.SetLivenessGate(live, LivenessEnforce)
	ctx := ctxWithToken(t, a)

	if _, err := srv.Heartbeat(ctx, &agentv1.HeartbeatRequest{}); err != nil {
		t.Fatalf("Heartbeat must never be gated on liveness: %v", err)
	}
	if _, err := srv.Register(ctx, &agentv1.RegisterRequest{}); err != nil {
		t.Fatalf("Register must never be gated on liveness: %v", err)
	}
	if _, err := srv.ReportState(ctx, &agentv1.ReportStateRequest{State: agentv1.TaskState_TASK_STATE_RUNNING}); err != nil {
		t.Fatalf("ReportState must never be gated on liveness: %v", err)
	}
	if live.calls != 0 {
		t.Errorf("liveness checker consulted %d times on non-secret paths, want 0", live.calls)
	}
}
