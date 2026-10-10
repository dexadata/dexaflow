package api

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strconv"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/taskoutcome"
)

// synthID derives a stable synthetic identifier from composite key parts, for UI
// objects (task instances) Dexaflow keys by composite rather than a UUID.
func synthID(parts ...any) string {
	h := fnv.New64a()
	for _, p := range parts {
		if _, err := fmt.Fprintf(h, "%v|", p); err != nil {
			return "" // hash.Write never errors; satisfy the linter without ignoring it.
		}
	}
	return fmt.Sprintf("%016x", h.Sum64())
}

type tagDTO struct {
	Name string `json:"name"`
}

type scheduleIntervalDTO struct {
	Type  string `json:"__type"` //nolint:tagliatelle // Airflow API contract field name
	Value string `json:"value"`
}

type dagDTO struct {
	DagID            string               `json:"dag_id"`
	DagDisplayName   string               `json:"dag_display_name"`
	Description      string               `json:"description"`
	IsPaused         bool                 `json:"is_paused"`
	IsActive         bool                 `json:"is_active"`
	Owners           []string             `json:"owners"`
	Tags             []tagDTO             `json:"tags"`
	ScheduleInterval *scheduleIntervalDTO `json:"schedule_interval"`
	MaxActiveRuns    int                  `json:"max_active_runs"`
	Catchup          bool                 `json:"catchup"`
	// Fileloc is where Airflow says the DAG was defined. Airflow's DAGResponse
	// declares it as a required string, and it is the only place a consumer of the
	// list route can learn a DAG's location: OpenMetadata reads pipelineLocation
	// from here and never calls the details endpoint. Same derivation the details
	// route has always used.
	Fileloc string `json:"fileloc"`
}

type dagCollectionDTO struct {
	Dags         []dagDTO `json:"dags"`
	TotalEntries int      `json:"total_entries"`
}

func toDagDTO(d domain.DAG) dagDTO {
	tags := make([]tagDTO, 0, len(d.Tags))
	for _, t := range d.Tags {
		tags = append(tags, tagDTO{Name: t})
	}
	owners := []string{}
	if d.Owner != "" {
		owners = append(owners, d.Owner)
	}
	var schedule *scheduleIntervalDTO
	if d.Schedule != nil && *d.Schedule != "" {
		schedule = &scheduleIntervalDTO{Type: "CronExpression", Value: *d.Schedule}
	}
	return dagDTO{
		DagID:            d.DagID,
		DagDisplayName:   d.DagID,
		Description:      d.Description,
		IsPaused:         d.IsPaused,
		IsActive:         d.IsActive,
		Owners:           owners,
		Tags:             tags,
		ScheduleInterval: schedule,
		MaxActiveRuns:    d.MaxActiveRuns,
		Catchup:          d.Catchup,
		Fileloc:          d.DagID + "/dag.py",
	}
}

// dagRunDTO is the Airflow 3.2.1 DAGRunResponse. Every spec-required field is
// present — notably dag_versions (a required array the UI maps over; omitting it
// crashes the run view with "undefined.map"). Fields Dexaflow does not model are
// null/defaults.
type dagRunDTO struct {
	DagID              string          `json:"dag_id"`
	DagRunID           string          `json:"dag_run_id"`
	DagDisplayName     string          `json:"dag_display_name"`
	LogicalDate        time.Time       `json:"logical_date"`
	QueuedAt           time.Time       `json:"queued_at"`
	StartDate          *time.Time      `json:"start_date"`
	EndDate            *time.Time      `json:"end_date"`
	RunAfter           time.Time       `json:"run_after"`
	DataIntervalStart  *time.Time      `json:"data_interval_start"`
	DataIntervalEnd    *time.Time      `json:"data_interval_end"`
	LastSchedulingDec  *time.Time      `json:"last_scheduling_decision"`
	State              string          `json:"state"`
	RunType            string          `json:"run_type"`
	TriggeredBy        *string         `json:"triggered_by"`
	TriggeringUserName *string         `json:"triggering_user_name"`
	Conf               json.RawMessage `json:"conf"`
	Note               *string         `json:"note"`
	DagVersions        []any           `json:"dag_versions"`
	BundleVersion      *string         `json:"bundle_version"`
	Duration           *float64        `json:"duration"`
	PartitionKey       *string         `json:"partition_key"`
}

type dagRunCollectionDTO struct {
	DagRuns      []dagRunDTO `json:"dag_runs"`
	TotalEntries int         `json:"total_entries"`
}

func toDagRunDTO(r domain.DagRun) dagRunDTO {
	var dur *float64
	if r.StartedAt != nil {
		end := time.Now().UTC()
		if r.EndedAt != nil {
			end = *r.EndedAt
		}
		d := end.Sub(*r.StartedAt).Seconds()
		dur = &d
	}
	// Airflow sets the data interval to the window the run covers; for a manual /
	// unscheduled run that is a zero-width interval at the logical date. We model
	// runs this way (precise per-cron windows for scheduled DAGs are a follow-up),
	// so the field is never null.
	logical := r.LogicalDate
	dto := dagRunDTO{
		DagID:             r.DagID,
		DagRunID:          r.RunID,
		DagDisplayName:    r.DagID,
		LogicalDate:       logical,
		QueuedAt:          r.QueuedAt,
		StartDate:         r.StartedAt,
		EndDate:           r.EndedAt,
		RunAfter:          logical,
		DataIntervalStart: &logical,
		DataIntervalEnd:   &logical,
		State:             string(r.State),
		RunType:           r.RunType,
		Conf:              confOrEmptyObject(r.Conf),
		Note:              strPtrOrNil(r.Note),
		DagVersions:       []any{},
		Duration:          dur,
	}
	if r.Version != "" {
		// The SPA's clear dialog renders its "Run with latest bundle version"
		// control only when the run's bundle_version differs from the DAG's, and
		// hides it when either is null. Leaving this null is what made that
		// control invisible, so the operator had no way to ask for the current
		// version no matter what the API accepted (ADR 0020, 2026-09-15).
		v := r.Version
		dto.BundleVersion = &v
	}
	return dto
}

// confOrEmptyObject renders a run's conf for the API, defaulting an unset conf
// to the empty object so the field is never null.
func confOrEmptyObject(conf json.RawMessage) json.RawMessage {
	if len(conf) == 0 {
		return json.RawMessage("{}")
	}
	return conf
}

// taskInstanceDTO is the Airflow 3.2.1 TaskInstanceResponse. Every spec-required
// field is present; values Dexaflow does not track are null/defaults. id is a
// stable synthetic key (Dexaflow keys task instances by composite, not a UUID).
type taskInstanceDTO struct {
	ID               string          `json:"id"`
	TaskID           string          `json:"task_id"`
	DagID            string          `json:"dag_id"`
	DagRunID         string          `json:"dag_run_id"`
	MapIndex         int             `json:"map_index"`
	LogicalDate      *time.Time      `json:"logical_date"`
	RunAfter         *time.Time      `json:"run_after"`
	StartDate        *time.Time      `json:"start_date"`
	EndDate          *time.Time      `json:"end_date"`
	Duration         *float64        `json:"duration"`
	State            *string         `json:"state"`
	TryNumber        int             `json:"try_number"`
	MaxTries         int             `json:"max_tries"`
	TaskDisplayName  string          `json:"task_display_name"`
	DagDisplayName   string          `json:"dag_display_name"`
	Hostname         *string         `json:"hostname"`
	Unixname         *string         `json:"unixname"`
	Pool             string          `json:"pool"`
	PoolSlots        int             `json:"pool_slots"`
	Queue            *string         `json:"queue"`
	PriorityWeight   *int            `json:"priority_weight"`
	Operator         *string         `json:"operator"`
	OperatorName     *string         `json:"operator_name"`
	QueuedWhen       *time.Time      `json:"queued_when"`
	ScheduledWhen    *time.Time      `json:"scheduled_when"`
	Pid              *int            `json:"pid"`
	Executor         *string         `json:"executor"`
	ExecutorConfig   string          `json:"executor_config"`
	Note             *string         `json:"note"`
	RenderedMapIndex *string         `json:"rendered_map_index"`
	RenderedFields   json.RawMessage `json:"rendered_fields"`
	Trigger          *string         `json:"trigger"`
	TriggererJob     *string         `json:"triggerer_job"`
	DagVersion       *dagVersionDTO  `json:"dag_version"`
	// FailureReason is a Dexaflow EXTENSION, not an Airflow field: a short,
	// human-readable cause for a terminal failure. It is additive — the Airflow
	// SPA ignores fields it does not know — and exists because an attempt whose
	// agent died before it ever registered streams no logs, leaving "failed" with
	// no cause anywhere reachable from the API. Null when no cause was observed.
	FailureReason *string `json:"failure_reason"`
}

type taskInstanceCollectionDTO struct {
	TaskInstances []taskInstanceDTO `json:"task_instances"`
	TotalEntries  int               `json:"total_entries"`
}

func toTaskInstanceDTO(ti domain.TaskInstance) taskInstanceDTO {
	var state *string
	if ti.State != "" && ti.State != domain.TaskStateNone {
		s := string(ti.State)
		state = &s
	}
	op := strPtrOrNil(ti.Operator)
	// Defaults so the task panel never shows blank fields for values every task
	// has (Airflow shows queue "default", priority_weight, the run user).
	queue := "default"
	prio := 1
	unixname := "airflow"
	hostname := ti.Hostname
	if hostname == "" {
		hostname = "unknown"
	}
	return taskInstanceDTO{
		ID:               synthID(ti.RunID, ti.TaskID, ti.MapIndex),
		TaskID:           ti.TaskID,
		DagID:            ti.DagID,
		DagRunID:         ti.RunID,
		MapIndex:         ti.MapIndex,
		StartDate:        ti.StartedAt,
		EndDate:          ti.EndedAt,
		Duration:         ti.Duration,
		State:            state,
		TryNumber:        ti.TryNumber,
		MaxTries:         ti.MaxTries,
		TaskDisplayName:  ti.TaskID,
		DagDisplayName:   ti.DagID,
		Hostname:         &hostname,
		Unixname:         &unixname,
		Pool:             "default_pool",
		PoolSlots:        1,
		Queue:            &queue,
		PriorityWeight:   &prio,
		Operator:         op,
		OperatorName:     op,
		ExecutorConfig:   "{}",
		RenderedMapIndex: renderedMapIndex(ti.MapIndex),
		// Airflow returns rendered_fields as an object; the task Details tab calls
		// Object.keys on it, so it must never be null/absent. We do not render
		// templates yet (#25), so emit an empty object.
		RenderedFields: json.RawMessage("{}"),
		Note:           strPtrOrNil(ti.Note),
		ScheduledWhen:  ti.ScheduledAt,
		QueuedWhen:     ti.QueuedAt,
		FailureReason:  failureReasonPtr(ti.FailureReason),
	}
}

// maxFailureReasonLen bounds the reason served to clients. The stored cause can
// originate from a task's own reported error message, which is unbounded text
// from inside the pod; a run listing serves up to a thousand instances at once,
// so an unbounded field would let one pathological task bloat every response.
const maxFailureReasonLen = 1024

// failureReasonPtr renders the failure reason for the wire: nil when no cause was
// observed (never an empty string masquerading as an answer), truncated on a rune
// boundary otherwise so the response stays bounded and valid UTF-8.
func failureReasonPtr(reason string) *string {
	if reason == "" {
		return nil
	}
	bounded := taskoutcome.TruncateReason(reason, maxFailureReasonLen)
	return &bounded
}

// renderedMapIndex returns nil for an unmapped task (-1), else the index string.
func renderedMapIndex(idx int) *string {
	if idx < 0 {
		return nil
	}
	s := strconv.Itoa(idx)
	return &s
}
