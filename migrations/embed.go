// Package migrations embeds the SQL migration files so the control plane can
// apply them without the source tree present — a step toward a binaries-only
// `dexaflow lite` (no checked-out repo required). See issue #60.
package migrations

import "embed"

// Files holds the embedded up/down migration SQL, loadable by golang-migrate's
// iofs source.
//
//go:embed *.sql
var Files embed.FS
