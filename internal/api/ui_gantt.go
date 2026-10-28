package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// ganttTaskInstanceDTO is the Airflow 3.3 GanttTaskInstance: one attempt of
// one task, with the moments it was scheduled, queued, started and ended.
type ganttTaskInstanceDTO struct {
	TaskID          string     `json:"task_id"`
	TaskDisplayName string     `json:"task_display_name"`
	TryNumber       int        `json:"try_number"`
	State           *string    `json:"state"`
	ScheduledDttm   *time.Time `json:"scheduled_dttm"`
	QueuedDttm      *time.Time `json:"queued_dttm"`
	StartDate       *time.Time `json:"start_date"`
	EndDate         *time.Time `json:"end_date"`
	IsGroup         bool       `json:"is_group"`
	IsMapped        bool       `json:"is_mapped"`
}

// ganttResponseDTO is the Airflow GanttResponse.
type ganttResponseDTO struct {
	DagID         string                 `json:"dag_id"`
	RunID         string                 `json:"run_id"`
	TaskInstances []ganttTaskInstanceDTO `json:"task_instances"`
}

// ganttTaskScan caps how many task instances of one run the Gantt reads.
const ganttTaskScan = 10000

// ganttHandler implements GET /ui/gantt/{dag_id}/{run_id}: every attempt of
// every task of the run, oldest attempt first. The SPA's Gantt view polls it
// and reads task_instances unconditionally, so it must never answer the
// catch-all {}.
func ganttHandler(tasks TaskInstanceRepository) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, tenant := c.Request.Context(), tenantOf(c)
		dagID, runID := c.Param("dag_id"), c.Param("run_id")
		current, _, err := tasks.ListTaskInstances(ctx, tenant, dagID, runID, ganttTaskScan, 0)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		out := ganttResponseDTO{DagID: dagID, RunID: runID, TaskInstances: make([]ganttTaskInstanceDTO, 0, len(current))}
		seen := make(map[string]bool, len(current))
		for _, ti := range current {
			if seen[ti.TaskID] {
				continue // a mapped task lists once; its attempts cover every index
			}
			seen[ti.TaskID] = true
			attempts, aerr := tasks.ListTaskInstanceAttempts(ctx, tenant, dagID, runID, ti.TaskID)
			if aerr != nil {
				handleRepoError(c, aerr)
				return
			}
			for _, a := range attempts {
				dto := toTaskInstanceDTO(a)
				out.TaskInstances = append(out.TaskInstances, ganttTaskInstanceDTO{
					TaskID: a.TaskID, TaskDisplayName: dto.TaskDisplayName, TryNumber: a.TryNumber,
					State: dto.State, ScheduledDttm: a.ScheduledAt, QueuedDttm: a.QueuedAt,
					StartDate: a.StartedAt, EndDate: a.EndedAt, IsMapped: a.MapIndex >= 0,
				})
			}
		}
		c.JSON(http.StatusOK, out)
	}
}

// registerUIGantt mounts the Gantt view's data endpoint when task instances
// are readable.
func registerUIGantt(r gin.IRouter, tasks TaskInstanceRepository) {
	if tasks == nil {
		return
	}
	r.GET("/ui/gantt/:dag_id/:run_id", RequirePermission("read", "task_instance"), ganttHandler(tasks))
}
