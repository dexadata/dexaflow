package build

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// Pinned is what a registry says a reference names.
type Pinned struct {
	// Ref is the image as written with its tag replaced by the digest,
	// <repo>@sha256:<hex>.
	Ref string
	// Manifest is the raw manifest or index the digest names.
	Manifest []byte
}

// Resolver reads a pushed image's digest and manifest from its registry.
type Resolver interface {
	// Resolve returns what image (a tag reference) names in its registry.
	Resolve(ctx context.Context, image string) (Pinned, error)
}

// Registry is the CLI's registry client (ADR 0073, section 3). It speaks the
// registry API in process with the credentials `docker login` and its
// credential helpers wrote (authn.DefaultKeychain), never through a local
// image store, so an image any tool pushed can be pinned (ADR 0075).
type Registry struct{}

// Resolve reads the digest and the manifest that image names. A registry on
// localhost, *.localhost, 127.0.0.1 or a private address is spoken to over
// plain HTTP, as go-containerregistry decides; every other one over verified
// TLS.
func (Registry) Resolve(ctx context.Context, image string) (Pinned, error) {
	ref, err := name.ParseReference(image)
	if err != nil {
		return Pinned{}, fmt.Errorf("image %q is not a valid reference: %w", image, err)
	}
	desc, err := remote.Get(ref, remote.WithContext(ctx), remote.WithAuthFromKeychain(authn.DefaultKeychain),
		remote.WithTransport(transport()))
	if err != nil {
		return Pinned{}, fmt.Errorf("reading %q from its registry (credentials come from `docker login` "+
			"or its credential helpers, in ~/.docker/config.json): %w", image, err)
	}
	return Pinned{Ref: pinnedRef(image, desc.Digest.String()), Manifest: desc.Manifest}, nil
}

// pinnedRef replaces the tag or digest of image with digest and keeps the
// rest as written, so a docker.io image reads as the user wrote it.
func pinnedRef(image, digest string) string {
	repo := image
	if i := strings.Index(repo, "@"); i >= 0 {
		repo = repo[:i]
	}
	if i := strings.LastIndex(repo, ":"); i > strings.LastIndex(repo, "/") {
		repo = repo[:i]
	}
	return repo + "@" + digest
}

// transport is the registry client's HTTP transport: go-containerregistry's
// default, with *.localhost names dialed on the loopback (loopbackAddr) and
// never sent through a proxy (bypassLocalhost).
func transport() http.RoundTripper {
	base, ok := remote.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{Proxy: http.ProxyFromEnvironment}
	}
	t := base.Clone()
	t.Proxy = bypassLocalhost(t.Proxy)
	d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return d.DialContext(ctx, network, loopbackAddr(addr))
	}
	return t
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
