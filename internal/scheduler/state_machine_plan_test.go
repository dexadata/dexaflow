package scheduler

import (
	"math/rand/v2"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

// Every transition the planner asks for is legal in taskTransitions (#2146).
// The scheduler writes the planner's choices through ApplyTransition and
// ApplyTransitions, whose target state the caller picks, so the storage
// recorder (internal/storage/state_transitions_integration_test.go) cannot
// judge them; this is where those writes meet the table.
func TestPlannedTransitionsAreInTheTable(t *testing.T) {
	rng := rand.New(rand.NewPCG(2146, 1))
	seen, illegal := map[[2]domain.TaskState]bool{}, map[[2]domain.TaskState]bool{}
	for i := range 20000 {
		run := randomRun(rng, i)
		for _, p := range PlanRun(run) {
			from := run.States[p.TaskID]
			if from == "" {
				from = domain.TaskStateNone // a task with no row yet is none
			}
			seen[[2]domain.TaskState{from, p.To}] = true
			if !CanTransition(from, p.To) && !illegal[[2]domain.TaskState{from, p.To}] {
				illegal[[2]domain.TaskState{from, p.To}] = true
				t.Errorf("run %d: the planner moves %s from %s to %s, which taskTransitions does not allow", i, p.TaskID, from, p.To)
			}
		}
	}
	// The planner makes exactly the transitions PlannerTransitions lists: the
	// storage recorder trusts that list for the entries only the planner makes,
	// and the generator must keep reaching every rail.
	want := map[[2]domain.TaskState]bool{}
	for _, tr := range PlannerTransitions() {
		want[tr] = true
		if !seen[tr] {
			t.Errorf("no random run made the planner move %s -> %s; drop it from plannerTransitions or reach it", tr[0], tr[1])
		}
	}
	for tr := range seen {
		if !want[tr] {
			t.Errorf("the planner moves %s -> %s, which plannerTransitions does not list", tr[0], tr[1])
		}
	}
}
