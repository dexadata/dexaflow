package build

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// testRegistry starts an in-memory registry and returns its host:port. A
// 127.0.0.1 registry is spoken to over plain HTTP, as a k3d one on
// *.localhost is.
func testRegistry(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(registry.New())
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// pushImage pushes a random single-platform image under ref and returns its
// digest.
func pushImage(t *testing.T, ref string) v1.Hash {
	t.Helper()
	img, err := random.Image(64, 1)
	if err != nil {
		t.Fatal(err)
	}
	r, err := name.ParseReference(ref)
	if err != nil {
		t.Fatal(err)
	}
	if werr := remote.Write(r, img); werr != nil {
		t.Fatal(werr)
	}
	d, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// pushIndex pushes an index holding one random image per platform under ref
// and returns its digest.
func pushIndex(t *testing.T, ref string, platforms ...string) v1.Hash {
	t.Helper()
	var idx v1.ImageIndex = empty.Index
	for _, p := range platforms {
		img, err := random.Image(32, 1)
		if err != nil {
			t.Fatal(err)
		}
		plat, err := v1.ParsePlatform(p)
		if err != nil {
			t.Fatal(err)
		}
		idx = mutate.AppendManifests(idx, mutate.IndexAddendum{Add: img, Descriptor: v1.Descriptor{Platform: plat}})
	}
	r, err := name.ParseReference(ref)
	if err != nil {
		t.Fatal(err)
	}
	if werr := remote.WriteIndex(r, idx); werr != nil {
		t.Fatal(werr)
	}
	d, err := idx.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// The registry answers with the digest of what the tag names, and the pinned
// reference keeps the image as written, tag replaced by the digest.
func TestRegistryResolvesATagToItsDigest(t *testing.T) {
	host := testRegistry(t)
	d := pushImage(t, host+"/team/etl:v1")

	got, err := Registry{}.Resolve(context.Background(), host+"/team/etl:v1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if want := host + "/team/etl@" + d.String(); got.Ref != want {
		t.Errorf("Ref = %q, want %q", got.Ref, want)
	}
	if len(got.Manifest) == 0 {
		t.Error("Manifest is empty")
	}
}

// An index resolves to the index digest, and its manifest lists the
// platforms the multi-platform check reads.
func TestRegistryResolvesAnIndex(t *testing.T) {
	host := testRegistry(t)
	d := pushIndex(t, host+"/team/etl:v2", "linux/amd64", "linux/arm64")

	got, err := Registry{}.Resolve(context.Background(), host+"/team/etl:v2")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !strings.HasSuffix(got.Ref, "@"+d.String()) {
		t.Errorf("Ref = %q, want the index digest %s", got.Ref, d)
	}
	if missing := missingPlatforms(got.Manifest, []string{"linux/amd64", "linux/arm64"}); len(missing) > 0 {
		t.Errorf("the resolved manifest misses %v", missing)
	}
}

// A tag the registry does not have is an error that names the image and says
// where credentials come from, never an unpinned reference.
func TestRegistryRefusesAMissingImage(t *testing.T) {
	host := testRegistry(t)
	_, err := Registry{}.Resolve(context.Background(), host+"/team/etl:nope")
	if err == nil {
		t.Fatal("a missing tag resolved")
	}
	for _, want := range []string{host + "/team/etl:nope", "docker login"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not say %q", err, want)
		}
	}
}

func TestRegistryRefusesAMalformedReference(t *testing.T) {
	if _, err := (Registry{}).Resolve(context.Background(), "Not A Ref"); err == nil {
		t.Fatal("a malformed reference resolved")
	}
}

func TestPinnedRefKeepsTheSpelling(t *testing.T) {
	cases := map[string]string{
		"reg.io/team/etl:v1":                 "reg.io/team/etl@sha256:abc",
		"etl:v1":                             "etl@sha256:abc",
		"localhost:5000/etl:v1":              "localhost:5000/etl@sha256:abc",
		"localhost:5000/etl":                 "localhost:5000/etl@sha256:abc",
		"reg.io/etl:v1@sha256:old":           "reg.io/etl@sha256:abc",
		"k3d-x.localhost:5050/a/b/etl:1.2.3": "k3d-x.localhost:5050/a/b/etl@sha256:abc",
	}
	for image, want := range cases {
		if got := pinnedRef(image, "sha256:abc"); got != want {
			t.Errorf("pinnedRef(%q) = %q, want %q", image, got, want)
		}
	}
}

// A k3d registry is named *.localhost, which RFC 6761 reserves for the
// loopback but which not every resolver answers (macOS, a container without
// systemd-resolved). The client dials the loopback for such names itself.
func TestRegistryReachesALocalhostName(t *testing.T) {
	host := testRegistry(t)
	_, port, _ := strings.Cut(host, ":")
	d := pushImage(t, host+"/team/etl:v1")

	got, err := Registry{}.Resolve(context.Background(), "k3d-registry.localhost:"+port+"/team/etl:v1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if want := "k3d-registry.localhost:" + port + "/team/etl@" + d.String(); got.Ref != want {
		t.Errorf("Ref = %q, want %q", got.Ref, want)
	}
}

func TestLoopbackAddr(t *testing.T) {
	cases := map[string]string{
		"k3d-registry.localhost:5111": "127.0.0.1:5111",
		"LOCALHOST:5000":              "127.0.0.1:5000",
		"a.b.localhost:443":           "127.0.0.1:443",
		"reg.io:443":                  "reg.io:443",
		"notlocalhost:80":             "notlocalhost:80",
		"localhost.reg.io:443":        "localhost.reg.io:443",
	}
	for in, want := range cases {
		if got := loopbackAddr(in); got != want {
			t.Errorf("loopbackAddr(%q) = %q, want %q", in, got, want)
		}
	}
}

// A proxy from the environment is never used for a *.localhost registry: it
// would resolve the name on the proxy's side, where it is not this machine.
// Other hosts keep the proxy the environment names.
func TestRegistryBypassesTheProxyForLocalhostNames(t *testing.T) {
	proxy, _ := url.Parse("http://proxy.invalid:3128")
	viaProxy := func(*http.Request) (*url.URL, error) { return proxy, nil }
	for host, want := range map[string]*url.URL{
		"k3d-registry.localhost:5111": nil,
		"localhost:5000":              nil,
		"reg.io":                      proxy,
	} {
		r, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+host+"/v2/", http.NoBody)
		got, err := bypassLocalhost(viaProxy)(r)
		if err != nil || got != want {
			t.Errorf("proxy for %s = %v, %v; want %v", host, got, err, want)
		}
	}
	if transport().(*http.Transport).Proxy == nil {
		t.Error("the transport dropped the environment's proxy")
	}
}
