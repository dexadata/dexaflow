package build

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// Pinned is what a registry says a reference names.
type Pinned struct {
	// Ref is the image as written with its tag replaced by the digest,
	// <repo>@sha256:<hex>.
	Ref string
	// Manifest is the raw manifest or index the digest names.
	Manifest []byte
	// Platform is os/arch[/variant] from the config of a single image, filled
	// by the skip path only (Registry.Platform); empty for an index.
	Platform string
}

// missing returns the platforms of want the pinned image cannot serve: an
// index must list each one; a single image serves only its own platform.
func (p Pinned) missing(want []string) []string {
	if p.Platform == "" {
		return missingPlatforms(p.Manifest, want)
	}
	var out []string
	for _, w := range want {
		if !samePlatform(p.Platform, w) {
			out = append(out, w)
		}
	}
	return out
}

// samePlatform reports whether the image platform have serves the requested
// want, with the variant rules of missingPlatforms.
func samePlatform(have, want string) bool {
	h := strings.SplitN(strings.ToLower(have), "/", 3)
	w := strings.SplitN(strings.ToLower(want), "/", 3)
	if len(h) < 2 || len(w) < 2 || h[0] != w[0] || h[1] != w[1] {
		return false
	}
	hv := ""
	if len(h) == 3 {
		hv = h[2]
	}
	return len(w) < 3 || variantOf(h[1], hv) == w[2]
}

// Resolver reads a pushed image's digest and manifest from its registry.
type Resolver interface {
	// Resolve returns what image (a tag reference) names in its registry.
	Resolve(ctx context.Context, image string) (Pinned, error)
}

// Registry is the CLI's registry client (ADR 0073, section 3). It speaks the
// registry API in process, never through a local image store, so an image
// any tool pushed can be pinned (ADR 0075). Its credentials are those
// `dexaflow registry login` (StoreLogin), `docker login` and its credential
// helpers wrote (authn.DefaultKeychain) and, for a registry a cloud owns,
// that cloud CLI's session, as Auth says (ADR 0080). The zero value reads both, the default of registry.auth_method.
type Registry struct {
	// Auth is registry.auth_method; empty is AuthAuto.
	Auth AuthMethod
	// Cloud reads the cloud session; the cloud CLIs on PATH when nil.
	Cloud CloudKeychain
}

// Resolve reads the digest and the manifest that image names. Only a
// registry on localhost, a *.localhost name or a loopback address may answer
// over plain HTTP (plainHTTPOnLoopback); every other one is spoken to over
// verified TLS. Only an OCI or Docker v2 manifest or index is accepted, so
// the digest is always the one computed from the manifest returned.
func (r Registry) Resolve(ctx context.Context, image string) (Pinned, error) {
	ref, err := name.ParseReference(image)
	if err != nil {
		return Pinned{}, fmt.Errorf("image %q is not a valid reference: %w", image, err)
	}
	desc, err := remote.Get(ref, r.remoteOptions(ctx)...)
	if err != nil {
		return Pinned{}, fmt.Errorf("reading %q from its registry (credentials come from `dexaflow registry login` or `docker login` "+
			"or its credential helpers, in ~/.docker/config.json, or from the session of the cloud CLI that owns the registry): %w",
			image, err)
	}
	if !acceptedMediaTypes[desc.MediaType] {
		return Pinned{}, fmt.Errorf("reading %q from its registry: media type %q is not an OCI or Docker v2 manifest or index", image, desc.MediaType)
	}
	return Pinned{Ref: PinnedRef(image, desc.Digest.String()), Manifest: desc.Manifest}, nil
}

// CheckReference refuses an image that is not a reference Resolve can read,
// so a reference given on the command line is refused before anything is
// read or sent.
func CheckReference(image string) error {
	if _, err := name.ParseReference(image); err != nil {
		return fmt.Errorf("image %q is not a valid reference: %w", image, err)
	}
	return nil
}

// Platform reads os/arch[/variant] from the config of the single image ref
// names (a pinned reference), or "" when the config names no platform. It is
// a second request, made only by the skip path, which must know what a
// single image was built for.
func (r Registry) Platform(ctx context.Context, ref string) (string, error) {
	pr, err := name.ParseReference(ref)
	if err != nil {
		return "", fmt.Errorf("image %q is not a valid reference: %w", ref, err)
	}
	img, err := remote.Image(pr, r.remoteOptions(ctx)...)
	if err != nil {
		return "", fmt.Errorf("reading %q from its registry: %w", ref, err)
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		return "", fmt.Errorf("reading the image config: %w", err)
	}
	if cfg.OS == "" || cfg.Architecture == "" {
		return "", nil
	}
	p := cfg.OS + "/" + cfg.Architecture
	if cfg.Variant != "" {
		p += "/" + cfg.Variant
	}
	return p, nil
}

// remoteOptions are the options of every registry request: the credentials
// r.Auth reads (resolveAuth) and the transport that allows plain HTTP only
// on the loopback.
func (r Registry) remoteOptions(ctx context.Context) []remote.Option {
	return []remote.Option{remote.WithContext(ctx), remote.WithAuthFromKeychain(keychain{r: r}),
		remote.WithTransport(transport(remote.DefaultTransport))}
}

// PinnedRef replaces the tag or digest of image with digest and keeps the
// rest as written, so a docker.io image reads as the user wrote it.
func PinnedRef(image, digest string) string {
	repo := image
	if i := strings.Index(repo, "@"); i >= 0 {
		repo = repo[:i]
	}
	if i := strings.LastIndex(repo, ":"); i > strings.LastIndex(repo, "/") {
		repo = repo[:i]
	}
	return repo + "@" + digest
}

// acceptedMediaTypes are the manifest kinds whose digest go-containerregistry
// computes from the bytes it returns. A schema 1 manifest takes its digest
// from a response header instead, which nothing checks.
var acceptedMediaTypes = map[types.MediaType]bool{
	types.OCIManifestSchema1:    true,
	types.OCIImageIndex:         true,
	types.DockerManifestSchema2: true,
	types.DockerManifestList:    true,
}

// transport is the registry client's HTTP transport: base (go-containerregistry's
// default), with *.localhost names dialed on the loopback (loopbackAddr),
// never sent through a proxy (bypassLocalhost), and plain HTTP refused off
// the loopback (plainHTTPOnLoopback).
func transport(base http.RoundTripper) http.RoundTripper {
	b, ok := base.(*http.Transport)
	if !ok {
		b = &http.Transport{Proxy: http.ProxyFromEnvironment}
	}
	return plainHTTPOnLoopback{next: localTransport(b)}
}

// localTransport clones base and points *.localhost names at the loopback.
func localTransport(base *http.Transport) *http.Transport {
	t := base.Clone()
	t.Proxy = bypassLocalhost(t.Proxy)
	d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return d.DialContext(ctx, network, loopbackAddr(addr))
	}
	return t
}

// plainHTTPOnLoopback refuses a plain HTTP request to any host but localhost,
// a *.localhost name or a loopback address. go-containerregistry also picks
// plain HTTP for a private (RFC 1918) address and, when it pings a registry,
// races HTTPS against HTTP and keeps whichever answers first; on a shared
// network that would let anyone who answers HTTP on the registry's port
// receive the credentials and choose the digest.
type plainHTTPOnLoopback struct{ next http.RoundTripper }

// RoundTrip sends r through next unless it is plain HTTP off the loopback.
func (p plainHTTPOnLoopback) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme == "http" && !isLoopbackHost(r.URL.Hostname()) {
		return nil, fmt.Errorf("refusing plain HTTP to %s: only a registry on localhost, a *.localhost name "+
			"or a loopback address is read without TLS", r.URL.Host)
	}
	return p.next.RoundTrip(r)
}

// isLoopbackHost reports whether host is a localhost name or a loopback IP.
func isLoopbackHost(host string) bool {
	if isLocalhostName(host) {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// loopbackAddr maps a host:port whose host is localhost or ends in
// .localhost to 127.0.0.1:port. RFC 6761 reserves those names for the
// loopback, and k3d names its registries that way, but not every resolver
// answers them (macOS, a container without systemd-resolved), while the
// docker daemon that pushed the image did.
func loopbackAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if isLocalhostName(host) {
		return net.JoinHostPort("127.0.0.1", port)
	}
	return addr
}

// bypassLocalhost wraps a proxy function so a *.localhost host is reached
// directly: through a proxy the name would be resolved on the proxy's side.
// net/http skips the proxy for localhost and loopback addresses only.
func bypassLocalhost(proxy func(*http.Request) (*url.URL, error)) func(*http.Request) (*url.URL, error) {
	return func(r *http.Request) (*url.URL, error) {
		if isLocalhostName(r.URL.Hostname()) || proxy == nil {
			return nil, nil //nolint:nilnil // http.Transport.Proxy: a nil URL and no error means "no proxy"
		}
		return proxy(r)
	}
}

// isLocalhostName reports whether host is localhost or a name under it.
func isLocalhostName(host string) bool {
	h := strings.ToLower(host)
	return h == "localhost" || strings.HasSuffix(h, ".localhost")
}
