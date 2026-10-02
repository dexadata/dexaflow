package api

import (
	"hash/fnv"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/ui"
	"github.com/dexadata/dexaflow/internal/version"
)

// gridRunDTO is the Airflow 3.2.1 GridRunsResponse — a DAG run as a grid column.
// duration is wall-clock seconds; run_after maps to the logical date (Dexaflow
// has no separate run_after); has_missed_deadline is always false (deadlines are
// not modeled in the MVP).
type gridRunDTO struct {
	DagID             string     `json:"dag_id"`
	RunID             string     `json:"run_id"`
	QueuedAt          *time.Time `json:"queued_at"`
	StartDate         *time.Time `json:"start_date"`
	EndDate           *time.Time `json:"end_date"`
	RunAfter          time.Time  `json:"run_after"`
	State             string     `json:"state"`
	RunType           string     `json:"run_type"`
	HasMissedDeadline bool       `json:"has_missed_deadline"`
	Duration          float64    `json:"duration"`
}

func toGridRunDTO(r domain.DagRun) gridRunDTO {
	return gridRunDTO{
		DagID:             r.DagID,
		RunID:             r.RunID,
		QueuedAt:          nonZeroTime(r.QueuedAt),
		StartDate:         r.StartedAt,
		EndDate:           r.EndedAt,
		RunAfter:          r.LogicalDate,
		State:             string(r.State),
		RunType:           r.RunType,
		HasMissedDeadline: false,
		Duration:          runDurationSeconds(r),
	}
}

// dagRunLightDTO is the Airflow 3.2.1 DAGRunLightResponse. The spec types id as
// an integer, but Dexaflow keys runs by (dag_id, run_id); id is a stable
// non-negative hash of run_id, used purely as a display/key value. Every /ui
// endpoint that fetches a run does so by run_id. See docs/ui-compatibility.md.
type dagRunLightDTO struct {
	ID          uint32     `json:"id"`
	DagID       string     `json:"dag_id"`
	RunID       string     `json:"run_id"`
	LogicalDate *time.Time `json:"logical_date"`
	RunAfter    time.Time  `json:"run_after"`
	StartDate   *time.Time `json:"start_date"`
	EndDate     *time.Time `json:"end_date"`
	State       string     `json:"state"`
	Duration    *float64   `json:"duration"`
}

func toDagRunLightDTO(r domain.DagRun) dagRunLightDTO {
	var dur *float64
	if r.StartedAt != nil {
		d := runDurationSeconds(r)
		dur = &d
	}
	return dagRunLightDTO{
		ID:          synthRunID(r.RunID),
		DagID:       r.DagID,
		RunID:       r.RunID,
		LogicalDate: nonZeroTime(r.LogicalDate),
		RunAfter:    r.LogicalDate,
		StartDate:   r.StartedAt,
		EndDate:     r.EndedAt,
		State:       string(r.State),
		Duration:    dur,
	}
}

// nonZeroTime returns a pointer to t, or nil if t is the zero value, so the JSON
// renders an explicit null for unset timestamps.
func nonZeroTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// runDurationSeconds returns the run's wall-clock duration in seconds: start to
// end, start to now while running, or 0 before it starts.
func runDurationSeconds(r domain.DagRun) float64 {
	if r.StartedAt == nil {
		return 0
	}
	end := time.Now().UTC()
	if r.EndedAt != nil {
		end = *r.EndedAt
	}
	return end.Sub(*r.StartedAt).Seconds()
}

// synthRunID derives a stable non-negative integer key from a run_id.
func synthRunID(runID string) uint32 {
	h := fnv.New32a()
	if _, err := h.Write([]byte(runID)); err != nil {
		return 0 // hash.Write never errors; satisfy the linter without ignoring it.
	}
	return h.Sum32()
}

// versionHandler implements GET /api/v2/version (Airflow VersionInfo). The
// embedded SPA reads `version` to build its documentation links
// (https://airflow.apache.org/docs/apache-airflow/<version>/…), so this MUST be
// the pinned Airflow UI version, not leoflow's build version — otherwise the UI
// points users at a nonexistent Airflow docs release (#594). leoflow's own
// version is surfaced on the CLI (`dexaflow version`), the health endpoints, and
// the MCP `health://control-plane` resource. git_version keeps leoflow's commit
// as a build reference for the control plane actually serving this compat UI.
func versionHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"version": ui.Version(), "git_version": version.Get().GitCommit})
	}
}

// gridRunsHandler implements GET /ui/grid/runs/{dag_id}: recent runs as grid
// columns, most-recent first (the repository orders by logical_date DESC).
func gridRunsHandler(repo DagRunRepository) gin.HandlerFunc {
	return func(c *gin.Context) {
		limit, offset := pagination(c)
		runs, _, err := repo.ListDagRuns(c.Request.Context(), tenantOf(c), c.Param("dag_id"), limit, offset)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		out := make([]gridRunDTO, 0, len(runs))
		for _, r := range runs {
			out = append(out, toGridRunDTO(r))
		}
		c.JSON(http.StatusOK, out)
	}
}

// latestRunHandler implements GET /ui/dags/{dag_id}/latest_run. The spec response
// is DAGRunLightResponse|null, so when the DAG has no runs it returns 200 null
// (not 404) — the SPA renders an empty header rather than erroring.
func latestRunHandler(repo DagRunRepository) gin.HandlerFunc {
	return func(c *gin.Context) {
		runs, _, err := repo.ListDagRuns(c.Request.Context(), tenantOf(c), c.Param("dag_id"), 1, 0)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		if len(runs) == 0 {
			c.JSON(http.StatusOK, nil)
			return
		}
		c.JSON(http.StatusOK, toDagRunLightDTO(runs[0]))
	}
}

// registerUIViews mounts the read-only /ui view endpoints (and the UI-support
// /api/v2/version) whose repositories are configured.
func registerUIViews(r gin.IRouter, deps Dependencies) {
	r.GET("/api/v2/version", versionHandler())
	if deps.DagRuns != nil {
		r.GET("/ui/grid/runs/:dag_id", RequirePermission("read", "dag_run"), gridRunsHandler(deps.DagRuns))
		r.GET("/ui/dags/:dag_id/latest_run", RequirePermission("read", "dag_run"), latestRunHandler(deps.DagRuns))
	}
	if deps.Dags != nil && deps.LatestRuns != nil {
		r.GET("/ui/dags", RequirePermission("read", "dag"), uiDagsHandler(deps.Dags, deps.LatestRuns, deps.Favorites))
	}
	if deps.DagVersions != nil {
		r.GET("/api/v2/dags/:dag_id/dagVersions", RequirePermission("read", "dag"), dagVersionsHandler(deps.DagVersions))
		r.GET("/api/v2/dags/:dag_id/dagVersions/:version_number", RequirePermission("read", "dag"), dagVersionHandler(deps.DagVersions))
	}
}
