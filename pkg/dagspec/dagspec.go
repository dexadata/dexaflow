// Package dagspec validates a compiled dag.json with the rules the engine
// applies when the DAG is registered, so a service that registers DAGs through
// the API can refuse a bad one before it sends it (#1877).
//
// It works on the dag.json bytes, the same document POST
// /api/v2/dags/{dag_id}/dagVersions takes, and depends on no storage, server or
// API code. Validate covers what holds on every install: the JSON Schema, the
// task graph, params, execution metadata, placement, resource quantities and
// the cron schedule. CheckLimits adds the per-tenant limits an operator may set
// (min_schedule_interval_seconds and max_task_pool_slots). What depends on the
// server's own configuration (the resource unit, deploy.require_digest, source
// mode and the execution allowlist) and max_dags, which needs the tenant's
// count, are checked only by the server.
package dagspec

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
)

// ErrLimitExceeded is what CheckLimits returns, wrapped, when the DAG goes past
// one of the tenant limits. The server refuses the same DAG with 403.
var ErrLimitExceeded = domain.ErrLimitExceeded

// Limits are the tenant limits a dag.json is checked against. A zero field is
// unlimited, as on a tenant created without limits.
type Limits struct {
	// MinScheduleIntervalSeconds is the shortest gap, in seconds, the DAG's
	// schedule may leave between two runs.
	MinScheduleIntervalSeconds int
	// MaxTaskPoolSlots caps a task's pool_slots.
	MaxTaskPoolSlots int
}

// Validate reports whether dagJSON is a dag.json the engine registers, and
// every rule it breaks otherwise. A schedule that does not parse as a cron is
// refused here, as `dexaflow compile` refuses it: the server registers such a
// DAG but never schedules it.
func Validate(dagJSON []byte) error {
	_, err := parseValid(dagJSON)
	return err
}

// CheckLimits validates dagJSON as Validate does, then checks it against the
// tenant limits l. A limit the DAG goes past wraps ErrLimitExceeded, with the
// same message the server answers.
func CheckLimits(dagJSON []byte, l Limits) error {
	spec, err := parseValid(dagJSON)
	if err != nil {
		return err
	}
	if err := domain.CheckScheduleInterval(*spec, l.MinScheduleIntervalSeconds); err != nil {
		return err
	}
	return domain.CheckTaskPoolSlots(*spec, l.MaxTaskPoolSlots)
}

// MinScheduleInterval returns the shortest gap a schedule leaves between two
// runs, the figure min_schedule_interval_seconds is compared with. ok is false
// when the schedule never fires on a cron (empty, @once, @continuous, a date
// that does not exist) or does not parse.
func MinScheduleInterval(schedule string) (gap time.Duration, ok bool) {
	return domain.MinScheduleInterval(schedule)
}

// Schema returns the JSON Schema of dag.json that Validate applies, for a
// caller that validates in another language or shows it to an author.
func Schema() []byte {
	return domain.DAGSchemaJSON()
}

// parseValid decodes dagJSON as the server binds a registration body and
// applies the rules Validate documents.
func parseValid(dagJSON []byte) (*domain.DAGSpec, error) {
	if len(dagJSON) == 0 {
		return nil, errors.New("dag.json is empty")
	}
	var spec domain.DAGSpec
	if err := json.Unmarshal(dagJSON, &spec); err != nil {
		return nil, fmt.Errorf("dag.json does not decode: %w", err)
	}
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if err := spec.ValidateSchedule(); err != nil {
		return nil, err
	}
	return &spec, nil
}
