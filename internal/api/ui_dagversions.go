package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/domain"
)

// DagVersionLister lists a DAG's registered versions. The Airflow UI fetches
// this to resolve a version_number before requesting version-scoped structure
// (the Graph view); without it the graph never loads. See docs/ui-compatibility.md.
type DagVersionLister interface {
	ListDagVersions(ctx context.Context, tenant, dagID string) ([]domain.DagVersion, error)
}

// dagVersionDTO is the Airflow 3.2.1 DAGVersionResponse. Leoflow surfaces the
// deployment label (git describe in prod, "dev-<ts>" in dev) as bundle_version —
// Airflow's field for the deployed bundle's version — so a run is traceable to
// its deployment. bundle_url is null; bundle_name is a constant.
type dagVersionDTO struct {
	ID             string    `json:"id"`
	VersionNumber  int       `json:"version_number"`
	DagID          string    `json:"dag_id"`
	BundleName     string    `json:"bundle_name"`
	BundleVersion  *string   `json:"bundle_version"`
	CreatedAt      time.Time `json:"created_at"`
	DagDisplayName string    `json:"dag_display_name"`
	BundleURL      *string   `json:"bundle_url"`
}

type dagVersionCollectionDTO struct {
	DagVersions  []dagVersionDTO `json:"dag_versions"`
	TotalEntries int             `json:"total_entries"`
}

// toDagVersionDTO maps a domain version onto the Airflow DAGVersionResponse.
func toDagVersionDTO(dagID string, v domain.DagVersion) dagVersionDTO {
	return dagVersionDTO{
		ID:             v.ID,
		VersionNumber:  v.VersionNumber,
		DagID:          dagID,
		BundleName:     "leoflow",
		BundleVersion:  strPtrOrNil(v.Version),
		CreatedAt:      v.CreatedAt,
		DagDisplayName: dagID,
	}
}

// dagVersionsHandler implements GET /api/v2/dags/{dag_id}/dagVersions.
func dagVersionsHandler(lister DagVersionLister) gin.HandlerFunc {
	return func(c *gin.Context) {
		dagID := c.Param("dag_id")
		versions, err := lister.ListDagVersions(c.Request.Context(), tenantOf(c), dagID)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		out := dagVersionCollectionDTO{
			DagVersions:  make([]dagVersionDTO, 0, len(versions)),
			TotalEntries: len(versions),
		}
		for _, v := range versions {
			out.DagVersions = append(out.DagVersions, toDagVersionDTO(dagID, v))
		}
		c.JSON(http.StatusOK, out)
	}
}

// dagVersionHandler implements GET /api/v2/dags/{dag_id}/dagVersions/{version_number}:
// the single version the Code tab requests. It resolves the number against the
// version list (404 if absent).
func dagVersionHandler(lister DagVersionLister) gin.HandlerFunc {
	return func(c *gin.Context) {
		dagID := c.Param("dag_id")
		number, err := strconv.Atoi(c.Param("version_number"))
		if err != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", "version_number must be an integer")
			return
		}
		versions, err := lister.ListDagVersions(c.Request.Context(), tenantOf(c), dagID)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		for _, v := range versions {
			if v.VersionNumber == number {
				c.JSON(http.StatusOK, toDagVersionDTO(dagID, v))
				return
			}
		}
		AbortProblem(c, http.StatusNotFound, "not found", "dag version not found")
	}
}
