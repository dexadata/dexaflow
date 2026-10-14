package build

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
)

// Registry credentials (ADR 0080). The registry client reads a credential
// from the docker config and its credential helpers first, as ADR 0073
// decided, and, for a registry host a cloud owns (Artifact Registry and
// GCR, ECR, ACR), from the session of that cloud's own CLI: the token
// `gcloud auth print-access-token`, `aws ecr get-login-password` or
// `az acr login --expose-token` prints. No cloud SDK is linked and no
// credential is stored; a token lives in memory for cloudTokenTTL at most.

// AuthMethod is where the registry client reads a registry credential, the
// registry.auth_method of dexaflow.yaml.
type AuthMethod string

// The values of registry.auth_method.
const (
	// AuthAuto reads the docker config and its credential helpers, then,
	// when they hold nothing for the registry, the session of the cloud CLI
	// that owns its host. It is the default.
	AuthAuto AuthMethod = "auto"
	// AuthDockerConfig reads the docker config and its credential helpers
	// only, as every release before auto did.
	AuthDockerConfig AuthMethod = "docker_config"
	// AuthCloud reads the cloud CLI session only, so a stale entry in the
	// docker config (an ECR token is good for 12 hours) is never sent.
	AuthCloud AuthMethod = "cloud"
)

// ParseAuthMethod reads a registry.auth_method value. Empty is AuthAuto.
// The two values the schema accepted before any was implemented still
// parse, with a note to print: iam reads as cloud, env as auto.
func ParseAuthMethod(v string) (method AuthMethod, note string, err error) {
	switch v {
	case "", string(AuthAuto):
		return AuthAuto, "", nil
	case string(AuthDockerConfig), string(AuthCloud):
		return AuthMethod(v), "", nil
	case "iam":
		return AuthCloud, "registry.auth_method: iam is deprecated and reads as cloud (the cloud CLI's session); write cloud", nil
	case "env":
		return AuthAuto, "registry.auth_method: env is deprecated and was never read; it reads as auto. " +
			"Remove it, or set docker_config or cloud", nil
	}
	return "", "", fmt.Errorf("registry.auth_method %q is not one of auto, docker_config, cloud", v)
}

// SourceDockerConfig is the Source of a credential the docker config or one
// of its credential helpers holds.
const SourceDockerConfig = "the docker config"

// Credential is a registry credential and where it came from.
type Credential struct {
	// Username and Password are what the registry is sent.
	Username, Password string
	// Source names where the credential came from, for the command output,
	// such as "the gcloud session".
	Source string
}

// CloudKeychain reads a registry credential from a cloud session.
type CloudKeychain interface {
	// Credential returns the credential the cloud session holds for the
	// registry host, an empty Credential when no cloud owns host, or an
	// error saying why the session gave none.
	Credential(ctx context.Context, host string) (Credential, error)
}

// The cloud providers a registry host can belong to.
const (
	ProviderGoogle = "Google Cloud"
	ProviderAWS    = "AWS"
	ProviderAzure  = "Azure"
)

// CloudRegistry is a registry host a cloud CLI signs in to.
type CloudRegistry struct {
	// Provider is ProviderGoogle, ProviderAWS or ProviderAzure.
	Provider string
	// Host is the registry host, as `docker login` takes it.
	Host string
	// Region is the AWS region of an ECR host.
	Region string
	// Name is the registry name of an ACR host.
	Name string
}

// ecrHost matches a private ECR registry: <account>.dkr.ecr[-fips].<region>.amazonaws.com[.cn].
var ecrHost = regexp.MustCompile(`^[0-9]{12}\.dkr\.ecr(?:-fips)?\.([a-z0-9-]+)\.amazonaws\.com(?:\.cn)?$`)

// acrHost matches an Azure Container Registry: <name>.azurecr.io (or the
// China and US Government clouds).
var acrHost = regexp.MustCompile(`^([a-z0-9]{5,50})\.azurecr\.(?:io|cn|us)$`)

// DetectCloudRegistry reports whether host is a registry a cloud CLI signs
// in to, and which: gcr.io and its regional hosts and Artifact Registry
// (<location>-docker.pkg.dev) for Google Cloud, a private ECR registry for
// AWS, an ACR registry for Azure.
func DetectCloudRegistry(host string) (CloudRegistry, bool) {
	h := strings.ToLower(host)
	switch {
	case h == "gcr.io" || strings.HasSuffix(h, ".gcr.io") || strings.HasSuffix(h, "-docker.pkg.dev"):
		return CloudRegistry{Provider: ProviderGoogle, Host: h}, true
	case ecrHost.MatchString(h):
		return CloudRegistry{Provider: ProviderAWS, Host: h, Region: ecrHost.FindStringSubmatch(h)[1]}, true
	case acrHost.MatchString(h):
		return CloudRegistry{Provider: ProviderAzure, Host: h, Name: acrHost.FindStringSubmatch(h)[1]}, true
	}
	return CloudRegistry{}, false
}

// acrUsername is the user name an ACR access token is sent with.
const acrUsername = "00000000-0000-0000-0000-000000000000"

// tokenCommand is the argv that prints a registry token for c, and the
// user name the registry expects with it.
func (c CloudRegistry) tokenCommand() (argv []string, username string) {
	switch c.Provider {
	case ProviderAWS:
		return []string{"aws", "ecr", "get-login-password", "--region", c.Region}, "AWS"
	case ProviderAzure:
		return []string{"az", "acr", "login", "--name", c.Name, "--expose-token", "--output", "tsv", "--query", "accessToken"}, acrUsername
	}
	return []string{"gcloud", "auth", "print-access-token"}, "oauth2accesstoken"
}

// SessionCommand is the command that signs this machine in to the cloud
// whose session the registry client reads.
func (c CloudRegistry) SessionCommand() string {
	switch c.Provider {
	case ProviderAWS:
		return "aws sso login (or aws configure)"
	case ProviderAzure:
		return "az login"
	}
	return "gcloud auth login"
}

// LoginCommand is the command that signs tool (docker when empty) in to
// the registry at host once, so its pushes and the registry client's find a
// credential: for docker, the cloud CLI's own docker login; for another
// tool, the cloud's token piped to `<tool> login`; `<tool> login <host>`
// for a registry no cloud owns.
func LoginCommand(host, tool string) string {
	if tool == "" {
		tool = "docker"
	}
	c, ok := DetectCloudRegistry(host)
	if !ok {
		return tool + " login " + host
	}
	if tool == "docker" {
		switch c.Provider {
		case ProviderGoogle:
			return "gcloud auth configure-docker " + c.Host
		case ProviderAzure:
			return "az acr login --name " + c.Name
		}
	}
	argv, user := c.tokenCommand()
	return strings.Join(argv, " ") + " | " + tool + " login --username " + user + " --password-stdin " + c.Host
}

// CommandRunner runs argv and returns what it printed on stdout.
type CommandRunner func(ctx context.Context, argv []string) ([]byte, error)

// cloudTokenTTL is how long a token read from a cloud CLI is reused. Each
// cloud's token lives longer (an hour for gcloud, three for ACR, twelve for
// ECR), and a deploy makes several registry requests.
const cloudTokenTTL = 10 * time.Minute

// cachedCredential is a credential and when it was read.
type cachedCredential struct {
	cred Credential
	at   time.Time
}

// CloudCLI is the CloudKeychain of the cloud CLIs on PATH: gcloud, aws and
// az. It runs the one that owns the registry host and keeps its token for
// cloudTokenTTL; a failure is not kept, so a sign-in during a watch is
// picked up by the next request.
type CloudCLI struct {
	run CommandRunner
	now func() time.Time

	mu     sync.Mutex
	tokens map[string]cachedCredential
}

// NewCloudCLI returns a CloudCLI that runs the cloud CLIs through run.
func NewCloudCLI(run CommandRunner) *CloudCLI {
	return &CloudCLI{run: run, now: time.Now, tokens: map[string]cachedCredential{}}
}

// defaultCloud is the CloudKeychain of a Registry that sets none. It is one
// per process so that the token cache spans every registry request of a
// command; it holds nothing but that cache.
var defaultCloud = NewCloudCLI(execRunner)

// Credential runs the cloud CLI that owns host, or nothing when no cloud
// does.
func (c *CloudCLI) Credential(ctx context.Context, host string) (Credential, error) {
	reg, ok := DetectCloudRegistry(host)
	if !ok {
		return Credential{}, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if t, hit := c.tokens[reg.Host]; hit && c.now().Sub(t.at) < cloudTokenTTL {
		return t.cred, nil
	}
	argv, user := reg.tokenCommand()
	out, err := c.run(ctx, argv)
	if err != nil {
		return Credential{}, fmt.Errorf("reading a %s registry token with `%s`: %w", reg.Provider, strings.Join(argv, " "), err)
	}
	token := strings.TrimSpace(string(out))
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return Credential{}, fmt.Errorf("reading a %s registry token with `%s`: it printed no single-line token", reg.Provider, strings.Join(argv, " "))
	}
	cred := Credential{Username: user, Password: token, Source: "the " + argv[0] + " session"}
	c.tokens[reg.Host] = cachedCredential{cred: cred, at: c.now()}
	return cred, nil
}

// execRunner runs argv from PATH. A failure carries the last line the
// command wrote to stderr, which is where the cloud CLIs say why.
func execRunner(ctx context.Context, argv []string) ([]byte, error) {
	if _, err := exec.LookPath(argv[0]); err != nil {
		return nil, fmt.Errorf("%s is not on PATH", argv[0])
	}
	//nolint:gosec // G204: argv is a fixed cloud CLI command; region and name come from a host matched by ecrHost or acrHost.
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%w%s", err, lastLine(stderr.String()))
	}
	return out, nil
}

// lastLine returns ": " and the last non-empty line of s, at most 200
// bytes, or "" when s has none.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	l := strings.TrimSpace(lines[len(lines)-1])
	if l == "" {
		return ""
	}
	if len(l) > 200 {
		l = l[:200]
	}
	return ": " + l
}

// resolved is the credential the registry client sends to one registry.
type resolved struct {
	auth authn.Authenticator
	// source names where auth came from; empty when it is anonymous.
	source string
	// cloudErr says why the cloud session gave no credential, when it was
	// asked and could not.
	cloudErr error
}

// resolveAuth finds the credential for target by r.Auth: the docker config
// and its credential helpers, then the cloud session (auto); only the first
// (docker_config); only the second (cloud).
func (r Registry) resolveAuth(ctx context.Context, target authn.Resource) (resolved, error) {
	if r.Auth != AuthCloud {
		auth, err := authn.Resolve(ctx, authn.DefaultKeychain, target)
		if err != nil {
			return resolved{}, fmt.Errorf("reading the credential for %s from the docker config: %w", target.RegistryStr(), err)
		}
		if auth != authn.Anonymous || r.Auth == AuthDockerConfig {
			return resolved{auth: auth, source: sourceOf(auth, SourceDockerConfig)}, nil
		}
	}
	cloud := r.Cloud
	if cloud == nil {
		cloud = defaultCloud
	}
	cred, cerr := cloud.Credential(ctx, target.RegistryStr())
	if cerr != nil {
		// No cloud session is not a failure: the request goes anonymous,
		// and the push check reports why.
		return resolved{auth: authn.Anonymous, cloudErr: cerr}, nil //nolint:nilerr // cerr travels in resolved.cloudErr
	}
	if cred.Password == "" {
		return resolved{auth: authn.Anonymous}, nil
	}
	return resolved{auth: authn.FromConfig(authn.AuthConfig{Username: cred.Username, Password: cred.Password}), source: cred.Source}, nil
}

// sourceOf is source, or "" for the anonymous authenticator.
func sourceOf(auth authn.Authenticator, source string) string {
	if auth == authn.Anonymous {
		return ""
	}
	return source
}

// keychain is r as an authn.Keychain, for the requests of package remote.
type keychain struct{ r Registry }

// Resolve implements authn.Keychain.
func (k keychain) Resolve(target authn.Resource) (authn.Authenticator, error) {
	return k.ResolveContext(context.Background(), target)
}

// ResolveContext implements authn.ContextKeychain. A cloud session that
// gives nothing is anonymous here; the push check reports why.
func (k keychain) ResolveContext(ctx context.Context, target authn.Resource) (authn.Authenticator, error) {
	res, err := k.r.resolveAuth(ctx, target)
	if err != nil {
		return nil, err
	}
	return res.auth, nil
}
