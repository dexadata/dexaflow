package main

import (
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/storage"
)

// ADR 0090 decision 2: an absent or behind schema names the exact command for
// the install method the process runs under.
func TestRecoveryHintNamesTheCommandForTheInstallMethod(t *testing.T) {
	release := config.HelmSection{Release: "rel", Namespace: "data", ChartVersion: "0.5.6", MigrationsEnabled: true,
		MigrateImage: "ghcr.io/dexadata/dexaflow-migrate:0.5.6"}
	off := release
	off.MigrationsEnabled = false
	noImage := off
	noImage.MigrateImage = ""

	cases := []struct {
		name       string
		cfg        config.ServerConfig
		want       []string
		wantNoneOf []string
	}{
		{
			name:       "chart with migrations on",
			cfg:        config.ServerConfig{Helm: release},
			want:       []string{"`helm upgrade rel oci://ghcr.io/dexadata/charts/dexaflow -n data --version 0.5.6 --reuse-values` runs the migration Job again"},
			wantNoneOf: []string{"<release>", "dexaflow db migrate", "kubectl create job"},
		},
		{
			name:       "chart with migrations off",
			cfg:        config.ServerConfig{Helm: off},
			want:       []string{"migrations.enabled is false", "own migration step", "ghcr.io/dexadata/dexaflow-migrate:0.5.6"},
			wantNoneOf: []string{"helm upgrade", "dexaflow db migrate"},
		},
		{
			name: "chart with migrations off and no image passed",
			cfg:  config.ServerConfig{Helm: noImage},
			want: []string{"ghcr.io/dexadata/dexaflow-migrate:<this release>"},
		},
		{
			name: "a release that passed only its name",
			cfg:  config.ServerConfig{Helm: config.HelmSection{Release: "rel", MigrationsEnabled: true}},
			want: []string{"`helm upgrade rel oci://ghcr.io/dexadata/charts/dexaflow -n <namespace> --version <chart version> --reuse-values`"},
		},
		{
			name:       "Lite",
			cfg:        config.ServerConfig{UI: config.UISection{Edition: "lite"}},
			want:       []string{"`dexaflow db migrate`", "`dexaflow lite`"},
			wantNoneOf: []string{"helm upgrade"},
		},
		{
			name: "neither",
			cfg:  config.ServerConfig{},
			want: []string{"`helm upgrade <release> oci://ghcr.io/dexadata/charts/dexaflow -n <namespace> --version <chart version> --reuse-values`", "`dexaflow db migrate`"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newRecoveryHint(&tc.cfg)
			for _, class := range []string{storage.SchemaReasonAbsent, storage.SchemaReasonBehind} {
				got := h.forClass(class)
				for _, w := range tc.want {
					if !strings.Contains(got, w) {
						t.Errorf("%s: hint %q does not contain %q", class, got, w)
					}
				}
				for _, n := range tc.wantNoneOf {
					if strings.Contains(got, n) {
						t.Errorf("%s: hint %q contains %q", class, got, n)
					}
				}
			}
		})
	}
}

// The other reasons do not depend on the install method: migrating fixes
// none of them.
func TestRecoveryHintOtherReasonsNameNoMigration(t *testing.T) {
	h := newRecoveryHint(&config.ServerConfig{Helm: config.HelmSection{Release: "rel", MigrationsEnabled: true}})
	for _, class := range []string{storage.SchemaReasonDirty, storage.SchemaReasonUnreadable, storage.SchemaReasonReplaced} {
		if got := h.forClass(class); strings.Contains(got, "helm upgrade") {
			t.Errorf("%s: hint %q names helm upgrade", class, got)
		}
	}
	if got := h.forClass(storage.SchemaReasonReplaced); !strings.Contains(got, "installation.id") {
		t.Errorf("replaced: hint %q does not name installation.id", got)
	}
}
