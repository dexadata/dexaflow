package scheduler

import (
	"context"
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
)

func TestTaskGraphLookupFindsFirstOccurrence(t *testing.T) {
	tasks := []domain.TaskSpec{
		{TaskID: "a"},
		{TaskID: "b", DependsOn: []string{"a"}},
		{TaskID: "a", Type: "dup"},
	}
	g := NewTaskGraph(tasks)
	for id, want := range map[string]int{"a": 0, "b": 1} {
		got, ok := g.Lookup(id)
		if !ok || got != want {
			t.Errorf("Lookup(%q) = %d, %v; want %d, true", id, got, ok, want)
		}
	}
	if _, ok := g.Lookup("missing"); ok {
		t.Error("Lookup of an unknown task must report false")
	}
}

// TestPlanRunMatchesMapReference proves the index-addressed planner decides
// exactly what the map-keyed planner it replaced decided. referencePlanRun below
// is that planner, kept verbatim as the oracle. Randomized runs cover every task
// state, trigger rule, retry, reschedule, infra and dispatch-backoff rail, both
// admission gates, dependencies on tasks outside the task list, state rows for
// tasks outside the task list, and duplicate task ids. Each run is planned with
// no graph (PlanRun builds one) and with a prebuilt graph (the store's path).
func TestPlanRunMatchesMapReference(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for i := range 5000 {
		run := randomRun(rng, i)
		want := referencePlanRun(run)
		if got := PlanRun(run); !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d without graph:\n got %v\nwant %v\nrun %+v", i, got, want, run)
		}
		run.Graph = NewTaskGraph(run.Tasks)
		if got := PlanRun(run); !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d with graph:\n got %v\nwant %v\nrun %+v", i, got, want, run)
		}
	}
}

// TestStepDispatchesWithPrebuiltGraph pins that a run carrying the store's
// prebuilt graph dispatches the right task specs: the dispatcher must receive
// each queued task's own spec, looked up by id.
func TestStepDispatchesWithPrebuiltGraph(t *testing.T) {
	tasks := []domain.TaskSpec{
		{TaskID: "root", Type: "python"},
		{TaskID: "x", Type: "bash", DependsOn: []string{"root"}},
		{TaskID: "y", Type: "python", DependsOn: []string{"root"}},
	}
	run := RunState{
		RunID: "r1", DagID: "d", State: domain.DagRunStateRunning, Tasks: tasks,
		States: map[string]domain.TaskState{
			"root": domain.TaskStateSuccess, "x": domain.TaskStateScheduled, "y": domain.TaskStateScheduled,
		},
		Graph: NewTaskGraph(tasks),
	}
	store := newFakeStore(run)
	disp := &recordingDispatcher{}
	s := newScheduler(store)
	s.SetDispatcher(disp)
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := map[string]domain.TaskType{"x": "bash", "y": "python"}
	if !reflect.DeepEqual(disp.types, want) {
		t.Errorf("dispatched %v, want %v", disp.types, want)
	}
}

// recordingDispatcher records the task type each dispatched task id carried.
type recordingDispatcher struct{ types map[string]domain.TaskType }

func (d *recordingDispatcher) Dispatch(_ context.Context, _, _, _ string, task domain.TaskSpec) (executor.Disposition, error) {
	if d.types == nil {
		d.types = map[string]domain.TaskType{}
	}
	d.types[task.TaskID] = task.Type
	return executor.Dispatched, nil
}

var planStates = []domain.TaskState{
	"", domain.TaskStateNone, domain.TaskStateScheduled, domain.TaskStateQueued,
	domain.TaskStateRunning, domain.TaskStateSuccess, domain.TaskStateFailed,
	domain.TaskStateSkipped, domain.TaskStateUpstreamFailed, domain.TaskStateUpForRetry,
	domain.TaskStateUpForReschedule,
}

var allTriggerRules = []domain.TriggerRule{
	"", domain.TriggerRuleAllSuccess, domain.TriggerRuleAllFailed, domain.TriggerRuleAllDone,
	domain.TriggerRuleOneSuccess, domain.TriggerRuleOneFailed,
}

// randomRun builds a small run with random topology and per-task state.
func randomRun(rng *rand.Rand, seq int) RunState {
	n := 1 + rng.IntN(12)
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	run := RunState{
		RunID: fmt.Sprintf("run-%d", seq), DagID: "d", TenantID: "t", State: domain.DagRunStateRunning,
		States: map[string]domain.TaskState{}, Tries: map[string]int{}, MaxTries: map[string]int{},
		EndedAt: map[string]*time.Time{}, RetryDelaySeconds: map[string]int{},
		RescheduleAt: map[string]*time.Time{}, NextDispatchAt: map[string]*time.Time{},
		InfraFailed: map[string]bool{}, InfraAttempts: map[string]int{},
	}
	if rng.IntN(2) == 0 {
		run.Now = now
	}
	if rng.IntN(3) == 0 {
		run.MaxActiveTasks = 1 + rng.IntN(4)
		run.ActiveTaskCount = rng.IntN(3)
	}
	if rng.IntN(3) == 0 {
		run.PoolsEnabled = true
		run.PoolBudgets = map[string]int{PoolKey("t", "p1"): rng.IntN(3), PoolKey("t", defaultPoolName): 1 + rng.IntN(3)}
		run.PoolActive = map[string]int{PoolKey("t", "p1"): rng.IntN(2)}
	}
	at := func() *time.Time {
		v := now.Add(time.Duration(rng.IntN(120)-60) * time.Second)
		return &v
	}
	for i := range n {
		id := fmt.Sprintf("t%d", i)
		if i > 0 && rng.IntN(15) == 0 {
			id = fmt.Sprintf("t%d", rng.IntN(i)) // a duplicate task id
		}
		task := domain.TaskSpec{TaskID: id, TriggerRule: allTriggerRules[rng.IntN(len(allTriggerRules))]}
		if rng.IntN(3) == 0 {
			task.Pool = "p1"
		}
		for d := range i {
			if rng.IntN(3) == 0 {
				task.DependsOn = append(task.DependsOn, fmt.Sprintf("t%d", d))
			}
		}
		if rng.IntN(10) == 0 {
			task.DependsOn = append(task.DependsOn, "ghost") // not in the task list
		}
		run.Tasks = append(run.Tasks, task)
		if rng.IntN(8) != 0 {
			run.States[id] = planStates[rng.IntN(len(planStates))]
		}
		run.Tries[id] = 1 + rng.IntN(3)
		run.MaxTries[id] = 1 + rng.IntN(3)
		if rng.IntN(2) == 0 {
			run.EndedAt[id] = at()
		}
		if rng.IntN(2) == 0 {
			run.RetryDelaySeconds[id] = rng.IntN(60)
		}
		if rng.IntN(3) == 0 {
			run.RescheduleAt[id] = at()
		}
		if rng.IntN(3) == 0 {
			run.NextDispatchAt[id] = at()
		}
		if rng.IntN(4) == 0 {
			run.InfraFailed[id] = true
			run.InfraAttempts[id] = rng.IntN(infraMaxAttempts + 1)
		}
	}
	if rng.IntN(5) == 0 {
		run.States["ghost"] = planStates[rng.IntN(len(planStates))]
	}
	return run
}

// referencePlanRun is the map-keyed planner PlanRun replaced, kept verbatim as
// the oracle for TestPlanRunMatchesMapReference.
func referencePlanRun(run RunState) []PlannedTransition {
	upstreams := make(map[string][]string, len(run.Tasks))
	for _, t := range run.Tasks {
		upstreams[t.TaskID] = t.DependsOn
	}
	effective := make(map[string]domain.TaskState, len(run.States))
	for k, v := range run.States {
		effective[k] = v
	}
	decided := make(map[string]bool, len(run.Tasks))
	out := make([]PlannedTransition, 0, len(run.Tasks))

	out = append(out, referencePlanRetryTransitions(run, effective, decided)...)

	headroom := admissionHeadroom(run)
	promoted := 0
	var poolPromoted map[string]int
	for _, t := range run.Tasks {
		if decided[t.TaskID] {
			continue
		}
		switch effective[t.TaskID] {
		case domain.TaskStateNone:
			if to, ok := referenceDecideStart(t, upstreams[t.TaskID], effective); ok {
				out = append(out, PlannedTransition{TaskID: t.TaskID, To: to})
			}
		case domain.TaskStateScheduled:
			if !readyToDispatch(run, t.TaskID) {
				continue
			}
			if promoted >= headroom {
				continue
			}
			pk := poolKeyFor(run, t)
			if !poolHasSlot(run, pk, poolPromoted) {
				continue
			}
			out = append(out, PlannedTransition{TaskID: t.TaskID, To: domain.TaskStateQueued})
			promoted++
			if pk != "" {
				if poolPromoted == nil {
					poolPromoted = map[string]int{}
				}
				poolPromoted[pk]++
			}
		default:
		}
	}
	return out
}

func referencePlanRetryTransitions(run RunState, effective map[string]domain.TaskState, decided map[string]bool) []PlannedTransition {
	out := make([]PlannedTransition, 0, len(run.Tasks))
	for _, t := range run.Tasks {
		switch run.States[t.TaskID] {
		case domain.TaskStateFailed:
			switch {
			case run.InfraFailed[t.TaskID]:
				if infraReplaceable(run, t.TaskID) {
					effective[t.TaskID] = domain.TaskStateUpForRetry
					if readyToInfraReplace(run, t.TaskID) {
						out = append(out, PlannedTransition{TaskID: t.TaskID, To: domain.TaskStateNone})
						effective[t.TaskID] = domain.TaskStateNone
					}
				}
				decided[t.TaskID] = true
			case retriable(run, t.TaskID):
				out = append(out, PlannedTransition{TaskID: t.TaskID, To: domain.TaskStateUpForRetry})
				effective[t.TaskID] = domain.TaskStateUpForRetry
				decided[t.TaskID] = true
			}
		case domain.TaskStateUpForRetry:
			if !readyToRetry(run, t.TaskID) {
				decided[t.TaskID] = true
				continue
			}
			out = append(out, PlannedTransition{TaskID: t.TaskID, To: domain.TaskStateNone})
			effective[t.TaskID] = domain.TaskStateNone
			decided[t.TaskID] = true
		case domain.TaskStateUpForReschedule:
			if !readyToReschedule(run, t.TaskID) {
				decided[t.TaskID] = true
				continue
			}
			out = append(out, PlannedTransition{TaskID: t.TaskID, To: domain.TaskStateNone})
			effective[t.TaskID] = domain.TaskStateNone
			decided[t.TaskID] = true
		default:
		}
	}
	return out
}

func referenceDecideStart(t domain.TaskSpec, deps []string, states map[string]domain.TaskState) (domain.TaskState, bool) {
	upstreamStates := make([]domain.TaskState, 0, len(deps))
	for _, dep := range deps {
		upstreamStates = append(upstreamStates, states[dep])
	}
	switch EvaluateTriggerRule(triggerRuleOf(t), upstreamStates) {
	case DecisionSchedule:
		return domain.TaskStateScheduled, true
	case DecisionSkip:
		return domain.TaskStateSkipped, true
	case DecisionUpstreamFailed:
		return domain.TaskStateUpstreamFailed, true
	default:
		return "", false
	}
}
