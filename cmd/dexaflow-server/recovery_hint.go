package main

import (
	"strings"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/storage"
)

// chartRef is where the chart is published; the hint upgrades from it.
const chartRef = "oci://ghcr.io/dexadata/charts/dexaflow"

// recoveryDocs is the operate page for a database that was lost, restored or
// repointed, which the replaced reason points at.
const recoveryDocs = "https://dexaflow.dexadata.ai/operate/database-lost-restored-repointed/"

// recoveryHint names what to run about a schema this process cannot serve,
// for the install method it runs under (ADR 0090 decision 2). The chart passes
// the release in helm.*; `dexaflow lite` marks its server with ui.edition. A
// process that is neither gets the command with placeholders. It names no Job
// to copy with `kubectl create job --from`, since the hook Job is deleted once
// it succeeds.
type recoveryHint struct {
	lite bool
	helm config.HelmSection
}

func newRecoveryHint(cfg *config.ServerConfig) recoveryHint {
	return recoveryHint{lite: cfg.UI.Edition == "lite", helm: cfg.Helm}
}

// forClass is the action for one of the storage.SchemaReason classes.
func (h recoveryHint) forClass(class string) string {
	switch class {
	case storage.SchemaReasonDirty:
		return "a migration may be in flight; if none is running, follow the interrupted-migration steps of the upgrade guide"
	case storage.SchemaReasonUnreadable:
		return "check that the database is reachable with database.url; the server checks again on its own"
	case storage.SchemaReasonReplaced:
		// Migrating does not fix it: the migration Job refuses this database
		// too, and the server must never write to it unacknowledged.
		return "this is not the database this release was installed on (emptied, restored from another installation, or database.url changed); " +
			"restore the right database or point database.url back at it, or accept this one by setting installation.id " +
			"(see " + recoveryDocs + "); the server checks again on its own"
	default:
		return "apply the migrations: " + h.migrate() + "; the server checks again on its own"
	}
}

// migrate is the command that brings this install's schema current.
func (h recoveryHint) migrate() string {
	switch {
	case h.lite:
		return "run `dexaflow db migrate`, or restart `dexaflow lite`, which applies them before it starts the server"
	case h.helm.Release == "":
		return "on Helm, `" + h.helmUpgrade() + "` runs the migration Job again; on Lite, `dexaflow db migrate`"
	case !h.helm.MigrationsEnabled:
		image := h.helm.MigrateImage
		if image == "" {
			image = "ghcr.io/dexadata/dexaflow-migrate:<this release>"
		}
		return "migrations.enabled is false, so this release runs no migration Job: run your own migration step, `up` with the " +
			image + " image against this database"
	default:
		return "`" + h.helmUpgrade() + "` runs the migration Job again"
	}
}

// helmUpgrade is the upgrade that reruns the hook with the values the release
// has, on the chart version that rendered this pod. A value the chart did not
// pass stays a placeholder.
func (h recoveryHint) helmUpgrade() string {
	return strings.Join([]string{
		"helm upgrade", orPlaceholder(h.helm.Release, "<release>"), chartRef,
		"-n", orPlaceholder(h.helm.Namespace, "<namespace>"),
		"--version", orPlaceholder(h.helm.ChartVersion, "<chart version>"),
		"--reuse-values",
	}, " ")
}

func orPlaceholder(v, placeholder string) string {
	if v == "" {
		return placeholder
	}
	return v
}
