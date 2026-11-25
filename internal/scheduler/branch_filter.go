package scheduler

import "github.com/dexadata/dexaflow/internal/domain"

// branchSkips is the branch filter of ADR 0064 D2, the analog of Airflow's
// NotPreviouslySkippedDep. It marks, by slot, the tasks a branch parent's
// decision skips:
//
//   - a branch task skips each direct child outside follow(P), the chosen ids
//     plus everything downstream of them (Airflow's empty-branch rule: a join
//     that is also below a chosen task is not skipped);
//   - a falsy short-circuit skips its direct children, or every task below it
//     when it ignores the downstream trigger rules (the default).
//
// Only a parent that is success with a decision filters; one marked success by
// hand has none and filters nothing, as in Airflow. The caller applies the
// marks to none tasks only, before their trigger rule, whatever the rule
// (always included, since NotPreviouslySkippedDep is a dependency of its own).
// It returns nil, and costs nothing, for a DAG without a branch task.
func branchSkips(run RunState, g *TaskGraph, effective []domain.TaskState) skipSet {
	if len(g.branches) == 0 || len(run.BranchDecision) == 0 {
		return nil
	}
	var skip skipSet
	for _, p := range g.branches {
		if effective[p] != domain.TaskStateSuccess {
			continue
		}
		t := run.Tasks[p]
		if decision := run.BranchDecision[t.TaskID]; decision != nil {
			skip = skip.apply(g, p, *t.Branch, *decision)
		}
	}
	return skip
}

// skipSet marks tasks by slot; nil marks none.
type skipSet []bool

// has reports whether slot s is marked.
func (k skipSet) has(s int) bool { return s < len(k) && k[s] }

// mark returns the set with slot s marked, allocated on the first mark.
func (k skipSet) mark(g *TaskGraph, s int) skipSet {
	if k == nil {
		k = make(skipSet, len(g.slot))
	}
	k[s] = true
	return k
}

// apply marks what one branch parent's decision skips.
func (k skipSet) apply(g *TaskGraph, p int, spec domain.BranchSpec, decision domain.BranchDecision) skipSet {
	children := g.downstream[p]
	switch {
	case spec.Kind == domain.BranchKindBranch:
		followed := g.reach(decision.FollowTaskIDs)
		for _, c := range children {
			if !followed[c] {
				k = k.mark(g, c)
			}
		}
	case spec.Kind != domain.BranchKindShortCircuit || !decision.ShortCircuitFalsy:
		// A truthy short-circuit, or a kind this build does not know, skips nothing.
	case spec.IgnoresDownstreamTriggerRules():
		for s, below := range g.reachFrom(children) {
			if below {
				k = k.mark(g, s)
			}
		}
	default:
		for _, c := range children {
			k = k.mark(g, c)
		}
	}
	return k
}

// reach returns, by slot, the tasks named by ids and every task downstream of
// them. An id that is not in the graph selects nothing.
func (g *TaskGraph) reach(ids []string) []bool {
	seeds := make([]int, 0, len(ids))
	for _, id := range ids {
		if s, ok := g.index[id]; ok {
			seeds = append(seeds, s)
		}
	}
	return g.reachFrom(seeds)
}

// reachFrom returns, by slot, the seeds and every task downstream of them.
func (g *TaskGraph) reachFrom(seeds []int) []bool {
	seen := make([]bool, len(g.slot))
	stack := append([]int(nil), seeds...)
	for len(stack) > 0 {
		s := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[s] {
			continue
		}
		seen[s] = true
		stack = append(stack, g.downstream[s]...)
	}
	return seen
}
