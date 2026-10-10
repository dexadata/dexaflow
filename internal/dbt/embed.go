package dbt

import (
	"fmt"
	"sort"

	"github.com/dexadata/dexaflow/internal/domain"
)

// EmbedGroup namespaces a rendered group's tasks under groupName
// (taskID -> "<group>__<taskID>", since task_id forbids dots) and wires its
// external edges: the upstream task_ids become dependencies of the group's roots
// (tasks with no internal dependency). It returns the namespaced tasks and the
// group's leaf ids (tasks that nothing inside the group depends on) so the caller
// can attach them to the group's downstream tasks (ADR 0043).
func EmbedGroup(groupName string, tasks []domain.TaskSpec, upstream []string) ([]domain.TaskSpec, []string, error) {
	if groupName == "" {
		return nil, nil, fmt.Errorf("dbt group name is required")
	}
	ns := func(id string) string { return groupName + "__" + id }

	hasDependent := make(map[string]bool, len(tasks))
	embedded := make([]domain.TaskSpec, 0, len(tasks))
	for _, t := range tasks {
		nt := t
		nt.TaskID = ns(t.TaskID)
		if len(t.DependsOn) == 0 {
			// a root: inherit the group's external upstream
			nt.DependsOn = append([]string(nil), upstream...)
		} else {
			deps := make([]string, 0, len(t.DependsOn))
			for _, d := range t.DependsOn {
				deps = append(deps, ns(d))
				hasDependent[ns(d)] = true
			}
			nt.DependsOn = deps
		}
		embedded = append(embedded, nt)
	}
	sort.Slice(embedded, func(i, j int) bool { return embedded[i].TaskID < embedded[j].TaskID })

	var leaves []string
	for _, t := range embedded {
		if !hasDependent[t.TaskID] {
			leaves = append(leaves, t.TaskID)
		}
	}
	sort.Strings(leaves)
	return embedded, leaves, nil
}

// ExpandGroups replaces each dbt_group placeholder task with the rendered +
// embedded dbt group's tasks (namespaced), rewiring any downstream dependents from
// the placeholder onto the group's leaves. render maps a group name to its rendered
// dbt tasks (the caller supplies manifest loading + Render).
func ExpandGroups(tasks []domain.TaskSpec, render func(group string) ([]domain.TaskSpec, error)) ([]domain.TaskSpec, error) {
	leafReplacements := map[string][]string{} // placeholder id -> group leaf ids
	out := make([]domain.TaskSpec, 0, len(tasks))
	for _, t := range tasks {
		if t.Type != domain.TaskTypeDbtGroup {
			out = append(out, t)
			continue
		}
		dbtTasks, err := render(t.TaskID)
		if err != nil {
			return nil, fmt.Errorf("expanding dbt group %q: %w", t.TaskID, err)
		}
		embedded, leaves, err := EmbedGroup(t.TaskID, dbtTasks, t.DependsOn)
		if err != nil {
			return nil, err
		}
		out = append(out, embedded...)
		leafReplacements[t.TaskID] = leaves
	}
	for i := range out {
		out[i].DependsOn = rewireDeps(out[i].DependsOn, leafReplacements)
	}
	seen := make(map[string]bool, len(out))
	for _, t := range out {
		if seen[t.TaskID] {
			return nil, fmt.Errorf("task_id %q collides after expanding a dbt group; rename the conflicting task or group", t.TaskID)
		}
		seen[t.TaskID] = true
	}
	sortTasks(out)
	return out, nil
}

// rewireDeps replaces any dependency that is a placeholder id with that group's
// leaf ids, leaving other dependencies untouched.
func rewireDeps(deps []string, repl map[string][]string) []string {
	hit := false
	for _, d := range deps {
		if _, ok := repl[d]; ok {
			hit = true
			break
		}
	}
	if !hit {
		return deps
	}
	out := make([]string, 0, len(deps))
	for _, d := range deps {
		if leaves, ok := repl[d]; ok {
			out = append(out, leaves...)
			continue
		}
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}
