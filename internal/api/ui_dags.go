package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/domain"
)

// defaultDagRunsLimit is how many recent runs /ui/dags embeds per DAG when the
// client does not specify dag_runs_limit (Airflow's UI default).
const defaultDagRunsLimit = 14

// DagLatestRunsReader fetches the most-recent runs for a set of DAGs in one
// query, so /ui/dags can embed run history without an N+1.
type DagLatestRunsReader interface {
	LatestRunsForDags(ctx context.Context, tenant string, dagIDs []string, perDag int) (map[string][]domain.DagRun, error)
}

// dagTagDTO is the Airflow 3.2.1 DagTagResponse.
type dagTagDTO struct {
	Name  string `json:"name"`
	DagID string `json:"dag_id"`
}

// dagWithRunsDTO is the Airflow 3.2.1 DAGWithLatestDagRunsResponse. Every
// spec-required field is present; values Dexaflow does not track yet are null
// (nullable fields) or sensible defaults (non-nullable), so the DAG list renders
// without misbehaving. See docs/ui-compatibility.md.
type dagWithRunsDTO struct {
	DagID                       string           `json:"dag_id"`
	DagDisplayName              string           `json:"dag_display_name"`
	IsPaused                    bool             `json:"is_paused"`
	IsStale                     bool             `json:"is_stale"`
	LastParsedTime              *string          `json:"last_parsed_time"`
	LastParseDuration           *float64         `json:"last_parse_duration"`
	LastExpired                 *string          `json:"last_expired"`
	BundleName                  *string          `json:"bundle_name"`
	BundleVersion               *string          `json:"bundle_version"`
	RelativeFileloc             *string          `json:"relative_fileloc"`
	Fileloc                     string           `json:"fileloc"`
	Description                 *string          `json:"description"`
	TimetableSummary            *string          `json:"timetable_summary"`
	TimetableDescription        *string          `json:"timetable_description"`
	TimetablePartitioned        bool             `json:"timetable_partitioned"`
	Tags                        []dagTagDTO      `json:"tags"`
	MaxActiveTasks              int              `json:"max_active_tasks"`
	MaxActiveRuns               *int             `json:"max_active_runs"`
	MaxConsecutiveFailedDagRuns int              `json:"max_consecutive_failed_dag_runs"`
	HasTaskConcurrencyLimits    bool             `json:"has_task_concurrency_limits"`
	HasImportErrors             bool             `json:"has_import_errors"`
	NextDagrunLogicalDate       *string          `json:"next_dagrun_logical_date"`
	NextDagrunDataIntervalStart *string          `json:"next_dagrun_data_interval_start"`
	NextDagrunDataIntervalEnd   *string          `json:"next_dagrun_data_interval_end"`
	NextDagrunRunAfter          *string          `json:"next_dagrun_run_after"`
	AllowedRunTypes             []string         `json:"allowed_run_types"`
	Owners                      []string         `json:"owners"`
	AssetExpression             *json.RawMessage `json:"asset_expression"`
	LatestDagRuns               []dagRunLightDTO `json:"latest_dag_runs"`
	PendingActions              []any            `json:"pending_actions"`
	IsFavorite                  bool             `json:"is_favorite"`
	FileToken                   string           `json:"file_token"`
}

type dagWithRunsCollectionDTO struct {
	Dags         []dagWithRunsDTO `json:"dags"`
	TotalEntries int              `json:"total_entries"`
}

// defaultMaxActiveTasks mirrors Airflow's per-DAG task concurrency default;
// Dexaflow does not model it yet but the field is required.
const defaultMaxActiveTasks = 16

func toDagWithRunsDTO(d domain.DAG, runs []domain.DagRun) dagWithRunsDTO {
	tags := make([]dagTagDTO, 0, len(d.Tags))
	for _, t := range d.Tags {
		tags = append(tags, dagTagDTO{Name: t, DagID: d.DagID})
	}
	// A DAG always has an owner in the UI; default to "airflow" (as the detail
	// view does) so the list never shows a blank owner.
	owner := d.Owner
	if owner == "" {
		owner = "airflow"
	}
	owners := []string{owner}
	// Match the detail view's schedule summary: the raw schedule, or the explicit
	// "external triggers only" phrasing when the DAG is unscheduled.
	summary := d.Schedule
	if summary == nil {
		s := "Never, external triggers only"
		summary = &s
	}
	latest := make([]dagRunLightDTO, 0, len(runs))
	for _, r := range runs {
		latest = append(latest, toDagRunLightDTO(r))
	}
	maxRuns := d.MaxActiveRuns
	var bundle *string
	if d.CurrentVersion != "" {
		// Paired with the run's bundle_version by the SPA's clear dialog, which
		// offers "Run with latest bundle version" only when the two differ.
		v := d.CurrentVersion
		bundle = &v
	}
	return dagWithRunsDTO{
		BundleVersion:               bundle,
		DagID:                       d.DagID,
		DagDisplayName:              d.DagID,
		IsPaused:                    d.IsPaused,
		IsStale:                     !d.IsActive,
		Description:                 strPtrOrNil(d.Description),
		TimetableSummary:            summary,
		TimetableDescription:        timetableDescription(d.Schedule),
		TimetablePartitioned:        false,
		Tags:                        tags,
		MaxActiveTasks:              defaultMaxActiveTasks,
		MaxActiveRuns:               &maxRuns,
		MaxConsecutiveFailedDagRuns: 0,
		AllowedRunTypes:             []string{"manual", "scheduled"},
		Owners:                      owners,
		LatestDagRuns:               latest,
		PendingActions:              []any{},
		FileToken:                   "",
		// Mirrors the details endpoint, which has carried the same derivation all
		// along, and the /api/v2/dags list, which is the route OpenMetadata reads.
		// This one is the UI's own list; it is here so the three agree rather than
		// because any consumer needs it.
		Fileloc: d.DagID + "/dag.py",
	}
}

// pausedFilter reads the optional ?paused= query param. Absent or unparseable
// returns nil (no filter); "true"/"false" returns a pointer to the bool.
func pausedFilter(c *gin.Context) *bool {
	raw, ok := c.GetQuery("paused")
	if !ok {
		return nil
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		return nil
	}
	return &b
}

// strPtrOrNil returns nil for an empty string so the JSON field renders null.
func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// uiDagsHandler implements GET /ui/dags: the UI DAG list with per-DAG embedded
// latest runs, fetched in two constant queries (page of DAGs + one windowed
// runs query), never per-DAG.
func uiDagsHandler(dags DagRepository, latest DagLatestRunsReader, favorites FavoriteStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		// #217 diagnostic: the SPA polls this every 1s on Lite and reportedly
		// freezes when a DAG breaks. If a single slow ListDagsFiltered or
		// LatestRunsForDags lights up here while the watcher is reloading the
		// workspace, that's the contention point. Two timings: per-query +
		// total. Grep the log for "duration_ms" outliers.
		t0 := time.Now()
		limit, offset := pagination(c)
		tenant := tenantOf(c)
		ds, total, err := dags.ListDagsFiltered(c.Request.Context(), tenant,
			c.Query("last_dag_run_state"), pausedFilter(c), limit, offset)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		listMs := time.Since(t0).Milliseconds()
		runsLimit := defaultDagRunsLimit
		if n, perr := strconv.Atoi(c.Query("dag_runs_limit")); perr == nil && n > 0 {
			runsLimit = n
		}
		ids := make([]string, len(ds))
		for i, d := range ds {
			ids[i] = d.DagID
		}
		t1 := time.Now()
		runsByDag, err := latest.LatestRunsForDags(c.Request.Context(), tenant, ids, runsLimit)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		runsMs := time.Since(t1).Milliseconds()
		slog.Info("/ui/dags",
			"tenant", tenant, "total", total, "list_ms", listMs, "runs_ms", runsMs,
			"total_ms", time.Since(t0).Milliseconds())
		// Resolve the user's favorites once so each DAG can report is_favorite (the
		// star). Best-effort: a lookup failure just leaves every star unset.
		var favs map[string]bool
		if favorites != nil {
			if got, ferr := favorites.FavoriteDagIDs(c.Request.Context(), tenant, favoriteUserID(c)); ferr == nil {
				favs = got
			}
		}
		out := dagWithRunsCollectionDTO{Dags: make([]dagWithRunsDTO, 0, len(ds)), TotalEntries: total}
		for _, d := range ds {
			dto := toDagWithRunsDTO(d, runsByDag[d.DagID])
			dto.IsFavorite = favs[d.DagID]
			out.Dags = append(out.Dags, dto)
		}
		setPaginationLinks(c, total, limit, offset)
		c.JSON(http.StatusOK, out)
	}
}
