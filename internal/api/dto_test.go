package api

import (
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
)

func TestToDagDTO(t *testing.T) {
	sched := "0 5 * * *"
	d := toDagDTO(domain.DAG{
		DagID: "etl", Owner: "data", Tags: []string{"a", "b"},
		Schedule: &sched, IsPaused: true, MaxActiveRuns: 16,
	})
	if d.DagID != "etl" || d.DagDisplayName != "etl" || !d.IsPaused {
		t.Errorf("unexpected dag dto: %+v", d)
	}
	if len(d.Owners) != 1 || d.Owners[0] != "data" {
		t.Errorf("owners = %v", d.Owners)
	}
	if len(d.Tags) != 2 || d.Tags[0].Name != "a" {
		t.Errorf("tags = %v", d.Tags)
	}
	if d.ScheduleInterval == nil || d.ScheduleInterval.Type != "CronExpression" || d.ScheduleInterval.Value != sched {
		t.Errorf("schedule = %+v", d.ScheduleInterval)
	}
}

func TestToDagDTONilSchedule(t *testing.T) {
	if d := toDagDTO(domain.DAG{DagID: "manual"}); d.ScheduleInterval != nil {
		t.Errorf("nil schedule should map to nil interval, got %+v", d.ScheduleInterval)
	}
}

func TestToDagRunDTO(t *testing.T) {
	now := time.Now().UTC()
	r := toDagRunDTO(domain.DagRun{DagID: "etl", RunID: "r1", LogicalDate: now, State: domain.DagRunStateRunning, RunType: "scheduled"})
	if r.DagID != "etl" || r.DagRunID != "r1" || r.State != "running" || r.RunType != "scheduled" {
		t.Errorf("unexpected run dto: %+v", r)
	}
}

func TestToTaskInstanceDTO(t *testing.T) {
	ti := toTaskInstanceDTO(domain.TaskInstance{DagID: "etl", RunID: "r1", TaskID: "extract", State: domain.TaskStateRunning, Operator: "python", TryNumber: 1})
	if ti.TaskID != "extract" || ti.TryNumber != 1 {
		t.Errorf("unexpected ti dto: %+v", ti)
	}
	if ti.State == nil || *ti.State != "running" {
		t.Errorf("state = %v, want running", ti.State)
	}
	if ti.Operator == nil || *ti.Operator != "python" {
		t.Errorf("operator = %v, want python", ti.Operator)
	}
	if ti.ID == "" || ti.Pool != "default_pool" {
		t.Errorf("ti missing synthetic id / pool defaults: %+v", ti)
	}
}

func TestRenderedFieldsFor(t *testing.T) {
	spec := domain.DAGSpec{Tasks: []domain.TaskSpec{
		{TaskID: "transform", Type: domain.TaskTypePython, Entrypoint: "dag:transform",
			XComInput: map[string][]string{"data": {"extract"}}},
	}}
	got := string(renderedFieldsFor(spec, "transform"))
	if !strings.Contains(got, `"entrypoint":"dag:transform"`) || !strings.Contains(got, `"xcom_input"`) {
		t.Errorf("rendered_fields = %s, want entrypoint + xcom_input", got)
	}
	// Unknown task yields an empty object (never null).
	if s := string(renderedFieldsFor(spec, "nope")); s != "{}" {
		t.Errorf("unknown task rendered_fields = %s, want {}", s)
	}
}

func TestToTaskInstanceDTORenderedFieldsNeverNull(t *testing.T) {
	// The task Details/Rendered-Templates views call Object.keys on rendered_fields,
	// so it must serialize as an object, never null/absent.
	ti := toTaskInstanceDTO(domain.TaskInstance{DagID: "etl", RunID: "r1", TaskID: "extract"})
	if string(ti.RenderedFields) != "{}" {
		t.Errorf("rendered_fields = %q, want {}", string(ti.RenderedFields))
	}
}

func TestToTaskInstanceDTOStateTimestamps(t *testing.T) {
	scheduled := time.Date(2026, 5, 23, 10, 0, 0, 0, time.UTC)
	queued := time.Date(2026, 5, 23, 10, 0, 5, 0, time.UTC)
	ti := toTaskInstanceDTO(domain.TaskInstance{
		DagID: "etl", RunID: "r1", TaskID: "extract", State: domain.TaskStateRunning,
		ScheduledAt: &scheduled, QueuedAt: &queued,
	})
	if ti.ScheduledWhen == nil || !ti.ScheduledWhen.Equal(scheduled) {
		t.Errorf("scheduled_when = %v, want %v", ti.ScheduledWhen, scheduled)
	}
	if ti.QueuedWhen == nil || !ti.QueuedWhen.Equal(queued) {
		t.Errorf("queued_when = %v, want %v", ti.QueuedWhen, queued)
	}
}
