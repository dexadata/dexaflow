package domain

import (
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/build"
)

func TestDeployDefaultsValidateAcceptsUsableValues(t *testing.T) {
	cases := []DeployDefaults{
		{},
		{Registry: "ghcr.io/acme"},
		{Registry: "localhost:5000"},
		{Registry: "localhost:5000/team/dags"},
		{Registry: "123456789012.dkr.ecr.us-east-1.amazonaws.com/dags"},
		{Platforms: []string{"linux/amd64", "linux/arm64/v8"}},
		{BaseImage: "ghcr.io/acme/base:1"},
		{BaseImage: "python:3.12-slim@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
	}
	for _, d := range cases {
		if err := d.Validate("deploy.defaults"); err != nil {
			t.Errorf("Validate(%+v) = %v, want nil", d, err)
		}
	}
}

func TestDeployDefaultsValidateRefusesWhatABuildCannotUse(t *testing.T) {
	cases := []struct {
		name  string
		d     DeployDefaults
		field string
	}{
		{"scheme", DeployDefaults{Registry: "https://ghcr.io/acme"}, "deploy.defaults.registry"},
		{"credentials", DeployDefaults{Registry: "bob:secret@ghcr.io/acme"}, "deploy.defaults.registry"},
		{"tag", DeployDefaults{Registry: "ghcr.io/acme:1"}, "deploy.defaults.registry"},
		{"uppercase", DeployDefaults{Registry: "ghcr.io/Acme"}, "deploy.defaults.registry"},
		{"trailing slash", DeployDefaults{Registry: "ghcr.io/acme/"}, "deploy.defaults.registry"},
		{"whitespace", DeployDefaults{Registry: "ghcr.io/ac me"}, "deploy.defaults.registry"},
		{"platform form", DeployDefaults{Platforms: []string{"amd64"}}, "deploy.defaults.platforms"},
		{"empty platform", DeployDefaults{Platforms: []string{""}}, "deploy.defaults.platforms"},
		{"base image newline", DeployDefaults{BaseImage: "python:3.12\nRUN id"}, "deploy.defaults.base_image"},
		{"base image lexer", DeployDefaults{BaseImage: "python:$TAG"}, "deploy.defaults.base_image"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.d.Validate("deploy.defaults")
			if err == nil {
				t.Fatalf("Validate(%+v) = nil, want an error", tc.d)
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Errorf("error %q does not name %s", err, tc.field)
			}
		})
	}
}

func TestDeployDefaultsIsEmpty(t *testing.T) {
	if !(DeployDefaults{}).IsEmpty() {
		t.Error("zero value is not empty")
	}
	for _, d := range []DeployDefaults{{Registry: "r"}, {Platforms: []string{"linux/amd64"}}, {BaseImage: "b"}} {
		if d.IsEmpty() {
			t.Errorf("%+v reported empty", d)
		}
	}
}

// TestPlatformPatternMatchesTheBuilder: the server refuses at boot exactly the
// platforms a build would refuse, so the two expressions stay equal.
func TestPlatformPatternMatchesTheBuilder(t *testing.T) {
	if got, want := platformPattern.String(), build.PlatformPattern; got != want {
		t.Errorf("domain platformPattern = %q, build.PlatformPattern = %q; keep them equal", got, want)
	}
}

// pinnedRuntime is a runtime image pinned by a full digest, the only form
// source mode accepts (ADR 0067).
const pinnedRuntime = "ghcr.io/dexadata/dexaflow-runtime:0.5.3@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// TestDeployDefaultsSourceModeImage (#1772): the runtime image a cluster
// advertises for source mode counts as a default, and only a full digest
// pin is usable, as the server's own execution.source_mode.image.
func TestDeployDefaultsSourceModeImage(t *testing.T) {
	d := DeployDefaults{SourceModeImage: pinnedRuntime}
	if d.IsEmpty() {
		t.Error("defaults with only a source mode image reported empty")
	}
	if err := d.Validate("deploy.defaults"); err != nil {
		t.Errorf("Validate(pinned) = %v, want nil", err)
	}
	for _, bad := range []string{"ghcr.io/dexadata/dexaflow-runtime:0.5.3", "ghcr.io/dexadata/dexaflow-runtime@sha256:0123"} {
		err := DeployDefaults{SourceModeImage: bad}.Validate("deploy.defaults")
		if err == nil || !strings.Contains(err.Error(), "deploy.defaults.source_mode_image") {
			t.Errorf("Validate(%q) = %v, want an error naming deploy.defaults.source_mode_image", bad, err)
		}
	}
}

// TestDeployDefaultsBuilderServiceAccountAndWarmPools (#1890): the remote
// builder, the task service account and the warm pools a cluster advertises
// each count as a default, and a value no deploy could use is refused,
// naming its field.
func TestDeployDefaultsBuilderServiceAccountAndWarmPools(t *testing.T) {
	usable := []DeployDefaults{
		{RemoteBuilder: &RemoteBuilderConfig{CloudBuild: &CloudBuildConfig{Project: "acme-ci", Region: "us-central1"}}},
		{TaskServiceAccount: "dexaflow-task"},
		{TaskServiceAccount: "team.tasks-1"},
		{WarmPools: &WarmPoolDefaults{}},
		{WarmPools: &WarmPoolDefaults{Enabled: true, MinIdleWorkers: 2, MaxPoolSize: 8}},
	}
	for _, d := range usable {
		if d.IsEmpty() {
			t.Errorf("%+v reported empty", d)
		}
		if err := d.Validate("deploy.defaults"); err != nil {
			t.Errorf("Validate(%+v) = %v, want nil", d, err)
		}
	}
	refused := []struct {
		name  string
		d     DeployDefaults
		field string
	}{
		{"builder without backend", DeployDefaults{RemoteBuilder: &RemoteBuilderConfig{}}, "deploy.defaults.remote_builder"},
		{"builder without project", DeployDefaults{RemoteBuilder: &RemoteBuilderConfig{CloudBuild: &CloudBuildConfig{Region: "us-central1"}}}, "deploy.defaults.remote_builder.cloud_build.project"},
		{"builder bad project", DeployDefaults{RemoteBuilder: &RemoteBuilderConfig{CloudBuild: &CloudBuildConfig{Project: "X"}}}, "deploy.defaults.remote_builder.cloud_build.project"},
		{"service account capitals", DeployDefaults{TaskServiceAccount: "Tasks"}, "deploy.defaults.task_service_account"},
		{"service account control character", DeployDefaults{TaskServiceAccount: "tasks\x1b[2J"}, "deploy.defaults.task_service_account"},
		{"service account too long", DeployDefaults{TaskServiceAccount: strings.Repeat("a", 254)}, "deploy.defaults.task_service_account"},
		{"negative idle workers", DeployDefaults{WarmPools: &WarmPoolDefaults{Enabled: true, MinIdleWorkers: -1}}, "deploy.defaults.warm_pools.min_idle_workers"},
		{"negative pool size", DeployDefaults{WarmPools: &WarmPoolDefaults{Enabled: true, MaxPoolSize: -1}}, "deploy.defaults.warm_pools.max_pool_size"},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.d.Validate("deploy.defaults")
			if err == nil || !strings.Contains(err.Error(), tc.field) {
				t.Errorf("Validate(%+v) = %v, want an error naming %s", tc.d, err, tc.field)
			}
		})
	}
}

// TestWarmPoolDefaultsEffectiveMinIdleNil: nil settings keep no worker warm,
// whatever the DAG declares.
func TestWarmPoolDefaultsEffectiveMinIdleNil(t *testing.T) {
	var w *WarmPoolDefaults
	if got := w.EffectiveMinIdle(3); got != 0 {
		t.Fatalf("nil EffectiveMinIdle(3) = %d, want 0", got)
	}
}
