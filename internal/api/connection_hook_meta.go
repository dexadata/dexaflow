package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/connectors"
)

// The connection-type catalog the SPA's "Add/Edit Connection" form reads from
// GET /ui/connections/hook_meta. Each entry tells the form which standard fields
// to render (host/login/password/port/schema/description, with per-field hide /
// relabel / placeholder behavior) and which provider-specific custom fields to
// render (extra_fields, the credential fields stored in Connection.extra). The
// metadata is generated from a real Airflow install (internal/connectors —
// catalog.json), so it is the exact shape the Airflow 3.2 SPA's FlexibleForm
// renders. Without this the edit form renders empty (the form is entirely driven
// by this metadata).

type hookMetaEntry struct {
	ConnectionType  string         `json:"connection_type"`
	HookName        string         `json:"hook_name"`
	HookClassName   string         `json:"hook_class_name"`
	DefaultConnName string         `json:"default_conn_name"`
	ExtraFields     map[string]any `json:"extra_fields"`
	StandardFields  map[string]any `json:"standard_fields"`
}

// standardFieldKeys are the six standard fields the Airflow 3.2 FlexibleForm
// reads off EVERY connector unconditionally (its LDt helper does
// `g1(spec.description)`, `g1(spec.url_schema)`, …). g1 tolerates a null value
// (treats it as "use the field default") but NOT a missing key: an absent key is
// `undefined`, and `undefined.hidden` crashes the whole Connections page with
// "Cannot read properties of undefined (reading 'hidden')". The generated catalog
// only emits a standard field when it has an override, so most connectors are
// missing several keys — which is why opening Connections errored for the user.
var standardFieldKeys = []string{"description", "host", "login", "password", "port", "url_schema"}

// ensureStandardFields returns a copy that always carries all six standardFieldKeys.
// A missing key is filled with nil (the form's "use default" sentinel, which g1
// handles); existing values (including nil) are preserved. This is the fix for the
// undefined-key crash above.
func ensureStandardFields(m map[string]any) map[string]any {
	out := make(map[string]any, len(standardFieldKeys))
	for k, v := range m {
		out[k] = v
	}
	for _, k := range standardFieldKeys {
		if _, ok := out[k]; !ok {
			out[k] = nil
		}
	}
	return out
}

// sanitizeExtraFields replaces each nil extra-field spec with a minimal visible
// one ({"hidden": false}) and returns {} for a nil map. Unlike the standard fields
// (rendered through g1, which guards nil), the SPA renders extra fields by
// iterating the map and reading `spec.hidden` directly — a nil spec there would
// crash. Extra fields are keyed dynamically, so only nil VALUES (not missing keys)
// are a hazard.
func sanitizeExtraFields(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if v == nil {
			out[k] = map[string]any{"hidden": false}
			continue
		}
		out[k] = v
	}
	return out
}

// connectionTypeCatalog builds the form catalog from the shared connector
// registry (internal/connectors — ADR 0038's single source of truth), serving
// each entry's generated standard_fields + extra_fields verbatim.
func connectionTypeCatalog() []hookMetaEntry {
	cat := connectors.Catalog()
	out := make([]hookMetaEntry, len(cat))
	for i, c := range cat {
		out[i] = hookMetaEntry{
			ConnectionType:  c.ConnectionType,
			HookName:        c.HookName,
			HookClassName:   c.HookClassName,
			DefaultConnName: c.DefaultConnName,
			ExtraFields:     sanitizeExtraFields(c.ExtraFields),
			StandardFields:  ensureStandardFields(c.StandardFields),
		}
	}
	return out
}

// connectionHookMetaHandler serves the connection-type catalog the form needs.
func connectionHookMetaHandler() gin.HandlerFunc {
	catalog := connectionTypeCatalog()
	return func(c *gin.Context) { c.JSON(http.StatusOK, catalog) }
}
