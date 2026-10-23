package config

// HelmSection is the Helm release a Pro control plane was installed by. The
// chart sets it so the not-ready reason and the boot log can name the exact
// command that migrates this release's database (ADR 0090 decision 2). It
// changes nothing else: an install without it gets the same hint with
// placeholders.
type HelmSection struct {
	// Release (DEXAFLOW_HELM_RELEASE) is the Helm release name. Empty means
	// the process was not installed by the chart.
	Release string `mapstructure:"release"`
	// Namespace (DEXAFLOW_HELM_NAMESPACE) is the release namespace.
	Namespace string `mapstructure:"namespace"`
	// ChartVersion (DEXAFLOW_HELM_CHART_VERSION) is the version of the chart
	// that rendered this pod, so the hint upgrades to the same chart and does
	// not pick up a newer one.
	ChartVersion string `mapstructure:"chart_version"`
	// MigrationsEnabled (DEXAFLOW_HELM_MIGRATIONS_ENABLED, default true) is the
	// chart's migrations.enabled. When false the chart runs no migration Job,
	// so `helm upgrade` would not migrate anything and the hint names the
	// operator's own migration step instead.
	MigrationsEnabled bool `mapstructure:"migrations_enabled"`
	// MigrateImage (DEXAFLOW_HELM_MIGRATE_IMAGE) is the dexaflow-migrate
	// image and tag of this release, named by the hint when
	// MigrationsEnabled is false.
	MigrateImage string `mapstructure:"migrate_image"`
}
