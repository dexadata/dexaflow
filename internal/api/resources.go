package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/dexadata/dexaflow/internal/domain"
)

// ErrNotFound is returned by repositories when a resource does not exist.
var ErrNotFound = domain.ErrNotFound

// DagRepository reads, updates, and deletes registered DAGs.
type DagRepository interface {
	ListDags(ctx context.Context, tenant string, limit, offset int) ([]domain.DAG, int, error)
	GetDag(ctx context.Context, tenant, dagID string) (domain.DAG, error)
	SetPaused(ctx context.Context, tenant, dagID string, paused bool) (domain.DAG, error)
	DeleteDag(ctx context.Context, tenant, dagID string) error
	ClearDagHistory(ctx context.Context, tenant, dagID string) error
	ListDagsFiltered(ctx context.Context, tenant, runState string, paused *bool, limit, offset int) ([]domain.DAG, int, error)
}

// DagRunRepository reads and creates DAG runs.
type DagRunRepository interface {
	ListDagRuns(ctx context.Context, tenant, dagID string, limit, offset int) ([]domain.DagRun, int, error)
	GetDagRun(ctx context.Context, tenant, dagID, runID string) (domain.DagRun, error)
	CreateDagRun(ctx context.Context, tenant, dagID string, run domain.DagRun) (domain.DagRun, error)
	SetDagRunState(ctx context.Context, tenant, dagID, runID, state string) error
	DeleteDagRun(ctx context.Context, tenant, dagID, runID string) error
}

// TaskInstanceRepository reads task instances, clears them for re-run, and sets
// their state directly (the UI's mark-success/failed actions).
type TaskInstanceRepository interface {
	ListTaskInstances(ctx context.Context, tenant, dagID, runID string, limit, offset int) ([]domain.TaskInstance, int, error)
	// ListTaskInstanceAttempts returns every attempt for (run, task), oldest
	// first — the current row UNIONed with the archived history. The UI's
	// /tries endpoint needs all attempts to render its navigable tabs.
	ListTaskInstanceAttempts(ctx context.Context, tenant, dagID, runID, taskID string) ([]domain.TaskInstance, error)
	ClearTaskInstances(ctx context.Context, tenant, dagID, runID string, taskIDs []string, onlyFailed bool, opts domain.ClearOptions) (int, error)
	SetTaskInstanceState(ctx context.Context, tenant, dagID, runID, taskID, state string) error
}

// AuditWriter records task-level actions (clear, mark state) for the Audit Log
// view, with the acting user and the run/task in the entry.
type AuditWriter interface {
	RecordTaskActionAudit(ctx context.Context, tenant, userID, action, dagID, runID, taskID string, tryNumber int) error
}

// recordTaskAudit writes a best-effort audit entry for a task action; an audit
// failure must not fail the action the user requested.
func recordTaskAudit(c *gin.Context, audit AuditWriter, action, runID, taskID string, tryNumber int) {
	if audit == nil {
		return
	}
	userID := ""
	if u, ok := UserFromContext(c); ok {
		userID = u.ID
	}
	if err := audit.RecordTaskActionAudit(c.Request.Context(), tenantOf(c), userID, action,
		c.Param("dag_id"), runID, taskID, tryNumber); err != nil {
		slog.Warn("recording task audit", "action", action, "task", taskID, "error", err)
	}
}

func pagination(c *gin.Context) (limit, offset int) {
	limit, offset = 100, 0
	if n, err := strconv.Atoi(c.Query("limit")); err == nil && n > 0 {
		limit = n
	}
	if n, err := strconv.Atoi(c.Query("offset")); err == nil && n >= 0 {
		offset = n
	}
	return capPageLimit(c, limit), offset
}

// contextKeyMaxPageLimit carries server.max_page_limit into the handlers.
const contextKeyMaxPageLimit = "leoflow.max_page_limit"

// maxPageLimit stamps the configured page-size cap on every request, so the
// shared pagination helpers can read it. Registered only when a cap is set.
func maxPageLimit(n int) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(contextKeyMaxPageLimit, n)
		c.Next()
	}
}

// capPageLimit clamps n to server.max_page_limit when one is configured, the
// way Airflow serves a limit above [api] maximum_page_limit.
func capPageLimit(c *gin.Context, n int) int {
	if maxN := c.GetInt(contextKeyMaxPageLimit); maxN > 0 && n > maxN {
		return maxN
	}
	return n
}

// dagRunPageLister lists a DAG's runs without counting them all. Handlers
// that discard the total use it when the repository offers it.
type dagRunPageLister interface {
	ListDagRunsPage(ctx context.Context, tenant, dagID string, limit, offset int) ([]domain.DagRun, error)
}

// listDagRunsPage returns one page of runs, skipping the COUNT over every run
// of the DAG when the repository can.
func listDagRunsPage(c *gin.Context, repo DagRunRepository, dagID string, limit, offset int) ([]domain.DagRun, error) {
	if p, ok := repo.(dagRunPageLister); ok {
		return p.ListDagRunsPage(c.Request.Context(), tenantOf(c), dagID, limit, offset)
	}
	runs, _, err := repo.ListDagRuns(c.Request.Context(), tenantOf(c), dagID, limit, offset)
	return runs, err
}

// setPaginationLinks sets an RFC 5988 Link header with next/prev relations.
func setPaginationLinks(c *gin.Context, total, limit, offset int) {
	links := make([]string, 0, 2)
	path := c.Request.URL.Path
	if offset+limit < total {
		links = append(links, fmt.Sprintf(`<%s?limit=%d&offset=%d>; rel="next"`, path, limit, offset+limit))
	}
	if offset > 0 {
		prev := offset - limit
		if prev < 0 {
			prev = 0
		}
		links = append(links, fmt.Sprintf(`<%s?limit=%d&offset=%d>; rel="prev"`, path, limit, prev))
	}
	if len(links) > 0 {
		c.Header("Link", strings.Join(links, ", "))
	}
}

func tenantOf(c *gin.Context) string {
	if u, ok := UserFromContext(c); ok && u.TenantID != "" {
		return u.TenantID
	}
	return "default"
}

// statusClientClosedRequest (499, nginx convention) marks a request the client
// aborted before the server finished — not a server fault.
const statusClientClosedRequest = 499

// Client-facing details for a repository failure whose cause must stay
// server-side. They are constants because a repository error normally wraps a
// driver error, and pgconn renders a *pgconn.PgError as "severity: message
// (SQLSTATE code)" with our constraint, table and column names inside the
// message — schema disclosure to any authenticated tenant (CWE-209, #961).
//
// One phrase per condition, not one phrase overall: the split is the same one
// unreadyDetail makes in health.go. "Does not exist", "conflicts", "was
// rejected" and "could not be completed" send a caller to four different
// places, and collapsing them into a single opaque message would cost real
// diagnosability to buy no extra privacy.
const (
	detailNotFound     = "the requested resource does not exist"
	detailConflict     = "the request conflicts with the current state of the resource"
	detailClientClosed = "the client closed the request before it completed"
	detailInvalidInput = "the request was rejected by a validation rule"
	detailInternal     = "the request could not be completed; see the server logs"
)

// safeDetail returns the phrase Dexaflow composed for this failure, or fallback
// when the error carries no such phrase.
//
// The default is deny. Only a domain.SafeError — an error someone deliberately
// wrote as client-facing — contributes text to a response body; everything
// else, including every fmt.Errorf wrap of a driver error, is replaced. That
// way a newly introduced storage error cannot leak by omission: it leaks only
// if someone rewrites it as a SafeError, which is the one construct whose
// GoDoc says its message is shown to clients.
func safeDetail(err error, fallback string) string {
	var safe *domain.SafeError
	if errors.As(err, &safe) && safe.ClientMessage() != "" {
		return safe.ClientMessage()
	}
	return fallback
}

// handleRepoError maps a repository failure to a status and a client-safe
// detail, and hands the real error to the request log (see AbortProblemCause).
// It is the single funnel for storage errors in this package; a handler that
// renders a repository error any other way must apply the same redaction.
func handleRepoError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		AbortProblemCause(c, http.StatusNotFound, "not found", safeDetail(err, detailNotFound), err)
	case errors.Is(err, domain.ErrConflict):
		AbortProblemCause(c, http.StatusConflict, "conflict", safeDetail(err, detailConflict), err)
	// A canceled/timed-out context means the client went away (the UI routinely
	// supersedes in-flight grid requests). That is NOT a server error — mapping it
	// to 500 produced spurious ti_summaries 500s under rapid refresh. Report 499 so
	// it logs as a client-side 4xx, not a server fault. The detail is fixed rather
	// than composed: pgconn reports a connect timeout as a ConnectError whose text
	// carries the database user and name, and that error satisfies
	// errors.Is(err, context.DeadlineExceeded).
	//
	// Gate on the REQUEST's context, not on the error's chain. A pgconn connect
	// timeout satisfies errors.Is(err, context.DeadlineExceeded) while the caller
	// is still perfectly connected, so chain-matching alone reported a database
	// outage as the tenant hanging up: WARN instead of ERROR, a 4xx instead of a
	// 5xx, and a 5xx-rate alert that misses the one incident it exists to catch
	// (#1071). A caller who went away has a done context; a dead database does not.
	case c.Request != nil && c.Request.Context().Err() != nil &&
		(errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)):
		AbortProblemCause(c, statusClientClosedRequest, "client closed request", detailClientClosed, err)
	// A business-rule input failure (unknown role, undeclared variable/connection)
	// is the client's to fix, not a server fault, so the whole class maps to 400 —
	// otherwise it reads as a server error and points users at the logs instead of
	// their request. The rule that rejected it is stated only when the storage
	// layer composed the phrase itself with domain.Safef.
	case errors.Is(err, domain.ErrValidation):
		AbortProblemCause(c, http.StatusBadRequest, "invalid request", safeDetail(err, detailInvalidInput), err)
	default:
		AbortProblemCause(c, http.StatusInternalServerError, "internal error", detailInternal, err)
	}
}

func listDagsHandler(repo DagRepository) gin.HandlerFunc {
	return func(c *gin.Context) {
		limit, offset := pagination(c)
		dags, total, err := repo.ListDags(c.Request.Context(), tenantOf(c), limit, offset)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		out := dagCollectionDTO{Dags: make([]dagDTO, 0, len(dags)), TotalEntries: total}
		for _, d := range dags {
			out.Dags = append(out.Dags, toDagDTO(d))
		}
		setPaginationLinks(c, total, limit, offset)
		c.JSON(http.StatusOK, out)
	}
}

func getDagHandler(repo DagRepository) gin.HandlerFunc {
	return func(c *gin.Context) {
		d, err := repo.GetDag(c.Request.Context(), tenantOf(c), c.Param("dag_id"))
		if err != nil {
			handleRepoError(c, err)
			return
		}
		c.JSON(http.StatusOK, toDagDTO(d))
	}
}

func patchDagHandler(repo DagRepository) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body struct {
			IsPaused bool `json:"is_paused"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", err.Error())
			return
		}
		d, err := repo.SetPaused(c.Request.Context(), tenantOf(c), c.Param("dag_id"), body.IsPaused)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		c.JSON(http.StatusOK, toDagDTO(d))
	}
}

// deleteDagHandler implements DELETE /api/v2/dags/{dag_id}. By default (the UI
// trash) it CLEARS the DAG's run history — deletes its runs, task instances, and
// XCom while keeping the DAG registered — because a GitOps DAG does not reload on
// its own, so destroying it would be surprising and lossy (ADR 0020). With
// ?deregister=true it removes the DAG artifact entirely (cascade). Returns 204.
func deleteDagHandler(repo DagRepository) gin.HandlerFunc {
	return func(c *gin.Context) {
		dagID := c.Param("dag_id")
		var err error
		if c.Query("deregister") == "true" {
			err = repo.DeleteDag(c.Request.Context(), tenantOf(c), dagID)
		} else {
			err = repo.ClearDagHistory(c.Request.Context(), tenantOf(c), dagID)
		}
		if err != nil {
			handleRepoError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// deleteDagRunHandler implements DELETE /api/v2/dags/{dag_id}/dagRuns/{run_id},
// mirroring Airflow: it removes a single run (task instances and XCom cascade)
// and returns 204, or 404 when the run does not exist. The UI's "delete run"
// action calls this; without the route it 404'd as "API route not found".
func deleteDagRunHandler(repo DagRunRepository) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := repo.DeleteDagRun(c.Request.Context(), tenantOf(c), c.Param("dag_id"), c.Param("dag_run_id")); err != nil {
			handleRepoError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// listRunsFiltered returns a page of a DAG's runs, optionally restricted to the
// given states (the UI's "failed runs" widget filters with ?state=failed). With
// no state filter it pages directly; with one, it filters the full set first so
// the page and total reflect the filter (run counts are small — a SQL-level
// filter is a follow-up for scale).
func listRunsFiltered(c *gin.Context, repo DagRunRepository, states []string, limit, offset int) ([]domain.DagRun, int, error) {
	if len(states) == 0 {
		return repo.ListDagRuns(c.Request.Context(), tenantOf(c), c.Param("dag_id"), limit, offset)
	}
	want := make(map[string]bool, len(states))
	for _, s := range states {
		want[s] = true
	}
	all, err := listDagRunsPage(c, repo, c.Param("dag_id"), maxRunScan, 0)
	if err != nil {
		return nil, 0, err
	}
	filtered := make([]domain.DagRun, 0, len(all))
	for _, r := range all {
		if want[string(r.State)] {
			filtered = append(filtered, r)
		}
	}
	total := len(filtered)
	if offset >= total {
		return []domain.DagRun{}, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return filtered[offset:end], total, nil
}

// maxRunScan caps the in-memory state-filter scan of a DAG's runs.
const maxRunScan = 10000

// patchDagRunHandler implements PATCH /api/v2/dags/{dag_id}/dagRuns/{dag_run_id}:
// the UI's mark-run-success/failed action. It sets the run state (and audits the
// action with the acting user); note updates are a follow-up.
func patchDagRunHandler(repo DagRunRepository, audit AuditWriter) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body struct {
			State string `json:"state"`
			Note  string `json:"note"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", err.Error())
			return
		}
		runID := c.Param("dag_run_id")
		if body.State != "" {
			if !validRunState(body.State) {
				AbortProblem(c, http.StatusBadRequest, "bad request", "state must be queued, running, success, or failed")
				return
			}
			if err := repo.SetDagRunState(c.Request.Context(), tenantOf(c), c.Param("dag_id"), runID, body.State); err != nil {
				handleRepoError(c, err)
				return
			}
			recordTaskAudit(c, audit, "dagrun.mark."+body.State, runID, "", 0)
		}
		run, err := repo.GetDagRun(c.Request.Context(), tenantOf(c), c.Param("dag_id"), runID)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		c.JSON(http.StatusOK, toDagRunDTO(run))
	}
}

// validRunState reports whether s is a state the UI may set a run to directly.
func validRunState(s string) bool {
	switch domain.DagRunState(s) {
	case domain.DagRunStateQueued, domain.DagRunStateRunning, domain.DagRunStateSuccess, domain.DagRunStateFailed:
		return true
	default:
		return false
	}
}

func listDagRunsHandler(repo DagRunRepository) gin.HandlerFunc {
	return func(c *gin.Context) {
		// "~" is Airflow's wildcard for "all DAGs"; the UI home polls
		// GET /api/v2/dags/~/dagRuns for a global run view. Dexaflow has no
		// cross-DAG run query yet, so degrade to an empty collection (200) rather
		// than 404 (which would resolve "~" as a missing DAG). Real cross-DAG
		// aggregation is a follow-up.
		if c.Param("dag_id") == "~" {
			c.JSON(http.StatusOK, dagRunCollectionDTO{DagRuns: []dagRunDTO{}, TotalEntries: 0})
			return
		}
		limit, offset := pagination(c)
		states := c.QueryArray("state")
		runs, total, err := listRunsFiltered(c, repo, states, limit, offset)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		out := dagRunCollectionDTO{DagRuns: make([]dagRunDTO, 0, len(runs)), TotalEntries: total}
		for _, r := range runs {
			out.DagRuns = append(out.DagRuns, toDagRunDTO(r))
		}
		setPaginationLinks(c, total, limit, offset)
		c.JSON(http.StatusOK, out)
	}
}

func getDagRunHandler(repo DagRunRepository) gin.HandlerFunc {
	return func(c *gin.Context) {
		r, err := repo.GetDagRun(c.Request.Context(), tenantOf(c), c.Param("dag_id"), c.Param("dag_run_id"))
		if err != nil {
			handleRepoError(c, err)
			return
		}
		c.JSON(http.StatusOK, toDagRunDTO(r))
	}
}

// applyDeclaredParams merges the DAG version's declared param defaults under the
// supplied conf (conf wins per key), validates each declared value against its
// JSON Schema, and returns the merged conf to persist. It returns ok=false with
// no change when the DAG declares no params (or the spec cannot be read) — the
// trigger then proceeds with the raw conf, identical to a param-free DAG. On a
// schema violation it aborts the request with 400 and returns ok=false. Conf
// keys with no declared param are carried through unchanged (Airflow's
// dag_run_conf_overrides_params default).
func applyDeclaredParams(c *gin.Context, specs DagSpecReader, dagID string, conf map[string]json.RawMessage) (json.RawMessage, bool) {
	if specs == nil {
		return nil, false
	}
	spec, err := specs.GetCurrentSpec(c.Request.Context(), tenantOf(c), dagID)
	if errors.Is(err, ErrNotFound) {
		// No registered version yet — nothing to validate against; the create
		// path handles the missing DAG. Not a validation-skipping error.
		return nil, false
	}
	if err != nil {
		// A real spec-read failure must NOT silently skip param validation on a
		// DAG that may declare typed params — fail loud instead of fail open.
		// The read goes to storage, so the error is redacted like any other
		// (#961) and the cause is logged.
		AbortProblemCause(c, http.StatusInternalServerError, "internal error", detailInternal, err)
		return nil, false
	}
	if len(spec.Params) == 0 {
		return nil, false
	}
	merged := mergeParams(spec.Params, conf)
	if !validateMergedParams(c, spec.Params, merged) {
		return nil, false
	}
	out, merr := json.Marshal(merged)
	if merr != nil {
		AbortProblemCause(c, http.StatusInternalServerError, "internal error", detailInternal, merr)
		return nil, false
	}
	return out, true
}

// mergeParams overlays the supplied conf onto the declared param defaults (conf
// wins per key). A param with no default contributes nothing; undeclared conf
// keys are carried through.
func mergeParams(params map[string]domain.ParamSpec, conf map[string]json.RawMessage) map[string]json.RawMessage {
	merged := make(map[string]json.RawMessage, len(params)+len(conf))
	for name, p := range params {
		if len(p.Default) > 0 {
			merged[name] = p.Default
		}
	}
	for k, v := range conf {
		merged[k] = v
	}
	return merged
}

// validateMergedParams enforces required params (a declared param with no
// default must be supplied) and validates each declared value against its JSON
// Schema. It aborts the request with 400 on the first violation and returns
// false; true means the merged conf is valid to persist.
func validateMergedParams(c *gin.Context, params map[string]domain.ParamSpec, merged map[string]json.RawMessage) bool {
	for name, p := range params {
		if len(p.Default) != 0 {
			continue
		}
		if _, ok := merged[name]; !ok {
			AbortProblem(c, http.StatusBadRequest, "bad request",
				fmt.Sprintf("conf param %q is required (the DAG declares it with no default)", name))
			return false
		}
	}
	for name, p := range params {
		if len(p.Schema) == 0 || string(p.Schema) == "{}" {
			continue
		}
		val, present := merged[name]
		if !present {
			continue
		}
		if verr := validateParamValue(p.Schema, val); verr != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request",
				fmt.Sprintf("conf param %q: %s", name, verr.Error()))
			return false
		}
	}
	return true
}

// validateParamValue checks one conf value against a param's JSON Schema,
// returning an error describing the first violation.
func validateParamValue(schema, value json.RawMessage) error {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema))
	if err != nil {
		return fmt.Errorf("parsing schema: %w", err)
	}
	comp := jsonschema.NewCompiler()
	if aerr := comp.AddResource("param_schema.json", doc); aerr != nil {
		return fmt.Errorf("loading schema: %w", aerr)
	}
	compiled, err := comp.Compile("param_schema.json")
	if err != nil {
		return fmt.Errorf("compiling schema: %w", err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(value))
	if err != nil {
		return fmt.Errorf("value is not valid JSON: %w", err)
	}
	return compiled.Validate(inst)
}

func createDagRunHandler(repo DagRunRepository, specs DagSpecReader, audit AuditWriter) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body struct {
			DagRunID    string          `json:"dag_run_id"`
			LogicalDate *time.Time      `json:"logical_date"`
			Note        string          `json:"note"`
			Conf        json.RawMessage `json:"conf"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", err.Error())
			return
		}
		// conf becomes the run's params; it must be a JSON object so it maps to
		// keyed values, not an array or scalar. Absent conf leaves the run at the
		// persisted empty-object default.
		var conf map[string]json.RawMessage
		if len(body.Conf) > 0 {
			if err := json.Unmarshal(body.Conf, &conf); err != nil {
				AbortProblem(c, http.StatusBadRequest, "bad request", "conf must be a JSON object: "+err.Error())
				return
			}
			// A JSON null unmarshals into a nil map without error; treat it as
			// absent so the run defaults to the empty object rather than
			// persisting a literal null (conf is contractually never null).
			if conf == nil {
				body.Conf = nil
			}
		}
		// Merge the DAG version's declared param defaults under the supplied conf
		// (conf wins per key), validate each declared value against its schema, and
		// persist the merged result so defaults are materialized into the run. A DAG
		// that declares no params (or no Specs reader wired) leaves conf untouched —
		// behavior identical to the pre-params conf pipeline.
		if merged, ok := applyDeclaredParams(c, specs, c.Param("dag_id"), conf); ok {
			body.Conf = merged
		} else if c.IsAborted() {
			return
		}
		logical := time.Now().UTC()
		if body.LogicalDate != nil {
			logical = *body.LogicalDate
		}
		runID := body.DagRunID
		if runID != "" {
			// A caller-supplied run id becomes a path segment in the log sink.
			if err := domain.ValidateRunID(runID); err != nil {
				AbortProblem(c, http.StatusBadRequest, "bad request", err.Error())
				return
			}
		}
		if runID == "" {
			// Airflow-style identifier; also avoids an empty/duplicate run_id,
			// which dag_runs forbids via UNIQUE (dag_id, run_id).
			runID = "manual__" + logical.Format(time.RFC3339)
		}
		run := domain.DagRun{
			RunID:       runID,
			LogicalDate: logical,
			State:       domain.DagRunStateQueued,
			RunType:     "manual",
			QueuedAt:    time.Now().UTC(),
			Note:        body.Note,
			Conf:        body.Conf,
		}
		created, err := repo.CreateDagRun(c.Request.Context(), tenantOf(c), c.Param("dag_id"), run)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		// Audit the trigger (run-level: no task) with the acting user as owner.
		recordTaskAudit(c, audit, "dagrun."+run.RunType+".trigger", created.RunID, "", 0)
		c.JSON(http.StatusCreated, toDagRunDTO(created))
	}
}

func listTaskInstancesHandler(repo TaskInstanceRepository, runs DagRunRepository, versions DagVersionLister) gin.HandlerFunc {
	return func(c *gin.Context) {
		// "~" wildcards "all runs"; the UI overview polls
		// dagRuns/~/taskInstances?state=failed for its Failed-Tasks widget. We
		// have no cross-run task-instance query, so degrade to empty (200) rather
		// than 404 (which resolves "~" as a missing run). Follow-up: real query.
		if c.Param("dag_run_id") == "~" {
			c.JSON(http.StatusOK, taskInstanceCollectionDTO{TaskInstances: []taskInstanceDTO{}, TotalEntries: 0})
			return
		}
		limit, offset := pagination(c)
		tis, total, err := repo.ListTaskInstances(c.Request.Context(), tenantOf(c), c.Param("dag_id"), c.Param("dag_run_id"), limit, offset)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		// All instances in a run share the run-derived fields and DAG version, so
		// resolve them once rather than per task instance.
		logical, version := resolveRunContext(c, runs, versions)
		out := taskInstanceCollectionDTO{TaskInstances: make([]taskInstanceDTO, 0, len(tis)), TotalEntries: total}
		for _, ti := range tis {
			dto := toTaskInstanceDTO(ti)
			dto.LogicalDate, dto.RunAfter, dto.DagVersion = logical, logical, version
			out.TaskInstances = append(out.TaskInstances, dto)
		}
		setPaginationLinks(c, total, limit, offset)
		c.JSON(http.StatusOK, out)
	}
}

// clearTaskIDs accepts the Airflow UI's task_ids, where each element is either a
// task id string OR a [task_id, map_index] tuple (mapped tasks). Lite is unmapped,
// so the map index is dropped. Without this, the real UI payload (e.g.
// [["hello", -1]]) fails to bind and the clear request 400s.
type clearTaskIDs []string

// UnmarshalJSON decodes each task_ids element as a string or a [task_id, map_index] tuple.
func (t *clearTaskIDs) UnmarshalJSON(b []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	out := make([]string, 0, len(raw))
	for _, el := range raw {
		var s string
		if json.Unmarshal(el, &s) == nil {
			out = append(out, s)
			continue
		}
		var tuple []json.RawMessage
		if err := json.Unmarshal(el, &tuple); err != nil || len(tuple) == 0 {
			return fmt.Errorf("task_ids element must be a string or [task_id, map_index] tuple")
		}
		var id string
		if err := json.Unmarshal(tuple[0], &id); err != nil {
			return fmt.Errorf("task_ids tuple task id: %w", err)
		}
		out = append(out, id)
	}
	*t = out
	return nil
}

type clearRequest struct {
	TaskIDs            clearTaskIDs `json:"task_ids"`
	DagRunID           string       `json:"dag_run_id"`
	OnlyFailed         *bool        `json:"only_failed"`
	OnlyRunning        *bool        `json:"only_running"`
	ResetDagRuns       *bool        `json:"reset_dag_runs"`
	RunOnLatestVersion *bool        `json:"run_on_latest_version"`
	DryRun             *bool        `json:"dry_run"`
	IncludeUpstream    bool         `json:"include_upstream"`
	IncludeDownstream  bool         `json:"include_downstream"`
	IncludePast        bool         `json:"include_past"`
	IncludeFuture      bool         `json:"include_future"`
}

func clearTaskInstancesHandler(repo TaskInstanceRepository, runs DagRunRepository, versions DagVersionLister, specs DagSpecReader, audit AuditWriter) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body clearRequest
		if err := c.ShouldBindJSON(&body); err != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", err.Error())
			return
		}
		// only_running is declared here and in the spec and is read by nothing, so
		// it has always been accepted and discarded. Refuse it rather than keep
		// discarding it, because the default change below makes silence dangerous:
		// Airflow rejects only_failed and only_running both true and defaults
		// only_failed to true, so `{"only_failed": false, "only_running": true}` is
		// the ONLY way to ask Airflow for "clear the running ones". Read here with
		// only_running dropped, that same body means "clear every task instance
		// named, successes included" - the narrowest request in the compatibility
		// target becoming the widest one here.
		//
		// Refusing is honest. Implementing it is a feature: Airflow sets RESTARTING
		// and kills the running task, so it needs a running-only reset and pod
		// termination, not a predicate change.
		if body.OnlyRunning != nil && *body.OnlyRunning {
			AbortProblem(c, http.StatusBadRequest, "bad request",
				"only_running is not supported: Dexaflow cannot clear a running task instance. "+
					"Airflow's equivalent sets the task to RESTARTING and kills it; this server has no such path, "+
					"and honoring only_failed=false alone would clear every task instance named by the request, "+
					"including ones that succeeded. Wait for the task to settle, or name the task instances explicitly.")
			return
		}
		// Airflow 3.2.1 declares only_failed=True and dry_run=True on
		// ClearTaskInstancesBody. leoflow defaulted both the other way, so the same
		// unflagged request that previews on Airflow executed here, over every named
		// task instance including the ones that had succeeded (#1137). Compatibility
		// with that API is the declared target, and the safe direction happens to be
		// the same direction, so a nil field now means what Airflow means.
		onlyFailed := body.OnlyFailed == nil || *body.OnlyFailed
		// Expand the task set by the DAG topology when up/downstream is requested,
		// then fan the same set across the target runs (current + past/future).
		taskIDs := expandClearTasks(c, specs, body)
		targets := clearTargetRuns(c, runs, body.DagRunID, body.IncludePast, body.IncludeFuture)
		affected := make([]taskInstanceDTO, 0)
		for _, rid := range targets {
			affected = append(affected, affectedTaskInstances(c, repo, runs, versions, rid, taskIDs, onlyFailed)...)
		}
		// The UI previews with dry_run=true before confirming; it expects the set of
		// affected task instances back (TaskInstanceCollectionResponse), not a count.
		if body.DryRun == nil || *body.DryRun {
			c.JSON(http.StatusOK, taskInstanceCollectionDTO{TaskInstances: affected, TotalEntries: len(affected)})
			return
		}
		// Defaults match Apache Airflow 3.x: a run keeps the version it was created
		// with unless the caller asks for the current one. A clear then reproduces
		// the attempt it is clearing, which is what makes clearing a week-old task
		// mean anything; testing a fix is a NEW run, or an explicit
		// run_on_latest_version=true.
		opts := domain.ClearOptions{ResetDagRun: true, RunOnLatestVersion: false}
		if body.ResetDagRuns != nil {
			opts.ResetDagRun = *body.ResetDagRuns
		}
		if body.RunOnLatestVersion != nil {
			opts.RunOnLatestVersion = *body.RunOnLatestVersion
		}
		for _, rid := range targets {
			if _, err := repo.ClearTaskInstances(c.Request.Context(), tenantOf(c), c.Param("dag_id"), rid, taskIDs, onlyFailed, opts); err != nil {
				handleRepoError(c, err)
				return
			}
		}
		for _, ti := range affected {
			recordTaskAudit(c, audit, "taskinstance.clear", ti.DagRunID, ti.TaskID, ti.TryNumber)
		}
		c.JSON(http.StatusOK, taskInstanceCollectionDTO{TaskInstances: affected, TotalEntries: len(affected)})
	}
}

// expandClearTasks expands the requested task_ids along the DAG topology when
// include_upstream/downstream is set (needs the spec); otherwise returns them
// unchanged. An empty task list (clear-the-whole-run) is left empty.
func expandClearTasks(c *gin.Context, specs DagSpecReader, body clearRequest) []string {
	seeds := []string(body.TaskIDs)
	if specs == nil || len(seeds) == 0 || (!body.IncludeUpstream && !body.IncludeDownstream) {
		return seeds
	}
	spec, err := specs.GetCurrentSpec(c.Request.Context(), tenantOf(c), c.Param("dag_id"))
	if err != nil {
		return seeds
	}
	return expandTaskIDs(spec.Tasks, seeds, body.IncludeUpstream, body.IncludeDownstream)
}

// clearTargetRuns resolves which runs a clear applies to: just the named run, or
// also the DAG's past/future runs (by logical_date) when include_past/future is
// set. Falls back to the single run if the run set cannot be listed.
func clearTargetRuns(c *gin.Context, runs DagRunRepository, currentRunID string, includePast, includeFuture bool) []string {
	if (!includePast && !includeFuture) || runs == nil {
		return []string{currentRunID}
	}
	dagID := c.Param("dag_id")
	cur, err := runs.GetDagRun(c.Request.Context(), tenantOf(c), dagID, currentRunID)
	if err != nil {
		return []string{currentRunID}
	}
	all, err := listDagRunsPage(c, runs, dagID, maxRunScan, 0)
	if err != nil {
		return []string{currentRunID}
	}
	out := []string{currentRunID}
	for _, r := range all {
		if r.RunID == currentRunID {
			continue
		}
		if (includePast && r.LogicalDate.Before(cur.LogicalDate)) || (includeFuture && r.LogicalDate.After(cur.LogicalDate)) {
			out = append(out, r.RunID)
		}
	}
	return out
}

// expandTaskIDs grows a seed set of task ids along the DAG topology:
// includeDownstream adds transitive descendants (tasks depending on a seed),
// includeUpstream adds transitive ancestors (tasks a seed depends on). Seeds are
// always included; with neither flag the seeds are returned unchanged.
func expandTaskIDs(tasks []domain.TaskSpec, seeds []string, includeUpstream, includeDownstream bool) []string {
	deps := make(map[string][]string, len(tasks))     // task -> its upstreams
	children := make(map[string][]string, len(tasks)) // task -> its downstreams
	for _, t := range tasks {
		deps[t.TaskID] = t.DependsOn
		for _, d := range t.DependsOn {
			children[d] = append(children[d], t.TaskID)
		}
	}
	result := map[string]bool{}
	var walk func(id string, adj map[string][]string)
	walk = func(id string, adj map[string][]string) {
		for _, next := range adj[id] {
			if !result[next] {
				result[next] = true
				walk(next, adj)
			}
		}
	}
	for _, s := range seeds {
		result[s] = true
		if includeUpstream {
			walk(s, deps)
		}
		if includeDownstream {
			walk(s, children)
		}
	}
	out := make([]string, 0, len(result))
	for id := range result {
		out = append(out, id)
	}
	return out
}

// affectedTaskInstances returns the task instances a clear would touch: the named
// tasks (or all when none named), restricted to failed ones when onlyFailed.
func affectedTaskInstances(c *gin.Context, repo TaskInstanceRepository, runs DagRunRepository, versions DagVersionLister,
	runID string, taskIDs []string, onlyFailed bool) []taskInstanceDTO {
	tis, _, err := repo.ListTaskInstances(c.Request.Context(), tenantOf(c), c.Param("dag_id"), runID, 1000, 0)
	if err != nil {
		return []taskInstanceDTO{}
	}
	want := make(map[string]bool, len(taskIDs))
	for _, id := range taskIDs {
		want[id] = true
	}
	logical, version := resolveRunContextFor(c, runs, versions, runID)
	out := make([]taskInstanceDTO, 0, len(tis))
	for _, ti := range tis {
		if len(want) > 0 && !want[ti.TaskID] {
			continue
		}
		if onlyFailed && ti.State != domain.TaskStateFailed && ti.State != domain.TaskStateUpstreamFailed {
			continue
		}
		dto := toTaskInstanceDTO(ti)
		dto.LogicalDate, dto.RunAfter, dto.DagVersion = logical, logical, version
		out = append(out, dto)
	}
	return out
}

// markStateRequest is the body of the mark-success/failed PATCH.
type markStateRequest struct {
	NewState string `json:"new_state"`
}

// patchTaskInstanceHandler implements the UI's mark-success/failed PATCH on
// .../taskInstances/{task_id}[/{map_index}][/dry_run]. The catch-all action is
// the trailing path after the task_id; a "dry_run" segment previews without
// changing state. It returns the (would-be) updated task instance.
func patchTaskInstanceHandler(repo TaskInstanceRepository, runs DagRunRepository, versions DagVersionLister, audit AuditWriter) gin.HandlerFunc {
	return func(c *gin.Context) {
		// #271 diagnostic: timing the mark-state mutation so we can prove the
		// backend write is instant when the user reports "marking a task as
		// failed takes forever". The wall-clock here is the server's share
		// of the latency; anything slower than ~50ms here is a real server
		// regression, while anything beyond that on the UX side belongs to
		// the SPA's cache invalidation (see #271 update).
		t0 := time.Now()
		action := strings.Trim(c.Param("action"), "/")
		dryRun := action == "dry_run" || strings.HasSuffix(action, "/dry_run")
		var body markStateRequest
		if err := c.ShouldBindJSON(&body); err != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", err.Error())
			return
		}
		if !validMarkState(body.NewState) {
			AbortProblem(c, http.StatusBadRequest, "bad request", "new_state must be success, failed, or skipped")
			return
		}
		dagID, runID, taskID := c.Param("dag_id"), c.Param("dag_run_id"), c.Param("task_id")
		if !dryRun {
			if err := repo.SetTaskInstanceState(c.Request.Context(), tenantOf(c), dagID, runID, taskID, body.NewState); err != nil {
				handleRepoError(c, err)
				return
			}
		}
		writeMs := time.Since(t0).Milliseconds()
		dto, ok := findTaskInstanceDTO(c, repo, runs, versions, runID, taskID)
		if !ok {
			AbortProblem(c, http.StatusNotFound, "not found", "task instance not found")
			return
		}
		if dryRun {
			s := body.NewState
			dto.State = &s // preview the state the confirm would apply
		} else {
			recordTaskAudit(c, audit, "taskinstance.mark."+body.NewState, runID, taskID, dto.TryNumber)
		}
		slog.Info("PATCH /taskInstances mark-state",
			"tenant", tenantOf(c), "dag", dagID, "run", runID, "task", taskID,
			"new_state", body.NewState, "dry_run", dryRun,
			"write_ms", writeMs, "total_ms", time.Since(t0).Milliseconds())
		c.JSON(http.StatusOK, dto)
	}
}

// validMarkState reports whether s is a state the UI may set a task to directly.
func validMarkState(s string) bool {
	return s == string(domain.TaskStateSuccess) || s == string(domain.TaskStateFailed) || s == string(domain.TaskStateSkipped)
}

// findTaskInstanceDTO loads one task instance (map_index -1) for the run, enriched.
func findTaskInstanceDTO(c *gin.Context, repo TaskInstanceRepository, runs DagRunRepository, versions DagVersionLister, runID, taskID string) (taskInstanceDTO, bool) {
	tis, _, err := repo.ListTaskInstances(c.Request.Context(), tenantOf(c), c.Param("dag_id"), runID, 1000, 0)
	if err != nil {
		return taskInstanceDTO{}, false
	}
	for _, ti := range tis {
		if ti.TaskID == taskID {
			dto := toTaskInstanceDTO(ti)
			enrichTaskInstance(c, &dto, runs, versions)
			return dto, true
		}
	}
	return taskInstanceDTO{}, false
}

// resolveRunContextFor is resolveRunContext for an explicit run id (clear posts
// the run in its body rather than the path).
func resolveRunContextFor(c *gin.Context, runs DagRunRepository, versions DagVersionLister, runID string) (*time.Time, *dagVersionDTO) {
	dagID := c.Param("dag_id")
	var logical *time.Time
	var pinned string
	if runs != nil && runID != "" {
		if run, err := runs.GetDagRun(c.Request.Context(), tenantOf(c), dagID, runID); err == nil {
			logical = &run.LogicalDate
			pinned = run.Version
		}
	}
	var version *dagVersionDTO
	if versions != nil {
		if vs, err := versions.ListDagVersions(c.Request.Context(), tenantOf(c), dagID); err == nil && len(vs) > 0 {
			version = &dagVersionDTO{
				ID: vs[0].ID, VersionNumber: vs[0].VersionNumber, DagID: dagID,
				BundleName: "leoflow", CreatedAt: vs[0].CreatedAt, DagDisplayName: dagID,
			}
			// The RUN's pinned label, not vs[0]'s. The single-task clear dialog
			// gates its version control on
			//   he !== ge && ge !== null && ge !== ''
			// where he is the DAG's current label and ge is this one. Filling it
			// from vs[0] makes the two equal and the control stays hidden — the
			// same invisible result as leaving it null, reached a different way.
			version.BundleVersion = strPtrOrNil(pinned)
		}
	}
	return logical, version
}

// taskInstanceActionHandler dispatches the catch-all under
// /taskInstances/{task_id}/* : "logs/{try}" streams the attempt's logs, while a
// bare "{map_index}" returns the single task instance (TaskInstanceResponse).
func taskInstanceActionHandler(tasks TaskInstanceRepository, logs LogReader, xcoms XComReader, runs DagRunRepository, versions DagVersionLister, specs DagSpecReader) gin.HandlerFunc {
	return func(c *gin.Context) {
		action := strings.Trim(c.Param("action"), "/")
		if rest, ok := strings.CutPrefix(action, "logs/"); ok {
			try, err := strconv.Atoi(rest)
			if err != nil {
				AbortProblem(c, http.StatusBadRequest, "bad request", "try_number must be an integer")
				return
			}
			serveLogs(c, logs, tasks, try)
			return
		}
		if xcoms != nil {
			if action == "xcomEntries" {
				serveXComEntries(c, xcoms)
				return
			}
			if key, ok := strings.CutPrefix(action, "xcomEntries/"); ok {
				serveXComValue(c, xcoms, key)
				return
			}
		}
		if action == "links" {
			// The task Details view reads g.extra_links and calls Object.keys on it,
			// so the response must carry an extra_links object (a bare {} or a 400
			// crashes the view). We serve the operator's UI deep-link buttons from the
			// "_extra_links" XCom the agent stored (#375); empty when the task exposed
			// none or the store has no entry.
			serveExtraLinks(c, xcoms)
			return
		}
		if action == "tries" || strings.HasPrefix(action, "tries/") {
			serveTaskTries(c, tasks, runs, versions, strings.TrimPrefix(action, "tries"))
			return
		}
		// The SPA also addresses sub-resources under an explicit map_index, e.g.
		// "{map_index}/tries" — the attempts list for one instance, requested right
		// after a Clear (captured live from the 3.2 UI). The leading integer is the
		// map_index (-1 for the single unmapped instance); route the sub-resource.
		// TODO(#103): this handles only "{map_index}/tries" and serveTaskTries
		// ignores the map_index. Correct while tasks are unmapped (map_index always
		// -1); generalize the {map_index}/{subresource} routing + filter by
		// map_index before dynamic task mapping lands.
		if slash := strings.IndexByte(action, '/'); slash > 0 {
			if _, err := strconv.Atoi(action[:slash]); err == nil {
				if sub := action[slash+1:]; sub == "tries" || strings.HasPrefix(sub, "tries/") {
					serveTaskTries(c, tasks, runs, versions, strings.TrimPrefix(sub, "tries"))
					return
				}
			}
		}
		if action == "" {
			// A bare path is the single (unmapped) instance.
			serveSingleTaskInstance(c, tasks, runs, versions, specs, -1)
			return
		}
		mapIndex, err := strconv.Atoi(action)
		if err != nil {
			// Not a map_index and not a known sub-resource. Say so honestly: the old
			// "map_index must be an integer" 400 was misleading for an action that
			// was never a map_index (e.g. a sub-resource the UI added).
			AbortProblem(c, http.StatusNotFound, "not found",
				fmt.Sprintf("unsupported task instance action %q", action))
			return
		}
		serveSingleTaskInstance(c, tasks, runs, versions, specs, mapIndex)
	}
}

// serveSingleTaskInstance returns one task instance (TaskInstanceResponse) by
// map_index, enriched with run-derived fields and the spec's rendered_fields.
func serveSingleTaskInstance(c *gin.Context, tasks TaskInstanceRepository, runs DagRunRepository, versions DagVersionLister, specs DagSpecReader, mapIndex int) {
	tis, _, err := tasks.ListTaskInstances(c.Request.Context(), tenantOf(c),
		c.Param("dag_id"), c.Param("dag_run_id"), 1000, 0)
	if err != nil {
		handleRepoError(c, err)
		return
	}
	for _, ti := range tis {
		if ti.TaskID != c.Param("task_id") || ti.MapIndex != mapIndex {
			continue
		}
		dto := toTaskInstanceDTO(ti)
		enrichTaskInstance(c, &dto, runs, versions)
		// Populate rendered_fields from the task spec so the Rendered Templates
		// tab shows the operator's fields rather than empty.
		if specs != nil {
			if spec, serr := specs.GetCurrentSpec(c.Request.Context(), tenantOf(c), c.Param("dag_id")); serr == nil {
				dto.RenderedFields = renderedFieldsFor(spec, ti.TaskID)
			}
		}
		c.JSON(http.StatusOK, dto)
		return
	}
	AbortProblem(c, http.StatusNotFound, "not found", "task instance not found")
}

// enrichTaskInstance fills the run-derived fields (logical_date, run_after) and
// the DAG version object, so the task panel shows them rather than null.
func enrichTaskInstance(c *gin.Context, dto *taskInstanceDTO, runs DagRunRepository, versions DagVersionLister) {
	logical, version := resolveRunContext(c, runs, versions)
	dto.LogicalDate, dto.RunAfter, dto.DagVersion = logical, logical, version
}

// resolveRunContext looks up a run's logical date and the DAG's latest version
// once, for sharing across the task instances of a single run. Either result is
// nil when the source repository is absent or the lookup fails.
func resolveRunContext(c *gin.Context, runs DagRunRepository, versions DagVersionLister) (*time.Time, *dagVersionDTO) {
	dagID, runID := c.Param("dag_id"), c.Param("dag_run_id")
	var logical *time.Time
	var pinned string
	if runs != nil {
		if run, err := runs.GetDagRun(c.Request.Context(), tenantOf(c), dagID, runID); err == nil {
			logical = &run.LogicalDate
			pinned = run.Version
		}
	}
	var version *dagVersionDTO
	if versions != nil {
		if vs, err := versions.ListDagVersions(c.Request.Context(), tenantOf(c), dagID); err == nil && len(vs) > 0 {
			version = &dagVersionDTO{
				ID: vs[0].ID, VersionNumber: vs[0].VersionNumber, DagID: dagID,
				BundleName: "leoflow", CreatedAt: vs[0].CreatedAt, DagDisplayName: dagID,
			}
			// The RUN's pinned label, not vs[0]'s. The single-task clear dialog
			// gates its version control on
			//   he !== ge && ge !== null && ge !== ''
			// where he is the DAG's current label and ge is this one. Filling it
			// from vs[0] makes the two equal and the control stays hidden — the
			// same invisible result as leaving it null, reached a different way.
			version.BundleVersion = strPtrOrNil(pinned)
		}
	}
	return logical, version
}

// serveTaskTries answers the task try-history endpoints the UI's task Details
// tab reads: "tries" returns the collection of attempts for the task, and
// "tries/{n}" (suffix "/{n}") returns a single attempt by try_number.
//
// Returns one entry per attempt: the current task_instances row plus every
// archived task_instance_history row for the same (run, task), oldest first.
// The UI renders one navigable tab per entry; clicking a tab loads the
// matching <try_number>.log via the separate logs endpoint (Lima bug #241).
func serveTaskTries(c *gin.Context, tasks TaskInstanceRepository, runs DagRunRepository, versions DagVersionLister, suffix string) {
	tis, err := tasks.ListTaskInstanceAttempts(c.Request.Context(), tenantOf(c),
		c.Param("dag_id"), c.Param("dag_run_id"), c.Param("task_id"))
	if err != nil {
		handleRepoError(c, err)
		return
	}
	matches := make([]taskInstanceDTO, 0, len(tis))
	for _, ti := range tis {
		dto := toTaskInstanceDTO(ti)
		enrichTaskInstance(c, &dto, runs, versions)
		matches = append(matches, dto)
	}
	if single := strings.TrimPrefix(suffix, "/"); single != "" {
		if len(matches) == 0 {
			AbortProblem(c, http.StatusNotFound, "not found", "task instance not found")
			return
		}
		// Match by try_number when parseable; fall back to the latest attempt so
		// a malformed suffix doesn't 404 (the UI sometimes lazily polls).
		if n, perr := strconv.Atoi(single); perr == nil {
			for _, m := range matches {
				if m.TryNumber == n {
					c.JSON(http.StatusOK, m)
					return
				}
			}
		}
		c.JSON(http.StatusOK, matches[len(matches)-1])
		return
	}
	c.JSON(http.StatusOK, taskInstanceCollectionDTO{TaskInstances: matches, TotalEntries: len(matches)})
}

// stubHandler reports a feature that arrives in a later phase.
func stubHandler(feature string) gin.HandlerFunc {
	return func(c *gin.Context) {
		AbortProblem(c, http.StatusNotImplemented, "not implemented", feature+" arrives in Phase 4")
	}
}

// registerResources mounts the /api/v2 resource routes whose repositories are
// configured. Routes for nil repositories are omitted.
func registerResources(r gin.IRouter, deps Dependencies) {
	if deps.Dags != nil {
		g := r.Group("/api/v2/dags")
		g.GET("", RequirePermission("read", "dag"), listDagsHandler(deps.Dags))
		g.GET("/:dag_id", RequirePermission("read", "dag"), getDagHandler(deps.Dags))
		g.GET("/:dag_id/details", RequirePermission("read", "dag"), dagDetailsHandler(deps.Dags, deps.DagVersions, deps.Specs))
		g.PATCH("/:dag_id", RequirePermission("write", "dag"), patchDagHandler(deps.Dags))
		g.DELETE("/:dag_id", RequirePermission("write", "dag"), deleteDagHandler(deps.Dags))
	}
	if deps.DagRuns != nil {
		g := r.Group("/api/v2/dags/:dag_id/dagRuns")
		g.GET("", RequirePermission("read", "dag_run"), listDagRunsHandler(deps.DagRuns))
		g.POST("", RequirePermission("execute", "dag"), createDagRunHandler(deps.DagRuns, deps.Specs, deps.Audit))
		g.GET("/:dag_run_id", RequirePermission("read", "dag_run"), getDagRunHandler(deps.DagRuns))
		g.PATCH("/:dag_run_id", RequirePermission("write", "dag_run"), patchDagRunHandler(deps.DagRuns, deps.Audit))
		g.DELETE("/:dag_run_id", RequirePermission("write", "dag_run"), deleteDagRunHandler(deps.DagRuns))
	}
	if deps.Tasks != nil {
		r.GET("/api/v2/dags/:dag_id/dagRuns/:dag_run_id/taskInstances",
			RequirePermission("read", "task_instance"), listTaskInstancesHandler(deps.Tasks, deps.DagRuns, deps.DagVersions))
		// The "logs/:try_number" and ":map_index" routes share the :task_id
		// parent; gin cannot mix a static and a wildcard child there, so one
		// catch-all dispatches both (single task instance vs its logs). The
		// bare-path twin is registered next to it (#291): the Airflow SPA
		// fetches `/taskInstances/:task_id` (no trailing slash) and gin's
		// auto-redirect would otherwise emit a 301 with no Cache-Control
		// header, leaving a cacheable hop the browser can short-circuit. With
		// RedirectTrailingSlash disabled in server.go and both routes
		// registered, every hit reaches the handler through the no-store
		// middleware.
		actionH := taskInstanceActionHandler(deps.Tasks, deps.Logs, deps.Xcoms, deps.DagRuns, deps.DagVersions, deps.Specs)
		r.GET("/api/v2/dags/:dag_id/dagRuns/:dag_run_id/taskInstances/:task_id",
			RequirePermission("read", "task_instance"), actionH)
		r.GET("/api/v2/dags/:dag_id/dagRuns/:dag_run_id/taskInstances/:task_id/*action",
			RequirePermission("read", "task_instance"), actionH)
		// Mark-success/failed: PATCH the task instance. The UI hits both the bare
		// path and one carrying optional /{map_index} and /dry_run segments.
		patchTI := patchTaskInstanceHandler(deps.Tasks, deps.DagRuns, deps.DagVersions, deps.Audit)
		r.PATCH("/api/v2/dags/:dag_id/dagRuns/:dag_run_id/taskInstances/:task_id", RequirePermission("write", "task_instance"), patchTI)
		r.PATCH("/api/v2/dags/:dag_id/dagRuns/:dag_run_id/taskInstances/:task_id/*action", RequirePermission("write", "task_instance"), patchTI)
		r.POST("/api/v2/dags/:dag_id/clearTaskInstances",
			RequirePermission("write", "task_instance"), clearTaskInstancesHandler(deps.Tasks, deps.DagRuns, deps.DagVersions, deps.Specs, deps.Audit))
	}
	if deps.Versions != nil {
		r.POST("/api/v2/dags/:dag_id/versions", RequirePermission("write", "dag"), registerVersionHandler(deps.Versions))
	}
	if deps.Xcoms != nil {
		r.GET("/api/v2/xcoms/:dag_id/:dag_run_id/:task_id/:key", RequirePermission("read", "xcom"), xcomHandler(deps.Xcoms))
	} else {
		r.GET("/api/v2/xcoms/:dag_id/:dag_run_id/:task_id/:key", RequirePermission("read", "xcom"), stubHandler("XCom retrieval"))
	}
}
