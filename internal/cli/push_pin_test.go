package cli

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dexadata/dexaflow/internal/api"
	"github.com/dexadata/dexaflow/internal/domain"
)

// recordingVersions is a versions repository that keeps the image of every
// spec registered, so a test can read what the control plane received.
type recordingVersions struct {
	mu     sync.Mutex
	images []string
}

func (r *recordingVersions) RegisterDagVersionAs(_ context.Context, _ string, reg domain.DagRegistration) (domain.DagRegistered, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.images = append(r.images, reg.Spec.Image)
	return domain.DagRegistered{Created: true}, nil
}

func (r *recordingVersions) SetCurrentDagVersionAs(context.Context, string, domain.DagSetCurrent) (domain.CurrentVersionChange, error) {
	return domain.CurrentVersionChange{}, nil
}

// registered returns the images registered so far.
func (r *recordingVersions) registered() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.images...)
}

// enforcingServer is the real API under deploy.require_digest=enforce,
// recording what it registers.
func enforcingServer(t *testing.T) (*httptest.Server, *recordingVersions) {
	t.Helper()
	repo := &recordingVersions{}
	srv := httptest.NewServer(api.NewServer(api.Dependencies{
		Logger: discardTestLogger(), DevNoAuth: true, Versions: repo, RequireDigest: domain.DigestRequireEnforce,
	}))
	t.Cleanup(srv.Close)
	return srv, repo
}

// writePushSpec writes a dag.json whose image is image and returns its path.
func writePushSpec(t *testing.T, image string) string {
	t.Helper()
	spec := strings.Replace(pushSpec, pushSpecImage, image, 1)
	f := filepath.Join(t.TempDir(), "dag.json")
	if err := os.WriteFile(f, []byte(spec), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

// compiledImage reads the image field of the dag.json at path.
func compiledImage(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // a path this test created
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Image string `json:"image"`
	}
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	return spec.Image
}

// `compile --build --push` writes the pushed image's digest into dag.json
// (#1703), from the read the push already makes, and names it in the
// summary; `push` of that file is then accepted by a server that enforces.
func TestCompilePushPinsTheImageInDagJSON(t *testing.T) {
	dir, reg := platformProjectWith(t, singleManifest, "")
	builder, _ := argvBuilder(t)
	out, _, err := run(t, "compile", dir, "--build", "--push", "--builder", builder)
	if err != nil {
		t.Fatalf("compile --build --push: %v\n%s", err, out)
	}
	want := reg.host + "/team/etl@" + reg.digest
	if got := compiledImage(t, filepath.Join(dir, "dag.json")); got != want {
		t.Fatalf("dag.json image = %q, want %q", got, want)
	}
	if !strings.Contains(out, "image "+want) {
		t.Errorf("summary does not name the pinned image:\n%s", out)
	}
	if reg.readCount() != 1 {
		t.Errorf("the registry was read %d time(s), want once, by the push", reg.readCount())
	}

	srv, repo := enforcingServer(t)
	if _, _, err := run(t, "push", filepath.Join(dir, "dag.json"), "--server", srv.URL, "--token", "x"); err != nil {
		t.Fatalf("push under enforce: %v", err)
	}
	if got := repo.registered(); len(got) != 1 || got[0] != want {
		t.Errorf("registered %q, want %q", got, want)
	}
	if reg.readCount() != 1 {
		t.Errorf("push read the registry for an image already pinned (%d reads)", reg.readCount())
	}
}

// A build that is not pushed has no registry digest: dag.json keeps the tag
// and the registry is not read.
func TestCompileBuildWithoutPushKeepsTheTag(t *testing.T) {
	dir, reg := platformProjectWith(t, singleManifest, "")
	builder, _ := argvBuilder(t)
	if out, _, err := run(t, "compile", dir, "--build", "--builder", builder); err != nil {
		t.Fatalf("compile --build: %v\n%s", err, out)
	}
	if got := compiledImage(t, filepath.Join(dir, "dag.json")); domain.IsDigestPinned(got) {
		t.Errorf("dag.json image = %q, want the tag", got)
	}
	if reg.readCount() != 0 {
		t.Errorf("the registry was read %d time(s), want none", reg.readCount())
	}
}

// `push` of a dag.json whose image is a tag resolves it in the registry and
// registers the digest (#1703), which a server that enforces accepts.
func TestPushResolvesATagToItsDigest(t *testing.T) {
	reg := newFakeOCIRegistry(t, singleManifest)
	f := writePushSpec(t, reg.host+"/team/etl:v1")
	srv, repo := enforcingServer(t)
	out, _, err := run(t, "push", f, "--server", srv.URL, "--token", "x")
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	want := reg.host + "/team/etl@" + reg.digest
	if got := repo.registered(); len(got) != 1 || got[0] != want {
		t.Fatalf("registered %q, want %q", got, want)
	}
	if !strings.Contains(out, want) {
		t.Errorf("stdout does not name the pinned image:\n%s", out)
	}
	if got := reg.readList(); len(got) != 1 || got[0] != "team/etl:v1" {
		t.Errorf("registry reads = %q, want the tag once", got)
	}
}

// --image registers another image than the one in dag.json: a tag is
// resolved as the file's would be, a pinned reference is used as given.
func TestPushImageFlag(t *testing.T) {
	reg := newFakeOCIRegistry(t, singleManifest)
	f := writePushSpec(t, "elsewhere.invalid/x:old")
	srv, repo := enforcingServer(t)
	if _, _, err := run(t, "push", f, "--image", reg.host+"/team/etl:v2", "--server", srv.URL, "--token", "x"); err != nil {
		t.Fatalf("push --image tag: %v", err)
	}
	pinned := "ghcr.io/acme/etl@sha256:" + strings.Repeat("a", 64)
	if _, _, err := run(t, "push", f, "--image", pinned, "--server", srv.URL, "--token", "x"); err != nil {
		t.Fatalf("push --image pinned: %v", err)
	}
	want := []string{reg.host + "/team/etl@" + reg.digest, pinned}
	if got := repo.registered(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("registered %q, want %q", got, want)
	}
	if reg.readCount() != 1 {
		t.Errorf("registry reads = %d, want one (the tag only)", reg.readCount())
	}
}

// A tag the registry cannot resolve is registered as written, with a
// warning that says why; a server that enforces then refuses it, and the
// error names both the reason and the fix.
func TestPushUnresolvableTagSaysWhyAndTheFix(t *testing.T) {
	image := missingRegistry(t) + "/team/etl:v1"
	f := writePushSpec(t, image)
	srv, repo := enforcingServer(t)
	_, errOut, err := run(t, "push", f, "--server", srv.URL, "--token", "x")
	if err == nil {
		t.Fatalf("push of an unresolvable tag under enforce succeeded; registered %q", repo.registered())
	}
	for _, want := range []string{"deploy.require_digest=enforce", "could not pin", "MANIFEST_UNKNOWN", "--image <repository>@sha256:<digest>"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	if !strings.Contains(errOut, "could not pin") || !strings.Contains(errOut, image) {
		t.Errorf("stderr = %q, want the warning naming the image", errOut)
	}
}
