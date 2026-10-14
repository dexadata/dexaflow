package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/domain"
)

// deployDefaultsHandler implements GET /api/v2/deployDefaults, a Dexaflow
// route Airflow has no counterpart for (#1704): the registry prefix,
// platforms and base image the operator set in deploy.defaults, which
// `dexaflow deploy` applies to whatever a project leaves unset. The values
// are the cluster's, the same for every tenant, and none is a secret. Every
// field is always present; an unset one is empty.
func deployDefaultsHandler(d domain.DeployDefaults) gin.HandlerFunc {
	body := d
	body.Platforms = append([]string{}, d.Platforms...)
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, body)
	}
}
