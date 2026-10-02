package cli

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/setup"
)

// devTestCmd returns a cobra command whose stdout/stderr are discarded, for
// exercising dev helpers without noisy output.
func devTestCmd() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	return cmd
}

func TestDevBannerMarksEnvironment(t *testing.T) {
	b := liteBanner("http://localhost:8080")
	if !strings.Contains(b, "LITE") {
		t.Errorf("banner must shout LITE, got:\n%s", b)
	}
	if !strings.Contains(b, "http://localhost:8080") {
		t.Errorf("banner must show the UI url, got:\n%s", b)
	}
	if !strings.Contains(b, "\x1b[") {
		t.Errorf("banner must be colored (ANSI), got:\n%s", b)
	}
}

func TestMtimesChangedDetectsEdits(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "dag.py")
	if err := os.WriteFile(f, []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	prev := projectMtimes([]string{f})

	// No change: same snapshot compares equal.
	if mtimesChanged(prev, projectMtimes([]string{f})) {
		t.Error("unchanged file reported as changed")
	}

	// Edit bumps the modtime (force it forward so the test is not flaky).
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(f, future, future); err != nil {
		t.Fatal(err)
	}
	if !mtimesChanged(prev, projectMtimes([]string{f})) {
		t.Error("edited file not detected")
	}
}

func TestMtimesChangedDetectsAddAndRemove(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "dexaflow.yaml")
	b := filepath.Join(dir, "dag.py")
	if err := os.WriteFile(a, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	prev := projectMtimes([]string{a, b}) // b absent

	if err := os.WriteFile(b, []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !mtimesChanged(prev, projectMtimes([]string{a, b})) {
		t.Error("newly-created file not detected")
	}
}

func TestResolveBinaryExplicitAndFallback(t *testing.T) {
	// Explicit wins.
	if got, err := resolveBinary("/custom/leoflow-server", "leoflow-server"); err != nil || got != "/custom/leoflow-server" {
		t.Errorf("explicit = (%q,%v), want /custom/leoflow-server", got, err)
	}
	// ./bin fallback: chdir into a temp dir holding bin/<name>.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	name := "leoflow-fake-bin"
	if err := os.WriteFile(filepath.Join(dir, "bin", name), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	// The fallback must be absolute: the subprocess executor runs the agent in a
	// different working directory, so a relative path would not resolve.
	got, err := resolveBinary("", name)
	if err != nil || !filepath.IsAbs(got) || filepath.Base(got) != name {
		t.Errorf("fallback = (%q,%v), want an absolute path ending in %s", got, err, name)
	}
	// Not found anywhere → actionable error.
	if _, err := resolveBinary("", "definitely-not-a-real-binary-xyz"); err == nil {
		t.Error("expected error for a missing binary")
	}
}

// TestWorkspaceWatchPaths replaces the deleted TestDevWatchPaths. The
// watcher now walks a workspace (Phase 3) instead of a single project pair,
// so the contract is "every project's yaml+source + the workspace root".
func TestWorkspaceWatchPaths(t *testing.T) {
	ws := &WorkspaceSpec{
		Path: "ws",
		Projects: []Project{
			{
				Path:       filepath.Join("ws", "etl"),
				DagID:      "etl",
				ConfigPath: filepath.Join("ws", "etl", "dexaflow.yaml"),
				HasYAML:    true,
				Config:     &domain.LeoflowConfig{DagSource: "dag.py"},
			},
		},
	}
	got := workspaceWatchPaths(ws)
	want := []string{
		filepath.Join("ws", "etl", "dexaflow.yaml"),
		filepath.Join("ws", "etl", "dag.py"),
		"ws", // workspace root, so a new subdir nudges mtime
	}
	if len(got) != len(want) {
		t.Fatalf("watch paths = %v, want %v", got, want)
	}
	seen := map[string]bool{}
	for _, p := range got {
		seen[p] = true
	}
	for _, w := range want {
		if !seen[w] {
			t.Errorf("missing watched path %q (got %v)", w, got)
		}
	}
}

func TestDevReadyOnce(t *testing.T) {
	ctx := context.Background()
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ok.Close()
	if !devReadyOnce(ctx, ok.URL) {
		t.Error("ready server should report ready")
	}

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	down.Close() // closed: connection refused
	if devReadyOnce(ctx, down.URL) {
		t.Error("closed server should not report ready")
	}
}

func TestWaitForReadyReturnsWhenUp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if err := waitForReady(context.Background(), srv.URL); err != nil {
		t.Errorf("waitForReady = %v, want nil", err)
	}
}

func TestWaitForReadyHonorsCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Nothing is listening here and the context is already canceled.
	if err := waitForReady(ctx, "http://127.0.0.1:0"); err == nil {
		t.Error("expected error when context is canceled")
	}
}

func TestNewDevCommandFlagDefaults(t *testing.T) {
	cmd := newLiteCommand()
	for flag, want := range map[string]string{
		"compose": "", // empty = a managed compose under ~/.leoflow, materialized on first run
		"host":    "127.0.0.1",
		"image":   "leoflow-dev:local",
	} {
		if got, _ := cmd.Flags().GetString(flag); got != want {
			t.Errorf("--%s default = %q, want %q", flag, got, want)
		}
	}
}

func TestDevPrintHelpers(t *testing.T) {
	var b bytes.Buffer
	devPrintf(&b, "x=%d", 7)
	devPrintln(&b, "line")
	if b.String() != "x=7line\n" {
		t.Errorf("print helpers wrote %q", b.String())
	}
}

func TestRunDevValidatesProject(t *testing.T) {
	cmd := devTestCmd()
	// A discoverable project (it has a dag.py) whose dexaflow.yaml is invalid: an
	// unknown alert type the schema rejects. runDev validates every project up
	// front — prepareWorkspace is its first step — so it must refuse before it
	// touches any infrastructure. This stays at the validation gate on purpose:
	// letting runDev proceed would start Postgres and a companion server and hang
	// the test, exactly the host-dependent boot that hid behind the #699 panic.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "dag.py"), []byte("# dag\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	invalid := "dag_id: sales\nalerts:\n  on_failure:\n    - type: carrier-pigeon\n      conn: x\n"
	if err := os.WriteFile(filepath.Join(dir, "dexaflow.yaml"), []byte(invalid), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runDev(cmd, dir, devOptions{}); err == nil {
		t.Error("expected runDev to refuse a project with an invalid dexaflow.yaml")
	}
}

func TestStartDevServerStartsAndErrors(t *testing.T) {
	cmd := devTestCmd()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A real, harmless binary starts successfully and a *Cmd is returned.
	srv, err := startDevServer(ctx, cmd, "/bin/sleep", subprocessServerEnv(liteEnvParams{host: "127.0.0.1", port: 8088, adminHash: "", adminEmail: "", jwtSecret: ""}, "/bin/true", t.TempDir(), "python3", t.TempDir()))
	if err != nil || srv == nil {
		t.Fatalf("startDevServer(real bin) = (%v,%v), want a running cmd", srv, err)
	}
	cancel()
	_ = srv.Wait()

	// A nonexistent binary fails at Start.
	if _, e := startDevServer(context.Background(), cmd, "/no/such/leoflow-server", sharedServerEnv(liteEnvParams{host: "127.0.0.1", port: 8088, adminHash: "", adminEmail: "", jwtSecret: ""})); e == nil {
		t.Error("expected error starting a nonexistent server binary")
	}
}

func TestDevWatchLoopExitsOnCancel(t *testing.T) {
	cmd := devTestCmd()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled: the loop must do its initial pass then return nil

	dir := t.TempDir()
	ws := &WorkspaceSpec{Path: dir, RootCfg: &domain.LeoflowConfig{}}
	ws.RootCfg.ApplyDefaults()
	reloads := 0
	reload := func() error { reloads++; return nil }
	if err := devWatchLoop(ctx, cmd, ws, reload, nil, nil); err != nil {
		t.Errorf("devWatchLoop on canceled ctx = %v, want nil", err)
	}
	if reloads != 1 {
		t.Errorf("reload ran %d times, want 1 initial pass", reloads)
	}
}

func TestVenvPythonOSAware(t *testing.T) {
	home := filepath.FromSlash("/home/u/.leoflow/dev")
	got := venvPython(home)
	want := filepath.Join(home, "venv", "bin", "python")
	if runtime.GOOS == "windows" {
		want = filepath.Join(home, "venv", "Scripts", "python.exe")
	}
	if got != want {
		t.Errorf("venvPython = %q, want %q", got, want)
	}
}

func TestDevDagImageRef(t *testing.T) {
	if got := devDagImageRef("my_etl"); got != "leoflow-dev-my_etl:dev" {
		t.Errorf("devDagImageRef = %q, want leoflow-dev-my_etl:dev", got)
	}
}

func TestDevDockerfile(t *testing.T) {
	df, derr := devDockerfile("leoflow-base:py3.11", "dag.py", nil, nil)
	if derr != nil {
		t.Fatal(derr)
	}
	for _, must := range []string{"FROM leoflow-base:py3.11", "COPY dag.py", "PYTHONPATH"} {
		if !strings.Contains(df, must) {
			t.Errorf("generated Dockerfile missing %q:\n%s", must, df)
		}
	}
	if strings.Contains(df, "pip install") {
		t.Errorf("no deps -> no pip install line:\n%s", df)
	}
	// Declared dependencies are pip-installed before COPY (cached layer).
	withDeps, werr := devDockerfile("leoflow-base:py3.11", "dag.py", []string{"duckdb==1.1.3", "pandas"}, nil)
	if werr != nil {
		t.Fatal(werr)
	}
	// The specifiers are quoted and the option list terminated, the same as the
	// compile generator does. This assertion used to pin the RAW join, which is
	// the #1064 shape: an entry beginning with a dash stayed an option to pip.
	if !strings.Contains(withDeps, "RUN pip install --no-cache-dir -- 'duckdb==1.1.3' 'pandas'") {
		t.Errorf("deps not installed in Dockerfile:\n%s", withDeps)
	}
	if strings.Index(withDeps, "pip install") > strings.Index(withDeps, "COPY") {
		t.Errorf("pip install must come before COPY for layer caching:\n%s", withDeps)
	}
}

func TestMergeLiteDefaults(t *testing.T) {
	cfg := &config.Config{LiteExecutor: "subprocess", LitePort: 9091}

	t.Run("applies config when flags unset", func(t *testing.T) {
		o := devOptions{executor: "k8s", port: 8088}
		mergeLiteDefaults(&o, cfg, false, false)
		if o.executor != "subprocess" || o.port != 9091 {
			t.Errorf("got executor=%q port=%d, want subprocess/9091", o.executor, o.port)
		}
	})

	t.Run("command-line flags win", func(t *testing.T) {
		o := devOptions{executor: "k8s", port: 8088}
		mergeLiteDefaults(&o, cfg, true, true)
		if o.executor != "k8s" || o.port != 8088 {
			t.Errorf("got executor=%q port=%d, want the flag values kept", o.executor, o.port)
		}
	})

	t.Run("nil/empty config is a no-op", func(t *testing.T) {
		o := devOptions{executor: "k8s", port: 8088}
		mergeLiteDefaults(&o, nil, false, false)
		mergeLiteDefaults(&o, &config.Config{}, false, false)
		if o.executor != "k8s" || o.port != 8088 {
			t.Errorf("got executor=%q port=%d, want unchanged", o.executor, o.port)
		}
	})
}

func TestServerEnvBuilders(t *testing.T) {
	sub := strings.Join(subprocessServerEnv(liteEnvParams{host: "127.0.0.1", port: 8088, adminHash: "", adminEmail: "", jwtSecret: ""}, "/bin/agent", "/proj", "/venv/py", "/h/venvs"), "\n")
	// Lite binds its own HTTP/gRPC/metrics ports (distinct from the demo's 8080/9090/9091).
	for _, must := range []string{"LEOFLOW_EXECUTOR_TYPE=subprocess", "LEOFLOW_EXECUTOR_AGENT_PATH=/bin/agent", "LEOFLOW_EXECUTOR_SUBPROCESS_WORKDIR=/proj", "LEOFLOW_PYTHON=/venv/py", "127.0.0.1:9099", "LEOFLOW_SERVER_HTTP_ADDR=127.0.0.1:8088", "LEOFLOW_SERVER_GRPC_ADDR=:9099", "LEOFLOW_SERVER_METRICS_ADDR=:9098"} {
		if !strings.Contains(sub, must) {
			t.Errorf("subprocessServerEnv missing %q", must)
		}
	}
	clu := strings.Join(clusterServerEnv(liteEnvParams{host: "127.0.0.1", port: 8088, adminHash: "", adminEmail: "", jwtSecret: ""}, "/home/u/.leoflow/dev/kubeconfig"), "\n")
	for _, must := range []string{"LEOFLOW_EXECUTOR_TYPE=kubernetes", "KUBECONFIG=/home/u/.leoflow/dev/kubeconfig", "LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR=" + devHostGRPCAddr(8088)} {
		if !strings.Contains(clu, must) {
			t.Errorf("clusterServerEnv missing %q", must)
		}
	}
	// gRPC/metrics derive from --port so two Lite instances coexist: a different
	// --port must yield different gRPC/metrics ports (and OTel is off in Lite).
	if !strings.Contains(sub, "LEOFLOW_OBSERVABILITY_OTEL_ENABLED=false") {
		t.Error("Lite env should disable the OTLP exporter (no local collector)")
	}
	alt := strings.Join(subprocessServerEnv(liteEnvParams{host: "127.0.0.1", port: 8090, adminHash: "", adminEmail: "", jwtSecret: ""}, "/bin/agent", "/proj", "/venv/py", "/h/venvs"), "\n")
	for _, must := range []string{"LEOFLOW_SERVER_GRPC_ADDR=:9101", "LEOFLOW_SERVER_METRICS_ADDR=:9100", "127.0.0.1:9101"} {
		if !strings.Contains(alt, must) {
			t.Errorf("--port 8090 should offset gRPC/metrics, missing %q", must)
		}
	}
	// Both carry the shared settings (isolated DB + LITE badge). The DSN
	// itself is resolved at runtime (devDSNs() detects managed-socket vs
	// docker-tcp + reads the persisted db-port), so assert the key prefix and
	// the database name, not the full URL — otherwise the test couples to the
	// caller's filesystem state and fails on CI runners that have a stale
	// managed-socket fixture from a sibling test.
	if !strings.Contains(clu, "LEOFLOW_DATABASE_URL=") || !strings.Contains(clu, "/"+devDBName) || !strings.Contains(clu, "LEOFLOW_UI_EDITION=lite") {
		t.Error("clusterServerEnv missing shared settings (LEOFLOW_DATABASE_URL pointing at the dev DB + LITE badge)")
	}
}

func TestSharedServerEnvAuthModes(t *testing.T) {
	// No admin configured -> dev no-auth fallback (loopback only).
	noAdmin := strings.Join(sharedServerEnv(liteEnvParams{host: "127.0.0.1", port: 8088, adminHash: "", adminEmail: "", jwtSecret: ""}), "\n")
	if !strings.Contains(noAdmin, "LEOFLOW_AUTH_DEV_NO_AUTH=true") {
		t.Error("with no admin, expected the dev no-auth fallback")
	}
	if strings.Contains(noAdmin, "LEOFLOW_BOOTSTRAP_PASSWORD_HASH") {
		t.Error("no admin should not set a bootstrap hash")
	}
	// Admin hash configured -> real auth: bootstrap the admin, NO bypass.
	withAdmin := strings.Join(sharedServerEnv(liteEnvParams{host: "127.0.0.1", port: 8088, adminHash: "$2a$12$hash", adminEmail: "admin@leoflow.local", jwtSecret: ""}), "\n")
	if strings.Contains(withAdmin, "LEOFLOW_AUTH_DEV_NO_AUTH") {
		t.Error("with an admin hash, the dev no-auth bypass must be OFF")
	}
	for _, must := range []string{"LEOFLOW_BOOTSTRAP_PASSWORD_HASH=$2a$12$hash", "LEOFLOW_BOOTSTRAP_EMAIL=admin@leoflow.local", "LEOFLOW_UI_EDITION=lite"} {
		if !strings.Contains(withAdmin, must) {
			t.Errorf("real-auth env missing %q", must)
		}
	}
	// Lite mints 30-day sessions (not the server's 1h default) so the user is not
	// silently logged out mid-session.
	for _, env := range []string{noAdmin, withAdmin} {
		if !strings.Contains(env, "LEOFLOW_AUTH_JWT_TOKEN_TTL_SECONDS=2592000") {
			t.Errorf("lite env missing 30-day token TTL; got:\n%s", env)
		}
		// A local single-user tool gets a generous login rate limit so fat-fingering
		// the password does not lock the user out.
		if !strings.Contains(env, "LEOFLOW_AUTH_LOGIN_RATE_LIMIT_PER_MINUTE=30") {
			t.Errorf("lite env missing generous login rate limit; got:\n%s", env)
		}
		// Logs go under the per-user ~/.leoflow, NOT a shared /tmp path that
		// collides across users (root vs non-root permission-denied trap).
		if !strings.Contains(env, filepath.Join(".dexaflow", "dev", "logs")) {
			t.Errorf("logs dir must be per-user under ~/.dexaflow/dev/logs; got:\n%s", env)
		}
		if strings.Contains(env, "leoflow-dev-logs") {
			t.Errorf("logs dir must not use the shared /tmp path; got:\n%s", env)
		}
	}
}

func TestLiteEditorEnv(t *testing.T) {
	env := strings.Join(liteEditorEnv("/home/u/proj", "/home/u/.leoflow"), "\n")
	if !strings.Contains(env, "LEOFLOW_UI_WORKSPACE=/home/u/proj") {
		t.Errorf("editor env missing workspace; got %q", env)
	}
	wantMonaco := "LEOFLOW_UI_MONACO_DIR=" + setup.MonacoDir("/home/u/.leoflow")
	if !strings.Contains(env, wantMonaco) {
		t.Errorf("editor env missing %q; got %q", wantMonaco, env)
	}
}

func TestK3dArgBuilders(t *testing.T) {
	if got := strings.Join(k3dCreateArgs("leoflow-dev"), " "); got != "cluster create leoflow-dev --wait" {
		t.Errorf("k3dCreateArgs = %q", got)
	}
	if got := strings.Join(k3dImportArgs("leoflow-dev", "base:1", "dag:dev"), " "); got != "image import base:1 dag:dev --cluster leoflow-dev" {
		t.Errorf("k3dImportArgs = %q", got)
	}
}

func TestDevKubeconfigPath(t *testing.T) {
	home := filepath.FromSlash("/home/u/.leoflow/dev")
	if got := devKubeconfigPath(home); got != filepath.Join(home, "kubeconfig") {
		t.Errorf("devKubeconfigPath = %q", got)
	}
}

func TestEnsureProjectDockerfile(t *testing.T) {
	cmd := devTestCmd()
	cfg := &domain.LeoflowConfig{DagID: "p", DagSource: "dag.py"}

	// Generates one when absent.
	dir := t.TempDir()
	if err := ensureProjectDockerfile(cmd, dir, cfg); err != nil {
		t.Fatalf("ensureProjectDockerfile: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	if err != nil {
		t.Fatalf("Dockerfile not generated: %v", err)
	}
	if !strings.Contains(string(data), "FROM "+devBaseImage) {
		t.Errorf("generated Dockerfile = %q", data)
	}

	// Leaves an existing Dockerfile untouched.
	dir2 := t.TempDir()
	custom := []byte("FROM my/custom:image\n")
	if werr := os.WriteFile(filepath.Join(dir2, "Dockerfile"), custom, 0o600); werr != nil {
		t.Fatal(werr)
	}
	if err := ensureProjectDockerfile(cmd, dir2, cfg); err != nil {
		t.Fatalf("ensureProjectDockerfile (existing): %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir2, "Dockerfile"))
	if !bytes.Equal(got, custom) {
		t.Errorf("existing Dockerfile was overwritten: %q", got)
	}
}

func TestDevSubprocessSetupMissingAgent(t *testing.T) {
	// The comment used to promise "no agent on PATH or ./bin" while only changing
	// the working directory, so on any machine with leoflow installed the agent
	// WAS on PATH and this test failed — green in CI, red for every developer who
	// had run the installer. A test that is red locally for an unrelated reason
	// trains people to ignore a red suite, which is how a real failure gets waved
	// through. Isolate all three lookup locations, not one.
	t.Chdir(t.TempDir())          // nothing in ./bin
	t.Setenv("PATH", t.TempDir()) // nothing on PATH
	t.Setenv("HOME", t.TempDir()) // nothing in ~/.dexaflow/bin
	cmd := devTestCmd()
	ws := &WorkspaceSpec{Path: ".", RootCfg: &domain.LeoflowConfig{DagID: "p"}}
	ws.RootCfg.ApplyDefaults()
	if _, _, err := devSubprocessSetup(context.Background(), cmd, ws, devOptions{}, t.TempDir()); err == nil {
		t.Error("expected error when the agent binary is missing")
	}
}

func TestBaseImageBuildArgs(t *testing.T) {
	got := strings.Join(baseImageBuildArgs(), " ")
	for _, must := range []string{"build", "runtime/Dockerfile", "PYTHON_VERSION=" + devPyVersion, devBaseImage} {
		if !strings.Contains(got, must) {
			t.Errorf("baseImageBuildArgs %q missing %q", got, must)
		}
	}
}

func TestKubectlNamespaceArgs(t *testing.T) {
	got := strings.Join(kubectlNamespaceArgs("/kc"), " ")
	if got != "--kubeconfig /kc create namespace "+devNamespace {
		t.Errorf("kubectlNamespaceArgs = %q", got)
	}
}

func TestDevClusterSetupStubbed(t *testing.T) {
	// Cluster mode builds the base image from runtime/Dockerfile (CWD context);
	// run from a tree that has it so ensureBaseImage's source-tree precheck passes.
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "runtime"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "runtime", "Dockerfile"), []byte("FROM scratch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(src)

	origRun, origOut := devRun, devOutput
	defer func() { devRun, devOutput = origRun, origOut }()
	var runs []string
	devRun = func(_ context.Context, _ *cobra.Command, name string, args ...string) error {
		runs = append(runs, name+" "+strings.Join(args, " "))
		return nil
	}
	devOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "docker": // image inspect → "absent" so the base image is built
			return nil, errors.New("no such image")
		case name == "k3d" && len(args) > 1 && args[1] == "list": // cluster absent → create
			return []byte(""), nil
		case name == "k3d": // kubeconfig get
			return []byte("apiVersion: v1\nkind: Config\n"), nil
		case name == "kubectl":
			return []byte("namespace/leoflow created"), nil
		}
		return nil, nil
	}

	home, dir := t.TempDir(), t.TempDir()
	cfg := &domain.LeoflowConfig{DagID: "etl", DagSource: "dag.py"}
	cfg.ApplyDefaults()
	// Cluster mode requires a single-project workspace (v1 scope); build one
	// with just the cfg above. The dir holds the project (Dockerfile is
	// written into it by ensureProjectDockerfile inside devClusterSetup).
	ws := &WorkspaceSpec{
		Path:    dir,
		RootCfg: cfg,
		Projects: []Project{{
			Path:       dir,
			DagID:      "etl",
			ConfigPath: filepath.Join(dir, "dexaflow.yaml"),
			HasYAML:    true,
			Config:     cfg,
		}},
	}
	env, makeReload, err := devClusterSetup(context.Background(), devTestCmd(), ws, devOptions{}, home)
	if err != nil {
		t.Fatalf("devClusterSetup: %v", err)
	}
	if makeReload == nil || makeReload(func() string { return "tok" }) == nil {
		t.Fatal("expected a reload factory")
	}
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "LEOFLOW_EXECUTOR_TYPE=kubernetes") || !strings.Contains(joined, "KUBECONFIG="+devKubeconfigPath(home)) {
		t.Errorf("cluster env did not target the isolated cluster: %v", env)
	}
	if _, e := os.Stat(devKubeconfigPath(home)); e != nil {
		t.Error("isolated kubeconfig was not written")
	}
	if _, e := os.Stat(filepath.Join(dir, "Dockerfile")); e != nil {
		t.Error("default Dockerfile was not generated")
	}
	// The base image build and cluster create were invoked.
	if !strings.Contains(strings.Join(runs, "\n"), "docker build") || !strings.Contains(strings.Join(runs, "\n"), "cluster create") {
		t.Errorf("expected docker build + k3d create, got: %v", runs)
	}
}

func TestK3dImportStubbed(t *testing.T) {
	origRun := devRun
	defer func() { devRun = origRun }()
	var got string
	devRun = func(_ context.Context, _ *cobra.Command, name string, args ...string) error {
		got = name + " " + strings.Join(args, " ")
		return nil
	}
	if err := k3dImport(context.Background(), devTestCmd(), "leoflow-dev-etl:dev"); err != nil {
		t.Fatalf("k3dImport: %v", err)
	}
	if !strings.Contains(got, "image import leoflow-dev-etl:dev") || !strings.Contains(got, "--cluster "+devClusterName) {
		t.Errorf("k3dImport invoked %q", got)
	}
}

// resolveBinary used to consult PATH before anything else, so `dexaflow lite` ran
// whatever leoflow-server happened to be installed first — however old. A
// validation run against v0.1.2-rc.1 spent its first boot exercising a
// v0.1.0-rc.4 server that predated every feature under test, and only noticed
// afterwards. The binaries are co-versioned (ADR 0028), so the one shipped
// beside the running CLI is the one it was built and tested against; an older
// copy earlier on PATH is never the intended answer (#471).
func TestResolveBinaryPrefersTheSiblingOverPATH(t *testing.T) {
	name := "leoflow-sibling-probe"
	// A stale copy on PATH, which the old order would have returned.
	pathDir := t.TempDir()
	writeFakeBinary(t, filepath.Join(pathDir, name))
	t.Setenv("PATH", pathDir)

	// The one shipped beside the running executable.
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable unavailable: %v", err)
	}
	sibling := filepath.Join(filepath.Dir(exe), name)
	writeFakeBinary(t, sibling)
	t.Cleanup(func() { _ = os.Remove(sibling) })
	// resolveBinary canonicalises through EvalSymlinks, and on macOS /var is a
	// symlink to /private/var — compare canonical forms, not the raw strings.
	if resolved, rerr := filepath.EvalSymlinks(sibling); rerr == nil {
		sibling = resolved
	}

	got, err := resolveBinary("", name)
	if err != nil {
		t.Fatalf("resolveBinary: %v", err)
	}
	if got != sibling {
		t.Errorf("resolveBinary = %q, want the sibling %q — a stale copy on PATH must not win", got, sibling)
	}
}

// With no sibling present, the installer's directory is consulted before PATH:
// `curl | sh` puts the matching trio in ~/.dexaflow/bin, and those belong
// together too.
func TestResolveBinaryPrefersInstallDirOverPATH(t *testing.T) {
	name := "leoflow-installdir-probe"
	pathDir := t.TempDir()
	writeFakeBinary(t, filepath.Join(pathDir, name))
	t.Setenv("PATH", pathDir)

	home := t.TempDir()
	t.Setenv("HOME", home)
	installed := filepath.Join(home, ".dexaflow", "bin", name)
	if err := os.MkdirAll(filepath.Dir(installed), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeBinary(t, installed)

	got, err := resolveBinary("", name)
	if err != nil {
		t.Fatalf("resolveBinary: %v", err)
	}
	if got != installed {
		t.Errorf("resolveBinary = %q, want the install dir %q", got, installed)
	}
}

// PATH still works when nothing better exists — this must not become a
// regression for anyone relying on it.
func TestResolveBinaryStillFallsBackToPATH(t *testing.T) {
	name := "leoflow-pathonly-probe"
	pathDir := t.TempDir()
	want := filepath.Join(pathDir, name)
	writeFakeBinary(t, want)
	t.Setenv("PATH", pathDir)
	t.Setenv("HOME", t.TempDir())

	got, err := resolveBinary("", name)
	if err != nil {
		t.Fatalf("resolveBinary: %v", err)
	}
	if got != want {
		t.Errorf("resolveBinary = %q, want %q from PATH", got, want)
	}
}

// A directory named like the binary must not be mistaken for it.
func TestResolveBinaryIgnoresDirectories(t *testing.T) {
	name := "leoflow-dir-probe"
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".dexaflow", "bin", name), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveBinary("", name); err == nil {
		t.Error("a directory with the binary's name must not resolve as the binary")
	}
}

// TestCompanionVersionNilContextDoesNotPanic locks the #699 regression: the
// best-effort companion version check must never panic the CLI. A nil parent
// reaching context.WithTimeout aborts ("cannot create context from nil parent")
// and takes the whole internal/cli test binary down with it, hiding every other
// test in the package. companionVersion is documented best-effort, so an
// unresolvable binary simply yields "" — never a panic.
func TestCompanionVersionNilContextDoesNotPanic(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "leoflow-server")
	//nolint:staticcheck // SA1012: passing a nil context is exactly the guarded case under test.
	if got := companionVersion(nil, missing); got != "" {
		t.Errorf("companionVersion with a nil context: got %q, want \"\"", got)
	}
}

func writeFakeBinary(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestDevDockerfileCopiesDbtGroupProjects: the `dexaflow lite` cluster path had
// the same #20 gap as the compile path — it layered only the DAG source, so a
// hybrid DAG's dbt task groups ran against project directories that were not in
// the image. The Lite subprocess loop reads from disk and never notices, which
// is exactly why this went unseen: the gap only appears once something builds.
func TestDevDockerfileCopiesDbtGroupProjects(t *testing.T) {
	cfg := &domain.LeoflowConfig{DbtGroups: map[string]*domain.DbtConfig{
		"transform": {Project: "./transform"},
		"marketing": {Project: "marketing"},
	}}
	df, derr := devDockerfile("leoflow-base:py3.11", "dag.py", nil, dbtGroupProjectDirs(cfg))
	if derr != nil {
		t.Fatal(derr)
	}
	for _, want := range []string{
		"COPY dag.py /home/leoflow/dag.py",
		"COPY marketing /home/leoflow/marketing",
		"COPY transform /home/leoflow/transform",
		"ENV PYTHONPATH=/home/leoflow",
	} {
		if !strings.Contains(df, want) {
			t.Errorf("devDockerfile() missing %q, got:\n%s", want, df)
		}
	}
	// Sorted, for the same reproducibility reason as the compile path.
	if strings.Index(df, "COPY marketing") > strings.Index(df, "COPY transform") {
		t.Errorf("devDockerfile() emits group COPYs out of sorted order:\n%s", df)
	}
}

// Companion binaries are named dexaflow-* since the rename; installs and
// builds from before it ship leoflow-*. Both must resolve, the new name wins
// when both sit in the same place, and location still beats name: a co-versioned
// legacy sibling is preferred over a new-name copy that is merely on PATH.
func TestResolveCompanionAcceptsBothNames(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	t.Run("legacy only", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("PATH", dir)
		legacy := filepath.Join(dir, "leoflow-compatprobe")
		writeFakeBinary(t, legacy)
		got, err := resolveCompanion("", "compatprobe")
		if err != nil || got != legacy {
			t.Fatalf("resolveCompanion = %q, %v; want %q", got, err, legacy)
		}
	})

	t.Run("both in one place prefers the new name", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("PATH", dir)
		writeFakeBinary(t, filepath.Join(dir, "leoflow-compatprobe"))
		current := filepath.Join(dir, "dexaflow-compatprobe")
		writeFakeBinary(t, current)
		got, err := resolveCompanion("", "compatprobe")
		if err != nil || got != current {
			t.Fatalf("resolveCompanion = %q, %v; want %q", got, err, current)
		}
	})

	t.Run("legacy install dir beats new name on PATH", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		installed := filepath.Join(home, ".dexaflow", "bin", "leoflow-compatprobe")
		if err := os.MkdirAll(filepath.Dir(installed), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFakeBinary(t, installed)
		pathDir := t.TempDir()
		writeFakeBinary(t, filepath.Join(pathDir, "dexaflow-compatprobe"))
		t.Setenv("PATH", pathDir)
		got, err := resolveCompanion("", "compatprobe")
		if err != nil || got != installed {
			t.Fatalf("resolveCompanion = %q, %v; want %q", got, err, installed)
		}
	})

	t.Run("new install dir is searched", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("PATH", t.TempDir())
		installed := filepath.Join(home, ".dexaflow", "bin", "dexaflow-compatprobe")
		if err := os.MkdirAll(filepath.Dir(installed), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFakeBinary(t, installed)
		got, err := resolveCompanion("", "compatprobe")
		if err != nil || got != installed {
			t.Fatalf("resolveCompanion = %q, %v; want %q", got, err, installed)
		}
	})

	t.Run("explicit path wins", func(t *testing.T) {
		got, err := resolveCompanion("/custom/agent", "compatprobe")
		if err != nil || got != "/custom/agent" {
			t.Fatalf("resolveCompanion = %q, %v", got, err)
		}
	})
}
