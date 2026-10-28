package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/domain"
)

// deployDefaultsHandler implements GET /api/v2/deployDefaults, a Dexaflow
// route Airflow has no counterpart for (#1704): the registry prefix,
// platforms, base image and remote builder the operator set in
// deploy.defaults, the source mode runtime image when
// deploy.defaults.source_mode advertises it (#1772), which `dexaflow deploy`
// applies to whatever a project leaves unset, and the task service account
// and warm pools when their switches advertise them (#1890), which it
// reports. The values are the cluster's, the same for every tenant, and none
// is a secret, except ci_oidc: the CI OIDC issuers of the caller's tenant
// (ADR 0092), so a user never reads another tenant's entries. Every field is
// always present; an unset one is empty or null.
func deployDefaultsHandler(d domain.DeployDefaults, ci CIOIDCVerifier) gin.HandlerFunc {
	base := d
	base.Platforms = append([]string{}, d.Platforms...)
	base.CIOIDC = nil
	return func(c *gin.Context) {
		body := base
		if user, ok := UserFromContext(c); ok && ci != nil {
			if issuers := ci.Advertised(user.TenantID); len(issuers) > 0 {
				body.CIOIDC = &domain.CIOIDCDefaults{Issuers: issuers}
			}
		}
		c.JSON(http.StatusOK, body)
	}
}
