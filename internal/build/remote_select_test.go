package build

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeRemote is a Remote that records what it was asked to build and, when
// push is set, pushes a plain image under the requested tag, as Cloud Build
// would.
type fakeRemote struct {
	t       *testing.T
	decline string
	ready   error
	push    bool
	reqs    []Request
}

func (f *fakeRemote) Path() Path                  { return PathRemote }
func (f *fakeRemote) Describe() string            { return "remote (fake)" }
func (f *fakeRemote) Ready(context.Context) error { return f.ready }
func (f *fakeRemote) Decline(_ []string) string   { return f.decline }
func (f *fakeRemote) Build(ctx context.Context, req Request) (Result, error) {
	f.reqs = append(f.reqs, req)
	if !f.push {
		return Result{Path: PathRemote, Image: req.Image, Pushed: true}, nil
	}
	pushImage(f.t, req.Image)
	pinned, err := Registry{}.Resolve(ctx, req.Image)
	if err != nil {
		return Result{}, err
	}
	return Result{Path: PathRemote, Image: req.Image, Pushed: true, Ref: pinned.Ref}, nil
}

// A build no daemonless path covers (here, a project Dockerfile) goes to the
// configured remote builder before the local daemon, and the builder line
// says why and where the remote builder was configured.
func TestSelectSendsAWholeBuildToTheRemoteBuilder(t *testing.T) {
	rem := &fakeRemote{t: t}
	b, reason := Select(context.Background(), Options{Remote: rem, RemoteSource: "dexaflow.yaml", Push: true,
		NoSplit: "the project ships its own Dockerfile, which needs a real build"}, "r.io/dags/etl:v1", []string{"linux/amd64"})
	if b != Builder(rem) {
		t.Fatalf("Select = %#v (reason %q), want the remote builder", b, reason)
	}
	for _, want := range []string{"the project ships its own Dockerfile", "the remote builder from dexaflow.yaml builds the whole image"} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason %q does not say %q", reason, want)
		}
	}
}

// The remote builder is passed over, with the reason on the builder line,
// when the build is not pushed, when it declines the platforms, and when its
// CLI is missing while the daemon's tool is on PATH.
func TestSelectPassesOverTheRemoteBuilderThatDoesNotApply(t *testing.T) {
	tool, _ := fakeTool(t, "")
	cases := map[string]struct {
		o   Options
		why string
	}{
		"not pushed": {Options{Remote: &fakeRemote{t: t}}, "pushes what it builds, and this build is not pushed"},
		"declined":   {Options{Remote: &fakeRemote{t: t, decline: "fake builds linux/amd64 only"}, Push: true}, "does not apply: fake builds linux/amd64 only"},
		"not ready":  {Options{Remote: &fakeRemote{t: t, ready: errors.New("gcloud is not on PATH")}, Push: true}, "does not apply: gcloud is not on PATH"},
	}
	for name, tc := range cases {
		tc.o.Tool, tc.o.NoSplit = tool, "the project ships its own Dockerfile"
		b, reason := Select(context.Background(), tc.o, "r.io/dags/etl:v1", []string{"linux/amd64"})
		if b.Path() != PathLocalDaemon {
			t.Errorf("%s: Select = %s, want the daemon", name, b.Describe())
		}
		if !strings.Contains(reason, tc.why) || !strings.Contains(reason, "the project ships its own Dockerfile") {
			t.Errorf("%s: reason %q does not say %q and why the build is whole", name, reason, tc.why)
		}
	}
}

// With neither gcloud nor the daemon's tool on PATH, the remote builder is
// kept, so the error is the one about what was configured.
func TestSelectKeepsAnUnreadyRemoteBuilderWithoutADaemon(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	rem := &fakeRemote{t: t, ready: errors.New("gcloud is not on PATH")}
	b, reason := Select(context.Background(), Options{Remote: rem, Push: true, NoSplit: "own Dockerfile"}, "r.io/dags/etl:v1", []string{"linux/amd64"})
	if b != Builder(rem) {
		t.Errorf("Select = %s (reason %q), want the remote builder", b.Describe(), reason)
	}
}

// A missing dependency image that the wheels cannot build is built by the
// remote builder from its Dockerfile alone (an empty context), then the code
// is appended through the registry; no daemon runs.
func TestSelectBuildsTheDependencyImageRemotely(t *testing.T) {
	host, split := splitFixture(t)
	split.NoWheels = "the project has system_packages, which need a real build"
	tool, log := fakeTool(t, "")
	rem := &fakeRemote{t: t, push: true}
	b, reason := Select(context.Background(), Options{Tool: tool, Split: split, Remote: rem, Push: true}, host+"/dags/etl:v1", []string{"linux/amd64"})
	if b.Path() != PathRemote || b.Describe() != "remote (fake) for the dependency image, then code-layer" {
		t.Fatalf("Select = %s (%s), want the remote builder for the dependency image (reason %q)", b.Path(), b.Describe(), reason)
	}
	for _, want := range []string{"deps-0123", "the remote builder builds and pushes it", "system_packages"} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason %q does not say %q", reason, want)
		}
	}
	res, err := b.Build(context.Background(), codeRequest(t, host+"/dags/etl:v1", "linux/amd64"))
	if err != nil {
		t.Fatal(err)
	}
	if got := calls(t, log); got != nil {
		t.Errorf("the daemon ran %v, want nothing", got)
	}
	if len(rem.reqs) != 1 {
		t.Fatalf("remote builds = %d, want one", len(rem.reqs))
	}
	got := rem.reqs[0]
	if got.Image != split.DepsImage || !got.Push || !got.Pin || got.ContextFiles == nil || len(got.ContextFiles) != 0 {
		t.Errorf("remote request = image %q push %v pin %v context %v, want the dependency image, pushed and pinned, from an empty listed context",
			got.Image, got.Push, got.Pin, got.ContextFiles)
	}
	if res.Path != PathRemote || !res.Pushed || !strings.Contains(res.Ref, "@sha256:") {
		t.Errorf("result = %+v, want the remote path, pushed and pinned", res)
	}
	if layers := entryNames(t, lastLayer(t, host+"/dags/etl:v1")); !strings.Contains(layers, "home/leoflow/dag.py") {
		t.Errorf("the image's last layer holds %s, want the DAG code", layers)
	}
}

// When the wheels fail after the path was chosen, the remote builder takes
// the daemon's place for the dependency image of the daemon's key.
func TestCodeLayerFallsBackToTheRemoteBuilderAfterTheWheels(t *testing.T) {
	host, split := splitFixture(t)
	split.DaemonDepsImage = host + "/dags/etl:deps-daemon"
	t.Setenv("PATH", t.TempDir()) // no daemon tool
	rem := &fakeRemote{t: t, push: true}
	c := CodeLayer{Split: *split, Remote: rem, Registry: Registry{}}
	pinned, path, err := c.daemonFallback(context.Background(), codeRequest(t, host+"/dags/etl:v1", "linux/amd64"), errors.New("uv failed"))
	if err != nil {
		t.Fatalf("daemonFallback: %v", err)
	}
	if path != PathRemote || len(rem.reqs) != 1 || rem.reqs[0].Image != split.DaemonDepsImage || !strings.Contains(pinned.Ref, "@sha256:") {
		t.Errorf("fallback = path %s, remote requests %d, ref %q, want the remote builder for %s", path, len(rem.reqs), pinned.Ref, split.DaemonDepsImage)
	}
}
