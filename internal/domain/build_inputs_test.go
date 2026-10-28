package domain

import (
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v3"
)

func buildInputsCfg(b *BuildConfig) *LeoflowConfig {
	c := &LeoflowConfig{SchemaVersion: "1.0", DagID: "sales", Build: b}
	c.ApplyDefaults()
	return c
}

// Build args, build secrets and a package index load from dexaflow.yaml in
// their documented form.
func TestBuildInputsLoadFromYAML(t *testing.T) {
	src := `
build:
  args:
    APP_ENV: prod
    PIP_VERSION: "24.0"
  secrets:
    - id: github_token
      env: GITHUB_TOKEN
  package_index:
    url: https://pypi.example.com/simple
    username_env: PYPI_USER
    password_env: PYPI_TOKEN
`
	var b struct {
		Build BuildConfig `yaml:"build"`
	}
	if err := yaml.Unmarshal([]byte(src), &b); err != nil {
		t.Fatal(err)
	}
	got := b.Build
	if got.Args["APP_ENV"] != "prod" || got.Args["PIP_VERSION"] != "24.0" {
		t.Errorf("args = %v", got.Args)
	}
	if len(got.Secrets) != 1 || got.Secrets[0] != (BuildSecret{ID: "github_token", Env: "GITHUB_TOKEN"}) {
		t.Errorf("secrets = %+v", got.Secrets)
	}
	want := PackageIndex{URL: "https://pypi.example.com/simple", UsernameEnv: "PYPI_USER", PasswordEnv: "PYPI_TOKEN"}
	if got.PackageIndex == nil || *got.PackageIndex != want {
		t.Errorf("package_index = %+v, want %+v", got.PackageIndex, want)
	}
	if err := buildInputsCfg(&got).Validate(); err != nil {
		t.Errorf("Validate = %v, want it accepted", err)
	}
}

// An index without credentials, and one on the loopback over plain HTTP (a
// local mirror), are accepted.
func TestValidateAcceptsAPackageIndexWithoutCredentials(t *testing.T) {
	for _, u := range []string{"https://pypi.example.com/simple/", "http://localhost:3141/root/pypi/+simple/", "http://127.0.0.1:8080/simple"} {
		if err := buildInputsCfg(&BuildConfig{PackageIndex: &PackageIndex{URL: u}}).Validate(); err != nil {
			t.Errorf("Validate(%s) = %v, want it accepted", u, err)
		}
	}
}

// Each value that would reach the build in a form it cannot take, or that
// would put a credential where a log or the image keeps it, is refused with
// the field named.
func TestValidateRefusesBadBuildInputs(t *testing.T) {
	for name, tc := range map[string]struct {
		b     *BuildConfig
		field string
	}{
		"arg name with a dash":   {&BuildConfig{Args: map[string]string{"APP-ENV": "x"}}, "build.args"},
		"arg name with a space":  {&BuildConfig{Args: map[string]string{"A B": "x"}}, "build.args"},
		"arg value with newline": {&BuildConfig{Args: map[string]string{"A": "x\ny"}}, "build.args.A"},
		"secret without id":      {&BuildConfig{Secrets: []BuildSecret{{Env: "TOKEN"}}}, "build.secrets[0].id"},
		"secret id with a comma": {&BuildConfig{Secrets: []BuildSecret{{ID: "a,src=/etc/passwd", Env: "TOKEN"}}}, "build.secrets[0].id"},
		"secret without env":     {&BuildConfig{Secrets: []BuildSecret{{ID: "tok"}}}, "build.secrets[0].env"},
		"secret env not a name":  {&BuildConfig{Secrets: []BuildSecret{{ID: "tok", Env: "A=B"}}}, "build.secrets[0].env"},
		"duplicate secret id":    {&BuildConfig{Secrets: []BuildSecret{{ID: "tok", Env: "A"}, {ID: "tok", Env: "B"}}}, "build.secrets[1].id"},
		"reserved secret id":     {&BuildConfig{Secrets: []BuildSecret{{ID: IndexSecretID, Env: "A"}}}, "build.secrets[0].id"},
		"index without url":      {&BuildConfig{PackageIndex: &PackageIndex{}}, "build.package_index.url"},
		"index not http":         {&BuildConfig{PackageIndex: &PackageIndex{URL: "ftp://pypi.example.com/simple"}}, "build.package_index.url"},
		"index plain http":       {&BuildConfig{PackageIndex: &PackageIndex{URL: "http://pypi.example.com/simple"}}, "build.package_index.url"},
		"index with credentials": {&BuildConfig{PackageIndex: &PackageIndex{URL: "https://me:tok@pypi.example.com/simple"}}, "build.package_index.url"},
		"index with a query":     {&BuildConfig{PackageIndex: &PackageIndex{URL: "https://pypi.example.com/simple?token=x"}}, "build.package_index.url"},
		"password without user":  {&BuildConfig{PackageIndex: &PackageIndex{URL: "https://pypi.example.com/simple", PasswordEnv: "TOK"}}, "build.package_index.username_env"},
		"user without password":  {&BuildConfig{PackageIndex: &PackageIndex{URL: "https://pypi.example.com/simple", UsernameEnv: "USR"}}, "build.package_index.password_env"},
		"username env not a name": {&BuildConfig{PackageIndex: &PackageIndex{URL: "https://pypi.example.com/simple",
			UsernameEnv: "1USR", PasswordEnv: "TOK"}}, "build.package_index.username_env"},
	} {
		err := buildInputsCfg(tc.b).Validate()
		if err == nil || !strings.Contains(err.Error(), tc.field) {
			t.Errorf("%s: Validate = %v, want a refusal naming %s", name, err, tc.field)
		}
	}
}

// A credential written into the index URL is refused without being echoed:
// the error would otherwise put it in the terminal and the CI log.
func TestValidateDoesNotEchoAnIndexCredential(t *testing.T) {
	err := buildInputsCfg(&BuildConfig{PackageIndex: &PackageIndex{URL: "https://me:s3cr3t-tok@pypi.example.com/simple"}}).Validate()
	if err == nil || strings.Contains(err.Error(), "s3cr3t-tok") {
		t.Fatalf("Validate = %v, want a refusal that does not quote the password", err)
	}
	if !strings.Contains(err.Error(), "username_env") {
		t.Errorf("Validate = %v, want the fields to use instead", err)
	}
}

// IndexURL composes the URL a build uses from the credentials in the
// environment, and says which variable is missing.
func TestPackageIndexURLReadsTheCredentialsFromTheEnvironment(t *testing.T) {
	p := &PackageIndex{URL: "https://pypi.example.com/simple", UsernameEnv: "PYPI_USER", PasswordEnv: "PYPI_TOKEN"}
	env := map[string]string{"PYPI_USER": "ci bot", "PYPI_TOKEN": "t@k/en"}
	got, err := p.IndexURL(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://ci%20bot:t%40k%2Fen@pypi.example.com/simple" {
		t.Errorf("IndexURL = %q", got)
	}
	delete(env, "PYPI_TOKEN")
	if _, err := p.IndexURL(func(k string) (string, bool) { v, ok := env[k]; return v, ok }); err == nil || !strings.Contains(err.Error(), "PYPI_TOKEN") {
		t.Errorf("IndexURL without the password = %v, want the variable named", err)
	}
	plain := &PackageIndex{URL: "https://pypi.example.com/simple"}
	if got, err := plain.IndexURL(func(string) (string, bool) { return "", false }); err != nil || got != plain.URL {
		t.Errorf("IndexURL without credentials = %q, %v", got, err)
	}
}

// LocalOnly says why a build must run on this machine: its secrets and the
// index credentials are read from this machine's environment.
func TestBuildConfigLocalOnly(t *testing.T) {
	cases := map[string]struct {
		b    *BuildConfig
		want string
	}{
		"nothing":       {nil, ""},
		"args only":     {&BuildConfig{Args: map[string]string{"A": "b"}}, ""},
		"secrets":       {&BuildConfig{Secrets: []BuildSecret{{ID: "tok", Env: "TOKEN"}}}, "build.secrets"},
		"package index": {&BuildConfig{PackageIndex: &PackageIndex{URL: "https://pypi.example.com/simple"}}, "build.package_index"},
	}
	for name, tc := range cases {
		got := tc.b.LocalOnly()
		if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("%s: LocalOnly = %q, want it to name %q", name, got, tc.want)
		}
	}
}

// A secret id names the file BuildKit mounts under /run/secrets, so "." and
// ".." (and any id starting with a dot or a dash) are refused: they would
// name the directory itself or its parent.
func TestBuildSecretIDCannotNameADirectory(t *testing.T) {
	for _, id := range []string{".", "..", ".hidden", "-x"} {
		b := &BuildConfig{Secrets: []BuildSecret{{ID: id, Env: "TOKEN"}}}
		if err := b.validateInputs(); err == nil || !strings.Contains(err.Error(), "build.secrets[0].id") {
			t.Errorf("validateInputs(id %q) = %v, want a refusal naming build.secrets[0].id", id, err)
		}
	}
	ok := &BuildConfig{Secrets: []BuildSecret{{ID: "npm.token-1", Env: "TOKEN"}}}
	if err := ok.validateInputs(); err != nil {
		t.Errorf("validateInputs(npm.token-1) = %v, want nil", err)
	}
}
