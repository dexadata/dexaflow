package scheduler

import (
	"reflect"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

// The branch filter of ADR 0064 D2, the NotPreviouslySkippedDep analog: a none
// task whose direct branch parent succeeded with a decision that does not
// follow it is skipped before its trigger rule is read.

func branchTask(id string, deps ...string) domain.TaskSpec {
	return domain.TaskSpec{TaskID: id, Type: domain.TaskTypePython, DependsOn: deps, Branch: &domain.BranchSpec{Kind: domain.BranchKindBranch}}
}

func shortCircuit(id string, ignore *bool, deps ...string) domain.TaskSpec {
	return domain.TaskSpec{TaskID: id, Type: domain.TaskTypePython, DependsOn: deps,
		Branch: &domain.BranchSpec{Kind: domain.BranchKindShortCircuit, IgnoreDownstreamTriggerRules: ignore}}
}

func task(id string, rule domain.TriggerRule, deps ...string) domain.TaskSpec {
	return domain.TaskSpec{TaskID: id, Type: domain.TaskTypePython, DependsOn: deps, TriggerRule: rule}
}

func follow(ids ...string) *domain.BranchDecision {
	return &domain.BranchDecision{FollowTaskIDs: ids}
}

func branchRun(tasks []domain.TaskSpec, states map[string]domain.TaskState, decisions map[string]*domain.BranchDecision) map[string]domain.TaskState {
	return planMap(RunState{Tasks: tasks, States: states, BranchDecision: decisions})
}

const (
	sNone    = domain.TaskStateNone
	sSuccess = domain.TaskStateSuccess
	sSkipped = domain.TaskStateSkipped
	sSched   = domain.TaskStateScheduled
)

func TestBranchFollowsOneChoice(t *testing.T) {
	tasks := []domain.TaskSpec{branchTask("pick"), task("a", "", "pick"), task("b", "", "pick")}
	got := branchRun(tasks, map[string]domain.TaskState{"pick": sSuccess, "a": sNone, "b": sNone},
		map[string]*domain.BranchDecision{"pick": follow("a")})
	want := map[string]domain.TaskState{"a": sSched, "b": sSkipped}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("plan = %v, want %v", got, want)
	}
}

func TestBranchFollowsSeveralChoices(t *testing.T) {
	tasks := []domain.TaskSpec{branchTask("pick"), task("a", "", "pick"), task("b", "", "pick"), task("c", "", "pick")}
	got := branchRun(tasks, map[string]domain.TaskState{"pick": sSuccess, "a": sNone, "b": sNone, "c": sNone},
		map[string]*domain.BranchDecision{"pick": follow("a", "c")})
	want := map[string]domain.TaskState{"a": sSched, "b": sSkipped, "c": sSched}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("plan = %v, want %v", got, want)
	}
}

func TestBranchNoneSkipsEveryChild(t *testing.T) {
	tasks := []domain.TaskSpec{branchTask("pick"), task("a", "", "pick"), task("b", domain.TriggerRuleAlways, "pick")}
	got := branchRun(tasks, map[string]domain.TaskState{"pick": sSuccess, "a": sNone, "b": sNone},
		map[string]*domain.BranchDecision{"pick": follow()})
	want := map[string]domain.TaskState{"a": sSkipped, "b": sSkipped}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("plan = %v, want %v", got, want)
	}
}

// TestBranchEmptyBranchJoin is Airflow's empty-branch rule: in pick -> join
// and pick -> t1 -> join, choosing t1 keeps join, which is downstream of t1.
func TestBranchEmptyBranchJoin(t *testing.T) {
	tasks := []domain.TaskSpec{branchTask("pick"), task("t1", "", "pick"), task("join", domain.TriggerRuleNoneFailedMinOneSuccess, "pick", "t1")}
	got := branchRun(tasks, map[string]domain.TaskState{"pick": sSuccess, "t1": sNone, "join": sNone},
		map[string]*domain.BranchDecision{"pick": follow("t1")})
	want := map[string]domain.TaskState{"t1": sSched}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("plan = %v, want %v (join waits on t1)", got, want)
	}
}

// TestBranchDiamondJoin is the canonical Airflow example: choose one of two,
// join with none_failed_min_one_success. The skip reaches the join through
// its trigger rule, which runs it because one upstream succeeded.
func TestBranchDiamondJoin(t *testing.T) {
	tasks := []domain.TaskSpec{branchTask("pick"), task("a", "", "pick"), task("b", "", "pick"),
		task("join", domain.TriggerRuleNoneFailedMinOneSuccess, "a", "b")}
	decisions := map[string]*domain.BranchDecision{"pick": follow("a")}
	if got := branchRun(tasks, map[string]domain.TaskState{"pick": sSuccess, "a": sSuccess, "b": sSkipped, "join": sNone}, decisions); got["join"] != sSched {
		t.Errorf("join = %q, want scheduled", got["join"])
	}
	if got := branchRun(tasks, map[string]domain.TaskState{"pick": sSuccess, "a": sSuccess, "b": sSkipped, "join": sNone},
		decisions); got["b"] != "" {
		t.Errorf("an already skipped child must not be planned again, got %q", got["b"])
	}
}

// TestBranchChoiceThatIsNotAChild: a valid id that is not a direct child is
// accepted, as in Airflow, and selects only what lies below it: a task in
// another part of the DAG selects none of the children, an ancestor of the
// branch task selects all of them.
func TestBranchChoiceThatIsNotAChild(t *testing.T) {
	tasks := []domain.TaskSpec{task("root", ""), task("elsewhere", ""), branchTask("pick", "root"), task("a", "", "pick"), task("b", "", "pick")}
	states := map[string]domain.TaskState{"root": sSuccess, "elsewhere": sSuccess, "pick": sSuccess, "a": sNone, "b": sNone}
	got := branchRun(tasks, states, map[string]*domain.BranchDecision{"pick": follow("elsewhere")})
	if want := (map[string]domain.TaskState{"a": sSkipped, "b": sSkipped}); !reflect.DeepEqual(got, want) {
		t.Errorf("choosing an unrelated task: plan = %v, want %v", got, want)
	}
	got = branchRun(tasks, states, map[string]*domain.BranchDecision{"pick": follow("root")})
	if want := (map[string]domain.TaskState{"a": sSched, "b": sSched}); !reflect.DeepEqual(got, want) {
		t.Errorf("choosing an ancestor: plan = %v, want %v", got, want)
	}
}

// TestBranchWithoutDecisionFiltersNothing: a branch task marked success by
// hand has no decision, so every child is eligible, as in Airflow.
func TestBranchWithoutDecisionFiltersNothing(t *testing.T) {
	tasks := []domain.TaskSpec{branchTask("pick"), task("a", "", "pick"), task("b", "", "pick")}
	got := branchRun(tasks, map[string]domain.TaskState{"pick": sSuccess, "a": sNone, "b": sNone}, nil)
	want := map[string]domain.TaskState{"a": sSched, "b": sSched}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("plan = %v, want %v", got, want)
	}
}

// TestShortCircuitFalsyIgnoringTriggerRules skips every descendant, one also
// reachable from a path the short-circuit does not cover included.
func TestShortCircuitFalsyIgnoringTriggerRules(t *testing.T) {
	tasks := []domain.TaskSpec{shortCircuit("gate", nil), task("other", ""), task("a", "", "gate"),
		task("b", domain.TriggerRuleAllDone, "a", "other")}
	got := branchRun(tasks, map[string]domain.TaskState{"gate": sSuccess, "other": sSuccess, "a": sSkipped, "b": sNone},
		map[string]*domain.BranchDecision{"gate": {ShortCircuitFalsy: true}})
	if got["b"] != sSkipped {
		t.Errorf("b = %q, want skipped", got["b"])
	}
}

// TestShortCircuitFalsyRespectingTriggerRules skips only the direct children
// and lets the trigger rules decide below them.
func TestShortCircuitFalsyRespectingTriggerRules(t *testing.T) {
	no := false
	tasks := []domain.TaskSpec{shortCircuit("gate", &no), task("a", "", "gate"), task("b", domain.TriggerRuleAllDone, "a")}
	decisions := map[string]*domain.BranchDecision{"gate": {ShortCircuitFalsy: true}}
	if got := branchRun(tasks, map[string]domain.TaskState{"gate": sSuccess, "a": sNone, "b": sNone}, decisions); got["a"] != sSkipped {
		t.Errorf("a = %q, want skipped", got["a"])
	}
	if got := branchRun(tasks, map[string]domain.TaskState{"gate": sSuccess, "a": sSkipped, "b": sNone}, decisions); got["b"] != sSched {
		t.Errorf("b = %q, want scheduled by all_done", got["b"])
	}
}

func TestShortCircuitTruthySkipsNothing(t *testing.T) {
	tasks := []domain.TaskSpec{shortCircuit("gate", nil), task("a", "", "gate")}
	got := branchRun(tasks, map[string]domain.TaskState{"gate": sSuccess, "a": sNone},
		map[string]*domain.BranchDecision{"gate": {}})
	if got["a"] != sSched {
		t.Errorf("a = %q, want scheduled", got["a"])
	}
}

// TestBranchReskipsAClearedChild: a cleared child goes back to none, and the
// persisted decision skips it again.
func TestBranchReskipsAClearedChild(t *testing.T) {
	tasks := []domain.TaskSpec{branchTask("pick"), task("a", "", "pick"), task("b", "", "pick")}
	got := branchRun(tasks, map[string]domain.TaskState{"pick": sSuccess, "a": sSuccess, "b": sNone},
		map[string]*domain.BranchDecision{"pick": follow("a")})
	if got["b"] != sSkipped {
		t.Errorf("cleared b = %q, want skipped again", got["b"])
	}
}

// TestBranchAlwaysChild: an always child is scheduled before its branch parent
// decides, and re-skipped when cleared after, as NotPreviouslySkippedDep does.
func TestBranchAlwaysChild(t *testing.T) {
	tasks := []domain.TaskSpec{branchTask("pick"), task("a", "", "pick"), task("x", domain.TriggerRuleAlways, "pick")}
	if got := branchRun(tasks, map[string]domain.TaskState{"pick": domain.TaskStateRunning, "a": sNone, "x": sNone}, nil); got["x"] != sSched {
		t.Errorf("always x before the decision = %q, want scheduled", got["x"])
	}
	got := branchRun(tasks, map[string]domain.TaskState{"pick": sSuccess, "a": sNone, "x": sNone},
		map[string]*domain.BranchDecision{"pick": follow("a")})
	if got["x"] != sSkipped {
		t.Errorf("cleared always x = %q, want skipped", got["x"])
	}
}

func TestBranchParentRetryingKeepsChildrenWaiting(t *testing.T) {
	tasks := []domain.TaskSpec{branchTask("pick"), task("a", "", "pick")}
	got := branchRun(tasks, map[string]domain.TaskState{"pick": domain.TaskStateUpForRetry, "a": sNone},
		map[string]*domain.BranchDecision{"pick": follow()})
	if _, planned := got["a"]; planned {
		t.Errorf("a = %q, want it to wait for the retry", got["a"])
	}
}

func TestBranchFilterIsDeterministic(t *testing.T) {
	tasks := []domain.TaskSpec{branchTask("pick"), task("a", "", "pick"), task("b", "", "pick"), task("c", "", "pick")}
	run := RunState{Tasks: tasks, States: map[string]domain.TaskState{"pick": sSuccess, "a": sNone, "b": sNone, "c": sNone},
		BranchDecision: map[string]*domain.BranchDecision{"pick": follow("b")}}
	first := PlanRun(run)
	for range 20 {
		if got := PlanRun(run); !reflect.DeepEqual(got, first) {
			t.Fatalf("PlanRun = %v, then %v", first, got)
		}
	}
}
