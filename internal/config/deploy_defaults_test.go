package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

// deploy.defaults (#1704): what the cluster tells `dexaflow deploy`. Unset by
// default, so an upgrade advertises nothing.

func TestDeployDefaultsUnsetByDefault(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if !c.Deploy.Defaults.IsEmpty() {
		t.Errorf("deploy.defaults = %+v, want empty", c.Deploy.Defaults)
	}
}

func TestDeployDefaultsEnvBinds(t *testing.T) {
	for _, prefix := range []string{"DEXAFLOW_", "LEOFLOW_"} {
		t.Run(prefix, func(t *testing.T) {
			t.Setenv(prefix+"DEPLOY_DEFAULTS_REGISTRY", "ghcr.io/acme")
			t.Setenv(prefix+"DEPLOY_DEFAULTS_PLATFORMS", "linux/amd64,linux/arm64")
			t.Setenv(prefix+"DEPLOY_DEFAULTS_BASE_IMAGE", "ghcr.io/acme/base:1")
			c, err := LoadServer("", nil)
			if err != nil {
				t.Fatalf("LoadServer: %v", err)
			}
			got := c.Deploy.Defaults
			if got.Registry != "ghcr.io/acme" || got.BaseImage != "ghcr.io/acme/base:1" ||
				!slices.Equal(got.Platforms, []string{"linux/amd64", "linux/arm64"}) {
				t.Errorf("deploy.defaults = %+v, want the values from %s variables", got, prefix)
			}
		})
	}
}

func TestDeployDefaultsFromConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.yaml")
	body := "deploy:\n  defaults:\n    registry: ghcr.io/acme\n    platforms: [linux/arm64]\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadServer(path, nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if c.Deploy.Defaults.Registry != "ghcr.io/acme" || !slices.Equal(c.Deploy.Defaults.Platforms, []string{"linux/arm64"}) {
		t.Errorf("deploy.defaults = %+v, want the file's values", c.Deploy.Defaults)
	}
}

// An invalid default fails at boot, naming the key, instead of reaching every
// deploy of the cluster.
func TestValidateDeployDefaults(t *testing.T) {
	cases := map[string]struct {
		d       domain.DeployDefaults
		wantErr string
	}{
		"unset":           {domain.DeployDefaults{}, ""},
		"all set":         {domain.DeployDefaults{Registry: "ghcr.io/acme", Platforms: []string{"linux/amd64"}, BaseImage: "python:3.12-slim"}, ""},
		"registry scheme": {domain.DeployDefaults{Registry: "https://ghcr.io/acme"}, "deploy.defaults.registry"},
		"bad platform":    {domain.DeployDefaults{Platforms: []string{"x86"}}, "deploy.defaults.platforms"},
		"bad base image":  {domain.DeployDefaults{BaseImage: "a b"}, "deploy.defaults.base_image"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := validWarmConfig()
			c.Deploy.Defaults.DeployDefaults = tc.d
			err := c.Validate()
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("Validate() = %v, want nil", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Errorf("Validate() = %v, want an error naming %q", err, tc.wantErr)
			}
		})
	}
}

// runtimeImage is a source-mode runtime image pinned by a full digest.
const runtimeImage = "ghcr.io/dexadata/dexaflow-runtime:0.5.3@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// TestDeployDefaultsSourceModeEnvBinds (#1772): deploy.defaults.source_mode
// is off by default and binds from both variable prefixes.
func TestDeployDefaultsSourceModeEnvBinds(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if c.Deploy.Defaults.SourceMode {
		t.Error("deploy.defaults.source_mode is on by default")
	}
	for _, prefix := range []string{"DEXAFLOW_", "LEOFLOW_"} {
		t.Run(prefix, func(t *testing.T) {
			t.Setenv(prefix+"DEPLOY_DEFAULTS_SOURCE_MODE", "true")
			c, err := LoadServer("", nil)
			if err != nil {
				t.Fatalf("LoadServer: %v", err)
			}
			if !c.Deploy.Defaults.SourceMode {
				t.Errorf("deploy.defaults.source_mode not bound from %sDEPLOY_DEFAULTS_SOURCE_MODE", prefix)
			}
		})
	}
}

// TestAdvertisedDeployDefaults: the cluster advertises its runtime image for
// source mode only when the operator asks for it and the mode is on, and the
// value is execution.source_mode.image itself, never a copy to keep in step.
func TestAdvertisedDeployDefaults(t *testing.T) {
	cases := map[string]struct {
		advertise, enabled bool
		want               string
	}{
		"advertised":          {advertise: true, enabled: true, want: runtimeImage},
		"not asked":           {advertise: false, enabled: true, want: ""},
		"asked, mode off":     {advertise: true, enabled: false, want: ""},
		"mode off, not asked": {advertise: false, enabled: false, want: ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := &ServerConfig{}
			c.Deploy.Defaults.Registry = "ghcr.io/acme"
			c.Deploy.Defaults.SourceMode = tc.advertise
			c.Execution.SourceMode = SourceModeSection{Enabled: tc.enabled, Image: runtimeImage}
			got := c.AdvertisedDeployDefaults()
			if got.SourceModeImage != tc.want || got.Registry != "ghcr.io/acme" {
				t.Errorf("AdvertisedDeployDefaults() = %+v, want source_mode_image %q and the registry kept", got, tc.want)
			}
		})
	}
}

// TestValidateDeployDefaultsSourceModeNeedsTheMode: advertising source mode
// while execution.source_mode is off would send every deploy to an image
// the server runs as a plain image, so it fails at boot.
func TestValidateDeployDefaultsSourceModeNeedsTheMode(t *testing.T) {
	c := validWarmConfig()
	c.Deploy.Defaults.SourceMode = true
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "deploy.defaults.source_mode") || !strings.Contains(err.Error(), "execution.source_mode.enabled") {
		t.Errorf("Validate() = %v, want an error naming deploy.defaults.source_mode and execution.source_mode.enabled", err)
	}
	c.Execution.SourceMode = SourceModeSection{Enabled: true, Image: runtimeImage}
	if err := c.Validate(); err != nil {
		t.Errorf("Validate() with the mode on = %v, want nil", err)
	}
}

// TestLoadServerRefusesASourceModeImage: the advertised runtime image is
// always execution.source_mode.image (deploy.defaults.source_mode turns it
// on), so a deploy.defaults.source_mode_image, which nothing reads, is
// refused at boot rather than ignored.
func TestLoadServerRefusesASourceModeImage(t *testing.T) {
	check := func(t *testing.T, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "deploy.defaults.source_mode_image") || !strings.Contains(err.Error(), "deploy.defaults.source_mode") ||
			!strings.Contains(err.Error(), "execution.source_mode.image") {
			t.Errorf("LoadServer() = %v, want a refusal naming deploy.defaults.source_mode_image and what to set instead", err)
		}
	}
	t.Run("file", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "server.yaml")
		if err := os.WriteFile(p, []byte("deploy:\n  defaults:\n    source_mode_image: "+runtimeImage+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := LoadServer(p, nil)
		check(t, err)
	})
	for _, prefix := range []string{"DEXAFLOW_", "LEOFLOW_"} {
		t.Run(prefix, func(t *testing.T) {
			t.Setenv(prefix+"DEPLOY_DEFAULTS_SOURCE_MODE_IMAGE", runtimeImage)
			_, err := LoadServer("", nil)
			check(t, err)
		})
	}
}

// TestDeployDefaultsBuilderServiceAccountAndWarmPoolsOffByDefault (#1890):
// an upgrade advertises no remote builder, task service account or warm
// pools.
func TestDeployDefaultsBuilderServiceAccountAndWarmPoolsOffByDefault(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	got := c.AdvertisedDeployDefaults()
	if got.RemoteBuilder != nil || got.TaskServiceAccount != "" || got.WarmPools != nil {
		t.Errorf("AdvertisedDeployDefaults() = %+v, want no remote builder, task service account or warm pools", got)
	}
	if !got.IsEmpty() {
		t.Errorf("AdvertisedDeployDefaults() = %+v, want empty", got)
	}
}

// TestDeployDefaultsBuilderServiceAccountAndWarmPoolsEnvBind (#1890): the
// remote builder and both switches bind from either variable prefix.
func TestDeployDefaultsBuilderServiceAccountAndWarmPoolsEnvBind(t *testing.T) {
	for _, prefix := range []string{"DEXAFLOW_", "LEOFLOW_"} {
		t.Run(prefix, func(t *testing.T) {
			t.Setenv(prefix+"DEPLOY_DEFAULTS_REMOTE_BUILDER_CLOUD_BUILD_PROJECT", "acme-ci")
			t.Setenv(prefix+"DEPLOY_DEFAULTS_REMOTE_BUILDER_CLOUD_BUILD_REGION", "us-central1")
			t.Setenv(prefix+"DEPLOY_DEFAULTS_REMOTE_BUILDER_CLOUD_BUILD_WORKER_POOL", "projects/acme-ci/locations/us-central1/workerPools/builds")
			t.Setenv(prefix+"DEPLOY_DEFAULTS_REMOTE_BUILDER_CLOUD_BUILD_SERVICE_ACCOUNT", "projects/acme-ci/serviceAccounts/builder@acme-ci.iam.gserviceaccount.com")
			t.Setenv(prefix+"DEPLOY_DEFAULTS_TASK_SERVICE_ACCOUNT", "true")
			t.Setenv(prefix+"DEPLOY_DEFAULTS_WARM_POOLS", "true")
			t.Setenv(prefix+"EXECUTOR_TASK_SERVICE_ACCOUNT", "dexaflow-task")
			c, err := LoadServer("", nil)
			if err != nil {
				t.Fatalf("LoadServer: %v", err)
			}
			got := c.AdvertisedDeployDefaults()
			want := domain.CloudBuildConfig{Project: "acme-ci", Region: "us-central1",
				WorkerPool:     "projects/acme-ci/locations/us-central1/workerPools/builds",
				ServiceAccount: "projects/acme-ci/serviceAccounts/builder@acme-ci.iam.gserviceaccount.com"}
			if got.RemoteBuilder == nil || got.RemoteBuilder.CloudBuild == nil || *got.RemoteBuilder.CloudBuild != want {
				t.Errorf("remote_builder = %+v, want %+v from the %s variables", got.RemoteBuilder, want, prefix)
			}
			if got.TaskServiceAccount != "dexaflow-task" {
				t.Errorf("task_service_account = %q, want executor.task_service_account", got.TaskServiceAccount)
			}
			if got.WarmPools == nil {
				t.Errorf("warm_pools not advertised with %sDEPLOY_DEFAULTS_WARM_POOLS", prefix)
			}
		})
	}
}

// TestDeployDefaultsRemoteBuilderFromConfigFile: the file form is the
// dotted key under deploy.defaults.
func TestDeployDefaultsRemoteBuilderFromConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.yaml")
	body := "deploy:\n  defaults:\n    remote_builder:\n      cloud_build: {project: acme-ci, region: europe-west1}\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadServer(path, nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	rb := c.AdvertisedDeployDefaults().RemoteBuilder
	if rb == nil || rb.CloudBuild == nil || rb.CloudBuild.Project != "acme-ci" || rb.CloudBuild.Region != "europe-west1" {
		t.Errorf("remote_builder = %+v, want the file's values", rb)
	}
}

// TestAdvertisedTaskServiceAccountAndWarmPools: the served values are the
// server's own executor.task_service_account and execution.* warm pool
// settings, and only when the operator turns each switch on.
func TestAdvertisedTaskServiceAccountAndWarmPools(t *testing.T) {
	c := validWarmConfig()
	c.Executor.TaskServiceAccount = "dexaflow-task"
	c.Execution.MinIdleWorkers = 1
	if got := c.AdvertisedDeployDefaults(); got.TaskServiceAccount != "" || got.WarmPools != nil {
		t.Errorf("switches off: AdvertisedDeployDefaults() = %+v, want neither advertised", got)
	}
	c.Deploy.Defaults.AdvertiseTaskServiceAccount = true
	c.Deploy.Defaults.AdvertiseWarmPools = true
	got := c.AdvertisedDeployDefaults()
	if got.TaskServiceAccount != "dexaflow-task" {
		t.Errorf("task_service_account = %q, want executor.task_service_account", got.TaskServiceAccount)
	}
	want := domain.WarmPoolDefaults{Enabled: true, MinIdleWorkers: 1, MaxPoolSize: 8}
	if got.WarmPools == nil || *got.WarmPools != want {
		t.Errorf("warm_pools = %+v, want %+v", got.WarmPools, want)
	}
	c.Execution.WarmPoolsEnabled = false
	if got := c.AdvertisedDeployDefaults().WarmPools; got == nil || got.Enabled {
		t.Errorf("warm pools off: warm_pools = %+v, want advertised as off", got)
	}
}

// TestValidateDeployDefaultsBuilderAndServiceAccount: an unusable remote
// builder, or a task service account switch with no account to serve, fails
// at boot, naming the key.
func TestValidateDeployDefaultsBuilderAndServiceAccount(t *testing.T) {
	c := validWarmConfig()
	c.Deploy.Defaults.RemoteBuilder.CloudBuild.Region = "us-central1"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "deploy.defaults.remote_builder.cloud_build.project") {
		t.Errorf("Validate() = %v, want an error naming deploy.defaults.remote_builder.cloud_build.project", err)
	}
	c.Deploy.Defaults.RemoteBuilder.CloudBuild.Project = "acme-ci"
	if err := c.Validate(); err != nil {
		t.Errorf("Validate() with a project = %v, want nil", err)
	}
	c.Deploy.Defaults.AdvertiseTaskServiceAccount = true
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "deploy.defaults.task_service_account") || !strings.Contains(err.Error(), "executor.task_service_account") {
		t.Errorf("Validate() = %v, want an error naming deploy.defaults.task_service_account and executor.task_service_account", err)
	}
	c.Executor.TaskServiceAccount = "dexaflow-task"
	if err := c.Validate(); err != nil {
		t.Errorf("Validate() with a task service account = %v, want nil", err)
	}
}

// TestDeployDefaultsBuildKitEnvBind (#1883): the in-cluster builder binds
// from either variable prefix and is served with its namespace and pod
// selector; the chart sets them when builder.enabled.
func TestDeployDefaultsBuildKitEnvBind(t *testing.T) {
	for _, prefix := range []string{"DEXAFLOW_", "LEOFLOW_"} {
		t.Run(prefix, func(t *testing.T) {
			t.Setenv(prefix+"DEPLOY_DEFAULTS_REMOTE_BUILDER_BUILDKIT_NAMESPACE", "dexaflow-builder")
			t.Setenv(prefix+"DEPLOY_DEFAULTS_REMOTE_BUILDER_BUILDKIT_POD_SELECTOR", "app.kubernetes.io/instance=df,app.kubernetes.io/component=builder")
			c, err := LoadServer("", nil)
			if err != nil {
				t.Fatalf("LoadServer: %v", err)
			}
			rb := c.AdvertisedDeployDefaults().RemoteBuilder
			want := domain.BuildKitConfig{Namespace: "dexaflow-builder", PodSelector: "app.kubernetes.io/instance=df,app.kubernetes.io/component=builder"}
			if rb == nil || rb.CloudBuild != nil || rb.BuildKit == nil || *rb.BuildKit != want {
				t.Errorf("remote_builder = %+v, want buildkit %+v from the %s variables", rb, want, prefix)
			}
		})
	}
}

// TestValidateDeployDefaultsBuildKit: a pod selector without a namespace, a
// bad namespace, and both backends at once fail at boot, naming the key.
func TestValidateDeployDefaultsBuildKit(t *testing.T) {
	c := validWarmConfig()
	c.Deploy.Defaults.RemoteBuilder.BuildKit.PodSelector = "app=buildkitd"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "deploy.defaults.remote_builder.buildkit.namespace") {
		t.Errorf("Validate() = %v, want an error naming deploy.defaults.remote_builder.buildkit.namespace", err)
	}
	c.Deploy.Defaults.RemoteBuilder.BuildKit.Namespace = "Builds"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "deploy.defaults.remote_builder.buildkit.namespace") {
		t.Errorf("Validate() = %v, want an error naming deploy.defaults.remote_builder.buildkit.namespace", err)
	}
	c.Deploy.Defaults.RemoteBuilder.BuildKit.Namespace = "dexaflow-builder"
	if err := c.Validate(); err != nil {
		t.Errorf("Validate() with a namespace = %v, want nil", err)
	}
	c.Deploy.Defaults.RemoteBuilder.CloudBuild.Project = "acme-ci"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "one backend") {
		t.Errorf("Validate() with both backends = %v, want an error saying one backend is set", err)
	}
}
