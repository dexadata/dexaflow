package config

import "testing"

// The helm.* keys are empty unless the chart sets them, migrations_enabled
// defaults to the chart's own default, and every key binds from the LEOFLOW_
// variables the chart renders (ADR 0090 decision 2).
func TestHelmReleaseDefaultsAndBinds(t *testing.T) {
	c := validServerConfig(t)
	if c.Helm != (HelmSection{MigrationsEnabled: true}) {
		t.Fatalf("helm defaults = %+v, want empty with migrations_enabled true", c.Helm)
	}
	t.Setenv("LEOFLOW_HELM_RELEASE", "rel")
	t.Setenv("LEOFLOW_HELM_NAMESPACE", "ns")
	t.Setenv("LEOFLOW_HELM_CHART_VERSION", "0.5.6")
	t.Setenv("LEOFLOW_HELM_MIGRATIONS_ENABLED", "false")
	t.Setenv("DEXAFLOW_HELM_MIGRATE_IMAGE", "ghcr.io/dexadata/dexaflow-migrate:0.5.6")
	c = validServerConfig(t)
	want := HelmSection{Release: "rel", Namespace: "ns", ChartVersion: "0.5.6", MigrateImage: "ghcr.io/dexadata/dexaflow-migrate:0.5.6"}
	if c.Helm != want {
		t.Fatalf("helm = %+v, want %+v", c.Helm, want)
	}
}
