package build

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	ggcrtransport "github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

// splitFixture is a registry with no images yet and the split of a build of
// <host>/dags/etl:v1.
func splitFixture(t *testing.T) (host string, split *Split) {
	t.Helper()
	host = testRegistry(t)
	return host, &Split{DepsImage: host + "/dags/etl:deps-0123", Dockerfile: "FROM base@sha256:00\nUSER 65532:65532\n"}
}

// codeRequest asks for a pushed, pinned build of image with the fixture code.
func codeRequest(t *testing.T, image string, platforms ...string) Request {
	t.Helper()
	var out bytes.Buffer
	return Request{Image: image, ContextDir: t.TempDir(), Platforms: platforms, Push: true, Pin: true, Code: codeFixture(t), Stdout: &out, Stderr: &out}
}

// With the dependency image for this content in the registry, only the code
// is pushed: no daemon runs, and the result is pinned to the registry's
// digest of the new image.
func TestSelectAppendsCodeToACachedDependencyImage(t *testing.T) {
	host, split := splitFixture(t)
	pushIndexWithAttestation(t, split.DepsImage, "linux/amd64")
	tool, log := fakeTool(t, "")
	b, reason := Select(context.Background(), Options{Tool: tool, Split: split}, host+"/dags/etl:v1", []string{"linux/amd64"})
	if b.Path() != PathCodeLayer || b.Describe() != "code-layer" {
		t.Fatalf("path = %s (%s), want %s (reason %q)", b.Path(), b.Describe(), PathCodeLayer, reason)
	}
	if !strings.Contains(reason, "deps-0123") || !strings.Contains(reason, "--rebuild") {
		t.Errorf("reason %q does not name the dependency image and how to build it again", reason)
	}
	res, err := b.Build(context.Background(), codeRequest(t, host+"/dags/etl:v1", "linux/amd64"))
	if err != nil {
		t.Fatal(err)
	}
	if got := calls(t, log); got != nil {
		t.Errorf("the daemon ran %v, want nothing", got)
	}
	pinned, err := Registry{}.Resolve(context.Background(), host+"/dags/etl:v1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != PathCodeLayer || !res.Pushed || res.Ref != pinned.Ref {
		t.Errorf("result = %+v, want path code-layer, pushed, pinned to %s", res, pinned.Ref)
	}
}

// Without the dependency image, the daemon builds and pushes it from its own
// Dockerfile in an empty context, then the code is appended to it.
func TestSelectBuildsTheDependencyImageWhenItIsMissing(t *testing.T) {
	host, split := splitFixture(t)
	tool, log := fakeTool(t, "")
	b, reason := Select(context.Background(), Options{Tool: tool, Split: split}, host+"/dags/etl:v1", []string{"linux/amd64"})
	if b.Path() != PathLocalDaemon || !strings.Contains(b.Describe(), "then code-layer") {
		t.Fatalf("path = %s (%s), want the daemon for the dependency image (reason %q)", b.Path(), b.Describe(), reason)
	}
	if !strings.Contains(reason, "deps-0123") {
		t.Errorf("reason %q does not name the dependency image", reason)
	}
	// What the daemon would have pushed.
	pushImage(t, split.DepsImage)
	req := codeRequest(t, host+"/dags/etl:v1", "linux/amd64")
	res, err := b.Build(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	got := calls(t, log)
	if len(got) != 2 || !strings.HasPrefix(got[0], "build --platform linux/amd64 -t "+split.DepsImage+" -f ") || got[1] != "push "+split.DepsImage {
		t.Fatalf("daemon calls = %q, want a build and a push of the dependency image", got)
	}
	if strings.Contains(got[0], req.ContextDir) {
		t.Errorf("the dependency image was built from the project directory: %q", got[0])
	}
	if res.Path != PathLocalDaemon || !res.Pushed || !strings.Contains(res.Ref, "@sha256:") {
		t.Errorf("result = %+v, want a pushed, pinned image", res)
	}
	if layers := entryNames(t, lastLayer(t, host+"/dags/etl:v1")); !strings.Contains(layers, "home/leoflow/dag.py") {
		t.Errorf("the image's last layer holds %s, want the DAG code", layers)
	}
}

// --rebuild builds the dependency image again even when the registry holds
// one, and a dependency image missing a platform is built again too.
func TestSelectBuildsTheDependencyImageAgain(t *testing.T) {
	host, split := splitFixture(t)
	pushIndexWithAttestation(t, split.DepsImage, "linux/amd64")
	cases := map[string]struct {
		o         Options
		platforms []string
		why       string
	}{
		"rebuild":          {Options{Split: split, Rebuild: true}, []string{"linux/amd64"}, "--rebuild"},
		"missing platform": {Options{Split: split}, []string{"linux/amd64", "linux/arm64"}, "linux/arm64"},
	}
	for name, tc := range cases {
		b, reason := Select(context.Background(), tc.o, host+"/dags/etl:v1", tc.platforms)
		if b.Path() != PathLocalDaemon || !strings.Contains(b.Describe(), "code-layer") {
			t.Errorf("%s: path = %s (%s), want the daemon for the dependency image", name, b.Path(), b.Describe())
		}
		if !strings.Contains(reason, tc.why) {
			t.Errorf("%s: reason %q does not say %q", name, reason, tc.why)
		}
	}
}

// The whole image for this content label comes first: when it is in the
// registry, nothing is built, split or not.
func TestSelectPrefersTheWholeImageOverTheCodeLayer(t *testing.T) {
	host, split := splitFixture(t)
	pushIndexWithAttestation(t, split.DepsImage, "linux/amd64")
	pushIndexWithAttestation(t, host+"/dags/etl:v1", "linux/amd64")
	b, _ := Select(context.Background(), Options{Reuse: true, Split: split}, host+"/dags/etl:v1", []string{"linux/amd64"})
	if b.Path() != PathSkip {
		t.Fatalf("path = %s, want %s", b.Path(), PathSkip)
	}
}

// No platform (the tool would pick its host's) keeps the whole build on the
// daemon, and a reason for not splitting reaches the builder line.
func TestSelectSplitsOnlyWhenItCan(t *testing.T) {
	host, split := splitFixture(t)
	b, _ := Select(context.Background(), Options{Split: split}, host+"/dags/etl:v1", nil)
	if _, ok := b.(LocalDaemon); !ok {
		t.Errorf("no platform: Select = %#v, want the whole build on the daemon", b)
	}
	b, reason := Select(context.Background(), Options{NoSplit: "the project ships its own Dockerfile"}, "r:1", []string{"linux/amd64"})
	if _, ok := b.(LocalDaemon); !ok || !strings.Contains(reason, "the project ships its own Dockerfile") {
		t.Errorf("no split: Select = %#v, reason %q, want the daemon and the reason", b, reason)
	}
}

// The code layer goes through the registry API, so a build that is not
// pushed cannot take it.
func TestCodeLayerNeedsAPush(t *testing.T) {
	host, split := splitFixture(t)
	pushIndexWithAttestation(t, split.DepsImage, "linux/amd64")
	b, _ := Select(context.Background(), Options{Split: split}, host+"/dags/etl:v1", []string{"linux/amd64"})
	req := codeRequest(t, host+"/dags/etl:v1", "linux/amd64")
	req.Push, req.Pin = false, false
	if _, err := b.Build(context.Background(), req); err == nil || !strings.Contains(err.Error(), "push") {
		t.Fatalf("err = %v, want one saying the code layer needs a push", err)
	}
}

// A dependency build that fails stops before anything is appended.
func TestCodeLayerStopsWhenTheDependencyBuildFails(t *testing.T) {
	host, split := splitFixture(t)
	tool, _ := fakeTool(t, "build")
	b, _ := Select(context.Background(), Options{Tool: tool, Split: split}, host+"/dags/etl:v1", []string{"linux/amd64"})
	_, err := b.Build(context.Background(), codeRequest(t, host+"/dags/etl:v1", "linux/amd64"))
	if err == nil || !strings.Contains(err.Error(), "dependency image") {
		t.Fatalf("err = %v, want one naming the dependency image", err)
	}
	if _, rerr := (Registry{}).Resolve(context.Background(), host+"/dags/etl:v1"); rerr == nil {
		t.Error("the image was pushed after the dependency build failed")
	}
}

// lastLayer returns the last layer of the single image ref names.
func lastLayer(t *testing.T, ref string) v1.Layer {
	t.Helper()
	layers, err := remoteImage(t, ref).Layers()
	if err != nil {
		t.Fatal(err)
	}
	return layers[len(layers)-1]
}

// errRegistry answers every Resolve with err; it can push, so only the read
// decides the path.
type errRegistry struct{ err error }

func (r errRegistry) Resolve(context.Context, string) (Pinned, error) { return Pinned{}, r.err }

func (errRegistry) AppendCode(context.Context, Pinned, []LayerFile, string) error { return nil }

// Only a registry that says the dependency image is not there has the daemon
// build it; a registry this client cannot read (401, a refused TLS
// handshake) keeps the whole build on the daemon, which may reach it.
func TestSelectSplitsOnlyWhenTheDependencyImageIsMissing(t *testing.T) {
	split := &Split{DepsImage: "reg.io/etl:deps-1", Dockerfile: "FROM b\n"}
	missing := &ggcrtransport.Error{StatusCode: 404, Errors: []ggcrtransport.Diagnostic{{Code: ggcrtransport.ManifestUnknownErrorCode}}}
	b, _ := Select(context.Background(), Options{Split: split, Registry: errRegistry{fmt.Errorf("reading: %w", missing)}}, "reg.io/etl:v1", []string{"linux/amd64"})
	if _, ok := b.(CodeLayer); !ok {
		t.Errorf("missing: Select = %#v, want the daemon to build the dependency image", b)
	}
	for name, err := range map[string]error{
		"unauthorized": fmt.Errorf("reading: %w", &ggcrtransport.Error{StatusCode: 401}),
		"tls":          errors.New("tls: failed to verify certificate"),
	} {
		b, reason := Select(context.Background(), Options{Split: split, Registry: errRegistry{err}}, "reg.io/etl:v1", []string{"linux/amd64"})
		if _, ok := b.(LocalDaemon); !ok || !strings.Contains(reason, "could not be read") {
			t.Errorf("%s: Select = %#v, reason %q, want the whole build on the daemon", name, b, reason)
		}
	}
}
