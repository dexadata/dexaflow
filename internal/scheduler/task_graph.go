package scheduler

import (
	"slices"

	"github.com/dexadata/dexaflow/internal/domain"
)

// TaskGraph is the index-addressed form of a run's task list: each task_id
// resolved to a position, and each task's upstreams resolved to positions, so
// the planner and dispatch look tasks up in O(1) instead of scanning the list.
//
// A graph depends only on the task list (ids and dependencies), which is
// immutable per dag_version, so the store builds it the first time the
// scheduler reads a version and every run of that version shares it read-only. A graph built for
// one task list must only be used with a list of the same ids in the same
// order; PlanRun falls back to building one when RunState.Graph is nil.
//
// Positions mirror the map-keyed planner this replaced: a task_id that appears
// more than once (compile validation rejects that, but the planner never
// trusted it) shares one state slot, the slot of its first occurrence; lookup
// returns that first occurrence, and its upstreams are those of its last
// occurrence.
type TaskGraph struct {
	// index maps a task_id to the position of its first occurrence.
	index map[string]int
	// slot maps each position to its task_id's first-occurrence position, the
	// key every per-task planner row is addressed by.
	slot []int
	// upstreamIDs holds, per slot, the upstream task_ids; upstream holds their
	// slots, -1 for an upstream outside the task list (its state is then read
	// from RunState.States, as the map-keyed planner did).
	upstreamIDs [][]string
	upstream    [][]int
	// branches holds the slots of the tasks that carry a BranchSpec (ADR
	// 0064), and downstream each slot's direct children. Both stay nil for a
	// DAG without a branch task, so the planner pays nothing for them.
	branches   []int
	downstream [][]int
}

// NewTaskGraph indexes a task list. It is O(T + E) and allocation-bounded by
// the list, so building it once per version is cheap.
func NewTaskGraph(tasks []domain.TaskSpec) *TaskGraph {
	n := len(tasks)
	g := &TaskGraph{
		index:       make(map[string]int, n),
		slot:        make([]int, n),
		upstreamIDs: make([][]string, n),
		upstream:    make([][]int, n),
	}
	for i, t := range tasks {
		first, seen := g.index[t.TaskID]
		if !seen {
			g.index[t.TaskID] = i
			first = i
		}
		g.slot[i] = first
	}
	edges := 0
	for _, t := range tasks {
		edges += len(t.DependsOn)
	}
	// One backing array for every task's upstream slots keeps the graph at a
	// constant number of allocations however wide the DAG is.
	flat := make([]int, edges)
	for i, t := range tasks {
		s := g.slot[i]
		deps := flat[:len(t.DependsOn):len(t.DependsOn)]
		flat = flat[len(t.DependsOn):]
		for j, dep := range t.DependsOn {
			if p, ok := g.index[dep]; ok {
				deps[j] = p
			} else {
				deps[j] = -1
			}
		}
		g.upstreamIDs[s] = t.DependsOn
		g.upstream[s] = deps
		if t.Branch != nil && i == s {
			g.branches = append(g.branches, s)
		}
	}
	if len(g.branches) > 0 {
		g.downstream = make([][]int, n)
		for s, deps := range g.upstream {
			for _, p := range deps {
				if p >= 0 && !slices.Contains(g.downstream[p], s) {
					g.downstream[p] = append(g.downstream[p], s)
				}
			}
		}
	}
	return g
}

// Lookup returns the position of the task with the given id (its first
// occurrence), or false when the task is not in the list.
func (g *TaskGraph) Lookup(taskID string) (int, bool) {
	i, ok := g.index[taskID]
	return i, ok
}

// taskGraph returns the run's prebuilt graph, or builds one when the run
// carries none (or one that cannot belong to its task list).
func (r *RunState) taskGraph() *TaskGraph {
	if r.Graph != nil && len(r.Graph.slot) == len(r.Tasks) {
		return r.Graph
	}
	return NewTaskGraph(r.Tasks)
}
