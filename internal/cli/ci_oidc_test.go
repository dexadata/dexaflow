package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	yaml "go.yaml.in/yaml/v3"
)

// ciOIDCOffer is a deployDefaults answer that offers one CI OIDC issuer per
// provider ci init writes a tokenless pipeline for.
const ciOIDCOffer = `{"ci_oidc":{"issuers":[` +
	`{"name":"github-acme","provider":"github","issuer":"https://token.actions.githubusercontent.com","audience":"https://dexaflow.acme.example"},` +
	`{"name":"gitlab-acme","provider":"gitlab","issuer":"https://gitlab.com","audience":"https://dexaflow.acme.example"},` +
	`{"name":"cloudbuild-acme","provider":"cloudbuild","issuer":"https://accounts.google.com","audience":"https://dexaflow.acme.example"}]}}`

// deployDefaultsServer is a control plane that answers GET
// /api/v2/deployDefaults with status and body, and records the bearer of
// each request.
type deployDefaultsServer struct {
	*httptest.Server
	mu      sync.Mutex
	bearers []string
}

func newDeployDefaultsServer(t *testing.T, status int, body string) *deployDefaultsServer {
	t.Helper()
	s := &deployDefaultsServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/deployDefaults" {
			http.NotFound(w, r)
			return
		}
		s.mu.Lock()
		s.bearers = append(s.bearers, r.Header.Get("Authorization"))
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body) //nolint:errcheck // test server
	}))
	t.Cleanup(s.Close)
	return s
}

// requests is how many times the server was asked.
func (s *deployDefaultsServer) requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.bearers)
}

// ciOIDCMarkers tell a tokenless pipeline from a token one, per system.
var ciOIDCMarkers = map[string]struct{ oidc, token string }{
	"github":     {"id-token: write", "secrets.DEXAFLOW_TOKEN"},
	"gitlab":     {"id_tokens:", "DEXAFLOW_TOKEN, masked and protected"},
	"cloudbuild": {"gcloud auth print-identity-token", "secretEnv: [DEXAFLOW_TOKEN]"},
	"generic":    {"dexaflow auth ci-token", "DEXAFLOW_TOKEN is not set"},
}

// TestCIInitChoosesTheAuth: with --auth auto (the default) ci init writes
// a tokenless pipeline when the server it deploys to offers a CI OIDC
// issuer for the system, and the deploy token path in every other case;
// --auth token never asks, and --auth oidc fails instead of falling back.
func TestCIInitChoosesTheAuth(t *testing.T) {
	cases := []struct {
		name, system string
		status       int
		body         string
		noServer     bool
		args         []string
		wantOIDC     bool
		wantErr      string
		wantNote     string
		wantNoAsk    bool
	}{
		{name: "github offered", system: "github", status: 200, body: ciOIDCOffer, wantOIDC: true},
		{name: "gitlab offered", system: "gitlab", status: 200, body: ciOIDCOffer, wantOIDC: true},
		{name: "cloudbuild offered", system: "cloudbuild", status: 200, body: ciOIDCOffer, wantOIDC: true},
		{name: "explicit oidc", system: "github", status: 200, body: ciOIDCOffer, args: []string{"--auth", "oidc"}, wantOIDC: true},
		{name: "other provider only", system: "github", status: 200,
			body:     `{"ci_oidc":{"issuers":[{"name":"gitlab-acme","provider":"gitlab","issuer":"https://gitlab.com","audience":"dx"}]}}`,
			wantNote: "no CI OIDC issuer for github"},
		{name: "nothing offered", system: "github", status: 200, body: `{"registry":"ghcr.io/acme"}`, wantNote: "no CI OIDC issuer for github"},
		{name: "older server", system: "github", status: 404, body: `{}`, wantNote: "deploy token"},
		{name: "not signed in", system: "github", status: 401, body: `{}`, wantNote: "deploy token"},
		{name: "no server", system: "github", noServer: true, wantNoAsk: true},
		{name: "generic", system: "generic", status: 200, body: ciOIDCOffer, wantNoAsk: true},
		{name: "explicit token", system: "github", status: 200, body: ciOIDCOffer, args: []string{"--auth", "token"}, wantNoAsk: true},
		{name: "unsafe audience", system: "github", status: 200,
			body:     `{"ci_oidc":{"issuers":[{"name":"gh","provider":"github","issuer":"https://token.actions.githubusercontent.com","audience":"a\" b"}]}}`,
			wantNote: "cannot be written into the pipeline"},
		{name: "unsafe name", system: "github", status: 200,
			body:     `{"ci_oidc":{"issuers":[{"name":"gh\nx","provider":"github","issuer":"https://token.actions.githubusercontent.com","audience":"dx"}]}}`,
			wantNote: "cannot be written into the pipeline"},
		{name: "older CLI", system: "gitlab", status: 200, body: ciOIDCOffer, args: []string{"--cli-version", "v0.5.4"}, wantNote: ciOIDCMinVersion},
		{name: "oidc not offered", system: "github", status: 200, body: `{}`, args: []string{"--auth", "oidc"}, wantErr: "no CI OIDC issuer for github"},
		{name: "oidc without server", system: "github", noServer: true, args: []string{"--auth", "oidc"}, wantErr: "DEXAFLOW_SERVER_URL"},
		{name: "oidc on generic", system: "generic", status: 200, body: ciOIDCOffer, args: []string{"--auth", "oidc"}, wantErr: "generic"},
		{name: "oidc older server", system: "github", status: 404, body: `{}`, args: []string{"--auth", "oidc"}, wantErr: "404"},
		{name: "oidc unsafe audience", system: "github", status: 200,
			body: `{"ci_oidc":{"issuers":[{"name":"gh","provider":"github","issuer":"https://x","audience":"a\" b"}]}}`,
			args: []string{"--auth", "oidc"}, wantErr: "cannot be written into the pipeline"},
		{name: "oidc older CLI", system: "gitlab", status: 200, body: ciOIDCOffer, args: []string{"--auth", "oidc", "--cli-version", "v0.5.4"}, wantErr: ciOIDCMinVersion},
		{name: "unknown mode", system: "github", status: 200, body: ciOIDCOffer, args: []string{"--auth", "maybe"}, wantErr: "--auth"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateCLIEnv(t)
			srv := newDeployDefaultsServer(t, tc.status, tc.body)
			if !tc.noServer {
				t.Setenv("DEXAFLOW_SERVER_URL", srv.URL)
				t.Setenv("DEXAFLOW_TOKEN", "dxf_cli_session")
			}
			root := ciWorkspace(t, "ghcr.io/acme/dags", "")
			d := testDeps()
			stubTagCommit(t, d)
			stubImageDigest(t, d)
			t.Chdir(root)
			args := append([]string{"ci", "init", tc.system, "--stdout", "--config", emptyConfig(t)}, tc.args...)
			if !strings.Contains(strings.Join(tc.args, " "), "--cli-version") {
				args = append(args, "--cli-version", ciGoldenVersion)
			}

			out, stderr, err := runDeps(t, d, args...)

			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one naming %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ci init: %v\n%s", err, stderr)
			}
			m := ciOIDCMarkers[tc.system]
			if got := strings.Contains(out, m.oidc); got != tc.wantOIDC {
				t.Errorf("tokenless pipeline = %v, want %v:\n%s", got, tc.wantOIDC, out)
			}
			if got := strings.Contains(out, m.token); got == tc.wantOIDC {
				t.Errorf("token path = %v, want %v:\n%s", got, !tc.wantOIDC, out)
			}
			if tc.wantOIDC && !strings.Contains(out, "https://dexaflow.acme.example") {
				t.Errorf("the pipeline does not ask for the issuer's audience:\n%s", out)
			}
			if tc.wantNote != "" && !strings.Contains(stderr, tc.wantNote) {
				t.Errorf("stderr = %q, want a note naming %q", stderr, tc.wantNote)
			}
			if tc.wantNoAsk && srv.requests() != 0 {
				t.Errorf("asked the server %d times, want never", srv.requests())
			}
			if !tc.wantNoAsk && !tc.noServer {
				if srv.requests() != 1 || srv.bearers[0] != "Bearer dxf_cli_session" {
					t.Errorf("deployDefaults requests %q, want one with the CLI's token (the list is per tenant)", srv.bearers)
				}
			}
		})
	}
}

// TestCIInitTokenPathIsUnchanged: when nothing is offered the files are
// byte for byte what --auth token writes, so existing pipelines and the
// golden files do not move.
func TestCIInitTokenPathIsUnchanged(t *testing.T) {
	for _, system := range []string{"github", "gitlab", "cloudbuild", "generic"} {
		isolateCLIEnv(t)
		root := ciWorkspace(t, "ghcr.io/acme/dags", "")
		auto, _, err := ciInit(t, root, system, "--stdout")
		if err != nil {
			t.Fatal(err)
		}
		token, _, err := ciInit(t, root, system, "--stdout", "--auth", "token")
		if err != nil {
			t.Fatal(err)
		}
		if auto != token {
			t.Errorf("%s: auto with no server differs from --auth token", system)
		}
	}
}

// fakeDexaflowOIDC is a dexaflow on PATH whose auth ci-token answers a
// session made from DEXAFLOW_OIDC_TOKEN (or fails with FAKE_EXCHANGE_FAIL),
// and whose deploy records the token it runs with.
const fakeDexaflowOIDC = `#!/bin/sh
case "$1" in
version) echo "dexaflow 0.6.0 (commit none, built unknown, go)";;
auth)
	[ "$2" = ci-token ] || exit 2
	echo "ci-token" >>"$FAKE_LOG"
	[ -z "${FAKE_EXCHANGE_FAIL:-}" ] || { echo "refused" >&2; exit 1; }
	echo "dxci_$DEXAFLOW_OIDC_TOKEN";;
deploy)
	echo "deploy token=$DEXAFLOW_TOKEN" >>"$FAKE_LOG"
	echo '{"dags":[]}';;
esac
`

// TestCIScriptExchangesTheOIDCToken runs the script of a tokenless
// pipeline against a fake CLI: the job's OIDC token is exchanged and the
// deploy runs with the session, a refused exchange stops the script before
// any deploy, a stored DEXAFLOW_TOKEN still works without the OIDC token,
// and LEOFLOW_OIDC_TOKEN is read too.
func TestCIScriptExchangesTheOIDCToken(t *testing.T) {
	for _, bin := range []string{"sh", "python3"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not installed")
		}
	}
	script := filepath.Join(t.TempDir(), "deploy.sh")
	writeTree(t, script, ciGolden(t)["github-oidc"][ciScriptPath])
	bin := fakeTool(t, "dexaflow", fakeDexaflowOIDC)
	writeExecutable(t, filepath.Join(bin, "uv"), "#!/bin/sh\n")
	run := func(env ...string) (string, error) {
		logPath := filepath.Join(t.TempDir(), "log")
		c := exec.CommandContext(t.Context(), "sh", script) //nolint:gosec // the script under test
		c.Dir = t.TempDir()
		c.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "FAKE_LOG="+logPath,
			"DEXAFLOW_SERVER_URL=https://pro.example", "DEXAFLOW_SUMMARY_FILE="+filepath.Join(t.TempDir(), "s.md"),
			"GITHUB_STEP_SUMMARY=", "DEXAFLOW_BASE_REF=", "DEXAFLOW_TOKEN=", "LEOFLOW_TOKEN=", "DEXAFLOW_OIDC_TOKEN=", "LEOFLOW_OIDC_TOKEN=")
		c.Env = append(c.Env, env...)
		out, err := c.CombinedOutput()
		l, _ := os.ReadFile(logPath) //nolint:errcheck // absent when nothing ran
		if err != nil {
			err = &scriptError{err: err, out: string(out)}
		}
		return strings.TrimSpace(string(l)), err
	}

	if log, err := run("DEXAFLOW_OIDC_TOKEN=job", "DEXAFLOW_TOKEN=dxf_stored"); err != nil || log != "ci-token\ndeploy token=dxci_job" {
		t.Errorf("with the OIDC token: err %v, ran %q, want the exchange then a deploy with the session", err, log)
	}
	if log, err := run("DEXAFLOW_OIDC_TOKEN=job", "FAKE_EXCHANGE_FAIL=1", "DEXAFLOW_TOKEN=dxf_stored"); err == nil || log != "ci-token" {
		t.Errorf("a refused exchange must stop the script before deploying: err %v, ran %q", err, log)
	}
	if log, err := run("DEXAFLOW_TOKEN=dxf_stored"); err != nil || log != "deploy token=dxf_stored" {
		t.Errorf("a stored token without the OIDC token: err %v, ran %q", err, log)
	}
	if log, err := run("LEOFLOW_OIDC_TOKEN=old"); err != nil || log != "ci-token\ndeploy token=dxci_old" {
		t.Errorf("LEOFLOW_OIDC_TOKEN: err %v, ran %q", err, log)
	}
	if log, err := run(); err == nil || log != "" || !strings.Contains(err.Error(), "neither DEXAFLOW_OIDC_TOKEN nor DEXAFLOW_TOKEN") {
		t.Errorf("without either token the script must stop before anything runs: err %v, ran %q", err, log)
	}
}

// TestCIGitHubAsksForTheIssuersAudience runs the deploy step of the
// tokenless workflow against a fake GitHub token endpoint: it asks for the
// entry's audience with the runner's request token, and hands the token it
// gets to the script as DEXAFLOW_OIDC_TOKEN.
func TestCIGitHubAsksForTheIssuersAudience(t *testing.T) {
	for _, bin := range []string{"sh", "python3"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not installed")
		}
	}
	var doc struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string            `yaml:"name"`
				Env  map[string]string `yaml:"env"`
				Run  string            `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
		Permissions map[string]string `yaml:"permissions"`
	}
	if err := yaml.Unmarshal([]byte(ciGolden(t)["github-oidc"][".github/workflows/dexaflow-deploy.yaml"]), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Permissions["id-token"] != "write" {
		t.Errorf("permissions = %v, want id-token: write", doc.Permissions)
	}
	var step string
	var env map[string]string
	for _, s := range doc.Jobs["deploy"].Steps {
		if strings.HasPrefix(s.Name, "Deploy") {
			step, env = s.Run, s.Env
		}
	}
	if _, stored := env["DEXAFLOW_TOKEN"]; stored || env["DEXAFLOW_OIDC_AUDIENCE"] != "https://dexaflow.acme.example" {
		t.Fatalf("deploy step env = %v, want the audience and no stored token", env)
	}
	var gotQuery, gotAuth string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery, gotAuth = r.URL.RawQuery, r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"value":"job-jwt"}`) //nolint:errcheck // test server
	}))
	t.Cleanup(provider.Close)
	dir := t.TempDir()
	writeTree(t, filepath.Join(dir, ciScriptPath), "#!/bin/sh\necho \"$DEXAFLOW_OIDC_TOKEN\" >\"$OUT\"\n")
	out := filepath.Join(dir, "out")
	cmd := exec.CommandContext(t.Context(), "sh", "-e", "-c", step) //nolint:gosec // the step under test
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "OUT="+out, "DEXAFLOW_OIDC_AUDIENCE="+env["DEXAFLOW_OIDC_AUDIENCE"],
		"ACTIONS_ID_TOKEN_REQUEST_URL="+provider.URL+"/token?api-version=2.0", "ACTIONS_ID_TOKEN_REQUEST_TOKEN=runner-secret")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the deploy step failed: %v\n%s", err, b)
	}
	if got, _ := os.ReadFile(out); strings.TrimSpace(string(got)) != "job-jwt" { //nolint:errcheck // checked here
		t.Errorf("DEXAFLOW_OIDC_TOKEN = %q, want the provider's token", got)
	}
	if gotQuery != "api-version=2.0&audience=https%3A%2F%2Fdexaflow.acme.example" || gotAuth != "bearer runner-secret" {
		t.Errorf("asked with query %q and Authorization %q", gotQuery, gotAuth)
	}
}

// TestCIInitTokenlessPipelinesStoreNoToken reads the GitLab and Cloud Build
// pipelines as their systems would: GitLab mints DEXAFLOW_OIDC_TOKEN for
// the entry's audience, and Cloud Build reads no secret.
func TestCIInitTokenlessPipelinesStoreNoToken(t *testing.T) {
	golden := ciGolden(t)
	var gitlab map[string]struct {
		IDTokens map[string]struct {
			Aud string `yaml:"aud"`
		} `yaml:"id_tokens"`
	}
	if err := yaml.Unmarshal([]byte(golden["gitlab-oidc"][".gitlab-ci.yml"]), &gitlab); err != nil {
		t.Fatal(err)
	}
	job := gitlab["dexaflow-deploy"]
	if got := job.IDTokens["DEXAFLOW_OIDC_TOKEN"].Aud; got != "https://dexaflow.acme.example" {
		t.Errorf("GitLab id_tokens = %+v, want DEXAFLOW_OIDC_TOKEN for the entry's audience", job.IDTokens)
	}
	var cloudbuild map[string]any
	if err := yaml.Unmarshal([]byte(golden["cloudbuild-oidc"]["cloudbuild.yaml"]), &cloudbuild); err != nil {
		t.Fatal(err)
	}
	steps, _ := cloudbuild["steps"].([]any) //nolint:errcheck // checked below
	if _, secrets := cloudbuild["availableSecrets"]; secrets || len(steps) != 1 {
		t.Fatalf("Cloud Build: availableSecrets %v, %d steps, want no secret and one step", secrets, len(steps))
	}
	if _, env := steps[0].(map[string]any)["secretEnv"]; env {
		t.Error("the Cloud Build step still reads secretEnv")
	}
}
