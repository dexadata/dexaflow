package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

// envOf is a getenv over a fixed map.
func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// gitInit makes dir a git work tree with one commit and the origin remote,
// and returns the full commit id.
func gitInit(t *testing.T, dir, origin string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	for _, args := range [][]string{
		{"init", "-q"}, {"remote", "add", "origin", origin},
		{"-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "c"},
	} {
		c := exec.CommandContext(t.Context(), "git", args...)
		c.Dir = dir
		c.Env = gitEnv(os.Environ())
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return gitOutput(context.Background(), dir, "rev-parse", "HEAD")
}

// TestCollectBuildInfoFromGitAndGitHub: in a git work tree the commit is the
// project's HEAD and the repository its origin remote, without the
// credentials a CI checkout puts in it; on GitHub Actions the pipeline is
// the workflow run.
func TestCollectBuildInfoFromGitAndGitHub(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sha := gitInit(t, dir, "https://x-access-token:s3cret@github.com/acme/etl.git")
	got := collectBuildInfo(context.Background(), dir, envOf(map[string]string{
		"GITHUB_ACTIONS": "true", "GITHUB_SERVER_URL": "https://github.com", "GITHUB_REPOSITORY": "acme/etl", "GITHUB_RUN_ID": "42",
	}))
	if got.Commit != sha || len(sha) < 40 {
		t.Errorf("commit = %q, want HEAD %q", got.Commit, sha)
	}
	if got.Repo != "https://github.com/acme/etl" {
		t.Errorf("repo = %q, want the origin without credentials or .git", got.Repo)
	}
	if got.PipelineURL != "https://github.com/acme/etl/actions/runs/42" {
		t.Errorf("pipeline = %q", got.PipelineURL)
	}
	if got.CLIVersion == "" {
		t.Error("cli_version is empty")
	}
	if err := got.Validate(); err != nil {
		t.Errorf("collected build info refused: %v", err)
	}
}

// TestCollectBuildInfoOutsideGit: outside git there is no commit, and the
// repository and pipeline come from the CI's own variables.
func TestCollectBuildInfoOutsideGit(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name               string
		env                map[string]string
		wantRepo, wantPipe string
	}{
		{"gitlab", map[string]string{"GITLAB_CI": "true", "CI_PROJECT_URL": "https://gitlab.example/acme/etl", "CI_PIPELINE_URL": "https://gitlab.example/acme/etl/-/pipelines/7"},
			"https://gitlab.example/acme/etl", "https://gitlab.example/acme/etl/-/pipelines/7"},
		{"jenkins", map[string]string{"BUILD_URL": "https://jenkins.example/job/etl/9/"}, "", "https://jenkins.example/job/etl/9/"},
		{"query and fragment stripped", map[string]string{"CI_PROJECT_URL": "https://gitlab.example/acme/etl?ref=main", "BUILD_URL": "https://jenkins.example/job/etl/9/?token=x#console"},
			"https://gitlab.example/acme/etl", "https://jenkins.example/job/etl/9/"},
		{"bad URL dropped", map[string]string{"BUILD_URL": "file:///etc/passwd"}, "", ""},
		{"no CI", map[string]string{}, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := collectBuildInfo(context.Background(), t.TempDir(), envOf(tc.env))
			if got.Commit != "" || got.Repo != tc.wantRepo || got.PipelineURL != tc.wantPipe {
				t.Errorf("build info = %+v, want repo %q pipeline %q and no commit", got, tc.wantRepo, tc.wantPipe)
			}
		})
	}
}

// TestBrowsableRemote turns a git remote into the http(s) URL of the
// repository, never keeping credentials, and gives up on what it cannot map.
func TestBrowsableRemote(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"https://github.com/acme/etl.git":         "https://github.com/acme/etl",
		"https://user:pw@gitlab.example/a/b":      "https://gitlab.example/a/b",
		"git@github.com:acme/etl.git":             "https://github.com/acme/etl",
		"ssh://git@gitlab.example:2222/a/b.git":   "https://gitlab.example/a/b",
		"http://git.internal/a":                   "http://git.internal/a",
		"/srv/git/etl.git":                        "",
		"file:///srv/git/etl.git":                 "",
		"":                                        "",
		"git@github.com:acme/etl.git\nhttps://x/": "",
	} {
		if got := browsableRemote(in); got != want {
			t.Errorf("browsableRemote(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestVersionPostSendsBuildInfo: the register carries each build info field
// as its build_* query parameter, and nothing when there is none.
func TestVersionPostSendsBuildInfo(t *testing.T) {
	t.Parallel()
	b := &domain.BuildInfo{Commit: "abc1234", Repo: "https://github.com/acme/etl", PipelineURL: "https://ci/1",
		Builder: "code-layer", BaseImage: "base:1", CLIVersion: "v0.5.4"}
	q, err := url.ParseQuery(strings.TrimPrefix(versionPost{build: b}.query(), "?"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"build_commit": "abc1234", "build_repo": "https://github.com/acme/etl", "build_pipeline_url": "https://ci/1",
		"build_builder": "code-layer", "build_base_image": "base:1", "build_cli_version": "v0.5.4"}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, q.Get(k), v)
		}
	}
	if got := (versionPost{build: &domain.BuildInfo{Commit: "abc1234"}}).query(); got != "?build_commit=abc1234" {
		t.Errorf("one field query = %q", got)
	}
	if got := (versionPost{}).query(); got != "" {
		t.Errorf("query without build info = %q", got)
	}
}

// TestRegisterDeployedDAGSendsBuildInfo: the deploy's register sends the
// build info it was given (where the deploy built the version).
func TestRegisterDeployedDAGSendsBuildInfo(t *testing.T) {
	t.Parallel()
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	out := filepath.Join(t.TempDir(), "dag.json")
	if err := os.WriteFile(out, []byte(pushSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	info := domain.BuildInfo{Commit: "abc1234", Builder: "local-daemon (docker)", BaseImage: "ghcr.io/dexadata/dexaflow-runtime:py3.12"}
	if _, err := registerDeployedDAG(context.Background(), &strings.Builder{}, registerRequest{serverURL: srv.URL, output: out,
		digestRef: "r@sha256:x", version: "v1", origin: labelDefault, build: info}); err != nil {
		t.Fatalf("registerDeployedDAG: %v", err)
	}
	if gotQuery.Get("build_commit") != "abc1234" || gotQuery.Get("build_builder") != "local-daemon (docker)" ||
		gotQuery.Get("build_base_image") != info.BaseImage {
		t.Errorf("register query = %v, want the build info", gotQuery)
	}
}

// TestDeploySendsWhereItBuilt drives a whole deploy from a git work tree on
// GitHub Actions: the register names the project's commit and repository,
// the workflow run, the builder that built the image, the base image the
// generated Dockerfile builds FROM and the CLI version (#1702).
func TestDeploySendsWhereItBuilt(t *testing.T) {
	reg := newFakeOCIRegistry(t, singleManifest)
	dir, stub, _ := deployFixtureAt(t, reg.host+"/team")
	sha := gitInit(t, dir, "git@github.com:acme/etl.git")
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_SERVER_URL", "https://github.com")
	t.Setenv("GITHUB_REPOSITORY", "acme/etl")
	t.Setenv("GITHUB_RUN_ID", "42")
	fake := &deployServer{}
	if out, stderr, err := deployDir(t, fake, dir, stub); err != nil {
		t.Fatalf("deploy: %v\n%s\n%s", err, out, stderr)
	}
	if len(fake.regBuilds) != 1 {
		t.Fatalf("registers = %d, want 1", len(fake.regBuilds))
	}
	got := fake.regBuilds[0]
	want := map[string]string{
		"build_commit": sha, "build_repo": "https://github.com/acme/etl",
		"build_pipeline_url": "https://github.com/acme/etl/actions/runs/42",
	}
	for k, v := range want {
		if got.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, got.Get(k), v)
		}
	}
	if !strings.HasPrefix(got.Get("build_base_image"), publishedBaseRepo+":py") {
		t.Errorf("build_base_image = %q, want the generated Dockerfile's FROM", got.Get("build_base_image"))
	}
	if !strings.Contains(got.Get("build_builder"), "local-daemon") || got.Get("build_cli_version") == "" {
		t.Errorf("build_builder %q, build_cli_version %q: want the builder and the CLI version", got.Get("build_builder"), got.Get("build_cli_version"))
	}
}
