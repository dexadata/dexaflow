// Package connectors is the single source of truth for the Airflow connector
// types Dexaflow knows about. The catalog is GENERATED, not hand-written: an
// offline step (scripts/gen_connectors.py) asks a real Apache Airflow install for
// the exact connection-form metadata its UI renders (via the same
// HookMetaService the /ui/connections/hook_meta endpoint uses) plus the pip
// package each conn_type ships in, and embeds it as catalog.json. Three consumers
// read it so they never drift: the admin connection form (internal/api), the
// `connectors:` sugar expansion at compile, and compile-time dependency
// validation (ADR 0038). Regenerate by installing more providers and re-running
// the script — never edit catalog.json by hand.
package connectors

import (
	_ "embed"
	"encoding/json"
)

//go:embed catalog.json
var catalogJSON []byte

//go:embed catalog.overlay.json
var catalogOverlayJSON []byte

// Connector is one curated connector type, mirroring a catalog.json entry. The
// field metadata (StandardFields/ExtraFields) is the precise shape the Airflow
// 3.2 SPA renders via its FlexibleForm component, so the admin form can serve it
// verbatim.
type Connector struct {
	// ConnectionType is the Airflow conn_type, e.g. "postgres".
	ConnectionType string `json:"connection_type"`
	// HookName is the human label shown in the admin form, e.g. "Postgres".
	HookName string `json:"hook_name"`
	// HookClassName is the dotted Airflow hook class.
	HookClassName string `json:"hook_class_name"`
	// DefaultConnName is Airflow's conventional default connection id; empty for
	// core types that declare none.
	DefaultConnName string `json:"default_conn_name"`
	// PipPackage is the PyPI package that provides the hook, e.g.
	// "apache-airflow-providers-postgres". Empty for core conn types (generic,
	// email): they appear in the form but are not resolvable by the `connectors:`
	// sugar (nothing to install).
	PipPackage string `json:"pip_package"`
	// StandardFields is the per-field behavior for the built-in fields
	// (host/login/password/port/schema/description): hidden flag, relabel title,
	// and placeholder.
	StandardFields map[string]any `json:"standard_fields"`
	// ExtraFields is the provider-specific custom fields (the credential fields
	// that live in Connection.extra), each a FlexibleForm param spec.
	ExtraFields map[string]any `json:"extra_fields"`
	// Aliases are extra short names the `connectors:` sugar accepts on top of
	// ConnectionType. They are a Dexaflow overlay (see aliasOverlay), not part of
	// Airflow's metadata, and exist only to smooth Airflow's asymmetric conn_type
	// vocabulary (e.g. "gcp" for the verbose "google_cloud_platform").
	Aliases []string `json:"-"`
}

// aliasOverlay maps a conn_type to friendly `connectors:` aliases. Hand-curated:
// Airflow's own vocabulary is asymmetric ("aws" is terse, "google_cloud_platform"
// is verbose), so the sugar accepts a short alias that resolves to the same
// provider. The canonical conn_type always works; aliases never reach the form.
var aliasOverlay = map[string][]string{
	"google_cloud_platform": {"gcp", "google"},
}

// connectorOverlay is a leoflow-owned augmentation for one conn_type.
type connectorOverlay struct {
	StandardFields map[string]any `json:"standard_fields"`
	ExtraFields    map[string]any `json:"extra_fields"`
}

// fieldOverlay augments generated catalog entries with leoflow-owned form
// fields — the dbt-auth fields Airflow's provider introspection does not emit
// (BigQuery's leoflow-invented `method` selector, Snowflake's key passphrase,
// the whole Databricks form). It is loaded from catalog.overlay.json, which —
// unlike the generated catalog.json — is hand-curated and safe to edit. Keyed
// by conn_type; only StandardFields and ExtraFields are merged.
var fieldOverlay map[string]connectorOverlay

var catalog []Connector

func init() {
	if err := json.Unmarshal(catalogJSON, &catalog); err != nil {
		panic("connectors: parsing embedded catalog.json: " + err.Error())
	}
	// The overlay file carries a leading "_comment" key documenting itself; it
	// names no connector. Decode raw, drop it, then type each entry so the
	// self-documenting comment does not have to satisfy connectorOverlay.
	rawOverlay := map[string]json.RawMessage{}
	if err := json.Unmarshal(catalogOverlayJSON, &rawOverlay); err != nil {
		panic("connectors: parsing embedded catalog.overlay.json: " + err.Error())
	}
	delete(rawOverlay, "_comment")
	fieldOverlay = make(map[string]connectorOverlay, len(rawOverlay))
	for ct, raw := range rawOverlay {
		var ov connectorOverlay
		if err := json.Unmarshal(raw, &ov); err != nil {
			panic("connectors: parsing overlay for " + ct + ": " + err.Error())
		}
		fieldOverlay[ct] = ov
	}
	for i := range catalog {
		catalog[i].Aliases = aliasOverlay[catalog[i].ConnectionType]
		if ov, ok := fieldOverlay[catalog[i].ConnectionType]; ok {
			catalog[i].StandardFields = mergeFields(catalog[i].StandardFields, ov.StandardFields)
			catalog[i].ExtraFields = mergeFields(catalog[i].ExtraFields, ov.ExtraFields)
		}
	}
}

// mergeFields returns base with every key from overlay added or replaced. The
// overlay augments the generated fields (never wholesale-replaces the map), so
// the provider's own fields survive; a nil base (Airflow emitted none, e.g.
// databricks) is allocated. base is not mutated.
func mergeFields(base, overlay map[string]any) map[string]any {
	if len(overlay) == 0 {
		return base
	}
	out := make(map[string]any, len(base)+len(overlay))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overlay {
		out[k] = v
	}
	return out
}

// Catalog returns every connector entry, including core types with no pip package
// — the admin form's connection-type dropdown needs the full set.
func Catalog() []Connector { return catalog }

// PackageFor returns the pip package for a connector name — its canonical
// ConnectionType or any of its sugar Aliases (e.g. "gcp"). ok=false when the name
// is unknown OR maps to a core type with no package (nothing for `connectors:` to
// install), so the caller can fall back to dependencies:.
func PackageFor(name string) (pkg string, ok bool) {
	for _, c := range catalog {
		if c.PipPackage == "" {
			continue
		}
		if c.ConnectionType == name {
			return c.PipPackage, true
		}
		for _, a := range c.Aliases {
			if a == name {
				return c.PipPackage, true
			}
		}
	}
	return "", false
}

// Resolve expands `connectors:` short names into their pip packages, preserving
// order. Unknown names are returned separately so the caller can fail compile
// with an actionable message rather than letting a typo slip to a runtime
// ModuleNotFoundError. ADR 0038.
func Resolve(types []string) (packages, unknown []string) {
	for _, t := range types {
		if pkg, ok := PackageFor(t); ok {
			packages = append(packages, pkg)
		} else {
			unknown = append(unknown, t)
		}
	}
	return packages, unknown
}

// Types returns the sugar-resolvable connector names (those backed by a pip
// package), for building error messages ("known: postgres, mysql, …").
func Types() []string {
	out := make([]string, 0, len(catalog))
	for _, c := range catalog {
		if c.PipPackage != "" {
			out = append(out, c.ConnectionType)
		}
	}
	return out
}
