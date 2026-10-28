package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

// DagVersionRepository registers compiled DAG versions and chooses which one
// is current.
type DagVersionRepository interface {
	// RegisterDagVersionAs upserts the DAG and inserts a version keyed by
	// reg.SpecHash on behalf of reg.ActorUserID, reporting whether a new
	// version was created (false if the hash already existed: the push is
	// idempotent) and the DAG's is_paused afterwards. reg.IsPaused, when set,
	// is the DAG's state after the register.
	RegisterDagVersionAs(ctx context.Context, tenant string, reg domain.DagRegistration) (domain.DagRegistered, error)
	// SetCurrentDagVersionAs makes the version req.Selector names current,
	// after req.Admit accepts its spec, on behalf of req.ActorUserID. Runs
	// that already exist keep their version; is_paused is never changed.
	SetCurrentDagVersionAs(ctx context.Context, tenant string, req domain.DagSetCurrent) (domain.CurrentVersionChange, error)
}

type versionResponse struct {
	DagID      string `json:"dag_id"`
	Version    string `json:"version"`
	SpecHash   string `json:"spec_hash"`
	Created    bool   `json:"created"`
	DagCreated bool   `json:"dag_created"`
	IsPaused   bool   `json:"is_paused"`
	// Execution says where each task runs and what the version's warm pool
	// does (#1707), from the decision dispatch places with.
	Execution *executionReport `json:"execution,omitempty"`
	// Warnings is what the register let through with a warning (#1703),
	// absent when there is none, so the answer of a register that warns
	// about nothing is the one it always was.
	Warnings []string `json:"warnings,omitempty"`
}

// dryRunResponse is the answer to a register dry run: what the register
// would do, and the warnings it would raise. Its Warnings, always present,
// takes the place of the register's.
type dryRunResponse struct {
	versionResponse
	DryRun   bool     `json:"dry_run"`
	Warnings []string `json:"warnings"`
	// TaskPods is what the version's task pods would run as (#1889).
	TaskPods *taskPodsReport `json:"task_pods"`
}

// dryRunHeader marks every answer of the register dry run route, refusals
// included, so a client can tell them from an answer of something else (an
// older server answers the route with 404).
const dryRunHeader = "Dexaflow-Dry-Run"

// UnitMisfitRecorder counts a task that does not fit the resource unit but is
// let through under executor.unit.enforce=warn, by stage. observability.Metrics
// satisfies it.
type UnitMisfitRecorder interface {
	RecordUnitMisfit(stage string)
}

// unitGate is what registration needs to apply executor.unit (ADR 0066 §3).
type unitGate struct {
	unit    *domain.ResourceUnit
	misfits UnitMisfitRecorder
	logger  *slog.Logger
}

// The stages a spec is admitted at: registering it, or making a registered
// version current again. They label the misfit metric and the warning.
const (
	stageRegister   = "register"
	stageSetCurrent = "set_current"
)

// check returns the refusal for spec, after logging and counting, at stage,
// every misfit tolerated under enforce: warn. A dry run lets nothing through,
// so it neither logs nor counts; the misfits are returned as warnings either
// way.
func (g unitGate) check(spec *domain.DAGSpec, stage string, dryRun bool) ([]string, error) {
	warned, refused := g.unit.CheckSpec(spec)
	if refused != nil {
		return nil, refused
	}
	warnings := make([]string, 0, len(warned))
	for _, w := range warned {
		warnings = append(warnings, w.Error())
	}
	if dryRun {
		return warnings, nil
	}
	for _, w := range warned {
		if g.logger != nil {
			g.logger.Warn("admitting a task that does not fit its size under executor.unit.enforce=warn",
				"stage", stage, "dag", logSafe(spec.DagID), "version", logSafe(spec.DagVersion), "error", logSafe(w.Error()))
		}
		if g.misfits != nil {
			g.misfits.RecordUnitMisfit(stage)
		}
	}
	return warnings, nil
}

// UnpinnedImageRecorder counts an image not pinned by digest that
// deploy.require_digest warned about (outcome "warned") or refused
// ("refused"), by stage. observability.Metrics satisfies it.
type UnpinnedImageRecorder interface {
	RecordUnpinnedImage(stage, outcome string)
}

// The outcomes of the unpinned image count.
const (
	unpinnedWarned  = "warned"
	unpinnedRefused = "refused"
)

// digestGate is what admission needs to apply deploy.require_digest (#1703).
type digestGate struct {
	mode     domain.DigestRequirement
	unpinned UnpinnedImageRecorder
	logger   *slog.Logger
}

// check applies the gate to spec's image at stage: the warning to give under
// warn, the refusal under enforce. Each is logged (a warning) and counted,
// except in a dry run, which records nothing.
func (g digestGate) check(spec *domain.DAGSpec, stage string, dryRun bool) (string, error) {
	check := g.mode.CheckImage
	if stage == stageSetCurrent {
		check = g.mode.CheckCurrentImage
	}
	warning, refused := check(spec.Image)
	if dryRun || (warning == "" && refused == nil) {
		return warning, refused
	}
	outcome := unpinnedRefused
	if refused == nil {
		outcome = unpinnedWarned
		if g.logger != nil {
			g.logger.Warn("admitting an image not pinned by digest under deploy.require_digest=warn",
				"setting", "deploy.require_digest", "stage", stage, "dag", logSafe(spec.DagID),
				"version", logSafe(spec.DagVersion), "image", logSafe(spec.Image))
		}
	}
	if g.unpinned != nil {
		g.unpinned.RecordUnpinnedImage(stage, outcome)
	}
	return warning, refused
}

// specAdmission is what a spec must pass, beyond the repository's own
// checks, to be registered or made current: its own validation, the resource
// unit, deploy.require_digest, source mode and the operator's execution
// allowlist.
type specAdmission struct {
	unit            unitGate
	digest          digestGate
	sourceModeImage string
	policy          *domain.ExecutionPolicy
}

// checkWarn returns the first refusal of spec, each one the client's to fix,
// or the warnings of an admitted spec. stage is stageRegister or
// stageSetCurrent. A dry run (#1705) gets the same refusals and warnings, and
// records nothing.
func (a specAdmission) checkWarn(spec *domain.DAGSpec, stage string, dryRun bool) ([]string, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	warnings, err := a.unit.check(spec, stage, dryRun)
	if err != nil {
		return nil, err
	}
	digestWarning, err := a.digest.check(spec, stage, dryRun)
	if err != nil {
		return nil, err
	}
	if digestWarning != "" {
		warnings = append(warnings, digestWarning)
	}
	if err := domain.CheckSourceMode(a.sourceModeImage, spec); err != nil {
		return nil, err
	}
	// The operator's execution allowlist (issue #629): refuse at push what
	// dispatch would refuse anyway, so the author learns it now.
	if err := a.policy.CheckSpec(spec); err != nil {
		return nil, err
	}
	return warnings, nil
}

// actorOf is the id of the authenticated user, or "" when there is none.
func actorOf(c *gin.Context) string {
	if u, ok := UserFromContext(c); ok {
		return u.ID
	}
	return ""
}

// pausedQuery reads the optional is_paused query parameter of a register:
// absent is nil (Airflow's rule applies), true or false sets the DAG's state.
// Any other value, an empty one included, is an error for the client to fix.
func pausedQuery(c *gin.Context) (*bool, error) {
	return boolQuery(c, "is_paused")
}

// boolQuery reads an optional boolean query parameter: nil when absent, else
// true or false. Any other value, an empty one included, is an error.
func boolQuery(c *gin.Context, name string) (*bool, error) {
	raw, ok := c.GetQuery(name)
	if !ok {
		return nil, nil //nolint:nilnil // absent is a valid "not set", not an error
	}
	switch raw {
	case "true":
		v := true
		return &v, nil
	case "false":
		v := false
		return &v, nil
	}
	return nil, errors.New(name + " must be true or false")
}

// pauseRefusal says why the caller may not set is_paused on a register, ""
// when it may or did not ask. Pausing is a run-state write: PATCH
// /dags/{dag_id} needs write:dag and, for a scoped token, dexaflow:run, so
// neither the deployer role nor a deploy-only token may pause or unpause here.
func pauseRefusal(c *gin.Context, paused *bool) string {
	u, ok := UserFromContext(c)
	if paused == nil || !ok {
		return ""
	}
	if !u.HasPermission("write", "dag") {
		return "missing permission write:dag (is_paused)"
	}
	if u.Scoped && !u.HasScope(auth.ScopeRun) {
		return "missing scope " + auth.ScopeRun + " (is_paused)"
	}
	return ""
}

// registerVersionHandler serves POST /api/v2/dags/{dag_id}/versions.
func registerVersionHandler(repo DagVersionRepository, admission specAdmission, execution executionReporter) gin.HandlerFunc {
	return func(c *gin.Context) {
		registerVersion(c, repo, admission, execution, nil)
	}
}

// dryRunVersionHandler serves POST /api/v2/dags/{dag_id}/versions/dry_run
// (#1705): every check and step the register runs, as the same caller in the
// same tenant, and the repository undoes it all. The answer is 200 with what
// would happen, or the refusal the register would give, each with
// dryRunHeader. A route of its own, not a parameter of the register, because
// an older server answers it with 404 instead of registering. A dry run
// writes no audit row, like the dry run of a clear: no state changed.
//
// The answer also says what the version's task pods would run as (pods,
// #1889), which the register does not report.
func dryRunVersionHandler(repo DagVersionRepository, admission specAdmission, execution executionReporter, pods taskPodReporter) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header(dryRunHeader, "true")
		registerVersion(c, repo, admission, execution, &pods)
	}
}

// registerVersion registers the spec of the request body, or for a dry run
// (dryRun, which reports the task pods) says what registering it would do,
// and where its tasks run.
func registerVersion(c *gin.Context, repo DagVersionRepository, admission specAdmission, execution executionReporter, dryRun *taskPodReporter) {
	paused, perr := pausedQuery(c)
	if perr != nil {
		AbortProblem(c, http.StatusBadRequest, "bad request", perr.Error())
		return
	}
	if msg := pauseRefusal(c, paused); msg != "" {
		AbortProblem(c, http.StatusForbidden, "forbidden", msg)
		return
	}
	promoted, prerr := promotedQuery(c)
	if prerr != nil {
		AbortProblem(c, http.StatusBadRequest, "bad request", prerr.Error())
		return
	}
	buildInfo, berr := buildInfoQuery(c)
	if berr != nil {
		AbortProblem(c, http.StatusBadRequest, "bad request", berr.Error())
		return
	}
	var spec domain.DAGSpec
	if err := c.ShouldBindJSON(&spec); err != nil {
		AbortProblem(c, http.StatusBadRequest, "bad request", err.Error())
		return
	}
	if c.Param("dag_id") != spec.DagID {
		AbortProblem(c, http.StatusBadRequest, "bad request", "dag_id in path does not match spec")
		return
	}
	warnings, aerr := admission.checkWarn(&spec, stageRegister, dryRun != nil)
	if aerr != nil {
		AbortProblem(c, http.StatusBadRequest, "invalid dag spec", aerr.Error())
		return
	}
	hash, err := spec.CanonicalHash()
	if err != nil {
		// Hashing touches no storage, so this error carries nothing from the
		// database, but a 500 detail is a constant here for the same reason
		// it is everywhere else: nobody re-audits a call site when the error
		// source behind it changes.
		AbortProblemCause(c, http.StatusInternalServerError, "internal error", detailInternal, err)
		return
	}
	got, err := repo.RegisterDagVersionAs(c.Request.Context(), tenantOf(c), domain.DagRegistration{
		Spec: spec, SpecHash: hash, ActorUserID: actorOf(c), IsPaused: paused, DryRun: dryRun != nil, PromotedFrom: promoted, BuildInfo: buildInfo,
		RequireDigest: admission.digest.mode,
	})
	if err != nil {
		handleRepoError(c, err)
		return
	}
	resp := versionResponse{DagID: spec.DagID, Version: spec.DagVersion, SpecHash: hash, Created: got.Created, DagCreated: got.DagCreated, IsPaused: got.IsPaused,
		Execution: execution.report(&spec, got.Created && !got.DagCreated, got.IsPaused), Warnings: warnings}
	if dryRun != nil {
		c.JSON(http.StatusOK, dryRunResponse{versionResponse: resp, DryRun: true, Warnings: warnings,
			TaskPods: dryRun.report(c.Request.Context(), &spec)})
		return
	}
	status := http.StatusOK
	if got.Created {
		status = http.StatusCreated
	}
	c.JSON(status, resp)
}
