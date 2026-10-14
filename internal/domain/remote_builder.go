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
)

// Validate refuses a remote builder a build could not use, naming the field
// under prefix (build.remote_builder in dexaflow.yaml).
func (r *RemoteBuilderConfig) Validate(prefix string) error {
	if r == nil {
		return nil
	}
	cb := r.CloudBuild
	if cb == nil {
		return fmt.Errorf("%s sets no backend; set %s.cloud_build.project to build on Google Cloud Build", prefix, prefix)
	}
	at := prefix + ".cloud_build."
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
