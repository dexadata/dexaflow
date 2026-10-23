package config

import (
	"errors"
	"fmt"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/envcompat"
	"github.com/spf13/viper"
)

// DeploySection is what the server tells `dexaflow deploy` (#1704).
type DeploySection struct {
	// Defaults (deploy.defaults.*) are the registry prefix, build platforms,
	// base image and remote builder a deploy to this cluster uses for
	// whatever the project, its workspace, its target and the person's config
	// leave unset, and the switches that advertise the source mode runtime
	// image, the task service account and the warm pools. Served on GET
	// /api/v2/deployDefaults; unset (the default) advertises nothing, so
	// every deploy resolves as it did before. None of them is a secret.
	Defaults DeployDefaultsSection `mapstructure:"defaults"`
	// RequireDigest (deploy.require_digest, #1703) is what registering a
	// version, or making one current again, does with an image not pinned by
	// a full sha256 digest: off (the default) accepts it, warn accepts it with
	// a warning, a log line and a count, enforce refuses it. DEXAFLOW_DEPLOY_REQUIRE_DIGEST (legacy
	// LEOFLOW_DEPLOY_REQUIRE_DIGEST).
	RequireDigest domain.DigestRequirement `mapstructure:"require_digest"`
}

// DeployDefaultsSection is deploy.defaults: the values served as they are,
// and the switch that adds the source mode runtime image to them.
type DeployDefaultsSection struct {
	domain.DeployDefaults `mapstructure:",squash"`
	// SourceMode (deploy.defaults.source_mode, default false) advertises
	// execution.source_mode.image as source_mode_image, so a deploy to this
	// cluster that names no runtime image of its own registers dag.py on it
	// with no build (#1772). It requires execution.source_mode.enabled.
	SourceMode bool `mapstructure:"source_mode"`
	// RemoteBuilder (deploy.defaults.remote_builder.cloud_build.*, #1890, or
	// deploy.defaults.remote_builder.buildkit.*, #1883) is the remote builder
	// a deploy to this cluster uses for builds no daemonless path covers,
	// when the project, its target and its workspace name none (ADR 0083).
	// Unset (the default) advertises none.
	RemoteBuilder RemoteBuilderSection `mapstructure:"remote_builder"`
	// AdvertiseTaskServiceAccount (deploy.defaults.task_service_account,
	// default false, #1890) serves executor.task_service_account as
	// task_service_account, so a deploy says which account its tasks run as
	// when they name none. It requires executor.task_service_account.
	AdvertiseTaskServiceAccount bool `mapstructure:"task_service_account"`
	// AdvertiseWarmPools (deploy.defaults.warm_pools, default false, #1890)
	// serves execution.warm_pools_enabled, execution.min_idle_workers and
	// execution.max_pool_size as warm_pools, so a deploy says how many idle
	// workers its DAG gets.
	AdvertiseWarmPools bool `mapstructure:"warm_pools"`
}

// RemoteBuilderSection is deploy.defaults.remote_builder: the backends of
// domain.RemoteBuilderConfig as server keys. At most one is set.
type RemoteBuilderSection struct {
	// CloudBuild is deploy.defaults.remote_builder.cloud_build.
	CloudBuild CloudBuildSection `mapstructure:"cloud_build"`
	// BuildKit is deploy.defaults.remote_builder.buildkit.
	BuildKit BuildKitSection `mapstructure:"buildkit"`
}

// BuildKitSection is deploy.defaults.remote_builder.buildkit, the in-cluster
// builder the chart runs (builder.enabled): the fields of
// domain.BuildKitConfig a server can know. The kubeconfig context is the
// client's own. Every field empty advertises none.
type BuildKitSection struct {
	// Namespace is the namespace of the builder.
	Namespace string `mapstructure:"namespace"`
	// PodSelector is the label selector of the builder pod.
	PodSelector string `mapstructure:"pod_selector"`
}

// CloudBuildSection is deploy.defaults.remote_builder.cloud_build, the
// fields of domain.CloudBuildConfig. Every field empty advertises none.
type CloudBuildSection struct {
	// Project is the Google Cloud project the build runs in.
	Project string `mapstructure:"project"`
	// Region is the Cloud Build region, such as us-central1.
	Region string `mapstructure:"region"`
	// WorkerPool is a private pool,
	// projects/<project>/locations/<region>/workerPools/<name>.
	WorkerPool string `mapstructure:"worker_pool"`
	// ServiceAccount is the account the build runs as,
	// projects/<project>/serviceAccounts/<email>.
	ServiceAccount string `mapstructure:"service_account"`
}

// sourceModeImageKey is the deploy.defaults key the server refuses: the
// runtime image it advertises is always execution.source_mode.image, turned
// on by deploy.defaults.source_mode.
const sourceModeImageKey = "deploy.defaults.source_mode_image"

// refuseAdvertisedSourceModeImage refuses deploy.defaults.source_mode_image,
// from the config file or from either variable prefix: nothing reads it
// (DeployDefaults.SourceModeImage is never decoded), and an operator who set
// it would believe the cluster advertises an image it does not.
func refuseAdvertisedSourceModeImage(v *viper.Viper) error {
	suffix := "DEPLOY_DEFAULTS_SOURCE_MODE_IMAGE"
	if err := v.BindEnv(sourceModeImageKey, envcompat.NewPrefix+suffix, envcompat.LegacyPrefix+suffix); err != nil {
		return fmt.Errorf("binding environment for %q: %w", sourceModeImageKey, err)
	}
	if !v.IsSet(sourceModeImageKey) {
		return nil
	}
	return fmt.Errorf("%s is not a setting: the cluster advertises its runtime image only as execution.source_mode.image, "+
		"so set deploy.defaults.source_mode: true (with execution.source_mode.enabled and execution.source_mode.image) instead", sourceModeImageKey)
}

// AdvertisedDeployDefaults are the deploy defaults GET /api/v2/deployDefaults
// serves: deploy.defaults, with the runtime image of source mode when
// deploy.defaults.source_mode asks for it and the mode is on, the remote
// builder when deploy.defaults.remote_builder sets one, and the task
// service account and warm pools of this server when their switches are on
// (#1890).
func (c *ServerConfig) AdvertisedDeployDefaults() domain.DeployDefaults {
	d := c.Deploy.Defaults.DeployDefaults
	d.SourceModeImage = ""
	if c.Deploy.Defaults.SourceMode {
		d.SourceModeImage = c.Execution.SourceMode.RuntimeImage()
	}
	d.RemoteBuilder = c.Deploy.Defaults.RemoteBuilder.config()
	d.TaskServiceAccount = ""
	if c.Deploy.Defaults.AdvertiseTaskServiceAccount {
		d.TaskServiceAccount = c.Executor.TaskServiceAccount
	}
	d.WarmPools = nil
	if c.Deploy.Defaults.AdvertiseWarmPools {
		w := c.Execution.WarmPoolDefaults()
		d.WarmPools = &w
	}
	return d
}

// config is the remote builder s configures, nil when it sets nothing.
// Both backends set is kept, so Validate refuses it.
func (s RemoteBuilderSection) config() *domain.RemoteBuilderConfig {
	cb, bk := s.CloudBuild, s.BuildKit
	if cb == (CloudBuildSection{}) && bk == (BuildKitSection{}) {
		return nil
	}
	out := &domain.RemoteBuilderConfig{}
	if cb != (CloudBuildSection{}) {
		out.CloudBuild = &domain.CloudBuildConfig{Project: cb.Project, Region: cb.Region, WorkerPool: cb.WorkerPool, ServiceAccount: cb.ServiceAccount}
	}
	if bk != (BuildKitSection{}) {
		out.BuildKit = &domain.BuildKitConfig{Namespace: bk.Namespace, PodSelector: bk.PodSelector}
	}
	return out
}

// validateDeployDefaults refuses a deploy.defaults value no deploy could
// use, so it fails at boot instead of in every deploy to the cluster.
func (c *ServerConfig) validateDeployDefaults() error {
	if c.Deploy.Defaults.SourceMode && !c.Execution.SourceMode.Enabled {
		return errors.New("deploy.defaults.source_mode advertises the source mode runtime image, but execution.source_mode.enabled is false; turn source mode on or leave deploy.defaults.source_mode off")
	}
	if c.Deploy.Defaults.AdvertiseTaskServiceAccount && c.Executor.TaskServiceAccount == "" {
		return errors.New("deploy.defaults.task_service_account advertises the task service account, but executor.task_service_account is empty; set it or leave deploy.defaults.task_service_account off")
	}
	d := c.AdvertisedDeployDefaults()
	// The source mode runtime image is execution.source_mode's, checked
	// with it; the rest is checked as a deploy will read it.
	d.SourceModeImage = ""
	return d.Validate("deploy.defaults")
}

// normalizeRequireDigest reads a YAML boolean deploy.require_digest as the
// chart does: a bare off in a config file is YAML for false, which means off,
// and true stays "true" (the weak decode would make it "1") so that Validate
// names the value the operator wrote.
func normalizeRequireDigest(v *viper.Viper, c *ServerConfig) {
	if b, ok := v.Get("deploy.require_digest").(bool); ok {
		c.Deploy.RequireDigest = domain.DigestRequireOff
		if b {
			c.Deploy.RequireDigest = "true"
		}
	}
}

// validateRequireDigest refuses a deploy.require_digest other than off, warn
// or enforce, so a typo fails at boot instead of registering unpinned images.
func (c *ServerConfig) validateRequireDigest() error {
	_, err := domain.ParseDigestRequirement(string(c.Deploy.RequireDigest))
	return err
}

// SourceModeSection is execution.source_mode (ADR 0067 §3).
type SourceModeSection struct {
	// Enabled turns source mode on. Default false: Pro ignores a version's source.
	// Bind via DEXAFLOW_EXECUTION_SOURCE_MODE_ENABLED (legacy
	// LEOFLOW_EXECUTION_SOURCE_MODE_ENABLED).
	Enabled bool `mapstructure:"enabled"`
	// Image is the runtime image, pinned by a full sha256 digest, that
	// source-mode versions name as their image. Bind via
	// DEXAFLOW_EXECUTION_SOURCE_MODE_IMAGE (legacy LEOFLOW_EXECUTION_SOURCE_MODE_IMAGE).
	Image string `mapstructure:"image"`
}

// RuntimeImage returns the runtime image when source mode is on, and "" when
// it is off, the form the dispatcher and the register check take.
func (s SourceModeSection) RuntimeImage() string {
	if !s.Enabled {
		return ""
	}
	return s.Image
}

// validateSourceMode requires a runtime image pinned by a full sha256 digest
// when source mode is on, so every source-mode task runs the exact image the
// operator vetted.
func (c *ServerConfig) validateSourceMode() error {
	m := c.Execution.SourceMode
	if !m.Enabled {
		return nil
	}
	if m.Image == "" {
		return errors.New("execution.source_mode.image is required when execution.source_mode.enabled (ADR 0067)")
	}
	if !domain.IsDigestPinned(m.Image) {
		return fmt.Errorf("execution.source_mode.image must be pinned by digest (image@sha256:<64 hex>), got %q (ADR 0067)", m.Image)
	}
	return nil
}
