// Package ui embeds and serves the pinned Apache Airflow React SPA bundle
// (AIRFLOW_UI_VERSION in the Makefile, recorded in assets/VERSION). The
// control plane ships the UI in-binary (ADR 0017); the bundle is produced
// reproducibly by `make fetch-airflow-ui`, which extracts the dist directory
// from the apache/airflow:<version> image. The serving contract mirrors Airflow's
// own FastAPI app: static files under /static and an index.html SPA fallback
// for every other path, with the <base href> templated per request.
package ui

import (
	"embed"
	"io/fs"
	"strings"
)

// embeddedAssets holds the committed SPA bundle. The all: prefix includes
// dotfiles such as .vite/manifest.json. Before `make fetch-airflow-ui` runs it
// contains only a placeholder index.html and VERSION marker.
//
//go:embed all:assets
var embeddedAssets embed.FS

// Assets returns the embedded SPA bundle rooted at the bundle directory, so a
// request for "index.html" or "assets/index-*.js" maps directly to a file.
func Assets() fs.FS {
	sub, err := fs.Sub(embeddedAssets, "assets")
	if err != nil {
		// fs.Sub only fails on a malformed path, which "assets" is not; a
		// failure here is a build-time programming error, not a runtime one.
		panic("ui: embedded assets sub-filesystem: " + err.Error())
	}
	return sub
}

// Version returns the pinned upstream Airflow tag recorded in the bundle's
// VERSION marker, or "unknown" if it cannot be read.
func Version() string {
	data, err := fs.ReadFile(embeddedAssets, "assets/VERSION")
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(data))
}
