package domain

import (
	"fmt"
	"regexp"
	"strings"
)

// RemoteBuilderConfig is build.remote_builder in dexaflow.yaml, and
// remote_builder in a workspace file's defaults and targets: the remote
// builder that builds the DAG image when a real build is needed (path 4 of
// ADR 0073, ADR 0083). One backend is set.
type RemoteBuilderConfig struct {
	// CloudBuild builds on Google Cloud Build through the gcloud CLI.
	CloudBuild *CloudBuildConfig `json:"cloud_build,omitempty" yaml:"cloud_build,omitempty"`
	// BuildKit builds on the in-cluster BuildKit daemon the Helm chart runs
	// (builder.enabled), through buildctl and kubectl.
	BuildKit *BuildKitConfig `json:"buildkit,omitempty" yaml:"buildkit,omitempty"`
}

// DefaultBuildKitPodSelector selects the pod of the chart's builder when
// BuildKitConfig.PodSelector is empty.
const DefaultBuildKitPodSelector = "app.kubernetes.io/component=builder"

// BuildKitConfig is the in-cluster BuildKit backend of the remote builder.
// The CLI finds a ready pod of the daemon with kubectl and builds on it with
// `buildctl --addr kube-pod://<pod>`, over kubectl exec, as the person who
// deploys: the Role the chart binds to builder.subjects decides who may.
type BuildKitConfig struct {
	// Namespace is the namespace of the builder, the chart's
	// builder.namespace. Required.
	Namespace string `json:"namespace,omitempty" yaml:"namespace,omitempty"`
	// PodSelector is the label selector of the builder pod, in equality form
	// (key=value, comma separated); DefaultBuildKitPodSelector when empty.
	PodSelector string `json:"pod_selector,omitempty" yaml:"pod_selector,omitempty"`
	// Context is the kubeconfig context that reaches the builder's cluster;
	// kubectl's current context when empty. Only a local file sets it: a
	// server cannot know the names of a client's contexts.
	Context string `json:"context,omitempty" yaml:"context,omitempty"`
}

// Selector returns the label selector of the builder pod.
func (b BuildKitConfig) Selector() string {
	if b.PodSelector == "" {
		return DefaultBuildKitPodSelector
	}
	return b.PodSelector
}

// CloudBuildConfig is the Cloud Build backend of the remote builder. Every
// value reaches gcloud as a --flag=value argument.
type CloudBuildConfig struct {
	// Project is the Google Cloud project the build runs in. Required.
	Project string `json:"project,omitempty" yaml:"project,omitempty"`
	// Region is the Cloud Build region, such as us-central1; gcloud's
	// builds/region, else global, when empty.
	Region string `json:"region,omitempty" yaml:"region,omitempty"`
	// WorkerPool is a private pool,
	// projects/<project>/locations/<region>/workerPools/<name>.
	WorkerPool string `json:"worker_pool,omitempty" yaml:"worker_pool,omitempty"`
	// ServiceAccount is the account the build runs as,
	// projects/<project>/serviceAccounts/<email>.
	ServiceAccount string `json:"service_account,omitempty" yaml:"service_account,omitempty"`
}

var (
	// gcpProject is a Google Cloud project id, optionally domain scoped
	// (example.com:my-project).
	gcpProject = regexp.MustCompile(`^([a-z0-9][a-z0-9.-]*[a-z0-9]:)?[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
	// gcpRegion is a region name, such as us-central1 or global.
	gcpRegion = regexp.MustCompile(`^[a-z][a-z0-9-]*[a-z0-9]$`)
	// workerPool is the resource name of a Cloud Build private pool.
	workerPool = regexp.MustCompile(`^projects/[^/\s]+/locations/[a-z][a-z0-9-]*/workerPools/[A-Za-z0-9_-]+$`)
	// serviceAccount is the resource name of a service account, the form
	// Cloud Build takes.
	serviceAccount = regexp.MustCompile(`^projects/[^/\s]+/serviceAccounts/[^/\s@]+@[^/\s@]+$`)
	// namespaceName is a Kubernetes namespace name (an RFC 1123 label).
	namespaceName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
	// equalitySelector is a label selector of key=value requirements, comma
	// separated, the form the chart's labels are matched with.
	equalitySelector = regexp.MustCompile(`^[A-Za-z0-9][-A-Za-z0-9_./]*=[-A-Za-z0-9_.]*(,[A-Za-z0-9][-A-Za-z0-9_./]*=[-A-Za-z0-9_.]*)*$`)
	// kubeContext is a kubeconfig context name kubectl cannot read as a flag:
	// printable, no space, not starting with '-'.
	kubeContext = regexp.MustCompile(`^[^- \x00-\x1f\x7f][^ \x00-\x1f\x7f]{0,252}$`)
)

// Validate refuses a remote builder a build could not use, naming the field
// under prefix (build.remote_builder in dexaflow.yaml).
func (r *RemoteBuilderConfig) Validate(prefix string) error {
	if r == nil {
		return nil
	}
	switch {
	case r.CloudBuild != nil && r.BuildKit != nil:
		return fmt.Errorf("%s sets cloud_build and buildkit; one backend is set", prefix)
	case r.BuildKit != nil:
		return r.BuildKit.validate(prefix + ".buildkit.")
	case r.CloudBuild == nil:
		return fmt.Errorf("%s sets no backend; set %s.cloud_build.project to build on Google Cloud Build, "+
			"or %s.buildkit.namespace to build on the in-cluster builder", prefix, prefix, prefix)
	}
	return r.CloudBuild.validate(prefix + ".cloud_build.")
}

// validate refuses a BuildKit backend kubectl or buildctl could not use,
// naming the field after at.
func (b *BuildKitConfig) validate(at string) error {
	switch {
	case b.Namespace == "":
		return fmt.Errorf("%snamespace is required: the namespace of the in-cluster builder, the chart's builder.namespace", at)
	case !namespaceName.MatchString(b.Namespace):
		return fmt.Errorf("%snamespace %q is not a Kubernetes namespace name", at, b.Namespace)
	case b.PodSelector != "" && !equalitySelector.MatchString(b.PodSelector):
		return fmt.Errorf("%spod_selector %q: want key=value labels, comma separated, such as %s", at, b.PodSelector, DefaultBuildKitPodSelector)
	case b.Context != "" && !kubeContext.MatchString(b.Context):
		return fmt.Errorf("%scontext %q is not a kubeconfig context name: no spaces or control characters, not starting with '-'", at, b.Context)
	}
	return nil
}

// validate refuses a Cloud Build backend gcloud could not use, naming the
// field after at.
func (cb *CloudBuildConfig) validate(at string) error {
	switch {
	case cb.Project == "":
		return fmt.Errorf("%sproject is required: the Google Cloud project the build runs in, such as my-project", at)
	case !gcpProject.MatchString(cb.Project):
		return fmt.Errorf("%sproject %q is not a Google Cloud project id", at, cb.Project)
	case cb.Region != "" && !gcpRegion.MatchString(cb.Region):
		return fmt.Errorf("%sregion %q is not a region name, such as us-central1", at, cb.Region)
	case cb.WorkerPool != "" && !workerPool.MatchString(cb.WorkerPool):
		return fmt.Errorf("%sworker_pool %q: want projects/<project>/locations/<region>/workerPools/<name>", at, cb.WorkerPool)
	case cb.WorkerPool != "" && cb.Region == "":
		return fmt.Errorf("%sregion is required with worker_pool: a build runs in the region of its pool", at)
	case cb.WorkerPool != "" && poolRegion(cb.WorkerPool) != cb.Region:
		return fmt.Errorf("%sworker_pool %q is in %s, not in region %s; a build runs in the region of its pool", at, cb.WorkerPool, poolRegion(cb.WorkerPool), cb.Region)
	case cb.ServiceAccount != "" && !serviceAccount.MatchString(cb.ServiceAccount):
		return fmt.Errorf("%sservice_account %q: want projects/<project>/serviceAccounts/<email>", at, cb.ServiceAccount)
	}
	return nil
}

// poolRegion is the location segment of a worker pool resource name.
func poolRegion(pool string) string {
	parts := strings.Split(pool, "/")
	if len(parts) < 4 {
		return ""
	}
	return parts[3]
}
