package build

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
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
		if got := PinnedRef(image, "sha256:abc"); got != want {
			t.Errorf("PinnedRef(%q) = %q, want %q", image, got, want)
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
}

// The transport the client uses carries the bypass: a *.localhost request
// gets no proxy, while another host keeps the proxy of the base transport.
func TestTransportWiresTheProxyBypass(t *testing.T) {
	proxy, _ := url.Parse("http://proxy.invalid:3128")
	base := &http.Transport{Proxy: func(*http.Request) (*url.URL, error) { return proxy, nil }}
	tr, ok := transport(base).(plainHTTPOnLoopback)
	if !ok {
		t.Fatalf("transport is %T, want plainHTTPOnLoopback", transport(base))
	}
	inner, ok := tr.next.(*http.Transport)
	if !ok {
		t.Fatalf("inner transport is %T", tr.next)
	}
	for host, want := range map[string]*url.URL{"k3d-registry.localhost:5111": nil, "reg.io": proxy} {
		r, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://"+host+"/v2/", http.NoBody)
		if got, err := inner.Proxy(r); err != nil || got != want {
			t.Errorf("proxy for %s = %v, %v; want %v", host, got, err, want)
		}
	}
}

// A registry on a private address that answers only plain HTTP is never
// read: go-containerregistry would fall back to HTTP for it, and anyone on
// the network answering HTTP would get the credentials and pick the digest.
// A loopback registry still is (TestRegistryResolvesATagToItsDigest).
func TestRegistryRefusesPlainHTTPOffTheLoopback(t *testing.T) {
	for _, u := range []string{"http://10.0.5.20:5000/v2/", "http://192.168.1.9:5000/v2/", "http://reg.io/v2/"} {
		if sent, err := roundTripVia(t, u); err == nil || sent {
			t.Errorf("plain HTTP to %s was sent (err %v)", u, err)
		}
	}
	for _, u := range []string{
		"http://127.0.0.1:5000/v2/", "http://[::1]:5000/v2/", "http://localhost:5000/v2/",
		"http://k3d-r.localhost:5111/v2/", "https://10.0.5.20:5000/v2/",
	} {
		if sent, err := roundTripVia(t, u); err != nil || !sent {
			t.Errorf("request to %s was refused: %v", u, err)
		}
	}
}

// roundTripVia sends a GET for u through plainHTTPOnLoopback and reports
// whether it reached the next transport.
func roundTripVia(t *testing.T, u string) (bool, error) {
	t.Helper()
	r, err := http.NewRequestWithContext(context.Background(), http.MethodGet, u, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	sent := false
	next := roundTripFunc(func(*http.Request) (*http.Response, error) {
		sent = true
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})
	resp, err := plainHTTPOnLoopback{next: next}.RoundTrip(r)
	if resp != nil {
		_ = resp.Body.Close()
	}
	return sent, err
}

// The end-to-end path: a plain HTTP registry reached through a private
// address fails to resolve, with the refusal in the error.
func TestRegistryResolveRefusesAPlainHTTPPrivateRegistry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := Registry{}.Resolve(ctx, "10.255.255.1:5000/team/etl:v1")
	if err == nil || !strings.Contains(err.Error(), "refusing plain HTTP") {
		t.Fatalf("Resolve of a private plain HTTP registry: %v, want a plain HTTP refusal", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A schema 1 manifest takes its digest from a response header that nothing
// checks, so the registry could name any digest; it is refused.
func TestRegistryRefusesASchema1Manifest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" {
			return
		}
		w.Header().Set("Content-Type", string(types.DockerManifestSchema1Signed))
		w.Header().Set("Docker-Content-Digest", "sha256:"+strings.Repeat("a", 64))
		_, _ = w.Write([]byte(`{"schemaVersion":1}`))
	}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	_, err := Registry{}.Resolve(context.Background(), host+"/team/etl:v1")
	if err == nil || !strings.Contains(err.Error(), "not an OCI or Docker v2") {
		t.Fatalf("Resolve of a schema 1 manifest: %v, want a media type refusal", err)
	}
}

// Platform reads what a single image's config names, which the skip path
// compares with the platform asked for.
func TestRegistryReadsThePlatformOfASingleImage(t *testing.T) {
	host := testRegistry(t)
	pushImage(t, host+"/team/etl:v1")
	// random.Image writes a config with no os or architecture.
	if got, err := (Registry{}).Platform(context.Background(), host+"/team/etl:v1"); err != nil || got != "" {
		t.Errorf("Platform = %q, %v; want empty for a config that names none", got, err)
	}
	img, _ := random.Image(16, 1)
	cfg, _ := img.ConfigFile()
	cfg.OS, cfg.Architecture, cfg.Variant = "linux", "arm64", "v8"
	img, _ = mutate.ConfigFile(img, cfg)
	r, _ := name.ParseReference(host + "/team/etl:arm")
	if werr := remote.Write(r, img); werr != nil {
		t.Fatal(werr)
	}
	if got, err := (Registry{}).Platform(context.Background(), host+"/team/etl:arm"); err != nil || got != "linux/arm64/v8" {
		t.Errorf("Platform = %q, %v; want linux/arm64/v8", got, err)
	}
}

// CheckReference accepts what Resolve can read and refuses the rest.
func TestCheckReference(t *testing.T) {
	for _, ok := range []string{"ghcr.io/acme/etl:v1", "localhost:5000/etl", "etl@sha256:" + strings.Repeat("a", 64)} {
		if err := CheckReference(ok); err != nil {
			t.Errorf("CheckReference(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Not A Reference", "ghcr.io/acme/etl@sha256:abc", "ghcr.io/Acme/etl:v1"} {
		if err := CheckReference(bad); err == nil {
			t.Errorf("CheckReference(%q) accepted it", bad)
		}
	}
}
