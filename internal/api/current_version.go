package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/domain"
)

// maxVersionLabelLen bounds the label a set-current request may name.
const maxVersionLabelLen = 255

// setCurrentVersionRequest is the body of PUT /api/v2/dags/{dag_id}/currentVersion:
// exactly one of a version label and a version_number.
type setCurrentVersionRequest struct {
	Version       *string `json:"version"`
	VersionNumber *int    `json:"version_number"`
}

// selector validates the request and returns the version it names.
func (r setCurrentVersionRequest) selector() (domain.DagVersionSelector, error) {
	switch {
	case (r.Version == nil) == (r.VersionNumber == nil):
		return domain.DagVersionSelector{}, errors.New("set exactly one of version and version_number")
	case r.Version != nil && (*r.Version == "" || len(*r.Version) > maxVersionLabelLen):
		return domain.DagVersionSelector{}, fmt.Errorf("version must be 1 to %d characters", maxVersionLabelLen)
	case r.Version != nil:
		return domain.DagVersionSelector{Version: *r.Version}, nil
	case *r.VersionNumber < 1:
		return domain.DagVersionSelector{}, errors.New("version_number must be 1 or more")
	default:
		return domain.DagVersionSelector{Number: *r.VersionNumber}, nil
	}
}

// currentVersionResponse reports a set-current: the version that was current
// (null when none was), the one that is now, and whether anything changed.
type currentVersionResponse struct {
	DagID   string               `json:"dag_id"`
	Changed bool                 `json:"changed"`
	From    *listedDagVersionDTO `json:"from"`
	To      listedDagVersionDTO  `json:"to"`
	// Warnings is what the change let through with a warning, such as an
	// image not pinned by digest under deploy.require_digest=warn (#1703);
	// absent when there is none.
	Warnings []string `json:"warnings,omitempty"`
}

// admissionError is a refusal of the target spec by the register checks; it
// is the client's to fix, so it answers 400 with its own text.
type admissionError struct{ err error }

func (e admissionError) Error() string { return e.err.Error() }
func (e admissionError) Unwrap() error { return e.err }

// setCurrentVersionHandler implements PUT /api/v2/dags/{dag_id}/currentVersion,
// a Dexaflow route Airflow does not have (#1702): it makes a registered version
// the one new runs use, after the checks a register of it would pass today.
func setCurrentVersionHandler(repo DagVersionRepository, admission specAdmission) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req setCurrentVersionRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", "the body must be a JSON object with version or version_number")
			return
		}
		sel, err := req.selector()
		if err != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", err.Error())
			return
		}
		dagID := c.Param("dag_id")
		var warnings []string
		admit := func(spec domain.DAGSpec) error {
			w, aerr := admission.checkWarn(&spec, stageSetCurrent, false)
			if aerr != nil {
				return admissionError{aerr}
			}
			warnings = w
			return nil
		}
		change, err := repo.SetCurrentDagVersionAs(c.Request.Context(), tenantOf(c), domain.DagSetCurrent{
			DagID: dagID, Selector: sel, ActorUserID: actorOf(c), Admit: admit, RequireDigest: admission.digest.mode,
		})
		var refused admissionError
		switch {
		case errors.As(err, &refused):
			AbortProblem(c, http.StatusBadRequest, "invalid dag spec", refused.Error())
			return
		case err != nil:
			handleRepoError(c, err)
			return
		}
		out := currentVersionResponse{DagID: dagID, Changed: change.Changed, To: toListedDagVersionDTO(dagID, change.To), Warnings: warnings}
		if change.From.ID != "" {
			from := toListedDagVersionDTO(dagID, change.From)
			out.From = &from
		}
		c.JSON(http.StatusOK, out)
	}
}
