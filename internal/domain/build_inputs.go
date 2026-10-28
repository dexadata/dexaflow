package domain

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// IndexSecretID is the id of the build secret that carries the package
// index URL, credentials included, to a Dockerfile build: the generated
// Dockerfile mounts it for its pip install, and a project's own Dockerfile
// may mount it too. A project cannot declare a secret of its own under it.
const IndexSecretID = "dexaflow_index_url"

// BuildSecret is one build secret: a Dockerfile mounts it with
// `RUN --mount=type=secret,id=<ID>`, and the build reads its value from the
// environment variable Env of the machine that builds. The value never
// reaches dexaflow.yaml, dag.json, the image or the build's command line.
type BuildSecret struct {
	// ID is the id a RUN line mounts the secret under.
	ID string `json:"id" yaml:"id"`
	// Env is the environment variable that holds the value.
	Env string `json:"env" yaml:"env"`
}

// PackageIndex is a Python package index that replaces PyPI for the
// dependencies of the image. Its credentials are read from the environment
// of the build, never written in the URL.
type PackageIndex struct {
	// URL is the index's simple API, such as https://pypi.example.com/simple.
	URL string `json:"url" yaml:"url"`
	// UsernameEnv is the environment variable holding the user name.
	UsernameEnv string `json:"username_env,omitempty" yaml:"username_env,omitempty"`
	// PasswordEnv is the environment variable holding the password or token.
	PasswordEnv string `json:"password_env,omitempty" yaml:"password_env,omitempty"`
}

// buildArgName is the form of an ARG name, and of an environment variable.
var buildArgName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// buildSecretID is the form of a secret id: no comma or equals sign, which
// would add options to `--secret id=<id>,env=<var>`, and no leading dot or
// dash, so the id never names /run/secrets itself or its parent.
var buildSecretID = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

// validateInputs refuses build args, build secrets and a package index the
// build could not pass on as written, naming the field.
func (b *BuildConfig) validateInputs() error {
	names := make([]string, 0, len(b.Args))
	for k := range b.Args {
		names = append(names, k)
	}
	slices.Sort(names)
	for _, k := range names {
		if !buildArgName.MatchString(k) {
			return fmt.Errorf("build.args: %q is not an ARG name (letters, digits and underscores, not starting with a digit)", k)
		}
		if strings.ContainsAny(b.Args[k], "\r\n\x00") {
			return fmt.Errorf("build.args.%s holds a line break or a NUL, which a build argument cannot carry", k)
		}
	}
	seen := map[string]bool{}
	for i, s := range b.Secrets {
		at := fmt.Sprintf("build.secrets[%d]", i)
		switch {
		case !buildSecretID.MatchString(s.ID):
			return fmt.Errorf("%s.id %q is not a secret id (letters, digits, '.', '_' and '-', starting with a letter, a digit or '_')", at, s.ID)
		case s.ID == IndexSecretID:
			return fmt.Errorf("%s.id %q is the id of the package index secret; set build.package_index instead", at, s.ID)
		case seen[s.ID]:
			return fmt.Errorf("%s.id %q is declared twice", at, s.ID)
		case !buildArgName.MatchString(s.Env):
			return fmt.Errorf("%s.env %q is not an environment variable name", at, s.Env)
		}
		seen[s.ID] = true
	}
	return b.PackageIndex.validate()
}

// validate refuses an index URL a build could not use, or one that carries
// a credential. The credential is never quoted back.
func (p *PackageIndex) validate() error {
	if p == nil {
		return nil
	}
	const at = "build.package_index"
	if p.URL == "" {
		return fmt.Errorf("%s.url is required: the index's simple API, such as https://pypi.example.com/simple", at)
	}
	u, err := url.Parse(p.URL)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%s.url is not an absolute URL, such as https://pypi.example.com/simple", at)
	}
	switch {
	case u.User != nil:
		return fmt.Errorf("%s.url carries credentials, which would reach logs and the build history; "+
			"remove them from the URL and set %s.username_env and %s.password_env to the variables that hold them", at, at, at)
	case u.Scheme != "https" && (u.Scheme != "http" || !loopbackHost(u.Hostname())):
		return fmt.Errorf("%s.url %q: use https (plain http only for an index on the loopback)", at, u.Redacted())
	case u.RawQuery != "" || u.Fragment != "":
		return fmt.Errorf("%s.url %q: remove the query and the fragment; credentials go in %s.username_env and %s.password_env", at, u.Scheme+"://"+u.Host+u.Path, at, at)
	}
	return p.validateCredentials(at)
}

// validateCredentials refuses credential variables that are not set in
// pairs or are not variable names.
func (p *PackageIndex) validateCredentials(at string) error {
	switch {
	case (p.UsernameEnv == "") != (p.PasswordEnv == ""):
		missing := "username_env"
		if p.PasswordEnv == "" {
			missing = "password_env"
		}
		return fmt.Errorf("%s.%s is required with the other credential variable: set both or neither", at, missing)
	case p.UsernameEnv != "" && !buildArgName.MatchString(p.UsernameEnv):
		return fmt.Errorf("%s.username_env %q is not an environment variable name", at, p.UsernameEnv)
	case p.PasswordEnv != "" && !buildArgName.MatchString(p.PasswordEnv):
		return fmt.Errorf("%s.password_env %q is not an environment variable name", at, p.PasswordEnv)
	}
	return nil
}

// loopbackHost reports whether host is localhost, a *.localhost name or a
// loopback address.
func loopbackHost(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// IndexURL returns the URL a build installs from: URL with the credentials
// of UsernameEnv and PasswordEnv, read with lookup (os.LookupEnv), or URL as
// written when it declares none. A declared variable that is not set is an
// error naming it, so a build never runs anonymously against an index that
// wants a credential.
func (p *PackageIndex) IndexURL(lookup func(string) (string, bool)) (string, error) {
	if p.UsernameEnv == "" {
		return p.URL, nil
	}
	user, ok := lookup(p.UsernameEnv)
	if !ok || user == "" {
		return "", fmt.Errorf("build.package_index.username_env names %s, which is not set", p.UsernameEnv)
	}
	pass, ok := lookup(p.PasswordEnv)
	if !ok || pass == "" {
		return "", fmt.Errorf("build.package_index.password_env names %s, which is not set", p.PasswordEnv)
	}
	u, err := url.Parse(p.URL)
	if err != nil {
		return "", fmt.Errorf("build.package_index.url is not a URL: %w", err)
	}
	u.User = url.UserPassword(user, pass)
	return u.String(), nil
}

// LocalOnly says why a build of this project must run on this machine, or
// "" when any builder may run it: build secrets and the package index reach
// the build from this machine's environment, and a remote builder would
// have to store them in its own build record.
func (b *BuildConfig) LocalOnly() string {
	if b == nil {
		return ""
	}
	var why []string
	if len(b.Secrets) > 0 {
		why = append(why, "build.secrets")
	}
	if b.PackageIndex != nil {
		why = append(why, "build.package_index")
	}
	if len(why) == 0 {
		return ""
	}
	return strings.Join(why, " and ") + " reach the build from this machine's environment, so only a builder on this machine takes it"
}
