package cli

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/build"
	"github.com/dexadata/dexaflow/internal/domain"
)

const (
	indexYAML = "build:\n  package_index:\n    url: https://pypi.example.com/simple\n" +
		"    username_env: TEST_PYPI_USER\n    password_env: TEST_PYPI_TOKEN\n"
	indexPassword = "s3cr3t-index-token"
)

func lookupFrom(env map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := env[k]; return v, ok }
}

// With a package index the generated pip install reads the index URL from a
// build secret, mounted for that one RUN, so the URL and its credentials
// never land in a layer, an ENV or the image history.
func TestGeneratedDockerfileInstallsFromThePackageIndexSecret(t *testing.T) {
	t.Parallel()
	cfg := &domain.LeoflowConfig{DagID: "etl", PythonVersion: "3.12", Dependencies: []string{"pandas==2.2.3"},
		Build: &domain.BuildConfig{PackageIndex: &domain.PackageIndex{URL: "https://pypi.example.com/simple"}}}
	want := `RUN --mount=type=secret,id=dexaflow_index_url,required=true PIP_INDEX_URL="$(cat /run/secrets/dexaflow_index_url)" pip install --no-cache-dir -- 'pandas==2.2.3'`
	df, err := generatedDockerfile(cfg, "dag.py")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(df, want+"\n") || strings.Contains(df, "pypi.example.com") {
		t.Errorf("generated Dockerfile:\n%s\nwant the pip install line %s and no index URL", df, want)
	}
	deps, err := dependencyDockerfile(cfg, "ghcr.io/dexadata/dexaflow-runtime@sha256:00")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(deps, want+"\n") {
		t.Errorf("dependency Dockerfile:\n%s\nwant the pip install line %s", deps, want)
	}
	cfg.Build = nil
	df, err = generatedDockerfile(cfg, "dag.py")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(df, "RUN pip install --no-cache-dir -- 'pandas==2.2.3'\n") || strings.Contains(df, "--mount") {
		t.Errorf("without an index the pip install is unchanged:\n%s", df)
	}
}

// The inputs of a build: the args as declared, each secret by the variable
// that holds it, and the index URL composed from its credentials, handed to
// the tool through one variable of its environment.
func TestResolveBuildInputs(t *testing.T) {
	t.Parallel()
	cfg := &domain.LeoflowConfig{Build: &domain.BuildConfig{
		Args:    map[string]string{"APP_ENV": "prod"},
		Secrets: []domain.BuildSecret{{ID: "github_token", Env: "GITHUB_TOKEN"}},
		PackageIndex: &domain.PackageIndex{URL: "https://pypi.example.com/simple",
			UsernameEnv: "TEST_PYPI_USER", PasswordEnv: "TEST_PYPI_TOKEN"},
	}}
	in := resolveBuildInputs(cfg, lookupFrom(map[string]string{"GITHUB_TOKEN": "gh", "TEST_PYPI_USER": "ci", "TEST_PYPI_TOKEN": "tok"}))
	if in.err != nil {
		t.Fatal(in.err)
	}
	wantSecrets := []build.Secret{{ID: "github_token", Env: "GITHUB_TOKEN"}, {ID: domain.IndexSecretID, Env: indexSecretEnv}}
	if !slices.Equal(in.secrets, wantSecrets) {
		t.Errorf("secrets = %+v, want %+v", in.secrets, wantSecrets)
	}
	if in.indexURL != "https://ci:tok@pypi.example.com/simple" || !slices.Equal(in.env, []string{indexSecretEnv + "=" + in.indexURL}) {
		t.Errorf("index=%q env=%q", in.indexURL, in.env)
	}
	if in.args["APP_ENV"] != "prod" {
		t.Errorf("args = %v", in.args)
	}
	if none := resolveBuildInputs(&domain.LeoflowConfig{}, lookupFrom(nil)); none.err != nil || none.secrets != nil || none.env != nil {
		t.Errorf("inputs of a project without any = %+v", none)
	}
}

// A secret or a credential whose variable is not set fails the build with
// the variable named, rather than building without it.
func TestResolveBuildInputsNamesAMissingVariable(t *testing.T) {
	t.Parallel()
	secret := &domain.LeoflowConfig{Build: &domain.BuildConfig{Secrets: []domain.BuildSecret{{ID: "github_token", Env: "GITHUB_TOKEN"}}}}
	if in := resolveBuildInputs(secret, lookupFrom(nil)); in.err == nil || !strings.Contains(in.err.Error(), "GITHUB_TOKEN") ||
		!strings.Contains(in.err.Error(), "github_token") {
		t.Errorf("err = %v, want the secret and its variable named", in.err)
	}
	index := &domain.LeoflowConfig{Build: &domain.BuildConfig{PackageIndex: &domain.PackageIndex{URL: "https://pypi.example.com/simple",
		UsernameEnv: "TEST_PYPI_USER", PasswordEnv: "TEST_PYPI_TOKEN"}}}
	if in := resolveBuildInputs(index, lookupFrom(map[string]string{"TEST_PYPI_USER": "ci"})); in.err == nil || !strings.Contains(in.err.Error(), "TEST_PYPI_TOKEN") {
		t.Errorf("err = %v, want the password variable named", in.err)
	}
}

// uv installs from the package index through its environment, never its
// argv, and a message it prints never carries the credential.
func TestUVInstallsFromThePackageIndex(t *testing.T) {
	t.Parallel()
	env := uvEnv("https://ci:tok@pypi.example.com/simple")
	if !slices.Contains(env, "UV_DEFAULT_INDEX=https://ci:tok@pypi.example.com/simple") {
		t.Errorf("uv env = %q, want the index", env)
	}
	if slices.ContainsFunc(uvEnv(""), func(kv string) bool { return strings.HasPrefix(kv, "UV_DEFAULT_INDEX=") }) {
		t.Error("uv runs with an index when the project sets none")
	}
	u := &uvTool{index: "https://ci:t%40k@pypi.example.com/simple"}
	got := u.scrub("error: Failed to fetch https://ci:t%40k@pypi.example.com/simple/pandas/ (password t@k)")
	if strings.Contains(got, "t%40k") || strings.Contains(got, "t@k") || !strings.Contains(got, "pypi.example.com/simple/pandas/") {
		t.Errorf("scrub = %q, want the credential gone and the rest kept", got)
	}
}

// The key of a dependency image built by the daemon covers the index, so an
// image built from one index is never reused for a build from another.
func TestDepsKeyCoversThePackageIndex(t *testing.T) {
	t.Parallel()
	cfg := &domain.LeoflowConfig{Build: &domain.BuildConfig{PackageIndex: &domain.PackageIndex{URL: "https://a.example.com/simple"}}}
	a := depsKey(depsKeyContent("FROM x\n", cfg), []string{"linux/amd64"})
	cfg.Build.PackageIndex.URL = "https://b.example.com/simple"
	b := depsKey(depsKeyContent("FROM x\n", cfg), []string{"linux/amd64"})
	plain := depsKey(depsKeyContent("FROM x\n", &domain.LeoflowConfig{}), []string{"linux/amd64"})
	if a == b || a == plain {
		t.Errorf("keys a=%s b=%s plain=%s, want each index its own key", a, b, plain)
	}
	if plain != depsKey("FROM x\n", []string{"linux/amd64"}) {
		t.Error("a project without an index changed its dependency key")
	}
}

// envBuilder is a fake docker without buildx that logs its argv and the
// index URL in its environment.
func envBuilder(t *testing.T) (builder, argvLog, envLog string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX-only")
	}
	dir := t.TempDir()
	builder = filepath.Join(dir, "docker")
	argvLog, envLog = filepath.Join(dir, "argv.log"), filepath.Join(dir, "env.log")
	script := "#!/bin/sh\n" +
		"if [ \"$1 $2\" = \"buildx version\" ]; then exit 1; fi\n" +
		"echo \"$*\" >> \"" + argvLog + "\"\n" +
		"printf '%s\\n' \"$" + indexSecretEnv + "\" >> \"" + envLog + "\"\n" +
		"exit 0\n"
	if err := os.WriteFile(builder, []byte(script), 0o700); err != nil { //nolint:gosec // a test fixture that must be executable
		t.Fatal(err)
	}
	return builder, argvLog, envLog
}

// Through compile --build: the tool gets the args and the secrets by
// variable name, the index URL only in its environment, and the password
// reaches neither the argv, the command output nor dag.json.
func TestCompileHandsTheBuildInputsToTheTool(t *testing.T) {
	dir := platformProject(t, indexYAML+"  args:\n    APP_ENV: prod\n  secrets:\n    - id: github_token\n      env: TEST_GITHUB_TOKEN\n")
	t.Setenv("TEST_PYPI_USER", "ci")
	t.Setenv("TEST_PYPI_TOKEN", indexPassword)
	t.Setenv("TEST_GITHUB_TOKEN", "gh-token")
	builder, argvLog, envLog := envBuilder(t)

	out, errOut, err := run(t, "compile", dir, "--build", "--builder", builder)
	if err != nil {
		t.Fatalf("compile --build: %v\n%s", err, errOut)
	}
	argv := argvLines(t, argvLog)
	if len(argv) != 1 {
		t.Fatalf("builder calls = %q, want one build", argv)
	}
	for _, want := range []string{"--build-arg APP_ENV=prod", "--secret id=github_token,env=TEST_GITHUB_TOKEN",
		"--secret id=dexaflow_index_url,env=" + indexSecretEnv} {
		if !strings.Contains(argv[0], want) {
			t.Errorf("build argv %q does not carry %s", argv[0], want)
		}
	}
	if got := strings.TrimSpace(readFileString(t, envLog)); got != "https://ci:"+indexPassword+"@pypi.example.com/simple" {
		t.Errorf("index URL in the tool's environment = %q", got)
	}
	dag := readFileString(t, filepath.Join(dir, "dag.json"))
	for where, text := range map[string]string{"argv": argv[0], "stdout": out, "stderr": errOut, "dag.json": dag} {
		if strings.Contains(text, indexPassword) || strings.Contains(text, "gh-token") {
			t.Errorf("a secret reached %s:\n%s", where, text)
		}
	}
	if !strings.Contains(errOut, "build.args") || !strings.Contains(errOut, "build.secrets") {
		t.Errorf("stderr does not warn that the generated Dockerfile reads no args or secrets:\n%s", errOut)
	}
}

// A missing secret fails the build before the tool runs.
func TestCompileRefusesAMissingBuildSecret(t *testing.T) {
	dir := platformProject(t, "build:\n  secrets:\n    - id: github_token\n      env: TEST_UNSET_TOKEN\n")
	if err := os.Unsetenv("TEST_UNSET_TOKEN"); err != nil {
		t.Fatal(err)
	}
	builder, argvLog, _ := envBuilder(t)
	_, _, err := run(t, "compile", dir, "--build", "--builder", builder)
	if err == nil || !strings.Contains(err.Error(), "TEST_UNSET_TOKEN") {
		t.Fatalf("err = %v, want the variable named", err)
	}
	if argvLines(t, argvLog) != nil {
		t.Error("the tool ran without the secret")
	}
}

// A project with a remote builder and build secrets builds on this
// machine, and the builder line says why.
func TestCompileKeepsABuildWithSecretsOnThisMachine(t *testing.T) {
	dir, _ := platformProjectWith(t, amd64AndArm64,
		"build:\n  remote_builder:\n    cloud_build:\n      project: acme-ci\n  secrets:\n    - id: github_token\n      env: TEST_GITHUB_TOKEN\n")
	writeProjectFile(t, dir, "Dockerfile", "FROM scratch\n")
	t.Setenv("TEST_GITHUB_TOKEN", "gh-token")
	builder, argvLog, _ := envBuilder(t)
	out, errOut, err := run(t, "compile", dir, "--build", "--push", "--builder", builder)
	if err != nil && !errors.Is(err, build.ErrPinRead) {
		t.Fatalf("compile --build --push: %v\n%s", err, errOut)
	}
	if !strings.Contains(out, "builder:    local-daemon") || !strings.Contains(out, "build.secrets reach the build from this machine's environment") {
		t.Errorf("builder line does not name the local daemon and why:\n%s", out)
	}
	if calls := argvLines(t, argvLog); len(calls) == 0 || !strings.HasPrefix(calls[0], "build ") ||
		!strings.Contains(calls[0], "--secret id=github_token,env=TEST_GITHUB_TOKEN") {
		t.Errorf("builder calls = %q, want the local build, pushed, with the secret", calls)
	}
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // a path this test created
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// Build args and the package index change what the image holds, so they
// change the content label, and the skip path never reuses an image built
// with others. A secret's value is not an input of the label: only its id
// and variable are.
func TestContentLabelCoversTheBuildInputs(t *testing.T) {
	base := labelOf(t, versionProject(t, ""))
	seen := map[string]string{"": base}
	for _, yaml := range []string{
		"build:\n  args:\n    APP_ENV: prod\n",
		"build:\n  args:\n    APP_ENV: dev\n",
		"build:\n  package_index:\n    url: https://a.example.com/simple\n",
		"build:\n  package_index:\n    url: https://b.example.com/simple\n",
	} {
		label := labelOf(t, versionProject(t, yaml))
		for other, l := range seen {
			if l == label {
				t.Errorf("label of %q equals the label of %q", yaml, other)
			}
		}
		seen[yaml] = label
	}
	withSecret := "build:\n  secrets:\n    - id: tok\n      env: TEST_LABEL_TOKEN\n"
	t.Setenv("TEST_LABEL_TOKEN", "one")
	first := labelOf(t, versionProject(t, withSecret))
	t.Setenv("TEST_LABEL_TOKEN", "two")
	if second := labelOf(t, versionProject(t, withSecret)); first != second {
		t.Error("the secret's value changed the content label")
	}
}

// uv releases before UV_DEFAULT_INDEX ignore it and resolve from PyPI, the
// dependency confusion the index is there to prevent: uv also gets the
// index as UV_INDEX_URL, which every release reads, with the same value.
func TestUVGetsTheIndexUnderBothVariables(t *testing.T) {
	t.Parallel()
	const index = "https://ci:tok@pypi.example.com/simple"
	env := uvEnv(index)
	for _, want := range []string{"UV_DEFAULT_INDEX=" + index, "UV_INDEX_URL=" + index} {
		if !slices.Contains(env, want) {
			t.Errorf("uv env = %q, want %s", env, want)
		}
	}
	if slices.ContainsFunc(uvEnv(""), func(kv string) bool { return strings.HasPrefix(kv, "UV_INDEX_URL=") }) {
		t.Error("uv runs with UV_INDEX_URL when the project sets no index")
	}
}
