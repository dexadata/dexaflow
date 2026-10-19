package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestNewInjectsBearerToken: a client built with a token carries
// "Authorization: Bearer <token>" on every request and targets the /api/v2 path
// the operation maps to. This is the pass-through auth the MCP and CLI rely on
// (ADR 0050 D9) — the client never mints a token, it forwards the caller's.
func TestNewInjectsBearerToken(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"dags":[],"total_entries":0}`))
	}))
	defer srv.Close()

	c, err := New(srv.URL, "tok123")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.ListDagsWithResponse(context.Background(), nil); err != nil {
		t.Fatalf("ListDags: %v", err)
	}
	if gotAuth != "Bearer tok123" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer tok123")
	}
	if gotPath != "/api/v2/dags" {
		t.Errorf("path = %q, want /api/v2/dags", gotPath)
	}
}

// TestNewOmitsAuthHeaderWithoutToken: an empty token leaves requests
// unauthenticated (dev/loopback), rather than sending an empty bearer.
func TestNewOmitsAuthHeaderWithoutToken(t *testing.T) {
	var hadAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hadAuth = r.Header["Authorization"]
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := New(srv.URL, "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.GetHealthzWithResponse(context.Background()); err != nil {
		t.Fatalf("GetHealthz: %v", err)
	}
	if hadAuth {
		t.Error("no token must send no Authorization header")
	}
}

// TestNewTrimsTrailingSlash: a base URL with a trailing slash must not produce
// a double slash in the request path.
func TestNewTrimsTrailingSlash(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := New(srv.URL+"/", "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.GetHealthzWithResponse(context.Background()); err != nil {
		t.Fatalf("GetHealthz: %v", err)
	}
	if gotPath != "/healthz" {
		t.Errorf("path = %q, want /healthz (no double slash)", gotPath)
	}
}

// TestWithDagSourceVersionSetsTheQueryParameter: GetDagSource keeps its
// signature (the overlay drops version_number from the generated client), and
// WithDagSourceVersion sends it, so a caller can read a given version's source.
// Without the editor no version_number is sent: the server serves the newest.
func TestWithDagSourceVersionSetsTheQueryParameter(t *testing.T) {
	var gotQuery []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = append(gotQuery, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":"# v1","dag_id":"etl","version_number":1}`))
	}))
	defer srv.Close()

	c, err := New(srv.URL, "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp, err := c.GetDagSourceWithResponse(context.Background(), "etl", WithDagSourceVersion(1))
	if err != nil {
		t.Fatalf("GetDagSource: %v", err)
	}
	if resp.JSON200 == nil || resp.JSON200.VersionNumber == nil || *resp.JSON200.VersionNumber != 1 {
		t.Errorf("response = %s", resp.Body)
	}
	if _, err = c.GetDagSourceWithResponse(context.Background(), "etl"); err != nil {
		t.Fatalf("GetDagSource: %v", err)
	}
	if len(gotQuery) != 2 || gotQuery[0] != "version_number=1" || gotQuery[1] != "" {
		t.Errorf("queries = %q, want [version_number=1, empty]", gotQuery)
	}
}

// TestDryRunRegisterSendsTheRegisterToTheDryRunRoute: RegisterDagVersion
// with the DryRunRegister editor posts the same body and query to
// /api/v2/dags/{dag_id}/versions/dry_run, the route that registers nothing.
func TestDryRunRegisterSendsTheRegisterToTheDryRunRoute(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"dry_run":true,"created":true}`))
	}))
	defer srv.Close()
	c, err := New(srv.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	paused := true
	resp, err := c.RegisterDagVersionWithResponse(context.Background(), "etl", &RegisterDagVersionParams{IsPaused: &paused},
		RegisterDagVersionJSONRequestBody{DagId: "etl"}, DryRunRegister)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v2/dags/etl/versions/dry_run" || gotQuery != "is_paused=true" {
		t.Errorf("request = %s?%s, want /api/v2/dags/etl/versions/dry_run?is_paused=true", gotPath, gotQuery)
	}
	if resp.JSON200 == nil || resp.JSON200.DryRun == nil || !*resp.JSON200.DryRun {
		t.Errorf("answer = %s, want dry_run true decoded", resp.Body)
	}
	// Without the editor the register goes where it always did.
	if _, err := c.RegisterDagVersionWithResponse(context.Background(), "etl", nil, RegisterDagVersionJSONRequestBody{DagId: "etl"}); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v2/dags/etl/versions" {
		t.Errorf("a register went to %s", gotPath)
	}
	// A list of the versions has the same path: the editor refuses it.
	get, gerr := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/api/v2/dags/etl/versions", http.NoBody)
	if gerr != nil {
		t.Fatal(gerr)
	}
	if err := DryRunRegister(context.Background(), get); err == nil || get.URL.Path != "/api/v2/dags/etl/versions" {
		t.Errorf("DryRunRegister on a GET: err=%v path=%s, want a refusal and the path untouched", err, get.URL.Path)
	}
}

// TestGetDeployDefaultsReadsTheClusterDefaults: GetDeployDefaults sends GET
// /api/v2/deployDefaults through the client's Doer, server URL and request
// editors, and decodes the answer.
func TestGetDeployDefaultsReadsTheClusterDefaults(t *testing.T) {
	var gotAuth, gotPath, gotMethod, gotProbe string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath, gotMethod, gotProbe = r.Header.Get("Authorization"), r.URL.Path, r.Method, r.Header.Get("X-Probe")
		_, _ = w.Write([]byte(`{"registry":"ghcr.io/acme","platforms":["linux/arm64"],"base_image":"b:1","source_mode_image":"r@sha256:1"}`))
	}))
	defer srv.Close()
	c, err := NewClient(srv.URL+"/", WithRequestEditorFn(bearerAuth("tok")))
	if err != nil {
		t.Fatal(err)
	}
	probe := func(_ context.Context, req *http.Request) error {
		req.Header.Set("X-Probe", "1")
		return nil
	}
	d, err := c.GetDeployDefaults(context.Background(), probe)
	if err != nil {
		t.Fatalf("GetDeployDefaults: %v", err)
	}
	if gotProbe != "1" {
		t.Error("the call's request editor did not run")
	}
	if gotMethod != http.MethodGet || gotPath != "/api/v2/deployDefaults" || gotAuth != "Bearer tok" {
		t.Errorf("request = %s %s (Authorization %q), want GET /api/v2/deployDefaults with the bearer", gotMethod, gotPath, gotAuth)
	}
	if d == nil || d.Registry != "ghcr.io/acme" || len(d.Platforms) != 1 || d.Platforms[0] != "linux/arm64" || d.BaseImage != "b:1" {
		t.Errorf("defaults = %+v, want the answer decoded", d)
	}
	if d != nil && d.SourceModeImage != "r@sha256:1" {
		t.Errorf("source_mode_image = %q, want the answer decoded (#1772)", d.SourceModeImage)
	}
}

// TestGetDeployDefaultsOlderServer: a server from before the route answers
// 404, which is ErrDeployDefaultsNotServed; any other status is another error.
func TestGetDeployDefaultsOlderServer(t *testing.T) {
	status := http.StatusNotFound
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
	defer srv.Close()
	c, err := NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if d, err := c.GetDeployDefaults(context.Background()); d != nil || !errors.Is(err, ErrDeployDefaultsNotServed) {
		t.Errorf("404: defaults=%+v err=%v, want ErrDeployDefaultsNotServed", d, err)
	}
	status = http.StatusForbidden
	if d, err := c.GetDeployDefaults(context.Background()); d != nil || err == nil || errors.Is(err, ErrDeployDefaultsNotServed) || !strings.Contains(err.Error(), "403") {
		t.Errorf("403: defaults=%+v err=%v, want an error naming 403", d, err)
	}
}

// TestGetDeployDefaultsBuilderServiceAccountAndWarmPools (#1890): the remote
// builder, task service account and warm pools are decoded, and a server
// that predates them, or advertises none, leaves them nil and empty.
func TestGetDeployDefaultsBuilderServiceAccountAndWarmPools(t *testing.T) {
	body := `{"registry":"","platforms":[],"base_image":"","source_mode_image":"",` +
		`"remote_builder":{"cloud_build":{"project":"acme-ci","region":"us-central1","worker_pool":"projects/acme-ci/locations/us-central1/workerPools/p","service_account":"projects/acme-ci/serviceAccounts/b@acme-ci.iam.gserviceaccount.com"}},` +
		`"task_service_account":"dexaflow-task","warm_pools":{"enabled":true,"min_idle_workers":1,"max_pool_size":8}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c, err := NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	d, err := c.GetDeployDefaults(context.Background())
	if err != nil {
		t.Fatalf("GetDeployDefaults: %v", err)
	}
	wantCB := DeployCloudBuild{Project: "acme-ci", Region: "us-central1", WorkerPool: "projects/acme-ci/locations/us-central1/workerPools/p",
		ServiceAccount: "projects/acme-ci/serviceAccounts/b@acme-ci.iam.gserviceaccount.com"}
	if d.RemoteBuilder == nil || d.RemoteBuilder.CloudBuild == nil || *d.RemoteBuilder.CloudBuild != wantCB {
		t.Errorf("remote_builder = %+v, want %+v", d.RemoteBuilder, wantCB)
	}
	if d.TaskServiceAccount != "dexaflow-task" {
		t.Errorf("task_service_account = %q, want dexaflow-task", d.TaskServiceAccount)
	}
	if want := (DeployWarmPools{Enabled: true, MinIdleWorkers: 1, MaxPoolSize: 8}); d.WarmPools == nil || *d.WarmPools != want {
		t.Errorf("warm_pools = %+v, want %+v", d.WarmPools, want)
	}
	for _, older := range []string{
		`{"registry":"ghcr.io/acme","platforms":[],"base_image":""}`,
		`{"registry":"","platforms":[],"base_image":"","source_mode_image":"","remote_builder":null,"task_service_account":"","warm_pools":null}`,
	} {
		body = older
		d, err := c.GetDeployDefaults(context.Background())
		if err != nil {
			t.Fatalf("GetDeployDefaults(%s): %v", older, err)
		}
		if d.RemoteBuilder != nil || d.TaskServiceAccount != "" || d.WarmPools != nil {
			t.Errorf("%s decoded as %+v, want no remote builder, task service account or warm pools", older, d)
		}
	}
}

// TestGetDeployDefaultsBuildKit (#1883): the in-cluster builder a cluster
// advertises is decoded with its namespace and pod selector.
func TestGetDeployDefaultsBuildKit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"registry":"","platforms":[],"base_image":"",` +
			`"remote_builder":{"buildkit":{"namespace":"dexaflow-builder","pod_selector":"app.kubernetes.io/instance=df,app.kubernetes.io/component=builder"}}}`))
	}))
	defer srv.Close()
	c, err := NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	d, err := c.GetDeployDefaults(context.Background())
	if err != nil {
		t.Fatalf("GetDeployDefaults: %v", err)
	}
	want := DeployBuildKit{Namespace: "dexaflow-builder", PodSelector: "app.kubernetes.io/instance=df,app.kubernetes.io/component=builder"}
	if d.RemoteBuilder == nil || d.RemoteBuilder.CloudBuild != nil || d.RemoteBuilder.BuildKit == nil || *d.RemoteBuilder.BuildKit != want {
		t.Errorf("remote_builder = %+v, want buildkit %+v", d.RemoteBuilder, want)
	}
}

// TestGetDeployDefaultsCIOIDC reads the CI OIDC issuers of the caller's
// tenant (ADR 0092), and none from a server that predates them.
func TestGetDeployDefaultsCIOIDC(t *testing.T) {
	body := `{"registry":"","platforms":[],"base_image":"","ci_oidc":{"issuers":[` +
		`{"name":"github-acme","provider":"github","issuer":"https://token.actions.githubusercontent.com","audience":"https://dexaflow.acme.example"}]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c, err := NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	d, err := c.GetDeployDefaults(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := DeployCIOIDCIssuer{Name: "github-acme", Provider: "github", Issuer: "https://token.actions.githubusercontent.com", Audience: "https://dexaflow.acme.example"}
	if d.CIOIDC == nil || len(d.CIOIDC.Issuers) != 1 || d.CIOIDC.Issuers[0] != want {
		t.Errorf("ci_oidc = %+v, want %+v", d.CIOIDC, want)
	}
	body = `{"registry":"","platforms":[],"base_image":""}`
	if d, err = c.GetDeployDefaults(context.Background()); err != nil || d.CIOIDC != nil {
		t.Errorf("an older server: %+v, %v; want no ci_oidc", d, err)
	}
}
