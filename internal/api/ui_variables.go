package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/domain"
)

// VariableStore reads and writes Airflow-style Variables for the Admin UI.
type VariableStore interface {
	ListVariables(ctx context.Context, tenant string, limit, offset int) ([]domain.Variable, int, error)
	GetVariable(ctx context.Context, tenant, key string) (domain.Variable, error)
	SetVariablePatch(ctx context.Context, tenant string, p domain.VariablePatch) error
	DeleteVariable(ctx context.Context, tenant, key string) error
}

// sensitiveKeyParts mark a variable whose value is masked in API responses, so
// secrets are not echoed back to the UI (mirrors Airflow's default).
var sensitiveKeyParts = []string{
	"secret", "password", "passwd", "passphrase", "token",
	"apikey", "api_key", "access_key", "private_key", "authorization", "credential",
	// keyfile covers a service-account JSON (BigQuery keyfile_dict / keyfile_json).
	"keyfile",
}

// isSensitiveKey reports whether a key name looks like it holds a secret, so its
// value is masked in API responses (variables and connection `extra`).
func isSensitiveKey(key string) bool {
	lower := strings.ToLower(key)
	for _, part := range sensitiveKeyParts {
		if strings.Contains(lower, part) {
			return true
		}
	}
	return false
}

// maskedValue returns the mask when the key looks sensitive, else the value.
func maskedValue(key, value string) string {
	if isSensitiveKey(key) {
		return secretMask
	}
	return value
}

// buildVariablePatch maps the tri-state variable body to a VariablePatch (#887),
// applying the masked round-trip rule: a value equal to the mask for a
// sensitive-looking key is treated as "unchanged". Because the `value` column is
// NOT NULL it cannot be preserved at the SQL layer, so "preserve" is resolved
// here to the stored value (available on update). On create there is nothing to
// preserve, so an absent or masked value becomes an explicit empty string (fail
// closed — never persist a literal mask as the real value). An explicit "" for a
// non-mask value always clears.
func buildVariablePatch(key string, value, desc *string, stored domain.Variable, isCreate bool) domain.VariablePatch {
	p := domain.VariablePatch{Key: key, Description: desc}
	switch {
	case value != nil && (!isSensitiveKey(key) || *value != secretMask):
		p.Value = value // explicit set (including "" to clear)
	case isCreate:
		empty := ""
		p.Value = &empty // nothing to preserve on create → empty
	default:
		v := stored.Value // update: absent or masked → preserve the stored value
		p.Value = &v
	}
	return p
}

// variableDTO is the Airflow 3.2.1 VariableResponse. Leoflow stores variables in
// plaintext for now (is_encrypted is false) and has no teams (team_name null).
type variableDTO struct {
	Key         string  `json:"key"`
	Value       string  `json:"value"`
	Description *string `json:"description"`
	IsEncrypted bool    `json:"is_encrypted"`
	TeamName    *string `json:"team_name"`
}

type variableCollectionDTO struct {
	Variables    []variableDTO `json:"variables"`
	TotalEntries int           `json:"total_entries"`
}

func toVariableDTO(v domain.Variable) variableDTO {
	return variableDTO{
		Key:         v.Key,
		Value:       maskedValue(v.Key, v.Value),
		Description: strPtrOrNil(v.Description),
		IsEncrypted: false,
		TeamName:    nil,
	}
}

// listVariablesHandler implements GET /api/v2/variables.
func listVariablesHandler(store VariableStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		limit, offset := pagination(c)
		vars, total, err := store.ListVariables(c.Request.Context(), tenantOf(c), limit, offset)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		out := variableCollectionDTO{Variables: make([]variableDTO, 0, len(vars)), TotalEntries: total}
		for _, v := range vars {
			out.Variables = append(out.Variables, toVariableDTO(v))
		}
		c.JSON(http.StatusOK, out)
	}
}

// getVariableHandler implements GET /api/v2/variables/{variable_key}.
func getVariableHandler(store VariableStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		v, err := store.GetVariable(c.Request.Context(), tenantOf(c), c.Param("variable_key"))
		if err != nil {
			handleRepoError(c, err)
			return
		}
		c.JSON(http.StatusOK, toVariableDTO(v))
	}
}

// createVariableHandler implements POST /api/v2/variables.
func createVariableHandler(store VariableStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body struct {
			Key         string  `json:"key"`
			Value       *string `json:"value"`
			Description *string `json:"description"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", err.Error())
			return
		}
		if body.Key == "" {
			AbortProblem(c, http.StatusBadRequest, "bad request", "key is required")
			return
		}
		// POST is an upsert (`leoflow variables set`), so merge against any existing
		// variable: a masked value for an existing sensitive key then preserves the
		// stored value, exactly as on PATCH. A genuinely new key (not found) is a
		// create, where a masked value has nothing to preserve → empty (fail closed).
		stored, gerr := store.GetVariable(c.Request.Context(), tenantOf(c), body.Key)
		isCreate := errors.Is(gerr, ErrNotFound)
		if gerr != nil && !isCreate {
			handleRepoError(c, gerr)
			return
		}
		patch := buildVariablePatch(body.Key, body.Value, body.Description, stored, isCreate)
		if err := store.SetVariablePatch(c.Request.Context(), tenantOf(c), patch); err != nil {
			handleRepoError(c, err)
			return
		}
		dto, err := effectiveVariableDTO(c, store, body.Key)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		c.JSON(http.StatusCreated, dto)
	}
}

// updateVariableHandler implements PATCH /api/v2/variables/{variable_key}.
func updateVariableHandler(store VariableStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := c.Param("variable_key")
		stored, gerr := store.GetVariable(c.Request.Context(), tenantOf(c), key)
		if gerr != nil {
			handleRepoError(c, gerr)
			return
		}
		var body struct {
			Value       *string `json:"value"`
			Description *string `json:"description"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", err.Error())
			return
		}
		patch := buildVariablePatch(key, body.Value, body.Description, stored, false)
		if err := store.SetVariablePatch(c.Request.Context(), tenantOf(c), patch); err != nil {
			handleRepoError(c, err)
			return
		}
		dto, err := effectiveVariableDTO(c, store, key)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		c.JSON(http.StatusOK, dto)
	}
}

// effectiveVariableDTO re-reads the variable after a write so the response
// reflects the merged result (preserved + cleared + set fields), with a
// sensitive-keyed value masked — never an echo of the request body.
func effectiveVariableDTO(c *gin.Context, store VariableStore, key string) (variableDTO, error) {
	v, err := store.GetVariable(c.Request.Context(), tenantOf(c), key)
	if err != nil {
		return variableDTO{}, err
	}
	return toVariableDTO(v), nil
}

// deleteVariableHandler implements DELETE /api/v2/variables/{variable_key}.
func deleteVariableHandler(store VariableStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := store.DeleteVariable(c.Request.Context(), tenantOf(c), c.Param("variable_key")); err != nil {
			handleRepoError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// registerUIVariables mounts the Admin Variables CRUD when a store is set;
// otherwise the empty stub stands in so the Admin page still renders.
func registerUIVariables(r gin.IRouter, store VariableStore) {
	if store == nil {
		r.GET("/api/v2/variables", apiEmptyCollection("variables"))
		return
	}
	r.GET("/api/v2/variables", RequirePermission("read", "variable"), listVariablesHandler(store))
	r.GET("/api/v2/variables/:variable_key", RequirePermission("read", "variable"), getVariableHandler(store))
	r.POST("/api/v2/variables", RequirePermission("write", "variable"), createVariableHandler(store))
	r.PATCH("/api/v2/variables/:variable_key", RequirePermission("write", "variable"), updateVariableHandler(store))
	r.DELETE("/api/v2/variables/:variable_key", RequirePermission("write", "variable"), deleteVariableHandler(store))
}
