package build

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	ggcrtransport "github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

// dockerHubKey is the key docker, and the registry client's keychain, file
// a Docker Hub credential under in the docker config.
const dockerHubKey = "https://index.docker.io/v1/"

// errLoginRefused marks a credential the registry did not accept.
var errLoginRefused = errors.New("the registry refused the credential")

// VerifyLogin checks that the registry host accepts user and secret: it
// speaks the registry API's handshake as a push would (a Basic challenge,
// or the token exchange of a Bearer one, as GHCR and Docker Hub answer) and
// asks /v2/ with what it got. The secret never appears in the error.
func VerifyLogin(ctx context.Context, host, user, secret string) error {
	reg, err := parseRegistryHost(host)
	if err != nil {
		return err
	}
	auth := authn.FromConfig(authn.AuthConfig{Username: user, Password: secret})
	rt, err := ggcrtransport.NewWithContext(ctx, reg, auth, transport(remote.DefaultTransport), nil)
	if err != nil {
		return refusal(reg.RegistryStr(), err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reg.Scheme()+"://"+reg.RegistryStr()+"/v2/", http.NoBody)
	if err != nil {
		return fmt.Errorf("asking %s: %w", reg.RegistryStr(), err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		return refusal(reg.RegistryStr(), err)
	}
	defer resp.Body.Close() //nolint:errcheck // the body is not read
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: %s answered %s", errLoginRefused, reg.RegistryStr(), resp.Status)
	}
	return nil
}

// CheckRegistryHost reports whether host names a registry the way a login
// takes it: a host with an optional port and nothing else, no scheme, path
// or user, and not empty, which the registry client would read as Docker
// Hub. The error quotes host escaped, never as given.
func CheckRegistryHost(host string) error {
	_, err := parseRegistryHost(host)
	return err
}

// parseRegistryHost is the registry host names, as CheckRegistryHost says.
func parseRegistryHost(host string) (name.Registry, error) {
	reg, err := name.NewRegistry(host, name.StrictValidation)
	if err != nil {
		return name.Registry{}, fmt.Errorf("%q is not a registry host: give a host name with an optional port, "+
			"such as ghcr.io or harbor.example.com:8443", host)
	}
	return reg, nil
}

// refusal words a failed handshake with the registry at host.
func refusal(host string, err error) error {
	var terr *ggcrtransport.Error
	if errors.As(err, &terr) && (terr.StatusCode == http.StatusUnauthorized || terr.StatusCode == http.StatusForbidden) {
		return fmt.Errorf("%w: %s answered %d %s", errLoginRefused, host, terr.StatusCode, http.StatusText(terr.StatusCode))
	}
	return fmt.Errorf("signing in to %s: %w", host, err)
}

// StoreLogin writes user and secret for the registry host to the docker
// config (DOCKER_CONFIG, else ~/.docker/config.json), where the registry
// client's keychain reads it and docker, podman and nerdctl find it too. A
// credHelpers entry for the host, else a credsStore, keeps the secret in
// that helper (the system keychain) through the credential helper protocol,
// as `docker login` does, and the file records only the host; otherwise the
// secret goes in the file's auths, base64 as docker writes it. Every other
// field of the file is kept, and the file stays readable by its owner only.
// Docker Hub is filed under its legacy key, whichever of its names host is.
// It returns the path of the config file. ctx bounds the helper's run.
func StoreLogin(ctx context.Context, host, user, secret string) (string, error) {
	key := loginKey(host)
	path := filepath.Join(dockerConfigDir(), "config.json")
	doc, err := readDockerConfig(path)
	if err != nil {
		return "", err
	}
	entry := map[string]string{}
	if helper := credentialHelper(doc, key); helper != "" {
		if herr := storeInHelper(ctx, helper, key, user, secret); herr != nil {
			return "", fmt.Errorf("storing the credential for %s with docker-credential-%s: %w", host, helper, herr)
		}
	} else {
		entry["auth"] = base64.StdEncoding.EncodeToString([]byte(user + ":" + secret))
	}
	if err := setAuth(doc, key, entry); err != nil {
		return "", err
	}
	if err := writeDockerConfig(path, doc); err != nil {
		return "", err
	}
	return path, nil
}

// dockerConfigDir is the directory of the docker config: DOCKER_CONFIG,
// else ~/.docker, as docker and the keychain resolve it.
func dockerConfigDir() string {
	if dir := os.Getenv("DOCKER_CONFIG"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".docker"
	}
	return filepath.Join(home, ".docker")
}

// readDockerConfig reads the docker config at path as its top-level fields,
// so writing it back keeps every field this code does not know; a missing
// file is an empty config.
func readDockerConfig(path string) (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the user's own docker config
	if errors.Is(err, os.ErrNotExist) {
		return map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the docker config %s: %w", path, err)
	}
	doc := map[string]json.RawMessage{}
	if strings.TrimSpace(string(data)) == "" {
		return doc, nil
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("the docker config %s is not JSON: %w", path, err)
	}
	return doc, nil
}

// credentialHelper is the helper that keeps the credential of key: its
// credHelpers entry, else credsStore, else "".
func credentialHelper(doc map[string]json.RawMessage, key string) string {
	var helpers map[string]string
	if raw, ok := doc["credHelpers"]; ok && json.Unmarshal(raw, &helpers) == nil && helpers[key] != "" {
		return helpers[key]
	}
	var store string
	if raw, ok := doc["credsStore"]; ok && json.Unmarshal(raw, &store) == nil {
		return store
	}
	return ""
}

// setAuth sets the auths entry of key in doc, keeping the other entries
// and the other fields of each.
func setAuth(doc map[string]json.RawMessage, key string, entry map[string]string) error {
	auths := map[string]map[string]json.RawMessage{}
	if raw, ok := doc["auths"]; ok {
		if err := json.Unmarshal(raw, &auths); err != nil {
			return fmt.Errorf("the auths of the docker config are not a map: %w", err)
		}
	}
	e := auths[key]
	if e == nil {
		e = map[string]json.RawMessage{}
	}
	delete(e, "auth")
	delete(e, "identitytoken")
	for k, v := range entry {
		enc, _ := json.Marshal(v) //nolint:errcheck // a string cannot fail to marshal
		e[k] = enc
	}
	auths[key] = e
	raw, err := json.Marshal(auths)
	if err != nil {
		return fmt.Errorf("encoding the auths of the docker config: %w", err)
	}
	doc["auths"] = raw
	return nil
}

// writeDockerConfig writes doc to path, readable by its owner only, through
// a file in the same directory renamed over it, so an interrupted write
// never leaves half a config.
func writeDockerConfig(path string, doc map[string]json.RawMessage) error {
	data, err := json.MarshalIndent(doc, "", "\t")
	if err != nil {
		return fmt.Errorf("encoding the docker config: %w", err)
	}
	if merr := os.MkdirAll(filepath.Dir(path), 0o700); merr != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), merr)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "config.json.*")
	if err != nil {
		return fmt.Errorf("writing the docker config %s: %w", path, err)
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // gone after the rename; cleans up a failed write
	if _, werr := tmp.Write(append(data, '\n')); werr != nil {
		tmp.Close() //nolint:errcheck,gosec // the write error is the one reported
		return fmt.Errorf("writing the docker config %s: %w", path, werr)
	}
	if cerr := tmp.Close(); cerr != nil {
		return fmt.Errorf("writing the docker config %s: %w", path, cerr)
	}
	if rerr := os.Rename(tmp.Name(), path); rerr != nil {
		return fmt.Errorf("writing the docker config %s: %w", path, rerr)
	}
	return nil
}

// storeInHelper stores the credential of key with docker-credential-<helper>,
// the credential helper protocol docker speaks: `store`, with the server,
// user and secret as JSON on stdin. The secret never reaches its argv, and
// is cut from what a failing helper printed before that goes in the error.
func storeInHelper(ctx context.Context, helper, key, user, secret string) error {
	if !helperName.MatchString(helper) {
		return fmt.Errorf("%q is not a credential helper name: the docker config names a docker-credential-<name> program on PATH", helper)
	}
	bin := "docker-credential-" + helper
	if _, err := exec.LookPath(bin); err != nil {
		return fmt.Errorf("%s is not on PATH", bin)
	}
	in, err := json.Marshal(map[string]string{"ServerURL": key, "Username": user, "Secret": secret})
	if err != nil {
		return fmt.Errorf("encoding the credential: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, helperTimeout)
	defer cancel()
	c := exec.CommandContext(ctx, bin, "store") //nolint:gosec // G204: the helper the user's docker config names, as docker runs it
	c.Stdin = bytes.NewReader(in)
	c.WaitDelay = pipeWaitDelay
	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, &out
	if err := c.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, redact(strings.TrimSpace(out.String()), secret))
	}
	return nil
}

// redact replaces secret in s, as written and as JSON encodes it.
func redact(s, secret string) string {
	if secret == "" {
		return s
	}
	s = strings.ReplaceAll(s, secret, "[redacted]")
	if enc, err := json.Marshal(secret); err == nil {
		s = strings.ReplaceAll(s, strings.Trim(string(enc), `"`), "[redacted]")
	}
	return s
}

// helperName is what a credential helper name may hold: a name, never a
// path, so the program run is a docker-credential-* one found on PATH.
var helperName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// helperTimeout bounds one run of a credential helper, which can hang when
// the program behind it is not running (Docker Desktop's helper, for one).
const helperTimeout = 30 * time.Second

// pipeWaitDelay bounds the wait for a helper's output once it has exited or
// been stopped, so a program it started that still holds the output open
// cannot outlast helperTimeout.
const pipeWaitDelay = 2 * time.Second

// loginKey is the docker config key of the registry host.
func loginKey(host string) string {
	reg, err := name.NewRegistry(host)
	if err != nil {
		return host
	}
	if reg.RegistryStr() == name.DefaultRegistry || host == "registry-1.docker.io" {
		return dockerHubKey
	}
	return reg.RegistryStr()
}
