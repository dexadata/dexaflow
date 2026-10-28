package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// New builds a typed /api/v2 client for the control plane at baseURL (an origin
// such as "http://localhost:8080", with or without a trailing slash). When token
// is non-empty every request carries "Authorization: Bearer <token>"; the MCP and
// CLI pass the caller's JWT through unchanged and never mint one (ADR 0050 D9). An
// empty token leaves requests unauthenticated, for dev/loopback use. Extra
// ClientOptions (e.g. WithHTTPClient) are applied after the auth editor.
func New(baseURL, token string, opts ...ClientOption) (*ClientWithResponses, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	all := make([]ClientOption, 0, len(opts)+1)
	if token != "" {
		all = append(all, WithRequestEditorFn(bearerAuth(token)))
	}
	all = append(all, opts...)
	c, err := NewClientWithResponses(baseURL, all...)
	if err != nil {
		return nil, fmt.Errorf("building /api/v2 client for %q: %w", baseURL, err)
	}
	return c, nil
}

// bearerAuth returns a request editor that sets the bearer Authorization header.
func bearerAuth(token string) RequestEditorFn {
	return func(_ context.Context, req *http.Request) error {
		req.Header.Set("Authorization", "Bearer "+token)
		return nil
	}
}

// WithDagSourceVersion returns a request editor that asks GET
// /api/v2/dagSources/{dag_id} for the source of the DAG version numbered n
// (its version_number in the dagVersions listing) instead of the newest one:
//
//	c.GetDagSourceWithResponse(ctx, dagID, client.WithDagSourceVersion(n))
//
// The generated GetDagSource does not take the parameter itself, so that its
// signature stays compatible (see overlay.yaml).
func WithDagSourceVersion(n int) RequestEditorFn {
	return func(_ context.Context, req *http.Request) error {
		q := req.URL.Query()
		q.Set("version_number", strconv.Itoa(n))
		req.URL.RawQuery = q.Encode()
		return nil
	}
}

// dryRunSuffix turns the register path into its dry run route.
const dryRunSuffix = "/dry_run"

// DryRunRegister is a RequestEditorFn for RegisterDagVersion that sends the
// register to POST /api/v2/dags/{dag_id}/versions/dry_run instead: the
// server runs every check of the register and writes nothing, answering 200
// with dry_run true, or the refusal the register would give. A server from
// before the route answers 404 and registers nothing. Pass it only to
// RegisterDagVersion; it refuses any request other than a POST to the
// register path.
//
// As an argument of the call it runs after the client-level editors
// (WithRequestEditorFn): an editor of the client that signs the path signs
// the register path, not the dry run route.
func DryRunRegister(_ context.Context, req *http.Request) error {
	if req.Method != http.MethodPost || !strings.HasSuffix(req.URL.Path, "/versions") || !strings.Contains(req.URL.Path, "/api/v2/dags/") {
		return fmt.Errorf("DryRunRegister applies to POST /api/v2/dags/{dag_id}/versions, not %s %s", req.Method, req.URL.Path)
	}
	req.URL.Path += dryRunSuffix
	if req.URL.RawPath != "" {
		req.URL.RawPath += dryRunSuffix
	}
	return nil
}

// deployDefaultsPath is the cluster deploy defaults route (#1704).
const deployDefaultsPath = "/api/v2/deployDefaults"

// deployDefaultsMaxBody bounds the answer GetDeployDefaults reads.
const deployDefaultsMaxBody = 64 << 10

// ErrDeployDefaultsNotServed is what GetDeployDefaults returns when the
// server answers 404: it predates GET /api/v2/deployDefaults, and so
// advertises no deploy defaults.
var ErrDeployDefaultsNotServed = errors.New("the server does not serve " + deployDefaultsPath)

// DeployDefaults are the cluster deploy defaults GET /api/v2/deployDefaults
// serves: what a deploy uses for the registry prefix, the build platforms,
// the base image, the source mode runtime image and the remote builder when
// nothing local sets them, and what it reports about the task service
// account and the warm pools. An unset field is empty or nil.
type DeployDefaults struct {
	// Registry is the image prefix a deploy pushes to, such as ghcr.io/acme.
	Registry string `json:"registry"`
	// Platforms are the platforms a deploy builds for (os/arch[/variant]).
	Platforms []string `json:"platforms"`
	// BaseImage is the base image for projects that set none.
	BaseImage string `json:"base_image"`
	// SourceModeImage is the runtime image a deploy registers a DAG's
	// dag.py on, with no build, when nothing local names one (source mode,
	// #1772). Empty when the cluster advertises none, or from a server
	// that predates the field.
	SourceModeImage string `json:"source_mode_image,omitempty"`
	// RemoteBuilder is the remote builder a deploy uses for builds no
	// daemonless path covers, when nothing local names one (#1890). Nil when
	// the cluster advertises none, or from a server that predates the field.
	RemoteBuilder *DeployRemoteBuilder `json:"remote_builder,omitempty"`
	// TaskServiceAccount is the ServiceAccount task pods run as when a task
	// sets no execution.service_account (#1890). Empty when the cluster
	// advertises none, or from a server that predates the field.
	TaskServiceAccount string `json:"task_service_account,omitempty"`
	// WarmPools are the cluster's warm pool settings (#1890). Nil when the
	// cluster advertises none, or from a server that predates the field.
	WarmPools *DeployWarmPools `json:"warm_pools,omitempty"`
	// CIOIDC lists the CI OIDC issuers of the caller's tenant (ADR 0092),
	// which `dexaflow ci init` writes a tokenless pipeline for. Nil when the
	// tenant has none, or from a server that predates the field.
	CIOIDC *DeployCIOIDC `json:"ci_oidc,omitempty"`
}

// DeployCIOIDC is the ci_oidc of DeployDefaults.
type DeployCIOIDC struct {
	// Issuers are the entries, in the server's order.
	Issuers []DeployCIOIDCIssuer `json:"issuers"`
}

// DeployCIOIDCIssuer is one CI OIDC issuer: its entry name, its provider
// (github, gitlab, cloudbuild or other), the iss of its tokens and the
// audience a pipeline asks its token for.
type DeployCIOIDCIssuer struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Issuer   string `json:"issuer"`
	Audience string `json:"audience"`
}

// DeployRemoteBuilder is the remote_builder of DeployDefaults: its backend.
type DeployRemoteBuilder struct {
	// CloudBuild builds on Google Cloud Build.
	CloudBuild *DeployCloudBuild `json:"cloud_build,omitempty"`
	// BuildKit builds on the in-cluster BuildKit daemon the Helm chart runs
	// (builder.enabled, #1883). Nil from a server that predates it.
	BuildKit *DeployBuildKit `json:"buildkit,omitempty"`
}

// DeployBuildKit is the in-cluster BuildKit backend of DeployRemoteBuilder.
type DeployBuildKit struct {
	// Namespace is the namespace of the builder.
	Namespace string `json:"namespace,omitempty"`
	// PodSelector is the label selector of the builder pod; empty selects
	// app.kubernetes.io/component=builder.
	PodSelector string `json:"pod_selector,omitempty"`
}

// DeployCloudBuild is the Cloud Build backend of DeployRemoteBuilder. An
// unset field is empty.
type DeployCloudBuild struct {
	// Project is the Google Cloud project the build runs in.
	Project string `json:"project,omitempty"`
	// Region is the Cloud Build region, such as us-central1.
	Region string `json:"region,omitempty"`
	// WorkerPool is a private pool,
	// projects/<project>/locations/<region>/workerPools/<name>.
	WorkerPool string `json:"worker_pool,omitempty"`
	// ServiceAccount is the account the build runs as,
	// projects/<project>/serviceAccounts/<email>.
	ServiceAccount string `json:"service_account,omitempty"`
}

// DeployWarmPools is the warm_pools of DeployDefaults: a DAG that declares
// no min_idle_workers gets MinIdleWorkers, every value is capped at
// MaxPoolSize (0 sets no cap), and nothing is kept warm unless Enabled.
type DeployWarmPools struct {
	// Enabled says whether the cluster keeps warm workers at all.
	Enabled bool `json:"enabled"`
	// MinIdleWorkers is the cluster's idle worker floor per version.
	MinIdleWorkers int `json:"min_idle_workers"`
	// MaxPoolSize caps one version's pool.
	MaxPoolSize int `json:"max_pool_size"`
}

// GetDeployDefaults reads GET /api/v2/deployDefaults through the client's
// Doer, server URL and request editors, then reqEditors. A server from
// before the route answers 404, which returns ErrDeployDefaultsNotServed;
// any other status but 200 is an error naming it. The route gets no generated method
// (see overlay.yaml): a new method on ClientInterface would break every
// implementation of it.
func (c *Client) GetDeployDefaults(ctx context.Context, reqEditors ...RequestEditorFn) (*DeployDefaults, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.Server, "/")+deployDefaultsPath, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("building the %s request: %w", deployDefaultsPath, err)
	}
	if eerr := c.applyEditors(ctx, req, reqEditors); eerr != nil {
		return nil, eerr
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", deployDefaultsPath, err)
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // read-only response
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, ErrDeployDefaultsNotServed
	default:
		return nil, fmt.Errorf("%s returned %d", deployDefaultsPath, resp.StatusCode)
	}
	var d DeployDefaults
	if derr := json.NewDecoder(io.LimitReader(resp.Body, deployDefaultsMaxBody)).Decode(&d); derr != nil {
		return nil, fmt.Errorf("decoding %s: %w", deployDefaultsPath, derr)
	}
	return &d, nil
}
