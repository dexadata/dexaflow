package api

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/domain"
)

// classRefDTO is the Airflow 3.2.1 ClassReference.
type classRefDTO struct {
	ModulePath *string `json:"module_path"`
	ClassName  string  `json:"class_name"`
}

// timeDeltaDTO is Airflow's serialized datetime.timedelta. The Tasks Details
// view calls Object.keys on retry_delay, so it must be an object, never null.
type timeDeltaDTO struct {
	Type         string `json:"__type"` //nolint:tagliatelle // Airflow API contract field name
	Days         int    `json:"days"`
	Seconds      int    `json:"seconds"`
	Microseconds int    `json:"microseconds"`
}

// taskResponseDTO is the Airflow 3.2.1 TaskResponse. Dexaflow models a small
// subset of operator attributes, so the rest are sensible defaults / null — the
// Tasks tab renders the task_id, operator, trigger rule, retries, and downstream
// links from real data.
type taskResponseDTO struct {
	TaskID                  string          `json:"task_id"`
	TaskDisplayName         string          `json:"task_display_name"`
	Owner                   *string         `json:"owner"`
	OperatorName            string          `json:"operator_name"`
	ClassRef                classRefDTO     `json:"class_ref"`
	TriggerRule             string          `json:"trigger_rule"`
	DependsOnPast           bool            `json:"depends_on_past"`
	WaitForDownstream       bool            `json:"wait_for_downstream"`
	Retries                 int             `json:"retries"`
	RetryDelay              timeDeltaDTO    `json:"retry_delay"`
	RetryExponentialBackoff bool            `json:"retry_exponential_backoff"`
	DownstreamTaskIDs       []string        `json:"downstream_task_ids"`
	IsMapped                bool            `json:"is_mapped"`
	StartDate               *string         `json:"start_date"`
	EndDate                 *string         `json:"end_date"`
	ExecutionTimeout        *string         `json:"execution_timeout"`
	DocMd                   *string         `json:"doc_md"`
	Pool                    string          `json:"pool"`
	PoolSlots               int             `json:"pool_slots"`
	Queue                   *string         `json:"queue"`
	PriorityWeight          int             `json:"priority_weight"`
	WeightRule              string          `json:"weight_rule"`
	UIColor                 string          `json:"ui_color"`
	UIFgColor               string          `json:"ui_fgcolor"`
	TemplateFields          []string        `json:"template_fields"`
	ExtraLinks              []string        `json:"extra_links"`
	Params                  json.RawMessage `json:"params"`
}

type taskCollectionDTO struct {
	Tasks        []taskResponseDTO `json:"tasks"`
	TotalEntries int               `json:"total_entries"`
}

// operatorName maps a Dexaflow task type to an Airflow-style operator name the UI
// displays.
// classRef splits a task into the (module_path, class_name) pair Airflow exposes
// on its tasks endpoint.
//
// module_path was declared here and never assigned, so every task carried
// `"module_path": null`. Airflow itself populates it, and OpenMetadata's REST
// connector types the pair as dict[str, str], so a null made its validator raise
// and discard the DAG, for every DAG, with no pipeline ingested and the test
// connection still reporting success (it probes reachability, not shape).
//
// The module paths are the ones this project already tells authors to import
// from, which are Airflow 3's: the core `airflow.operators.*` modules were
// removed and the compiler refuses them by name.
func classRef(t domain.TaskSpec) classRefDTO {
	// A captured provider operator already carries its own dotted class, so the
	// module is whatever precedes the final dot rather than a lookup.
	if t.Type == domain.TaskTypeAirflowOperator && t.OperatorClass != "" {
		if i := strings.LastIndex(t.OperatorClass, "."); i > 0 {
			mod := t.OperatorClass[:i]
			return classRefDTO{ModulePath: &mod, ClassName: t.OperatorClass[i+1:]}
		}
		return classRefDTO{ModulePath: strPtr("leoflow.tasks"), ClassName: t.OperatorClass}
	}
	switch t.Type {
	case domain.TaskTypePython:
		return classRefDTO{ModulePath: strPtr("airflow.providers.standard.operators.python"), ClassName: "PythonOperator"}
	case domain.TaskTypeBash:
		return classRefDTO{ModulePath: strPtr("airflow.providers.standard.operators.bash"), ClassName: "BashOperator"}
	default:
		// A leoflow-owned module rather than an Airflow one. The pair means "this
		// class, in this module", so naming a real Airflow module that does not
		// contain the class produces a reference that resolves to nothing: a
		// consumer treating it as an import gets an error, and a human reading the
		// catalog is told something false. leoflow.tasks is a non-empty string, so
		// OpenMetadata is satisfied, it cannot be mistaken for an importable path,
		// and it says the true thing: a leoflow task type with no Airflow class
		// behind it.
		return classRefDTO{ModulePath: strPtr("leoflow.tasks"), ClassName: operatorName(t.Type)}
	}
}

func strPtr(s string) *string { return &s }

func operatorName(t domain.TaskType) string {
	switch t {
	case domain.TaskTypePython:
		return "PythonOperator"
	case domain.TaskTypeBash:
		return "BashOperator"
	default:
		return string(t)
	}
}

// downstreamIDs returns the ids of tasks that declare taskID as an upstream.
func downstreamIDs(tasks []domain.TaskSpec, taskID string) []string {
	out := []string{}
	for _, t := range tasks {
		for _, dep := range t.DependsOn {
			if dep == taskID {
				out = append(out, t.TaskID)
			}
		}
	}
	sort.Strings(out)
	return out
}

func toTaskResponse(spec domain.DAGSpec, t domain.TaskSpec) taskResponseDTO {
	tr := strSafe(string(t.TriggerRule), "all_success")
	retries := 0
	if t.Retries != nil {
		retries = *t.Retries
	}
	return taskResponseDTO{
		TaskID:          t.TaskID,
		TaskDisplayName: t.TaskID,
		Owner:           strPtrOrNil(spec.Owner),
		OperatorName:    operatorName(t.Type),
		ClassRef:        classRef(t),
		TriggerRule:     tr,
		Retries:         retries,
		// Airflow's default retry_delay is 300s; emit it as a TimeDelta object
		// (the Tasks Details view calls Object.keys on it, so it must not be null).
		RetryDelay:        timeDeltaDTO{Type: "TimeDelta", Seconds: 300},
		DownstreamTaskIDs: downstreamIDs(spec.Tasks, t.TaskID),
		Pool:              cmp.Or(t.Pool, domain.DefaultPoolName),
		PoolSlots:         t.EffectivePoolSlots(),
		PriorityWeight:    1,
		WeightRule:        "downstream",
		UIColor:           "#fff",
		UIFgColor:         "#000",
		TemplateFields:    []string{},
		ExtraLinks:        []string{},
		Params:            json.RawMessage("{}"),
	}
}

// renderedFieldsFor builds the rendered_fields object the Task Details / Rendered
// Templates tab shows: the task's templatable operator fields. Dexaflow does not
// render Jinja (#25), so these are the spec values — for a TaskFlow task that
// means its entrypoint, env, and the upstream bindings it consumes (xcom_input).
// Returns {} when the task is unknown.
func renderedFieldsFor(spec domain.DAGSpec, taskID string) json.RawMessage {
	for _, t := range spec.Tasks {
		if t.TaskID != taskID {
			continue
		}
		// The task's resolved parameters, shown as the rendered fields. Only
		// non-empty values are included so the panel stays clean.
		fields := map[string]any{
			"task_id":  t.TaskID,
			"operator": operatorName(t.Type),
		}
		putIf := func(k string, v any, ok bool) {
			if ok {
				fields[k] = v
			}
		}
		putIf("entrypoint", t.Entrypoint, t.Entrypoint != "")
		putIf("env", t.Env, len(t.Env) > 0)
		putIf("xcom_input", t.XComInput, len(t.XComInput) > 0)
		putIf("depends_on", t.DependsOn, len(t.DependsOn) > 0)
		putIf("trigger_rule", string(t.TriggerRule), t.TriggerRule != "")
		putIf("retries", t.Retries, t.Retries != nil)
		putIf("retry_delay_seconds", t.RetryDelaySeconds, t.RetryDelaySeconds != nil)
		putIf("execution_timeout_seconds", t.ExecutionTimeoutSeconds, t.ExecutionTimeoutSeconds != nil)
		putIf("execution_mode", string(t.ExecutionMode), t.ExecutionMode != "")
		putIf("resources", t.Resources, t.Resources != nil)
		putIf("execution", t.Execution, t.Execution != nil)
		if b, err := json.Marshal(fields); err == nil {
			return b
		}
	}
	return json.RawMessage("{}")
}

// strSafe returns s, or the fallback when s is empty.
func strSafe(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// tasksHandler implements GET /api/v2/dags/{dag_id}/tasks.
func tasksHandler(specs DagSpecReader) gin.HandlerFunc {
	return func(c *gin.Context) {
		spec, err := specs.GetCurrentSpec(c.Request.Context(), tenantOf(c), c.Param("dag_id"))
		if err != nil {
			handleRepoError(c, err)
			return
		}
		sorted := topoSortTasks(spec.Tasks)
		out := taskCollectionDTO{Tasks: make([]taskResponseDTO, 0, len(sorted)), TotalEntries: len(sorted)}
		for _, t := range sorted {
			out.Tasks = append(out.Tasks, toTaskResponse(spec, t))
		}
		c.JSON(http.StatusOK, out)
	}
}

// taskDetailHandler implements GET /api/v2/dags/{dag_id}/tasks/{task_id}.
func taskDetailHandler(specs DagSpecReader) gin.HandlerFunc {
	return func(c *gin.Context) {
		spec, err := specs.GetCurrentSpec(c.Request.Context(), tenantOf(c), c.Param("dag_id"))
		if err != nil {
			handleRepoError(c, err)
			return
		}
		taskID := c.Param("task_id")
		for _, t := range spec.Tasks {
			if t.TaskID == taskID {
				c.JSON(http.StatusOK, toTaskResponse(spec, t))
				return
			}
		}
		AbortProblem(c, http.StatusNotFound, "not found", "task not found")
	}
}

// DagSourceReader reads one version of a DAG and the spec registered with it:
// the version numbered versionNumber (the version_number of the dagVersions
// listing), or the newest version when versionNumber is 0. It returns
// domain.ErrNotFound when the caller's tenant has no such version. The spec
// may be shared with a cache: callers only read it.
type DagSourceReader interface {
	GetDagVersionSpec(ctx context.Context, tenant, dagID string, versionNumber int) (domain.DagVersion, domain.DAGSpec, error)
}

// dagSourceHandler implements GET /api/v2/dagSources/{dag_id}, the Code view,
// with Airflow 3.2's semantics: the version_number query parameter selects the
// DAG version whose source is served, and without it (or with 0) the newest
// version is served (not necessarily the current one, after a rollback). An unknown
// version is a 404. It returns the original dag.py Python captured at compile
// time; versions compiled before source capture fall back to the compiled
// spec JSON so the tab still renders.
func dagSourceHandler(sources DagSourceReader) gin.HandlerFunc {
	return func(c *gin.Context) {
		dagID := c.Param("dag_id")
		number, ok := sourceVersionNumber(c)
		if !ok {
			return
		}
		version, spec, err := sources.GetDagVersionSpec(c.Request.Context(), tenantOf(c), dagID, number)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		content := spec.Source
		if content == "" {
			marshaled, merr := json.MarshalIndent(spec, "", "  ")
			if merr != nil {
				AbortProblem(c, http.StatusInternalServerError, "internal error", "encoding spec")
				return
			}
			content = string(marshaled)
		}
		c.JSON(http.StatusOK, gin.H{
			"content":          content,
			"dag_id":           dagID,
			"version_number":   version.VersionNumber,
			"dag_display_name": dagID,
		})
	}
}

// sourceVersionNumber reads dagSources' optional version_number: 0, the
// newest version, when it is absent or 0 (Airflow tests `if version_number:`).
// A value that is not an integer is a 400. A negative one names no version,
// so the reader answers it with a 404, as in Airflow. It reports false after
// aborting the request.
func sourceVersionNumber(c *gin.Context) (int, bool) {
	raw, set := c.GetQuery("version_number")
	if !set {
		return 0, true
	}
	number, err := strconv.Atoi(raw)
	if err != nil {
		AbortProblem(c, http.StatusBadRequest, "bad request", "version_number must be an integer")
		return 0, false
	}
	return number, true
}

// dagVersionSpecHandler implements GET
// /api/v2/dags/{dag_id}/dagVersions/{version_number}/spec: the dag.json
// registered with that version, current or not, as dagSpecHandler serves the
// current one. `dexaflow promote` reads it to register the same version on
// another control plane (#1708). A number that is not an integer is a 400;
// one that names no version of the caller's tenant, 0 and negatives
// included, is a 404 (0 is not the newest version here, unlike dagSources).
func dagVersionSpecHandler(sources DagSourceReader) gin.HandlerFunc {
	return func(c *gin.Context) {
		number, err := strconv.Atoi(c.Param("version_number"))
		if err != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", "version_number must be an integer")
			return
		}
		if number <= 0 {
			AbortProblem(c, http.StatusNotFound, "not found", "version not found")
			return
		}
		_, spec, err := sources.GetDagVersionSpec(c.Request.Context(), tenantOf(c), c.Param("dag_id"), number)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		c.JSON(http.StatusOK, spec)
	}
}

// registerUITasks mounts the task and spec endpoints when a spec reader is
// set, and the source endpoint when a source reader is.
func registerUITasks(r gin.IRouter, specs DagSpecReader, sources DagSourceReader) {
	if sources != nil {
		r.GET("/api/v2/dagSources/:dag_id", RequirePermission("read", "dag"), dagSourceHandler(sources))
		r.GET("/api/v2/dags/:dag_id/dagVersions/:version_number/spec", RequirePermission("read", "dag"), dagVersionSpecHandler(sources))
	}
	if specs == nil {
		return
	}
	r.GET("/api/v2/dags/:dag_id/tasks", RequirePermission("read", "task"), tasksHandler(specs))
	r.GET("/api/v2/dags/:dag_id/tasks/:task_id", RequirePermission("read", "task"), taskDetailHandler(specs))
	r.GET("/api/v2/dags/:dag_id/spec", RequirePermission("read", "dag"), dagSpecHandler(specs))
}

// dagSpecHandler implements GET /api/v2/dags/{dag_id}/spec: the compiled dag.json
// artifact (the immutable spec the scheduler runs), for tools and agents that
// need the structured graph rather than the dag.py source that dagSourceHandler
// serves. The DAGSpec marshals with its own json tags.
func dagSpecHandler(specs DagSpecReader) gin.HandlerFunc {
	return func(c *gin.Context) {
		spec, err := specs.GetCurrentSpec(c.Request.Context(), tenantOf(c), c.Param("dag_id"))
		if err != nil {
			handleRepoError(c, err)
			return
		}
		c.JSON(http.StatusOK, spec)
	}
}
