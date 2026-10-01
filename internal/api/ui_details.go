package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/domain"
)

// timetableDescription renders a schedule into a human phrase for the DAG detail
// header, covering the @presets and a few common cron shapes, falling back to
// the raw expression. Returns nil (JSON null) for an unscheduled DAG.
func timetableDescription(schedule *string) *string {
	if schedule == nil || *schedule == "" {
		return nil
	}
	s := strings.TrimSpace(*schedule)
	presets := map[string]string{
		"@once": "Once", "@hourly": "Hourly", "@daily": "Daily", "@midnight": "Daily",
		"@weekly": "Weekly", "@monthly": "Monthly", "@yearly": "Yearly", "@annually": "Yearly",
	}
	if v, ok := presets[s]; ok {
		return &v
	}
	if v := describeCron(s); v != "" {
		return &v
	}
	return &s
}

// describeCron handles a handful of common 5-field cron expressions, returning
// "" when the shape is not recognized (the caller then shows the raw cron).
func describeCron(expr string) string {
	f := strings.Fields(expr)
	if len(f) != 5 {
		return ""
	}
	minute, hour, dom, mon, dow := f[0], f[1], f[2], f[3], f[4]
	if dom != "*" || mon != "*" || dow != "*" {
		return ""
	}
	if n, ok := everyN(minute); ok && hour == "*" {
		return fmt.Sprintf("Every %d minutes", n)
	}
	if n, ok := everyN(hour); ok && minute == "0" {
		return fmt.Sprintf("Every %d hours", n)
	}
	m, merr := strconv.Atoi(minute)
	h, herr := strconv.Atoi(hour)
	if merr == nil && herr == nil {
		return fmt.Sprintf("At %02d:%02d, every day", h, m)
	}
	return ""
}

// everyN parses a "*/N" step field, returning N and whether it matched.
func everyN(field string) (int, bool) {
	if !strings.HasPrefix(field, "*/") {
		return 0, false
	}
	n, err := strconv.Atoi(field[2:])
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// dagDetailsDTO is the Airflow 3.2.1 DAGDetailsResponse. As with the DAG list,
// every spec-required field is present; values Leoflow does not yet model are
// null or sensible defaults. See docs/ui-compatibility.md.
type dagDetailsDTO struct {
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
	Catchup                     bool             `json:"catchup"`
	DagRunTimeout               *string          `json:"dag_run_timeout"`
	AssetExpression             *json.RawMessage `json:"asset_expression"`
	DocMd                       *string          `json:"doc_md"`
	StartDate                   *string          `json:"start_date"`
	EndDate                     *string          `json:"end_date"`
	IsPausedUponCreation        *bool            `json:"is_paused_upon_creation"`
	Params                      *json.RawMessage `json:"params"`
	RenderTemplateAsNativeObj   bool             `json:"render_template_as_native_obj"`
	TemplateSearchPath          *[]string        `json:"template_search_path"`
	Timezone                    *string          `json:"timezone"`
	LastParsed                  *string          `json:"last_parsed"`
	DefaultArgs                 *json.RawMessage `json:"default_args"`
	FileToken                   string           `json:"file_token"`
	Concurrency                 int              `json:"concurrency"`
	LatestDagVersion            *dagVersionDTO   `json:"latest_dag_version"`
}

func toDagDetailsDTO(d domain.DAG) dagDetailsDTO {
	tags := make([]dagTagDTO, 0, len(d.Tags))
	for _, t := range d.Tags {
		tags = append(tags, dagTagDTO{Name: t, DagID: d.DagID})
	}
	// A DAG always has an owner in the UI; default to "airflow" (Airflow's
	// default) when the spec declares none, so the field is never blank.
	owner := d.Owner
	if owner == "" {
		owner = "airflow"
	}
	maxRuns := d.MaxActiveRuns
	summary := d.Schedule
	if summary == nil {
		s := "Never, external triggers only"
		summary = &s
	}
	tz := d.ScheduleTZ
	if tz == "" {
		tz = "UTC"
	}
	bundle := "leoflow"
	fileloc := d.DagID + "/dag.py"
	emptyObj := json.RawMessage("{}")
	return dagDetailsDTO{
		DagID:                d.DagID,
		DagDisplayName:       d.DagID,
		IsPaused:             d.IsPaused,
		IsStale:              !d.IsActive,
		LastParsedTime:       rfc3339Ptr(d.LastParsedTime),
		LastParsed:           rfc3339Ptr(d.LastParsedTime),
		BundleName:           &bundle,
		RelativeFileloc:      &fileloc,
		Description:          strPtrOrNil(d.Description),
		TimetableSummary:     summary,
		TimetableDescription: timetableDescription(d.Schedule),
		TimetablePartitioned: false,
		Tags:                 tags,
		MaxActiveTasks:       defaultMaxActiveTasks,
		MaxActiveRuns:        &maxRuns,
		AllowedRunTypes:      []string{"manual", "scheduled"},
		Owners:               []string{owner},
		Catchup:              d.Catchup,
		StartDate:            rfc3339Ptr(d.StartDate),
		IsPausedUponCreation: boolPtr(false),
		Params:               &emptyObj,
		DefaultArgs:          &emptyObj,
		Timezone:             &tz,
		Concurrency:          defaultMaxActiveTasks,
		Fileloc:              fileloc,
		FileToken:            synthID(d.DagID, "fileloc"),
	}
}

// paramsToAirflowDict reshapes Leoflow's stored declared params
// (map[name]{default, schema}) into the Airflow 3.2.1 serialized param-dict the
// trigger dialog's flexible form renders from: {name: {value, schema,
// description}}. Three transforms carry the whole feature:
//   - default -> value (the form reads entry.value for the prefilled value).
//   - a required param (no default) still emits an entry, as value:null, so the
//     field renders and the form's required-detector flags an empty required
//     value.
//   - schema is carried verbatim (type/title/enum/section/description drive the
//     widget choice and grouping); an absent schema becomes {} so the entry is
//     well-formed.
//
// description is null for now — the compiled artifact does not yet carry a
// per-param description. An empty (or nil) param map yields {}, so a param-free
// DAG serializes exactly as before and the dialog stays JSON-only.
func paramsToAirflowDict(params map[string]domain.ParamSpec) json.RawMessage {
	type airflowParam struct {
		Value       json.RawMessage `json:"value"`
		Schema      json.RawMessage `json:"schema"`
		Description *string         `json:"description"`
	}
	out := make(map[string]airflowParam, len(params))
	for name, p := range params {
		value := json.RawMessage("null")
		if len(p.Default) > 0 {
			value = p.Default
		}
		schema := json.RawMessage("{}")
		if len(p.Schema) > 0 {
			schema = p.Schema
		}
		out[name] = airflowParam{Value: value, Schema: schema, Description: nil}
	}
	raw, err := json.Marshal(out)
	if err != nil {
		// The inputs are already valid raw JSON, so marshaling the wrapper
		// cannot realistically fail; degrade to {} rather than break details.
		return json.RawMessage("{}")
	}
	return raw
}

// rfc3339Ptr formats a time pointer as an RFC3339 string pointer (nil-safe).
func rfc3339Ptr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

// boolPtr returns a pointer to b.
func boolPtr(b bool) *bool { return &b }

// dagDetailsHandler implements GET /api/v2/dags/{dag_id}/details. It populates
// latest_dag_version from the version lister — the Graph view reads the
// version_number from there to fetch version-scoped structure, so a null version
// leaves the graph blank.
func dagDetailsHandler(repo DagRepository, versions DagVersionLister, specs DagSpecReader) gin.HandlerFunc {
	return func(c *gin.Context) {
		dagID := c.Param("dag_id")
		d, err := repo.GetDag(c.Request.Context(), tenantOf(c), dagID)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		dto := toDagDetailsDTO(d)
		// Serve the DAG's author-declared params in Airflow's param-dict shape so
		// the trigger dialog renders its native typed form. This read is
		// best-effort: any spec-read failure leaves params as {} and the dialog
		// falls back to JSON-only — details must never break on it.
		if specs != nil {
			if spec, serr := specs.GetCurrentSpec(c.Request.Context(), tenantOf(c), dagID); serr == nil && len(spec.Params) > 0 {
				raw := paramsToAirflowDict(spec.Params)
				dto.Params = &raw
			}
		}
		if versions != nil {
			if vs, verr := versions.ListDagVersions(c.Request.Context(), tenantOf(c), dagID); verr == nil && len(vs) > 0 {
				latest := vs[0] // ListDagVersions is newest-first.
				dto.LatestDagVersion = &dagVersionDTO{
					ID: latest.ID, VersionNumber: latest.VersionNumber, DagID: dagID,
					BundleName: "leoflow", CreatedAt: latest.CreatedAt, DagDisplayName: dagID,
				}
			}
		}
		if d.CurrentVersion != "" {
			// Paired with the run's bundle_version by the SPA's clear dialog.
			v := d.CurrentVersion
			dto.BundleVersion = &v
		}
		c.JSON(http.StatusOK, dto)
	}
}
