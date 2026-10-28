package domain

import (
	"fmt"
	"regexp"
	"strings"
)

// DeployDefaults are the build and push settings a cluster advertises to
// `dexaflow deploy` (#1704): what a project, its workspace and its target
// leave unset is taken from here, so a platform team states its registry
// once instead of in every repository. The task service account and the
// warm pools (#1890) are only reported by a deploy. The operator sets them
// in the server's deploy.defaults; GET /api/v2/deployDefaults serves them.
// None is a secret: registry credentials never go here.
type DeployDefaults struct {
	// Registry is the image prefix a project without registry.url pushes
	// to, such as ghcr.io/acme. The image name defaults to the dag_id.
	Registry string `json:"registry" mapstructure:"registry"`
	// Platforms replaces build.platforms for projects that set none.
	Platforms []string `json:"platforms" mapstructure:"platforms"`
	// BaseImage replaces base_image for projects that set none.
	BaseImage string `json:"base_image" mapstructure:"base_image"`
	// SourceModeImage is the runtime image a deploy registers a DAG's
	// dag.py on, with no build, when nothing local names one (source mode,
	// ADR 0067, #1772). The server fills it from execution.source_mode.image
	// when deploy.defaults.source_mode is on; it is never set by hand, so
	// it cannot drift from the image the server runs.
	SourceModeImage string `json:"source_mode_image" mapstructure:"-"`
	// RemoteBuilder replaces build.remote_builder for projects, targets and
	// workspaces that set none (ADR 0083, #1890); nil advertises none. The
	// server fills it from deploy.defaults.remote_builder.
	RemoteBuilder *RemoteBuilderConfig `json:"remote_builder" mapstructure:"-"`
	// TaskServiceAccount is the ServiceAccount task pods run as when a task
	// sets no execution.service_account (#1890). The server fills it from
	// executor.task_service_account when deploy.defaults.task_service_account
	// is on, so it cannot drift from the account pods get; a deploy reports
	// it and never writes it into dag.json.
	TaskServiceAccount string `json:"task_service_account" mapstructure:"-"`
	// WarmPools is what the cluster does with a DAG's min_idle_workers
	// (ADR 0058, ADR 0082, #1890); nil advertises nothing. The server fills
	// it from execution.* when deploy.defaults.warm_pools is on.
	WarmPools *WarmPoolDefaults `json:"warm_pools" mapstructure:"-"`
	// CIOIDC lists the CI OIDC issuers of the caller's tenant (ADR 0092),
	// so `dexaflow ci init` writes a pipeline that deploys without a stored
	// token; nil when the tenant has none. Unlike the fields above it
	// depends on the caller: the server fills it per request, and it is no
	// deploy setting, so IsEmpty and Validate ignore it.
	CIOIDC *CIOIDCDefaults `json:"ci_oidc" mapstructure:"-"`
}

// CIOIDCDefaults are the CI OIDC issuers a deployDefaults answer lists
// (ADR 0092).
type CIOIDCDefaults struct {
	// Issuers are the entries of the caller's tenant, in configuration order.
	Issuers []CIOIDCIssuer `json:"issuers"`
}

// CIOIDCIssuer is one CI OIDC issuer as `dexaflow ci init` needs it: which
// provider it is and the audience a pipeline asks its token for. It carries
// no rule and no tenant.
type CIOIDCIssuer struct {
	// Name is the entry's name in the server configuration.
	Name string `json:"name"`
	// Provider is github, gitlab, cloudbuild or other.
	Provider string `json:"provider"`
	// Issuer is the exact iss of the provider's tokens.
	Issuer string `json:"issuer"`
	// Audience is the aud the pipeline asks its token for.
	Audience string `json:"audience"`
}

// WarmPoolDefaults are the warm pool settings of a cluster, as a deploy
// reads them (#1890): a DAG that declares no min_idle_workers gets
// MinIdleWorkers, and every value is capped at MaxPoolSize, while warm pools
// are Enabled. EffectiveMinIdle resolves them for one version.
type WarmPoolDefaults struct {
	// Enabled is execution.warm_pools_enabled: off, no worker is kept warm
	// whatever a DAG declares.
	Enabled bool `json:"enabled"`
	// MinIdleWorkers is execution.min_idle_workers, the idle warm workers
	// kept for a version whose DAG declares none.
	MinIdleWorkers int `json:"min_idle_workers"`
	// MaxPoolSize is execution.max_pool_size, the cap on one version's pool;
	// 0 sets none.
	MaxPoolSize int `json:"max_pool_size"`
}

// platformPattern is the os/arch[/variant] form a platform takes, the one
// build.ParsePlatforms accepts (build.PlatformPattern; a test keeps the two
// equal, as domain cannot import build).
var platformPattern = regexp.MustCompile(`^[A-Za-z0-9]+/[A-Za-z0-9_]+(/[A-Za-z0-9]+)?$`)

// serviceAccountNamePattern is a Kubernetes ServiceAccount name: a DNS
// subdomain (RFC 1123), lowercase, of at most maxServiceAccountName bytes.
var serviceAccountNamePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)

// maxServiceAccountName is the longest name Kubernetes accepts.
const maxServiceAccountName = 253

// IsEmpty reports whether d sets no deploy setting (CIOIDC is not one).
func (d DeployDefaults) IsEmpty() bool {
	return d.Registry == "" && len(d.Platforms) == 0 && d.BaseImage == "" && d.SourceModeImage == "" &&
		d.RemoteBuilder == nil && d.TaskServiceAccount == "" && d.WarmPools == nil
}

// Validate refuses a value a deploy could not use, naming the field under
// prefix (deploy.defaults on the server). An empty field is valid: it
// leaves the setting to the project and the built-in default.
func (d DeployDefaults) Validate(prefix string) error {
	if err := CheckRegistryPrefix(prefix+".registry", d.Registry); err != nil {
		return err
	}
	for _, p := range d.Platforms {
		if !platformPattern.MatchString(p) {
			return fmt.Errorf("%s.platforms: invalid platform %q: want os/arch or os/arch/variant, such as linux/amd64 or linux/arm64/v8", prefix, p)
		}
	}
	if d.BaseImage != "" {
		if err := CheckImageReference(prefix+".base_image", d.BaseImage); err != nil {
			return err
		}
	}
	if d.SourceModeImage != "" && !IsDigestPinned(d.SourceModeImage) {
		return fmt.Errorf("%s.source_mode_image %q is not pinned by a full digest (image@sha256:<64 hex>), the only form source mode runs (ADR 0067)", prefix, d.SourceModeImage)
	}
	if err := d.RemoteBuilder.Validate(prefix + ".remote_builder"); err != nil {
		return err
	}
	if sa := d.TaskServiceAccount; sa != "" && (len(sa) > maxServiceAccountName || !serviceAccountNamePattern.MatchString(sa)) {
		return fmt.Errorf("%s.task_service_account %q is not a Kubernetes ServiceAccount name: lowercase letters, digits, '-' and '.', at most %d characters", prefix, sa, maxServiceAccountName)
	}
	return d.WarmPools.validate(prefix + ".warm_pools")
}

// validate refuses a negative count, naming the field under prefix. Nil is
// valid: nothing is advertised.
func (w *WarmPoolDefaults) validate(prefix string) error {
	switch {
	case w == nil:
		return nil
	case w.MinIdleWorkers < 0:
		return fmt.Errorf("%s.min_idle_workers is %d; want 0 or more", prefix, w.MinIdleWorkers)
	case w.MaxPoolSize < 0:
		return fmt.Errorf("%s.max_pool_size is %d; want 0 or more", prefix, w.MaxPoolSize)
	}
	return nil
}

// EffectiveMinIdle resolves the warm-worker target for one dag_version under
// model A2 (ADR 0058 N1b2b): the DAG author declares desired warmth per DAG
// (dagMinIdle), the operator caps and floors it.
//
//   - Warm pools OFF (or w nil) => always 0. This is what makes a default
//     deploy a byte-for-byte no-op: with warmth gated off no warm pod is ever
//     targeted, so the reconciler (when it runs at all) reconciles every pool
//     to zero.
//   - The DAG author's value wins when set (> 0); when the DAG declares none (0)
//     it falls back to the operator's execution.min_idle_workers floor.
//   - The resolved value is clamped to [0, max_pool_size] so an author can never
//     provision more warmth than the operator's per-version cap allows, and a
//     nonsensical negative never underflows.
func (w *WarmPoolDefaults) EffectiveMinIdle(dagMinIdle int) int {
	if w == nil || !w.Enabled {
		return 0
	}
	target := dagMinIdle
	if target == 0 {
		// The author declared no warmth; inherit the operator's floor.
		target = w.MinIdleWorkers
	}
	if target < 0 {
		target = 0
	}
	if w.MaxPoolSize > 0 && target > w.MaxPoolSize {
		target = w.MaxPoolSize
	}
	return target
}

// CheckRegistryPrefix refuses a registry prefix that cannot start an image
// reference: a scheme, credentials or a
// digest (`@`), a tag on its last path segment, capitals, a trailing slash or
// anything CheckImageReference refuses. A host alone, with or without a port,
// is valid. Empty is valid: no prefix is set.
func CheckRegistryPrefix(field, v string) error {
	if v == "" {
		return nil
	}
	if err := CheckImageReference(field, v); err != nil {
		return err
	}
	switch {
	case strings.Contains(v, "://"):
		return fmt.Errorf("%s %q has a scheme; give the registry prefix without it, such as ghcr.io/acme", field, v)
	case strings.Contains(v, "@"):
		return fmt.Errorf("%s %q contains '@': a registry prefix carries no credentials and no digest", field, v)
	case strings.HasSuffix(v, "/"):
		return fmt.Errorf("%s %q ends with '/'; remove it", field, v)
	case strings.ToLower(v) != v:
		return fmt.Errorf("%s %q has capitals; image references are lowercase", field, v)
	}
	if i := strings.LastIndex(v, "/"); i >= 0 && strings.Contains(v[i+1:], ":") {
		return fmt.Errorf("%s %q ends with a tag; give the prefix only, the deploy adds the image name and tag", field, v)
	}
	return nil
}
