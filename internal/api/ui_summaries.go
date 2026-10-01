package api

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/domain"
)

// TaskSummaryReader fetches task instances across a set of runs of a DAG, the
// source for the grid's per-cell state summaries.
type TaskSummaryReader interface {
	TaskInstancesForRuns(ctx context.Context, tenant, dagID string, runIDs []string) ([]domain.TaskInstance, error)
}

// lightTISummaryDTO is the Airflow 3.2.1 LightGridTaskInstanceSummary. child_states
// is null in the MVP (no dynamic task mapping); state is null for un-run tasks.
type lightTISummaryDTO struct {
	TaskID          string     `json:"task_id"`
	TaskDisplayName string     `json:"task_display_name"`
	State           *string    `json:"state"`
	ChildStates     *struct{}  `json:"child_states"`
	MinStartDate    *time.Time `json:"min_start_date"`
	MaxEndDate      *time.Time `json:"max_end_date"`
}

// gridTISummariesDTO is one Airflow 3.2.1 GridTISummaries — the NDJSON record
// emitted per DAG run.
type gridTISummariesDTO struct {
	DagID         string              `json:"dag_id"`
	RunID         string              `json:"run_id"`
	TaskInstances []lightTISummaryDTO `json:"task_instances"`
}

// taskAgg accumulates a task's summary across its tries: latest-try state, the
// earliest start and the latest end.
type taskAgg struct {
	taskID   string
	tryMax   int
	state    domain.TaskState
	minStart *time.Time
	maxEnd   *time.Time
}

// parseRunIDs reads run_ids from repeated query params and/or comma-separated
// values, preserving order and dropping blanks/duplicates.
func parseRunIDs(c *gin.Context) []string {
	seen := make(map[string]bool)
	out := make([]string, 0)
	for _, raw := range c.QueryArray("run_ids") {
		for _, id := range strings.Split(raw, ",") {
			id = strings.TrimSpace(id)
			if id != "" && !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}

// aggregateSummaries groups task instances by run then task, keeping the
// latest-try state and the min start / max end across tries. It returns the
// per-run map and the freshness key (latest timestamp, total instance count).
func aggregateSummaries(tis []domain.TaskInstance) (byRun map[string]map[string]*taskAgg, latest time.Time, count int) {
	byRun = make(map[string]map[string]*taskAgg)
	for _, ti := range tis {
		tasks := byRun[ti.RunID]
		if tasks == nil {
			tasks = make(map[string]*taskAgg)
			byRun[ti.RunID] = tasks
		}
		a := tasks[ti.TaskID]
		if a == nil {
			a = &taskAgg{taskID: ti.TaskID, tryMax: -1}
			tasks[ti.TaskID] = a
		}
		if ti.TryNumber >= a.tryMax {
			a.tryMax = ti.TryNumber
			a.state = ti.State
		}
		a.minStart = earliest(a.minStart, ti.StartedAt)
		a.maxEnd = latestOf(a.maxEnd, ti.EndedAt)
		latest = maxTime(latest, ti.StartedAt, ti.EndedAt)
	}
	return byRun, latest, len(tis)
}

func earliest(cur, candidate *time.Time) *time.Time {
	if candidate == nil {
		return cur
	}
	if cur == nil || candidate.Before(*cur) {
		return candidate
	}
	return cur
}

func latestOf(cur, candidate *time.Time) *time.Time {
	if candidate == nil {
		return cur
	}
	if cur == nil || candidate.After(*cur) {
		return candidate
	}
	return cur
}

func maxTime(cur time.Time, candidates ...*time.Time) time.Time {
	for _, c := range candidates {
		if c != nil && c.After(cur) {
			cur = *c
		}
	}
	return cur
}

// stateOrNil renders a task state as a pointer, with the "none" sentinel as JSON
// null (Airflow has no "none" member in TaskInstanceState).
func stateOrNil(s domain.TaskState) *string {
	if s == "" || s == domain.TaskStateNone {
		return nil
	}
	v := string(s)
	return &v
}

// tiSummariesHandler implements GET /ui/grid/ti_summaries/{dag_id}: an NDJSON
// stream (application/x-ndjson), one GridTISummaries object per requested run.
// One DB query backs it; results are grouped in Go. A weak ETag over the latest
// timestamp and instance count enables conditional GETs.
func tiSummariesHandler(reader TaskSummaryReader) gin.HandlerFunc {
	return func(c *gin.Context) {
		dagID := c.Param("dag_id")
		runIDs := parseRunIDs(c)
		tis, err := reader.TaskInstancesForRuns(c.Request.Context(), tenantOf(c), dagID, runIDs)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		byRun, latest, count := aggregateSummaries(tis)

		// #289: mark-state PATCH changes a TI's state without moving its
		// started/ended timestamps, so the original `count + latest` ETag
		// was identical before and after the mutation and the SPA's
		// TanStack Query kept serving the cached body. Fold a fingerprint
		// of every (run, task, state) into the ETag so any state change
		// invalidates it. Sort for stability (map iteration is random).
		fingerprints := make([]string, 0, len(tis))
		for _, ti := range tis {
			fingerprints = append(fingerprints, ti.RunID+":"+ti.TaskID+":"+string(ti.State))
		}
		sort.Strings(fingerprints)
		h := fnv.New64a()
		_, _ = h.Write([]byte(strings.Join(fingerprints, "|")))
		etag := fmt.Sprintf(`W/"%d-%d-%x"`, count, latest.UnixNano(), h.Sum64())
		c.Header("ETag", etag)
		c.Header("Content-Type", "application/x-ndjson")
		if match := c.GetHeader("If-None-Match"); match == etag {
			c.Status(http.StatusNotModified)
			return
		}

		enc := json.NewEncoder(c.Writer)
		for _, runID := range runIDs {
			rec := gridTISummariesDTO{DagID: dagID, RunID: runID, TaskInstances: []lightTISummaryDTO{}}
			for _, a := range byRun[runID] {
				rec.TaskInstances = append(rec.TaskInstances, lightTISummaryDTO{
					TaskID:          a.taskID,
					TaskDisplayName: a.taskID,
					State:           stateOrNil(a.state),
					MinStartDate:    a.minStart,
					MaxEndDate:      a.maxEnd,
				})
			}
			if err := enc.Encode(rec); err != nil {
				return // client disconnected mid-stream.
			}
		}
	}
}

// registerUISummaries mounts the grid ti-summaries stream when a reader is set.
func registerUISummaries(r gin.IRouter, reader TaskSummaryReader) {
	if reader == nil {
		return
	}
	r.GET("/ui/grid/ti_summaries/:dag_id", RequirePermission("read", "task_instance"), tiSummariesHandler(reader))
}
