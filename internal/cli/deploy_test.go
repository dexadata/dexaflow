package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

func TestRegisterDeployedDAGRepinsAndRegisters(t *testing.T) {
	t.Parallel()
	var gotPath, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	// A compiled dag.json carrying the tag ref; register must re-pin it to the
	// digest before posting, and the file on disk must end up re-pinned too.
	out := filepath.Join(t.TempDir(), "dag.json")
	if err := os.WriteFile(out, []byte(pushSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	digest := "ghcr.io/org/etl@sha256:cafef00d"

	var sb strings.Builder
	res, err := registerDeployedDAG(context.Background(), &sb, registerRequest{serverURL: srv.URL, token: "tok", output: out, digestRef: digest, version: "v1", origin: labelDefault})
	if err != nil {
		t.Fatalf("registerDeployedDAG: %v", err)
	}
	if res.DagID != "etl" || res.Status != deployStatusRegistered || res.Digest != "sha256:cafef00d" || res.Image != digest {
		t.Errorf("result = %+v, want etl registered with the digest", res)
	}
	if gotPath != "/api/v2/dags/etl/versions" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("auth = %q, want Bearer tok", gotAuth)
	}
	if !strings.Contains(gotBody, digest) {
		t.Errorf("posted spec was not re-pinned to the digest: %s", gotBody)
	}
	if !strings.Contains(sb.String(), "Deployed etl") {
		t.Errorf("summary = %q, want a Deployed line", sb.String())
	}
	// The on-disk dag.json must also be re-pinned (the immutable artifact).
	repinned, _ := os.ReadFile(out)
	if !strings.Contains(string(repinned), digest) {
		t.Errorf("dag.json on disk was not re-pinned: %s", repinned)
	}
}

func TestRegisterDeployedDAGSurfacesServerError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "forbidden")
	}))
	defer srv.Close()

	out := filepath.Join(t.TempDir(), "dag.json")
	if err := os.WriteFile(out, []byte(pushSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := registerDeployedDAG(context.Background(), io.Discard, registerRequest{serverURL: srv.URL, token: "tok", output: out, digestRef: "r@sha256:x", version: "v1", origin: labelDefault})
	if err == nil {
		t.Error("expected an error when the control plane rejects the register")
	}
}

// TestRegisterDeployedDAGExplainsTheTrustedIssuerRefusal: a register the
// server refuses because deploys go through its trusted issuer (#1773) says
// so, rather than a bare 403.
func TestRegisterDeployedDAGExplainsTheTrustedIssuerRefusal(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Dexaflow-Deploy-Via", "trusted-issuer")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"detail":"deploys go through the trusted issuer"}`)
	}))
	defer srv.Close()

	out := filepath.Join(t.TempDir(), "dag.json")
	if err := os.WriteFile(out, []byte(pushSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := registerDeployedDAG(context.Background(), io.Discard, registerRequest{serverURL: srv.URL, token: "tok", output: out, digestRef: "r@sha256:x", version: "v1", origin: labelDefault})
	if err == nil || !strings.Contains(err.Error(), "takes deploys only through its trusted issuer") {
		t.Errorf("err = %v, want it to say deploys go through the trusted issuer", err)
	}
}

func TestTriggerDeployRunPostsToDagRuns(t *testing.T) {
	t.Parallel()
	var gotPath, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var sb strings.Builder
	if _, err := triggerDeployRun(context.Background(), &sb, srv.URL, "tok", "etl"); err != nil {
		t.Fatalf("triggerDeployRun: %v", err)
	}
	if gotPath != "/api/v2/dags/etl/dagRuns" {
		t.Errorf("path = %q, want the dagRuns trigger endpoint", gotPath)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("auth = %q", gotAuth)
	}
	if !strings.Contains(gotBody, "dag_run_id") {
		t.Errorf("body = %q, want a dag_run_id", gotBody)
	}
	if !strings.Contains(sb.String(), "triggered run") {
		t.Errorf("summary = %q, want a triggered-run line", sb.String())
	}
}

func TestTriggerDeployRunSurfacesError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer srv.Close()
	if _, err := triggerDeployRun(context.Background(), io.Discard, srv.URL, "tok", "etl"); err == nil {
		t.Error("expected an error when the trigger is rejected")
	}
}

func TestDeployRunsPastGuardThenFailsAtCompile(t *testing.T) {
	// A project WITH a registry passes the guard, so deploy proceeds to resolve
	// the image ref and invoke compile. With the parser stubbed to a failing
	// command, the run fails after the guard — exercising the orchestration
	// middle without a real parser or builder.
	dir := filepath.Join(t.TempDir(), "proj")
	if _, _, err := run(t, "init", dir); err != nil {
		t.Fatalf("init: %v", err)
	}
	f := filepath.Join(dir, "dexaflow.yaml")
	data, _ := os.ReadFile(f)
	withRegistry := string(data) + "\nregistry:\n  url: ghcr.io/org\n  image_name: etl\n"
	if err := os.WriteFile(f, []byte(withRegistry), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("LEOFLOW_PARSER_CMD", "false") // deterministic compile failure, no python
	if _, _, err := run(t, "deploy", dir, "--server", "http://127.0.0.1:0"); err == nil {
		t.Error("expected deploy to fail at compile when the parser fails")
	}
}

func TestResolveProjectDirFindsByDagID(t *testing.T) {
	ws := t.TempDir()
	for _, name := range []string{"alpha", "beta"} {
		if _, _, err := run(t, "init", filepath.Join(ws, name)); err != nil {
			t.Fatalf("init %s: %v", name, err)
		}
	}
	spec, err := ResolveWorkspace(ws)
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	if len(spec.Projects) == 0 {
		t.Fatal("expected discovered projects")
	}
	want := spec.Projects[0]
	got, gerr := resolveProjectDir(ws, want.DagID)
	if gerr != nil {
		t.Fatalf("resolveProjectDir(%q): %v", want.DagID, gerr)
	}
	if got != want.Path {
		t.Errorf("dir = %q, want %q", got, want.Path)
	}
	if _, err := resolveProjectDir(ws, "definitely-not-a-dag"); err == nil {
		t.Error("expected an error for an unknown dag_id")
	}
}

func TestDeployUnknownDagIDFails(t *testing.T) {
	t.Chdir(t.TempDir()) // outside any workspace or repository: the config's workspace applies
	ws := t.TempDir()
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("workspace: "+ws+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, "deploy", "ghost-dag", "--config", cfgPath); err == nil {
		t.Error("expected deploy of an unknown dag_id to fail")
	}
}

func TestDeployAllEmptyWorkspaceFails(t *testing.T) {
	t.Chdir(t.TempDir()) // outside any workspace or repository: the config's workspace applies
	ws := t.TempDir()    // empty: no DAG projects
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("workspace: "+ws+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, "deploy", "--all", "--config", cfgPath); err == nil {
		t.Error("expected --all on an empty workspace to fail")
	}
}

func TestDeployAllRejectsExtraArg(t *testing.T) {
	t.Chdir(t.TempDir())
	if _, _, err := run(t, "deploy", "somedir", "--all"); err == nil {
		t.Error("expected --all combined with a path/dag_id to be rejected")
	}
}

func TestDeployAllReportsPerProjectFailure(t *testing.T) {
	// A workspace with one scaffolded project that has no registry: --all must
	// iterate to it, fail it at the registry guard, and report a non-zero result.
	// Run from a folder outside any workspace or repository, where the
	// config's workspace is the one deploy reads.
	t.Chdir(t.TempDir())
	ws := t.TempDir()
	if _, _, err := run(t, "init", filepath.Join(ws, "one")); err != nil {
		t.Fatalf("init: %v", err)
	}
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("workspace: "+ws+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, "deploy", "--all", "--config", cfgPath); err == nil {
		t.Error("expected --all to report the registry-less project's failure")
	}
}

func TestDeployByDagIDReachesProjectThenGuard(t *testing.T) {
	t.Chdir(t.TempDir()) // outside any workspace or repository: the config's workspace applies
	ws := t.TempDir()
	if _, _, err := run(t, "init", filepath.Join(ws, "solo")); err != nil {
		t.Fatalf("init: %v", err)
	}
	spec, err := ResolveWorkspace(ws)
	if err != nil || len(spec.Projects) == 0 {
		t.Fatalf("resolve workspace: %v", err)
	}
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if werr := os.WriteFile(cfgPath, []byte("workspace: "+ws+"\n"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	// Deploying by the resolved dag_id reaches its project, which has no
	// registry -> the guard fires. Proves the dag_id->dir resolution path.
	_, _, derr := run(t, "deploy", spec.Projects[0].DagID, "--config", cfgPath)
	if derr == nil || !strings.Contains(derr.Error(), "registry") {
		t.Errorf("want a registry guard error via dag_id resolution, got %v", derr)
	}
}

func TestResolveServerTokenPrecedence(t *testing.T) {
	cmd := newDeployCommand()
	// Flags win outright — config is not even consulted.
	srv, tok, err := resolveServerToken(cmd, "https://pro", "flag-tok")
	if err != nil {
		t.Fatalf("resolveServerToken: %v", err)
	}
	if srv != "https://pro" || tok != "flag-tok" {
		t.Errorf("flags should win: got server=%q token=%q", srv, tok)
	}
}

func TestResolveServerTokenFallsBackToEnv(t *testing.T) {
	t.Setenv("LEOFLOW_SERVER_URL", "https://env-pro")
	t.Setenv("LEOFLOW_TOKEN", "env-tok")
	cmd := newDeployCommand()
	srv, tok, err := resolveServerToken(cmd, "", "")
	if err != nil {
		t.Fatalf("resolveServerToken: %v", err)
	}
	if srv != "https://env-pro" || tok != "env-tok" {
		t.Errorf("config/env fallback: got server=%q token=%q", srv, tok)
	}
}

func TestDeployImageRef(t *testing.T) {
	t.Parallel()
	base := func(strategy string) *domain.LeoflowConfig {
		return &domain.LeoflowConfig{Registry: &domain.RegistryConfig{
			URL: "ghcr.io/org", ImageName: "etl", TagStrategy: strategy,
		}}
	}
	// version strategy tags by the version label.
	if got := deployImageRef(base("version"), "v3", gitRevision{sha: "1a2b3c4"}); got != "ghcr.io/org/etl:v3" {
		t.Errorf("version: ref = %q, want ghcr.io/org/etl:v3", got)
	}
	// git_sha strategy tags by the actual commit sha (the #2 fix — it used to
	// collapse to the version label).
	if got := deployImageRef(base("git_sha"), "v3", gitRevision{sha: "1a2b3c4"}); got != "ghcr.io/org/etl:1a2b3c4" {
		t.Errorf("git_sha: ref = %q, want ghcr.io/org/etl:1a2b3c4", got)
	}
}

func TestRepinImageInSpec(t *testing.T) {
	t.Parallel()
	in := []byte(`{"schema_version":"1.0","dag_id":"etl","dag_version":"v1","image":"ghcr.io/org/etl:v1","tasks":[{"task_id":"a","type":"python","entrypoint":"dag:a"}]}`)
	out, err := repinImageInSpec(in, "ghcr.io/org/etl@sha256:deadbeef")
	if err != nil {
		t.Fatalf("repinImageInSpec: %v", err)
	}
	var m map[string]any
	if uerr := json.Unmarshal(out, &m); uerr != nil {
		t.Fatalf("output is not valid JSON: %v", uerr)
	}
	if m["image"] != "ghcr.io/org/etl@sha256:deadbeef" {
		t.Errorf("image = %v, want the digest-pinned ref", m["image"])
	}
	// Every other field must survive the re-pin untouched.
	if m["dag_id"] != "etl" || m["dag_version"] != "v1" {
		t.Errorf("re-pin dropped fields: %v", m)
	}
	if tasks, ok := m["tasks"].([]any); !ok || len(tasks) != 1 {
		t.Errorf("tasks not preserved: %v", m["tasks"])
	}
}

func TestDeployCommandRejectsMissingRegistry(t *testing.T) {
	// A scaffolded project has no registry: deploy must fail at the guard,
	// before touching any builder or server.
	dir := filepath.Join(t.TempDir(), "proj")
	if _, _, err := run(t, "init", dir); err != nil {
		t.Fatalf("init: %v", err)
	}
	_, stderr, err := run(t, "deploy", dir)
	if err == nil {
		t.Fatal("expected deploy to fail on a project with no registry")
	}
	if !strings.Contains(err.Error()+stderr, "registry") {
		t.Errorf("error should name the missing registry; got err=%v stderr=%q", err, stderr)
	}
}

func TestRequireRegistryRejectsUnconfigured(t *testing.T) {
	t.Parallel()
	// ApplyDefaults instantiates Registry but leaves URL empty — deploy must
	// still reject it, loudly and actionably (ADR 0041).
	cfg := &domain.LeoflowConfig{}
	cfg.ApplyDefaults()

	err := requireRegistry(cfg)
	if err == nil {
		t.Fatal("expected requireRegistry to reject a config with no registry URL")
	}
	msg := err.Error()
	for _, want := range []string{"registry", "dexaflow.yaml", "docker login"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q; got:\n%s", want, msg)
		}
	}
}

// TestRequireRegistryNamesTheRegistryLogins checks that the error names the
// credential each kind of registry takes (ADR 0080): the cloud CLI session for
// Artifact Registry, ECR and ACR, docker login for the others, and no longer
// tells every user to run docker login for ghcr.io.
func TestRequireRegistryNamesTheRegistryLogins(t *testing.T) {
	t.Parallel()
	cfg := &domain.LeoflowConfig{}
	cfg.ApplyDefaults()
	err := requireRegistry(cfg)
	if err == nil {
		t.Fatal("expected requireRegistry to reject a config with no registry URL")
	}
	msg := err.Error()
	for _, want := range []string{
		"Artifact Registry, GCR: gcloud auth login",
		"ECR: aws sso login, aws configure, environment credentials or an instance role",
		"ACR: az login",
		"any other registry: dexaflow registry login <registry> (or docker login <registry>)",
		"the deploy preflight names it",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q; got:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "authenticate your builder once") {
		t.Errorf("error message still tells everyone to docker login ghcr.io:\n%s", msg)
	}
}

func TestRequireRegistryRejectsMissingImageName(t *testing.T) {
	t.Parallel()
	cfg := &domain.LeoflowConfig{Registry: &domain.RegistryConfig{URL: "ghcr.io/org"}}
	if err := requireRegistry(cfg); err == nil {
		t.Error("expected requireRegistry to reject a registry URL with no image_name")
	}
}

func TestRequireRegistryAcceptsConfigured(t *testing.T) {
	t.Parallel()
	cfg := &domain.LeoflowConfig{Registry: &domain.RegistryConfig{URL: "ghcr.io/org", ImageName: "etl"}}
	if err := requireRegistry(cfg); err != nil {
		t.Errorf("requireRegistry on a configured registry: %v", err)
	}
}
