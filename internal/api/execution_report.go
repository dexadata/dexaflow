package api

import (
	"fmt"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
)

// The pool states of the register response's execution report.
const (
	// poolStateOff: warm pools are off; every task runs on a dedicated pod.
	poolStateOff = "off"
	// poolStateNone: warm pools are on, but this version gets no warm worker.
	poolStateNone = "none"
	// poolStateOn: this version keeps idle warm workers while it has an
	// active run.
	poolStateOn = "on"
)

// previousPoolDraining marks a new version of an existing DAG with warm pools
// on: the previous version's warm workers keep serving its active runs and
// are removed once its last one ends (ADR 0058 D1).
const previousPoolDraining = "draining"

// executionReport is what a register (or its dry run) says about where the
// version's tasks run (#1707): Dexaflow's own field, not an Airflow one.
type executionReport struct {
	WarmPools bool            `json:"warm_pools"`
	Tasks     []taskExecution `json:"tasks"`
	Pool      poolReport      `json:"pool"`
}

// taskExecution is one task's execution mode, and why it is dedicated.
type taskExecution struct {
	TaskID string `json:"task_id"`
	Mode   string `json:"mode"`
	Reason string `json:"reason,omitempty"`
}

// poolReport is the state of the version's warm pool after the register.
type poolReport struct {
	State          string `json:"state"`
	Detail         string `json:"detail"`
	MinIdleWorkers int    `json:"min_idle_workers,omitempty"`
	MaxPoolSize    int    `json:"max_pool_size,omitempty"`
	PreviousPool   string `json:"previous_pool,omitempty"`
}

// noIdleWorkersReason is why a warm-eligible task takes a dedicated pod when
// no idle warm worker is kept for its version: the reconciler creates warm
// workers only to restore the idle buffer, so with a target of 0 none exists.
const noIdleWorkersReason = "no idle warm workers kept for this DAG"

// executionReporter builds the execution report from the warm policy
// dispatch places with and the operator's warm pool settings.
type executionReporter struct {
	policy domain.WarmPolicy
	pools  config.ExecutionSection
	// absent says why this server runs no warm worker at all although warm
	// pools are on (no reconciler: the subprocess executor, or pod dispatch
	// disabled), "" when it runs them.
	absent string
}

// report says where each task of spec runs and what its warm pool does.
// newVersion is a new version of a DAG that already existed, whose previous
// version's pool then drains. A task dispatch would offer to a warm worker
// is reported dedicated when no warm worker of its version can exist, with
// the reason, since the offer then always misses.
func (e executionReporter) report(spec *domain.DAGSpec, newVersion bool) *executionReport {
	version := domain.WarmVersion{Image: spec.Image, HasSource: spec.Source != "", Staging: spec.Staging}
	pool, noWorker := e.pool(spec, version)
	r := &executionReport{WarmPools: e.policy.Enabled, Tasks: make([]taskExecution, 0, len(spec.Tasks)), Pool: pool}
	eligible := 0
	for _, task := range spec.Tasks {
		reason := e.policy.DedicatedReason(version, task)
		if reason == "" {
			reason = noWorker
		}
		mode := domain.ExecutionModeDedicated
		if reason == "" {
			mode = domain.ExecutionModeWarm
			eligible++
		}
		r.Tasks = append(r.Tasks, taskExecution{TaskID: task.TaskID, Mode: mode, Reason: reason})
	}
	if r.Pool.State == poolStateOn && eligible == 0 {
		r.Pool.Detail += "; no task of this version is warm-eligible, so they serve none of its tasks"
	}
	if newVersion && e.couldKeepWorkers(spec) {
		r.Pool.PreviousPool = previousPoolDraining
		r.Pool.Detail += "; the previous version's warm workers, if it has any, drain once its last active run ends"
	}
	return r
}

// couldKeepWorkers reports whether this server keeps warm workers for a
// version like spec, so that the previous version may hold some.
func (e executionReporter) couldKeepWorkers(spec *domain.DAGSpec) bool {
	return e.policy.Enabled && e.absent == "" &&
		(e.pools.EffectiveMinIdle(0) > 0 || e.pools.EffectiveMinIdle(spec.MinIdleWorkers) > 0)
}

// pool is the warm pool of version, and the reason a task otherwise
// warm-eligible takes a dedicated pod because no warm worker of the version
// can exist ("" when one can): off with warm pools off; none for a version
// excluded as a whole, on a server that runs no warm workers, or when no
// idle worker is kept for it; else the idle workers it keeps
// (EffectiveMinIdle, the reconciler's target) up to max_pool_size while it
// has an active run.
func (e executionReporter) pool(spec *domain.DAGSpec, version domain.WarmVersion) (p poolReport, noWorker string) {
	if !e.policy.Enabled {
		return poolReport{State: poolStateOff, Detail: "warm pools off: every task runs on a dedicated pod"}, ""
	}
	if reason := e.policy.VersionReason(version); reason != "" {
		return poolReport{State: poolStateNone, Detail: "no warm pool for this version: " + reason}, ""
	}
	if e.absent != "" {
		return poolReport{State: poolStateNone, Detail: "no warm pool: " + e.absent}, e.absent
	}
	idle := e.pools.EffectiveMinIdle(spec.MinIdleWorkers)
	if idle == 0 {
		return poolReport{State: poolStateNone,
			Detail: "no idle warm worker is kept for this version (min_idle_workers 0), so its tasks run on dedicated pods"}, noIdleWorkersReason
	}
	return poolReport{State: poolStateOn, MinIdleWorkers: idle, MaxPoolSize: e.pools.MaxPoolSize,
		Detail: fmt.Sprintf("%d idle warm workers (up to %d) start with this version's first run and stay while it has an active run",
			idle, e.pools.MaxPoolSize)}, ""
}
