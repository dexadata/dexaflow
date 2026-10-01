package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/domain"
)

// secretMask is the placeholder a read API returns in place of a secret
// (a connection password/extra value, a sensitive-keyed variable). On write it
// is treated as "unchanged": a field equal to it preserves the stored value
// rather than overwriting it, so a round-tripped GET (Admin UI re-submitting a
// connection it read) never persists the mask over the real secret (#874, #887).
const secretMask = "***"

// redactExtra masks secret-bearing values in a connection's free-form `extra`
// before it is returned by the read API (#11). The extra is where provider
// secrets live — Databricks `client_secret`/`token`, Snowflake/`private_key`,
// BigQuery `keyfile_dict` — so echoing it verbatim leaks credentials on a plain
// GET. Non-sensitive keys (host, http_path, account, warehouse, schema, method)
// are preserved so the UI/operator can still read the connection's shape.
//
// Keys are matched by name via isSensitiveKey, which also covers Airflow's
// prefixed form (extra__<type>__client_secret). If the extra is not a JSON
// object (unexpected), it is redacted wholesale — fail closed rather than risk
// echoing a secret. A masked value written straight back is treated as
// "unchanged" by the write path (unmaskExtra restores it from the stored blob),
// so a round-tripped GET never persists the mask over the real secret (#874).
func redactExtra(extra string) string {
	if extra == "" {
		return extra
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(extra), &m); err != nil {
		return secretMask // not a JSON object → fail closed rather than echo a possible secret
	}
	redactMap(m)
	b, err := json.Marshal(m)
	if err != nil {
		return secretMask
	}
	return string(b)
}

// redactMap masks, to secretMask, every value whose key is sensitive, recursing
// into nested objects and arrays so a secret nested under a non-sensitive key
// (extra is free-form) is still caught, not just top-level ones.
func redactMap(m map[string]any) {
	for k, v := range m {
		if isSensitiveKey(k) {
			m[k] = secretMask
			continue
		}
		redactAny(v)
	}
}

func redactAny(v any) {
	switch t := v.(type) {
	case map[string]any:
		redactMap(t)
	case []any:
		for _, e := range t {
			redactAny(e)
		}
	}
}

// ConnectionStore reads and writes Airflow-style Connections for the Admin UI.
// Password is encrypted at rest by the store (ADR 0019) and never returned.
type ConnectionStore interface {
	ListConnections(ctx context.Context, tenant string, limit, offset int) ([]domain.Connection, int, error)
	GetConnection(ctx context.Context, tenant, connID string) (domain.Connection, error)
	SetConnectionPatch(ctx context.Context, tenant string, p domain.ConnectionPatch) error
	DeleteConnection(ctx context.Context, tenant, connID string) error
}

// connectionDTO is the Airflow 3.2.1 ConnectionResponse. The password is never
// included — it is write-only.
type connectionDTO struct {
	ConnectionID string  `json:"connection_id"`
	ConnType     string  `json:"conn_type"`
	Description  *string `json:"description"`
	Host         *string `json:"host"`
	Login        *string `json:"login"`
	Schema       *string `json:"schema"`
	Port         *int    `json:"port"`
	Extra        *string `json:"extra"`
}

type connectionCollectionDTO struct {
	Connections  []connectionDTO `json:"connections"`
	TotalEntries int             `json:"total_entries"`
}

func toConnectionDTO(c domain.Connection) connectionDTO {
	return connectionDTO{
		ConnectionID: c.ConnID,
		ConnType:     c.ConnType,
		Description:  strPtrOrNil(c.Description),
		Host:         strPtrOrNil(c.Host),
		Login:        strPtrOrNil(c.Login),
		Schema:       strPtrOrNil(c.Schema),
		Port:         c.Port,
		Extra:        strPtrOrNil(redactExtra(c.Extra)),
	}
}

// connectionBody is the POST/PATCH payload; password is accepted (write-only).
// Every optional field is a pointer so the write path is tri-state (#887): a key
// omitted from the JSON is nil (preserve the stored value), a key present with
// "" clears the field, and a value sets it. ConnType is required on every write.
type connectionBody struct {
	ConnectionID string  `json:"connection_id"`
	ConnType     string  `json:"conn_type"`
	Description  *string `json:"description"`
	Host         *string `json:"host"`
	Login        *string `json:"login"`
	Password     *string `json:"password"`
	Schema       *string `json:"schema"`
	Port         *int    `json:"port"`
	Extra        *string `json:"extra"`
}

// toDomain flattens the tri-state body to a plain domain.Connection (absent
// fields become ""). It is used only by the non-persisting "test" probe, which
// validates the posted connection's structure and has no stored value to merge.
func (b connectionBody) toDomain(connID string) domain.Connection {
	return domain.Connection{
		ConnID: connID, ConnType: b.ConnType, Host: strVal(b.Host), Schema: strVal(b.Schema),
		Login: strVal(b.Login), Password: strVal(b.Password), Port: b.Port,
		Extra: strVal(b.Extra), Description: strVal(b.Description),
	}
}

// toPatch builds a tri-state ConnectionPatch from the body, applying the
// secret-mask rule (#874): a password equal to the mask is treated as absent
// (preserve), and each masked value inside extra is restored from the stored
// blob rather than overwriting the real secret. stored is the current connection
// (extra decrypted) on update, or the zero value on create. Non-secret fields
// pass through unchanged, so an omitted key preserves and an explicit "" clears.
func (b connectionBody) toPatch(connID string, stored domain.Connection) domain.ConnectionPatch {
	p := domain.ConnectionPatch{
		ConnID: connID, ConnType: b.ConnType,
		Host: b.Host, Login: b.Login, Schema: b.Schema,
		Port: b.Port, Description: b.Description,
	}
	// Password: absent preserves; the mask means "unchanged" → also preserve.
	if b.Password != nil && *b.Password != secretMask {
		p.Password = b.Password
	}
	// Extra: absent preserves; present is unmasked against the stored extra so a
	// round-tripped GET (secrets shown as the mask) never persists the mask. If
	// the unmask cannot cleanly resolve every mask, the whole field is preserved
	// (fail closed) rather than risking a literal mask overwriting a real secret.
	if b.Extra != nil {
		if merged, preserve := unmaskExtra(*b.Extra, stored.Extra); !preserve {
			p.Extra = &merged
		}
	}
	return p
}

// unmaskExtra reconciles an incoming extra against the stored extra for the
// masked round-trip (#874). It returns the extra to persist and whether the
// whole field should instead be preserved (left NULL):
//   - incoming exactly the mask → the GET redacted the whole blob (extra was not
//     a JSON object); preserve.
//   - incoming a JSON object → every value (recursively) equal to the mask is
//     replaced by the stored value at the same key; a masked key with no stored
//     counterpart is dropped. If any mask survives (e.g. a mask buried in an
//     array the stored side cannot supply), preserve the whole field — fail
//     closed rather than persist a literal mask.
//   - otherwise (not the mask, not an object; includes "") → persist as-is, so
//     an explicit "" still clears the field.
func unmaskExtra(incoming, stored string) (result string, preserve bool) {
	if incoming == secretMask {
		return "", true
	}
	var in map[string]any
	if err := json.Unmarshal([]byte(incoming), &in); err != nil {
		return incoming, false
	}
	var st map[string]any
	if err := json.Unmarshal([]byte(stored), &st); err != nil {
		st = nil // stored may be "" or a non-object → merge against an empty map
	}
	unmaskMap(in, st, true)
	if containsMask(in) {
		return "", true // a mask we could not resolve remains → preserve, fail closed
	}
	b, err := json.Marshal(in)
	if err != nil {
		return "", true // cannot re-encode → preserve rather than risk a bad write
	}
	return string(b), false
}

// unmaskMap replaces, in place, every masked value in in with the value at the
// same key in stored, recursing into nested objects AND arrays (mirroring
// redactMap/redactAny, which mask secrets nested under arrays too). topLevel
// governs what happens to a masked string with no stored counterpart: at the top
// level it is deleted (a spurious masked key is dropped, never persisted as a
// literal mask), but deeper it is LEFT in place so containsMask forces the whole
// field to be preserved (fail closed) rather than silently editing a nested
// structure whose secret we cannot restore.
func unmaskMap(in, stored map[string]any, topLevel bool) {
	for k, v := range in {
		switch tv := v.(type) {
		case string:
			if tv == secretMask {
				if sv, ok := stored[k]; ok {
					in[k] = sv
				} else if topLevel {
					delete(in, k)
				}
				// deeper: leave the mask → containsMask preserves the whole field
			}
		case map[string]any:
			var sub map[string]any
			if sv, ok := stored[k].(map[string]any); ok {
				sub = sv
			}
			unmaskMap(tv, sub, false)
		case []any:
			var sub []any
			if sv, ok := stored[k].([]any); ok {
				sub = sv
			}
			unmaskSlice(tv, sub)
		}
	}
}

// unmaskSlice restores masked values inside an array by aligning with the stored
// array by index, mirroring redactAny's array recursion. A masked string element
// is restored from the stored element at the same index when present; map and
// nested-array elements are recursed. Anything a mask cannot be resolved from
// (a shorter/absent stored array, a type mismatch) is left as the mask, so
// containsMask forces the whole field to be preserved — never a silent drop that
// would shift array indices.
func unmaskSlice(in, stored []any) {
	for i, v := range in {
		var s any
		if i < len(stored) {
			s = stored[i]
		}
		switch tv := v.(type) {
		case string:
			if tv == secretMask && s != nil {
				in[i] = s
			}
			// else leave the mask → containsMask fails closed
		case map[string]any:
			var sub map[string]any
			if sm, ok := s.(map[string]any); ok {
				sub = sm
			}
			unmaskMap(tv, sub, false)
		case []any:
			var sub []any
			if ss, ok := s.([]any); ok {
				sub = ss
			}
			unmaskSlice(tv, sub)
		}
	}
}

// containsMask reports whether the mask survives anywhere in v (as a value in a
// map, an element of an array, or a bare string), so unmaskExtra can fail closed
// on anything it could not resolve.
func containsMask(v any) bool {
	switch t := v.(type) {
	case string:
		return t == secretMask
	case map[string]any:
		for _, e := range t {
			if containsMask(e) {
				return true
			}
		}
	case []any:
		for _, e := range t {
			if containsMask(e) {
				return true
			}
		}
	}
	return false
}

// strVal dereferences an optional string, returning "" for nil.
func strVal(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func listConnectionsHandler(store ConnectionStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		limit, offset := pagination(c)
		conns, total, err := store.ListConnections(c.Request.Context(), tenantOf(c), limit, offset)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		out := connectionCollectionDTO{Connections: make([]connectionDTO, 0, len(conns)), TotalEntries: total}
		for _, conn := range conns {
			out.Connections = append(out.Connections, toConnectionDTO(conn))
		}
		c.JSON(http.StatusOK, out)
	}
}

func getConnectionHandler(store ConnectionStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		conn, err := store.GetConnection(c.Request.Context(), tenantOf(c), c.Param("connection_id"))
		if err != nil {
			handleRepoError(c, err)
			return
		}
		c.JSON(http.StatusOK, toConnectionDTO(conn))
	}
}

func createConnectionHandler(store ConnectionStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body connectionBody
		if err := c.ShouldBindJSON(&body); err != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", err.Error())
			return
		}
		if body.ConnectionID == "" || body.ConnType == "" {
			AbortProblem(c, http.StatusBadRequest, "bad request", "connection_id and conn_type are required")
			return
		}
		// POST is an upsert (`leoflow connections set`), so merge against any
		// existing connection: a masked field then preserves the stored secret,
		// exactly as on PATCH. When the connection does not exist yet, stored is
		// the zero value and a masked field has nothing to preserve, so toPatch
		// drops it (fail closed) rather than persisting the literal mask.
		stored, gerr := store.GetConnection(c.Request.Context(), tenantOf(c), body.ConnectionID)
		if gerr != nil && !errors.Is(gerr, ErrNotFound) {
			handleRepoError(c, gerr)
			return
		}
		patch := body.toPatch(body.ConnectionID, stored)
		if err := store.SetConnectionPatch(c.Request.Context(), tenantOf(c), patch); err != nil {
			handleConnWriteError(c, err)
			return
		}
		dto, err := effectiveConnectionDTO(c, store, body.ConnectionID)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		c.JSON(http.StatusCreated, dto)
	}
}

func updateConnectionHandler(store ConnectionStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		connID := c.Param("connection_id")
		stored, gerr := store.GetConnection(c.Request.Context(), tenantOf(c), connID)
		if gerr != nil {
			handleRepoError(c, gerr)
			return
		}
		var body connectionBody
		if err := c.ShouldBindJSON(&body); err != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", err.Error())
			return
		}
		if body.ConnType == "" {
			AbortProblem(c, http.StatusBadRequest, "bad request", "conn_type is required")
			return
		}
		// Merge the tri-state body over the stored connection so an omitted field
		// preserves, an explicit "" clears, and a masked secret is left unchanged.
		patch := body.toPatch(connID, stored)
		if err := store.SetConnectionPatch(c.Request.Context(), tenantOf(c), patch); err != nil {
			handleConnWriteError(c, err)
			return
		}
		dto, err := effectiveConnectionDTO(c, store, connID)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		c.JSON(http.StatusOK, dto)
	}
}

// effectiveConnectionDTO re-reads the connection after a write so the response
// reflects the merged result (preserved + cleared + set fields), with secrets
// masked — never an echo of the request body, which would show empty for every
// field the caller left the store to preserve.
func effectiveConnectionDTO(c *gin.Context, store ConnectionStore, connID string) (connectionDTO, error) {
	got, err := store.GetConnection(c.Request.Context(), tenantOf(c), connID)
	if err != nil {
		return connectionDTO{}, err
	}
	return toConnectionDTO(got), nil
}

func deleteConnectionHandler(store ConnectionStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := store.DeleteConnection(c.Request.Context(), tenantOf(c), c.Param("connection_id")); err != nil {
			handleRepoError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// handleConnWriteError reports a missing encryption key as a clear 503 rather
// than a generic 500 (writes are refused without a key — ADR 0019).
func handleConnWriteError(c *gin.Context, err error) {
	if err.Error() == "no encryption key configured" {
		AbortProblem(c, http.StatusServiceUnavailable, "encryption unavailable",
			"set LEOFLOW_SECRET_KEY to manage connections (secrets are never stored in plaintext)")
		return
	}
	handleRepoError(c, err)
}

// registerUIConnections mounts the Admin Connections CRUD when a store is set;
// otherwise an empty-collection stub keeps the Admin page rendering.
func registerUIConnections(r gin.IRouter, store ConnectionStore, tester ConnectionTester) {
	if tester == nil {
		tester = defaultConnectionTester{}
	}
	// The panel's "Test" button (POST /api/v2/connections/test) tests the posted
	// body without persisting; available even when no store is configured.
	r.POST("/api/v2/connections/test", RequirePermission("write", "connection"), testConnectionHandler(tester))
	if store == nil {
		r.GET("/api/v2/connections", apiEmptyCollection("connections"))
		return
	}
	r.GET("/api/v2/connections", RequirePermission("read", "connection"), listConnectionsHandler(store))
	r.GET("/api/v2/connections/:connection_id", RequirePermission("read", "connection"), getConnectionHandler(store))
	r.POST("/api/v2/connections", RequirePermission("write", "connection"), createConnectionHandler(store))
	r.PATCH("/api/v2/connections/:connection_id", RequirePermission("write", "connection"), updateConnectionHandler(store))
	r.DELETE("/api/v2/connections/:connection_id", RequirePermission("write", "connection"), deleteConnectionHandler(store))
}
