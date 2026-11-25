package agentrpc

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// branchStore is a fakeStore that also records branch successes.
type branchStore struct {
	fakeStore
	branched []domain.BranchDecision
}

func (s *branchStore) ReportBranchSuccess(_ context.Context, _ auth.AgentIdentity, d domain.BranchDecision) error {
	s.branched = append(s.branched, d)
	return s.reportErr
}

func branchSpec(kind domain.BranchKind) TaskSpec {
	return TaskSpec{
		Operator: "python", Branch: &domain.BranchSpec{Kind: kind},
		DagTaskIDs: []string{"extract", "a", "b"}, DownstreamTaskIDs: []string{"a", "b"},
	}
}

func reportSuccess(t *testing.T, store Store, branch *agentv1.BranchDecision) *agentv1.ReportStateResponse {
	t.Helper()
	srv, a := newServer(store)
	resp, err := srv.ReportState(ctxWithToken(t, a), &agentv1.ReportStateRequest{
		State: agentv1.TaskState_TASK_STATE_SUCCESS, Branch: branch,
	})
	if err != nil {
		t.Fatalf("ReportState: %v", err)
	}
	return resp
}

// The server-side validation table of ADR 0064 D1, one test per row.

func TestBranchSuccessWithAValidDecisionIsPersisted(t *testing.T) {
	store := &branchStore{fakeStore: fakeStore{spec: branchSpec(domain.BranchKindBranch)}}
	reportSuccess(t, store, &agentv1.BranchDecision{FollowTaskIds: []string{"a"}})
	if len(store.reported) != 0 {
		t.Fatalf("plain reports %v, want the success through ReportBranchSuccess", store.reported)
	}
	if want := []domain.BranchDecision{{FollowTaskIDs: []string{"a"}}}; !reflect.DeepEqual(store.branched, want) {
		t.Errorf("branched = %+v, want %+v", store.branched, want)
	}
}

func TestBranchSuccessFollowingNothingIsPersisted(t *testing.T) {
	store := &branchStore{fakeStore: fakeStore{spec: branchSpec(domain.BranchKindBranch)}}
	reportSuccess(t, store, &agentv1.BranchDecision{})
	if len(store.branched) != 1 || len(store.branched[0].FollowTaskIDs) != 0 {
		t.Errorf("branched = %+v, want one decision following nothing", store.branched)
	}
}

func TestShortCircuitSuccessIsPersisted(t *testing.T) {
	store := &branchStore{fakeStore: fakeStore{spec: branchSpec(domain.BranchKindShortCircuit)}}
	reportSuccess(t, store, &agentv1.BranchDecision{ShortCircuitFalsy: true})
	if want := []domain.BranchDecision{{ShortCircuitFalsy: true}}; !reflect.DeepEqual(store.branched, want) {
		t.Errorf("branched = %+v, want %+v", store.branched, want)
	}
}

func assertFailedWith(t *testing.T, store *branchStore, reason string) {
	t.Helper()
	if len(store.branched) != 0 {
		t.Errorf("branched = %+v, want nothing persisted", store.branched)
	}
	if len(store.reported) != 1 || store.reported[0].state != domain.TaskStateFailed ||
		!strings.HasPrefix(store.reported[0].errMsg, reason+":") {
		t.Fatalf("reported = %+v, want one failure classified %s", store.reported, reason)
	}
}

func TestBranchSuccessWithAnUnknownIdFails(t *testing.T) {
	store := &branchStore{fakeStore: fakeStore{spec: branchSpec(domain.BranchKindBranch)}}
	reportSuccess(t, store, &agentv1.BranchDecision{FollowTaskIds: []string{"a", "nope"}})
	assertFailedWith(t, store, ReasonInvalidBranchDecision)
}

func TestBranchDecisionOfTheWrongKindFails(t *testing.T) {
	store := &branchStore{fakeStore: fakeStore{spec: branchSpec(domain.BranchKindBranch)}}
	reportSuccess(t, store, &agentv1.BranchDecision{ShortCircuitFalsy: true})
	assertFailedWith(t, store, ReasonInvalidBranchDecision)

	store = &branchStore{fakeStore: fakeStore{spec: branchSpec(domain.BranchKindShortCircuit)}}
	reportSuccess(t, store, &agentv1.BranchDecision{FollowTaskIds: []string{"a"}})
	assertFailedWith(t, store, ReasonInvalidBranchDecision)
}

func TestBranchSuccessWithoutADecisionFails(t *testing.T) {
	store := &branchStore{fakeStore: fakeStore{spec: branchSpec(domain.BranchKindBranch)}}
	reportSuccess(t, store, nil)
	assertFailedWith(t, store, ReasonBranchDecisionMissing)
}

func TestDecisionOnATaskThatIsNotABranchIsIgnored(t *testing.T) {
	store := &branchStore{fakeStore: fakeStore{spec: TaskSpec{Operator: "python"}}}
	reportSuccess(t, store, &agentv1.BranchDecision{FollowTaskIds: []string{"a"}})
	if len(store.branched) != 0 || len(store.reported) != 1 || store.reported[0].state != domain.TaskStateSuccess {
		t.Fatalf("reported %+v, branched %+v, want a plain success", store.reported, store.branched)
	}
}

// A branch decision on a failure report is ignored: only a success carries one.
func TestFailureOfABranchTaskIgnoresTheDecision(t *testing.T) {
	store := &branchStore{fakeStore: fakeStore{spec: branchSpec(domain.BranchKindBranch)}}
	srv, a := newServer(store)
	if _, err := srv.ReportState(ctxWithToken(t, a), &agentv1.ReportStateRequest{
		State: agentv1.TaskState_TASK_STATE_FAILED, ExitCode: 1, Branch: &agentv1.BranchDecision{},
	}); err != nil {
		t.Fatal(err)
	}
	if len(store.branched) != 0 || len(store.reported) != 1 || store.reported[0].state != domain.TaskStateFailed {
		t.Fatalf("reported %+v, branched %+v, want the failure as reported", store.reported, store.branched)
	}
}

func TestGetTaskSpecCarriesTheBranchFields(t *testing.T) {
	store := &fakeStore{spec: branchSpec(domain.BranchKindShortCircuit)}
	no := false
	store.spec.Branch.IgnoreDownstreamTriggerRules = &no
	srv, a := newServer(store)
	spec, err := srv.GetTaskSpec(ctxWithToken(t, a), &agentv1.GetTaskSpecRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if spec.GetBranch().GetKind() != "short_circuit" || spec.GetBranch().GetIgnoreDownstreamTriggerRules() {
		t.Errorf("branch = %+v", spec.GetBranch())
	}
	if !reflect.DeepEqual(spec.GetDagTaskIds(), []string{"extract", "a", "b"}) || !reflect.DeepEqual(spec.GetDownstreamTaskIds(), []string{"a", "b"}) {
		t.Errorf("ids = %v / %v", spec.GetDagTaskIds(), spec.GetDownstreamTaskIds())
	}
}

func TestGetTaskSpecOfATaskThatIsNotABranch(t *testing.T) {
	srv, a := newServer(&fakeStore{spec: TaskSpec{Operator: "python"}})
	spec, err := srv.GetTaskSpec(ctxWithToken(t, a), &agentv1.GetTaskSpecRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Branch != nil {
		t.Errorf("branch = %+v, want unset", spec.Branch)
	}
}
