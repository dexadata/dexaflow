package api

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/domain"
)

// DagVersionRepository registers compiled DAG versions and chooses which one
// is current.
type DagVersionRepository interface {
	// RegisterDagVersionBy upserts the DAG and inserts a version keyed by
	// specHash on behalf of the user actorUserID, reporting whether a new
	// version was created (false if the hash already existed: the push is
	// idempotent).
	RegisterDagVersionBy(ctx context.Context, tenant, actorUserID string, spec domain.DAGSpec, specHash string) (bool, error)
	// SetCurrentDagVersion makes the version sel names current, after admit
	// accepts its spec, on behalf of actorUserID. Runs that already exist keep
	// their version; is_paused is never changed.
	SetCurrentDagVersion(ctx context.Context, tenant, dagID string, sel domain.DagVersionSelector, actorUserID string, admit func(domain.DAGSpec) error) (domain.CurrentVersionChange, error)
}

type versionResponse struct {
	DagID    string `json:"dag_id"`
	Version  string `json:"version"`
	SpecHash string `json:"spec_hash"`
	Created  bool   `json:"created"`
}

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
// every misfit tolerated under enforce: warn.
func (g unitGate) check(spec *domain.DAGSpec, stage string) error {
	warned, refused := g.unit.CheckSpec(spec)
	if refused != nil {
		return refused
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
	return nil
}

// specAdmission is what a spec must pass, beyond the repository's own
// checks, to be registered or made current: its own validation, the resource
// unit, source mode and the operator's execution allowlist.
type specAdmission struct {
	unit            unitGate
	sourceModeImage string
	policy          *domain.ExecutionPolicy
}

// check returns the first refusal of spec; each one is the client's to fix.
// stage is stageRegister or stageSetCurrent.
func (a specAdmission) check(spec *domain.DAGSpec, stage string) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	if err := a.unit.check(spec, stage); err != nil {
		return err
	}
	if err := domain.CheckSourceMode(a.sourceModeImage, spec); err != nil {
		return err
	}
	// The operator's execution allowlist (issue #629): refuse at push what
	// dispatch would refuse anyway, so the author learns it now.
	return a.policy.CheckSpec(spec)
}

// actorOf is the id of the authenticated user, or "" when there is none.
func actorOf(c *gin.Context) string {
	if u, ok := UserFromContext(c); ok {
		return u.ID
	}
	return ""
}

func registerVersionHandler(repo DagVersionRepository, admission specAdmission) gin.HandlerFunc {
	return func(c *gin.Context) {
		var spec domain.DAGSpec
		if err := c.ShouldBindJSON(&spec); err != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", err.Error())
			return
		}
		if c.Param("dag_id") != spec.DagID {
			AbortProblem(c, http.StatusBadRequest, "bad request", "dag_id in path does not match spec")
			return
		}
		if err := admission.check(&spec, stageRegister); err != nil {
			AbortProblem(c, http.StatusBadRequest, "invalid dag spec", err.Error())
			return
		}
		hash, err := spec.CanonicalHash()
		if err != nil {
			// Hashing touches no storage, so this error carries nothing from the
			// database — but a 500 detail is a constant here for the same reason
			// it is everywhere else: nobody re-audits a call site when the error
			// source behind it changes.
			AbortProblemCause(c, http.StatusInternalServerError, "internal error", detailInternal, err)
			return
		}
		created, err := repo.RegisterDagVersionBy(c.Request.Context(), tenantOf(c), actorOf(c), spec, hash)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		c.JSON(status, versionResponse{DagID: spec.DagID, Version: spec.DagVersion, SpecHash: hash, Created: created})
	}
}
