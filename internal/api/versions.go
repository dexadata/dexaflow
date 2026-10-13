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
	// SetCurrentDagVersion makes the version sel names current, after admit
	// accepts its spec, on behalf of actorUserID. Runs that already exist keep
	// their version; is_paused is never changed.
	SetCurrentDagVersion(ctx context.Context, tenant, dagID string, sel domain.DagVersionSelector, actorUserID string, admit func(domain.DAGSpec) error) (domain.CurrentVersionChange, error)
}

type versionResponse struct {
	DagID      string `json:"dag_id"`
	Version    string `json:"version"`
	SpecHash   string `json:"spec_hash"`
	Created    bool   `json:"created"`
	DagCreated bool   `json:"dag_created"`
	IsPaused   bool   `json:"is_paused"`
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

// pausedQuery reads the optional is_paused query parameter of a register:
// absent is nil (Airflow's rule applies), true or false sets the DAG's state.
// Any other value, an empty one included, is an error for the client to fix.
func pausedQuery(c *gin.Context) (*bool, error) {
	raw, ok := c.GetQuery("is_paused")
	if !ok {
		return nil, nil //nolint:nilnil // absent is a valid "no override", not an error
	}
	switch raw {
	case "true":
		v := true
		return &v, nil
	case "false":
		v := false
		return &v, nil
	}
	return nil, errors.New("is_paused must be true or false")
}

func registerVersionHandler(repo DagVersionRepository, admission specAdmission) gin.HandlerFunc {
	return func(c *gin.Context) {
		paused, perr := pausedQuery(c)
		if perr != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", perr.Error())
			return
		}
		// Pausing is a run-state write: PATCH /dags/{dag_id} needs
		// dexaflow:run, so a deploy-only token may not pause or unpause here.
		if u, ok := UserFromContext(c); paused != nil && ok && u.Scoped && !u.HasScope(auth.ScopeRun) {
			AbortProblem(c, http.StatusForbidden, "forbidden", "missing scope "+auth.ScopeRun+" (is_paused)")
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
		got, err := repo.RegisterDagVersionAs(c.Request.Context(), tenantOf(c), domain.DagRegistration{
			Spec: spec, SpecHash: hash, ActorUserID: actorOf(c), IsPaused: paused,
		})
		if err != nil {
			handleRepoError(c, err)
			return
		}
		status := http.StatusOK
		if got.Created {
			status = http.StatusCreated
		}
		c.JSON(status, versionResponse{DagID: spec.DagID, Version: spec.DagVersion, SpecHash: hash, Created: got.Created, DagCreated: got.DagCreated, IsPaused: got.IsPaused})
	}
}
