package build

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

// sitePackages311 is the site-packages of a Python 3.11 runtime base.
const sitePackages311 = "usr/local/lib/python3.11/site-packages/"

// dpkgStatus is a dpkg status file listing glibc at version.
func dpkgStatus(version string) string {
	return "Package: tzdata\nStatus: install ok installed\nVersion: 2025b-4\n\n" +
		"Package: libc6\nStatus: install ok installed\nArchitecture: amd64\nVersion: " + version + "\n\n"
}

// tarLayer makes a layer of entries: a name ending in / is a directory, any
// other a regular file whose body is the value.
func tarLayer(t *testing.T, entries ...[2]string) v1.Layer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		h := &tar.Header{Name: e[0], Mode: 0o644, Typeflag: tar.TypeReg, Size: int64(len(e[1]))}
		if strings.HasSuffix(e[0], "/") {
			h.Typeflag, h.Mode, h.Size = tar.TypeDir, 0o755, 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil })
	if err != nil {
		t.Fatal(err)
	}
	return layer
}

// baseImage is a linux/amd64 image of layers whose config sets env.
func baseImage(t *testing.T, env []string, layers ...v1.Layer) v1.Image {
	t.Helper()
	return platformImage(t, "linux/amd64", env, layers...)
}

// platformImage is an image for platform of layers whose config sets env.
func platformImage(t *testing.T, platform string, env []string, layers ...v1.Layer) v1.Image {
	t.Helper()
	img, err := mutate.AppendLayers(empty.Image, layers...)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	cfg = cfg.DeepCopy()
	plat, err := v1.ParsePlatform(platform)
	if err != nil {
		t.Fatal(err)
	}
	cfg.OS, cfg.Architecture, cfg.Variant = plat.OS, plat.Architecture, plat.Variant
	cfg.Config.Env = env
	cfg.Config.User = "65532:65532"
	cfg.Config.WorkingDir = Workdir
	img, err = mutate.ConfigFile(img, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// runtimeLayers are the layers of a fake Python 3.11 runtime base: a first
// layer with pip 24 and six, then one that upgrades pip (a whiteout of the
// old dist-info) and adds the Task SDK, as the runtime Dockerfile does.
func runtimeLayers(t *testing.T) []v1.Layer {
	t.Helper()
	return []v1.Layer{
		tarLayer(t, [2]string{"var/lib/dpkg/status", dpkgStatus("2.41-12")},
			[2]string{sitePackages311, ""},
			[2]string{sitePackages311 + "pip-24.0.dist-info/", ""},
			[2]string{sitePackages311 + "pip-24.0.dist-info/RECORD", "x"},
			[2]string{sitePackages311 + "six-1.16.0.dist-info/METADATA", "x"}),
		tarLayer(t, [2]string{sitePackages311 + ".wh.pip-24.0.dist-info", ""},
			[2]string{sitePackages311 + "pip-26.2.1.dist-info/RECORD", "x"},
			[2]string{sitePackages311 + "apache_airflow_task_sdk-1.3.1.dist-info/RECORD", "x"},
			[2]string{"usr/local/lib/python3.11/os.py", "x"}),
	}
}

// pushRuntimeBase pushes an index holding one runtime base image per
// platform, plus an attestation manifest, and returns what the registry
// says it names.
func pushRuntimeBase(t *testing.T, ref string, platforms ...string) Pinned {
	t.Helper()
	var idx v1.ImageIndex = empty.Index
	for _, p := range platforms {
		img := platformImage(t, p, []string{"PATH=/usr/local/bin:/usr/bin", "PYTHON_VERSION=3.11.9"}, runtimeLayers(t)...)
		plat, err := v1.ParsePlatform(p)
		if err != nil {
			t.Fatal(err)
		}
		idx = mutate.AppendManifests(idx, mutate.IndexAddendum{Add: img, Descriptor: v1.Descriptor{Platform: plat}})
	}
	att := baseImage(t, nil)
	idx = mutate.AppendManifests(idx, mutate.IndexAddendum{Add: att, Descriptor: v1.Descriptor{
		Platform:    &v1.Platform{OS: "unknown", Architecture: "unknown"},
		Annotations: map[string]string{"vnd.docker.reference.type": "attestation-manifest"},
	}})
	r, err := name.ParseReference(ref)
	if err != nil {
		t.Fatal(err)
	}
	if werr := remote.WriteIndex(r, idx); werr != nil {
		t.Fatal(werr)
	}
	pinned, err := Registry{}.Resolve(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	return pinned
}

// countingRegistry starts an in-memory registry that counts the blob reads
// it serves, and returns its host:port and that count.
func countingRegistry(t *testing.T) (host string, blobReads *atomic.Int64) {
	t.Helper()
	blobReads = &atomic.Int64{}
	reg := registry.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/blobs/") {
			blobReads.Add(1)
		}
		reg.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://"), blobReads
}

// The base listing reads the Python version from the config, glibc from the
// dpkg status, and the distributions in site-packages with the whiteouts of
// later layers applied, for the platform asked from an index.
func TestRegistryReadsTheRuntimeBase(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	host := testRegistry(t)
	pinned := pushRuntimeBase(t, host+"/runtime:py3.11", "linux/amd64", "linux/arm64")
	got, err := Registry{}.Base(context.Background(), pinned, "linux/arm64")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"apache-airflow-task-sdk==1.3.1", "pip==26.2.1", "six==1.16.0"}
	if got.Python != "3.11.9" || got.Glibc != "2.41" || !got.SitePackages || !slices.Equal(got.Dists, want) {
		t.Errorf("base = %+v, want Python 3.11.9, glibc 2.41, site-packages with %q", got, want)
	}
	idx, err := v1.ParseIndexManifest(bytes.NewReader(pinned.Manifest))
	if err != nil {
		t.Fatal(err)
	}
	if got.Digest != idx.Manifests[1].Digest.String() {
		t.Errorf("digest = %s, want the arm64 image %s", got.Digest, idx.Manifests[1].Digest)
	}
	if got.MinorPython() != "3.11" {
		t.Errorf("minor = %q, want 3.11", got.MinorPython())
	}
}

// An opaque directory in a later layer hides what earlier layers put in it.
func TestRegistryAppliesAnOpaqueSitePackages(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	host := testRegistry(t)
	img := baseImage(t, []string{"PYTHON_VERSION=3.11.9"}, append(runtimeLayers(t),
		tarLayer(t, [2]string{sitePackages311 + ".wh..wh..opq", ""}, [2]string{sitePackages311 + "only-2.0.dist-info/RECORD", "x"}))...)
	r, err := name.ParseReference(host + "/runtime:opq")
	if err != nil {
		t.Fatal(err)
	}
	if werr := remote.Write(r, img); werr != nil {
		t.Fatal(werr)
	}
	pinned, err := Registry{}.Resolve(context.Background(), host+"/runtime:opq")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Registry{}.Base(context.Background(), pinned, "linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Dists, []string{"only==2.0"}) {
		t.Errorf("dists = %q, want only what the opaque layer holds", got.Dists)
	}
}

// An image whose config names no PYTHON_VERSION is not a runtime base: its
// layers are not read.
func TestRegistryReadsNoLayerOfAnImageThatIsNotARuntimeBase(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	host, reads := countingRegistry(t)
	img := baseImage(t, []string{"PATH=/bin"}, runtimeLayers(t)...)
	r, err := name.ParseReference(host + "/other:1")
	if err != nil {
		t.Fatal(err)
	}
	if werr := remote.Write(r, img); werr != nil {
		t.Fatal(werr)
	}
	pinned, err := Registry{}.Resolve(context.Background(), host+"/other:1")
	if err != nil {
		t.Fatal(err)
	}
	reads.Store(0)
	got, err := Registry{}.Base(context.Background(), pinned, "linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	if got.Python != "" || got.SitePackages || reads.Load() != 1 {
		t.Errorf("base = %+v after %d blob reads, want no Python and only the config read", got, reads.Load())
	}
}

// A second read of the same base platform image comes from the user cache
// and downloads no blob.
func TestRegistryCachesTheBaseListing(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	host, reads := countingRegistry(t)
	pinned := pushRuntimeBase(t, host+"/runtime:py3.11", "linux/amd64")
	first, err := Registry{}.Base(context.Background(), pinned, "linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	if reads.Load() == 0 {
		t.Fatal("the first read downloaded nothing")
	}
	reads.Store(0)
	again, err := Registry{}.Base(context.Background(), pinned, "linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	if reads.Load() != 0 {
		t.Errorf("the second read downloaded %d blobs, want none", reads.Load())
	}
	if !slices.Equal(again.Dists, first.Dists) || again.Python != first.Python || again.Digest != first.Digest {
		t.Errorf("cached base = %+v, want %+v", again, first)
	}
}

// A platform the index does not list is an error naming it.
func TestRegistryBaseNamesAMissingPlatform(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	host := testRegistry(t)
	pinned := pushRuntimeBase(t, host+"/runtime:py3.11", "linux/amd64")
	if _, err := (Registry{}).Base(context.Background(), pinned, "linux/arm64"); err == nil || !strings.Contains(err.Error(), "linux/arm64") {
		t.Fatalf("err = %v, want one naming linux/arm64", err)
	}
}

func TestParseGlibc(t *testing.T) {
	cases := map[string]string{
		dpkgStatus("2.41-12"):                 "2.41",
		dpkgStatus("2.36-9+deb12u10"):         "2.36",
		"Package: musl\nVersion: 1.2.5-1\n\n": "",
		"Package: libc6\nStatus: deinstall ok config-files\nVersion: 2.31-13\n\n": "",
	}
	for status, want := range cases {
		if got := parseGlibc([]byte(status)); got != want {
			t.Errorf("parseGlibc(%q) = %q, want %q", status, got, want)
		}
	}
}

// A single base image is read only for the platform its config names.
func TestRegistryBaseChecksThePlatformOfASingleImage(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	host := testRegistry(t)
	r, err := name.ParseReference(host + "/runtime:one")
	if err != nil {
		t.Fatal(err)
	}
	if werr := remote.Write(r, baseImage(t, []string{"PYTHON_VERSION=3.11.9"}, runtimeLayers(t)...)); werr != nil {
		t.Fatal(werr)
	}
	pinned, err := Registry{}.Resolve(context.Background(), host+"/runtime:one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (Registry{}).Base(context.Background(), pinned, "linux/arm64"); err == nil || !strings.Contains(err.Error(), "not linux/arm64") {
		t.Fatalf("err = %v, want a refusal naming the platform", err)
	}
	if got, err := (Registry{}).Base(context.Background(), pinned, "linux/amd64"); err != nil || got.Python != "3.11.9" {
		t.Fatalf("amd64: %+v, %v", got, err)
	}
}
