package scheduler

import (
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
)

// provisionalRun is a run whose root task "a" was failed as infra by a reaper
// on Kubernetes and not yet confirmed by the reconciler (ADR 0052 amendment,
// part 2), ended `ago` before now.
func provisionalRun(ago time.Duration, infraAttempts int) RunState {
	now := time.Now().UTC()
	ended := now.Add(-ago)
	return RunState{
		RunID:            "run-1",
		Tasks:            linear(),
		States:           map[string]domain.TaskState{"a": domain.TaskStateFailed, "b": domain.TaskStateNone},
		Tries:            map[string]int{"a": 1},
		MaxTries:         map[string]int{"a": 1},
		InfraFailed:      map[string]bool{"a": true},
		InfraProvisional: map[string]bool{"a": true},
		InfraAttempts:    map[string]int{"a": infraAttempts},
		EndedAt:          map[string]*time.Time{"a": &ended},
		Now:              now,
	}
}

// TestProvisionalInfraFailureIsNotReplaced: while the mark waits for the
// reconciler's confirmation, the task is not re-placed even after its re-place
// backoff has elapsed, so a durable SUCCESS record can still settle it.
func TestProvisionalInfraFailureIsNotReplaced(t *testing.T) {
	run := provisionalRun(dispatchBackoffBase+infraReplaceJitterWindow, 0)
	if run.Now.Sub(*run.EndedAt["a"]) >= InfraConfirmMaxWait {
		t.Fatalf("precondition: inside the confirmation window")
	}
	if got, ok := planMap(run)["a"]; ok {
		t.Errorf("a provisional infra failure must wait for confirmation, got a transition to %q", got)
	}
	if _, done := FinalizeRun(run); done {
		t.Error("a provisional infra failure keeps the run active")
	}
}

// TestProvisionalInfraFailureWithSpentBudgetKeepsDownstreamWaiting: even with
// the infra budget spent, a provisional mark is not a terminal failure yet, so
// the downstream is not condemned on a guess and the run does not finalize.
func TestProvisionalInfraFailureWithSpentBudgetKeepsDownstreamWaiting(t *testing.T) {
	run := provisionalRun(10*time.Second, infraMaxAttempts)
	got := planMap(run)
	if _, ok := got["b"]; ok {
		t.Errorf("downstream must wait while the upstream's infra mark is provisional, got %q", got["b"])
	}
	if _, done := FinalizeRun(run); done {
		t.Error("a provisional mark with a spent budget keeps the run active")
	}
}

// TestConfirmedInfraFailureIsReplaced: once confirmed, the planner re-places as
// before, after its backoff.
func TestConfirmedInfraFailureIsReplaced(t *testing.T) {
	run := provisionalRun(dispatchBackoffBase+infraReplaceJitterWindow, 0)
	run.InfraProvisional = nil
	if got := planMap(run)["a"]; got != domain.TaskStateNone {
		t.Errorf("a confirmed infra failure past its backoff re-places, got %q", got)
	}
}

// TestInfraConfirmValveOpensAfterTheBound: with no confirmation within
// InfraConfirmMaxWait of ended_at, the planner proceeds as it would for a
// confirmed mark: re-place within budget, condemn downstream when spent.
func TestInfraConfirmValveOpensAfterTheBound(t *testing.T) {
	run := provisionalRun(InfraConfirmMaxWait+time.Second, 0)
	if got := planMap(run)["a"]; got != domain.TaskStateNone {
		t.Errorf("past the valve the provisional mark is re-placed, got %q", got)
	}
	spent := provisionalRun(InfraConfirmMaxWait+time.Second, infraMaxAttempts)
	if got := planMap(spent)["b"]; got != domain.TaskStateUpstreamFailed {
		t.Errorf("past the valve a spent budget condemns downstream, got %q", got)
	}
	spent.States["b"] = domain.TaskStateUpstreamFailed
	if _, done := FinalizeRun(spent); !done {
		t.Error("past the valve a spent budget finalizes the run")
	}
}

// TestInfraConfirmValveTreatsMissingDataAsOpen: absent ended_at or a zero clock
// falls back to today's behavior, as every other planner gate does.
func TestInfraConfirmValveTreatsMissingDataAsOpen(t *testing.T) {
	run := provisionalRun(time.Second, 0)
	run.EndedAt = nil
	if got := planMap(run)["a"]; got != domain.TaskStateNone {
		t.Errorf("no ended_at: proceed as today, got %q", got)
	}
}
