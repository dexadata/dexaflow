package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/domain"
)

// maxUserPatchBody caps a PATCH /api/v2/users/{user_id} body; the only field is
// a boolean.
const maxUserPatchBody = 1 << 10

// UserActivator activates and deactivates accounts for the admin user API
// (#960). The store records user.activate or user.deactivate with the change,
// in one transaction, with actorUserID as the acting admin. A user that is not
// in tenant is domain.ErrNotFound.
type UserActivator interface {
	SetUserActive(ctx context.Context, tenant, actorUserID, userID string, active bool) (domain.User, error)
}

// patchUserBody is the PATCH /api/v2/users/{user_id} payload. is_active is the
// only field that can change today; roles and passwords are #2229.
type patchUserBody struct {
	IsActive *bool `json:"is_active"`
}

// patchUserHandler serves PATCH /api/v2/users/{user_id}: it activates or
// deactivates an account of the caller's tenant. A deactivated user's sessions
// and API tokens stop working at their next request, because every request
// reloads the user and refuses an inactive one. An admin cannot deactivate
// their own account, so an install cannot lock out the admin making the call.
func patchUserHandler(store UserActivator) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body patchUserBody
		dec := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, maxUserPatchBody))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil || dec.More() || body.IsActive == nil {
			AbortProblem(c, http.StatusBadRequest, "bad request",
				"the body must be a JSON object with is_active (true or false); roles and passwords cannot be changed here yet")
			return
		}
		userID := c.Param("user_id")
		actorID := ""
		if actor, ok := UserFromContext(c); ok {
			actorID = actor.ID
		}
		if !*body.IsActive && actorID != "" && actorID == userID {
			AbortProblem(c, http.StatusConflict, "conflict", "you cannot deactivate your own account")
			return
		}
		user, err := store.SetUserActive(c.Request.Context(), tenantOf(c), actorID, userID, *body.IsActive)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		c.JSON(http.StatusOK, toUserDTO(user))
	}
}
