package domain

import "strconv"

// The execution modes of a task attempt: on a warm worker of its DAG version
// (ADR 0058) or on a dedicated pod of its own (ADR 0002).
const (
	ExecutionModeWarm      = "warm"
	ExecutionModeDedicated = "dedicated"
)

// The reasons a whole DAG version runs on dedicated pods, whatever its tasks
// declare.
const (
	// DedicatedWarmPoolsOff: execution.warm_pools_enabled is off, the default.
	DedicatedWarmPoolsOff = "warm pools off"
	// DedicatedStaging: a warm worker carries no per-run /staging volume
	// (ADR 0058 D5).
	DedicatedStaging = "staging enabled"
	// DedicatedSourceMode: a warm pod is built before its task is known, so
	// it cannot carry the task's dag.py in its pod spec (ADR 0067 §3), and
	// execution.source_mode.warm_pools, which delivers it with each
	// assignment instead (ADR 0095), is off.
	DedicatedSourceMode = "source mode"
)

// WarmPolicy is what decides whether a task attempt may run on a warm worker:
// whether warm pools are on, and the operator settings a warm pod is created
// with before any task is known. Dispatch places an attempt with it, and the
// register response reports each task's mode with it, so the report says
// what dispatch does.
type WarmPolicy struct {
	// Enabled is execution.warm_pools_enabled, as the dispatcher sees it (a
	// warm placer is wired).
	Enabled bool
	// ServiceAccount is the operator's default task ServiceAccount, the one a
	// warm worker runs as for its whole life.
	ServiceAccount string
	// Unit is executor.unit (ADR 0066 §3): with it a warm pod is one unit.
	Unit *ResourceUnit
	// Limits is executor.defaults.limits_* (#537): without a unit they size
	// the warm pod.
	Limits *DefaultLimits
	// SourceModeImage is the runtime image when source mode is on, "" when it
	// is off (ADR 0067 §3).
	SourceModeImage string
	// SourceModeWarm is execution.source_mode.warm_pools: a source-mode
	// version may run on warm workers, which receive its dag.py with each
	// assignment (ADR 0095).
	SourceModeWarm bool
}

// WarmVersion is what of a DAG version the warm decision reads: the image its
// tasks run, whether it carries its dag.py, and its staging block.
type WarmVersion struct {
	Image     string
	HasSource bool
	Staging   *StagingConfig
}

// VersionReason says why no task of version v may run on a warm worker, or ""
// when its tasks may (each still subject to DedicatedReason).
func (p WarmPolicy) VersionReason(v WarmVersion) string {
	switch {
	case !p.Enabled:
		return DedicatedWarmPoolsOff
	case v.Staging != nil && v.Staging.Enabled:
		return DedicatedStaging
	case !p.SourceModeWarm && SourceModeApplies(p.SourceModeImage, v.Image, v.HasSource):
		return DedicatedSourceMode
	}
	return ""
}

// DedicatedReason says why an attempt of task t of version v takes a
// dedicated pod, or "" when it may be placed on a warm worker. Warm placement
// degrades, it never strands: a task the warm pod could not run as its own
// pod would takes the dedicated path.
func (p WarmPolicy) DedicatedReason(v WarmVersion, t TaskSpec) string {
	if reason := p.VersionReason(v); reason != "" {
		return reason
	}
	return p.taskReason(t)
}

// HasWarmTask reports whether at least one of tasks may be placed on a warm
// worker of a version VersionReason accepts: the per-task half of
// DedicatedReason. A version with no such task keeps no warm pool, since its
// idle workers would serve none of its tasks (#2065); a version with no task
// has none.
func (p WarmPolicy) HasWarmTask(tasks []TaskSpec) bool {
	for _, t := range tasks {
		if p.taskReason(t) == "" {
			return true
		}
	}
	return false
}

// taskReason says why task t takes a dedicated pod whatever its version, or
// "" when it may be placed on a warm worker.
func (p WarmPolicy) taskReason(t TaskSpec) string {
	if reason := warmServiceAccountReason(t, p.ServiceAccount); reason != "" {
		return reason
	}
	if field := warmPlacementField(t); field != "" {
		return field + " set"
	}
	return p.sizeReason(t)
}

// warmServiceAccountReason refuses a task that pins a ServiceAccount other
// than the warm workers': the warm pod cannot adopt it, and the task would
// run as the wrong identity.
func warmServiceAccountReason(t TaskSpec, warmSA string) string {
	if t.Execution == nil || t.Execution.ServiceAccount == "" || t.Execution.ServiceAccount == warmSA {
		return ""
	}
	return "service_account set"
}

// warmPlacementField names the first placement field or pod metadata t
// declares, "" for none. BuildPod applies them from a task's execution block
// on a dedicated pod; placed on a warm worker, such a task would run on the
// wrong node, outside the sandbox its runtime class asks for (gVisor),
// without its device, or outside the NetworkPolicy its labels select, with no
// error. The image pull policy is left out: it only matters before the
// container starts, and the warm image is the version's.
func warmPlacementField(t TaskSpec) string {
	e := t.Execution
	if e == nil {
		return ""
	}
	fields := []struct {
		name string
		set  bool
	}{
		{"node_selector", len(e.NodeSelector) > 0},
		{"tolerations", len(e.Tolerations) > 0},
		{"affinity", len(e.Affinity) > 0},
		{"topology_spread_constraints", len(e.TopologySpreadConstraints) > 0},
		{"priority_class_name", e.PriorityClassName != ""},
		{"runtime_class_name", e.RuntimeClassName != nil},
		{"termination_grace_period_seconds", e.TerminationGracePeriodSeconds != nil},
		{"resource_claims", len(e.ResourceClaims) > 0},
		{"labels", len(e.Labels) > 0},
		{"annotations", len(e.Annotations) > 0},
	}
	for _, f := range fields {
		if f.set {
			return f.name
		}
	}
	return ""
}

// sizeReason refuses a task larger than a warm pod: with a unit a warm pod is
// one unit, so only a size-1 task that declares no resources fits; without
// one the warm pod gets the default limits, so a task that would get a larger
// limit on a dedicated pod takes one.
func (p WarmPolicy) sizeReason(t TaskSpec) string {
	if p.Unit != nil {
		switch {
		case p.Unit.WarmEligible(t):
			return ""
		case t.EffectivePoolSlots() != 1:
			return "size " + strconv.Itoa(t.EffectivePoolSlots()) + " is above the warm pod's 1 unit"
		}
		return "resources set (a warm pod is 1 unit)"
	}
	if !p.Limits.WarmEligible(t) {
		return "resources above the warm pod's default limits"
	}
	return ""
}
