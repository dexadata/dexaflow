package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/dexadata/dexaflow/internal/build"
	"github.com/dexadata/dexaflow/internal/domain"
)

// indexSecretEnv is the variable of the container tool's environment that
// holds the package index URL, credentials included, for the build secret
// domain.IndexSecretID. Only the tool's process gets it.
const indexSecretEnv = "DEXAFLOW_BUILD_INDEX_URL"

// buildInputs are the build args, build secrets and package index of a
// build, as the builders take them (ADR 0088).
type buildInputs struct {
	// args are build.args as declared.
	args map[string]string
	// secrets are build.secrets, then the package index secret.
	secrets []build.Secret
	// env holds the composed index URL for the tool's environment.
	env []string
	// indexURL is the index URL with its credentials, for uv; "" without
	// an index.
	indexURL string
	// err says which variable a secret or a credential needs and is not
	// set. It fails a build that builds; a reused image needs none.
	err error
}

// resolveBuildInputs reads the inputs of cfg's build, the secret values
// and index credentials with lookup (os.LookupEnv). Only the index URL is
// read here, to compose it; a secret stays a variable name for the tool to
// read, and is only checked to be set.
func resolveBuildInputs(cfg *domain.LeoflowConfig, lookup func(string) (string, bool)) buildInputs {
	if cfg.Build == nil {
		return buildInputs{}
	}
	in := buildInputs{args: cfg.Build.Args}
	for i, s := range cfg.Build.Secrets {
		if v, ok := lookup(s.Env); !ok || v == "" {
			in.err = fmt.Errorf("build.secrets[%d] (id %s) reads the environment variable %s, which is not set", i, s.ID, s.Env)
			return in
		}
		in.secrets = append(in.secrets, build.Secret{ID: s.ID, Env: s.Env})
	}
	if cfg.Build.PackageIndex == nil {
		return in
	}
	u, err := cfg.Build.PackageIndex.IndexURL(lookup)
	if err != nil {
		in.err = err
		return in
	}
	in.indexURL = u
	in.secrets = append(in.secrets, build.Secret{ID: domain.IndexSecretID, Env: indexSecretEnv})
	in.env = []string{indexSecretEnv + "=" + u}
	return in
}

// checkBuildInputs fails a build b that builds when a secret or credential
// variable is not set (a reused image needs none), and warns about inputs
// the generated Dockerfile does not read.
func checkBuildInputs(w io.Writer, o compileOptions, cfg *domain.LeoflowConfig, b build.Builder, ownDockerfile bool) error {
	if o.inputs.err != nil && b.Path() != build.PathSkip {
		return o.inputs.err
	}
	warnUnreadBuildInputs(w, cfg, ownDockerfile)
	return nil
}

// withIndex gives uv the package index of the build, or, when its
// credentials are missing, takes uv out with why: the build fails on them
// later, unless it reuses an image.
func withIndex(uv *uvTool, noWheels string, o compileOptions, cfg *domain.LeoflowConfig) (tool *uvTool, why string) {
	if uv == nil || cfg.Build == nil || cfg.Build.PackageIndex == nil {
		return uv, noWheels
	}
	if o.inputs.err != nil {
		return nil, o.inputs.err.Error()
	}
	uv.index = o.inputs.indexURL
	return uv, noWheels
}

// warnUnreadBuildInputs warns that build.args and build.secrets reach no
// line of the generated Dockerfile: only a project's own Dockerfile reads
// them, with ARG and RUN --mount=type=secret.
func warnUnreadBuildInputs(w io.Writer, cfg *domain.LeoflowConfig, ownDockerfile bool) {
	if ownDockerfile || cfg.Build == nil {
		return
	}
	var fields []string
	if len(cfg.Build.Args) > 0 {
		fields = append(fields, "build.args")
	}
	if len(cfg.Build.Secrets) > 0 {
		fields = append(fields, "build.secrets")
	}
	if len(fields) == 0 {
		return
	}
	//nolint:errcheck // advisory output; a write failure must not fail the build
	fmt.Fprintf(w, "warning: %s reach the build, but the generated Dockerfile has no ARG or RUN --mount=type=secret line that reads them; "+
		"only your own Dockerfile does (build.dockerfile)\n", strings.Join(fields, " and "))
}

// depsKeyContent is what the key of a dependency image the daemon builds
// covers: its Dockerfile and, with a package index, the index URL without
// credentials, so an image built from one index is not reused for a build
// from another.
func depsKeyContent(dockerfile string, cfg *domain.LeoflowConfig) string {
	if cfg.Build == nil || cfg.Build.PackageIndex == nil {
		return dockerfile
	}
	return dockerfile + "# package index " + cfg.Build.PackageIndex.URL + "\n"
}
