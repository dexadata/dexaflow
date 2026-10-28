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
	"github.com/dexadata/dexaflow/internal/api/apitest"
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
	srv := httptest.NewServer(apitest.NewServer(api.Dependencies{
		Observability: api.ObservabilityDeps{
			Logger: discardTestLogger(),
		},
		Auth: api.AuthDeps{
			DevNoAuth: true,
		},
		Execution: api.ExecutionDeps{
			RequireDigest: domain.DigestRequireEnforce,
		},
		Stores: api.StoreDeps{
			Versions: repo,
		},
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

// A push that succeeded is not undone by a failed read of its digest
// (podman with both auth files, a plain-HTTP registry off the loopback):
// compile keeps the tag in dag.json and warns with the reason and the fix.
func TestCompilePushKeepsTheTagWhenTheDigestReadFails(t *testing.T) {
	dir := versionProject(t, "registry:\n  url: "+missingRegistry(t)+"/team\n  image_name: etl\n")
	t.Setenv("LEOFLOW_PARSER_CMD", recordingParser(t, filepath.Join(t.TempDir(), "parser.log")))
	builder, log := argvBuilder(t)
	out, errOut, err := run(t, "compile", dir, "--build", "--push", "--builder", builder)
	if err != nil {
		t.Fatalf("compile --build --push with an unreadable digest: %v\n%s", err, errOut)
	}
	if calls := argvLines(t, log); len(calls) != 2 || !strings.HasPrefix(calls[1], "push ") {
		t.Fatalf("builder calls = %q, want a build and a push", calls)
	}
	got := compiledImage(t, filepath.Join(dir, "dag.json"))
	if domain.IsDigestPinned(got) || !strings.Contains(got, "/team/etl:") {
		t.Errorf("dag.json image = %q, want the pushed tag", got)
	}
	for _, want := range []string{"warning:", got, "MANIFEST_UNKNOWN", "dexaflow push", "--image <repository>@sha256:<digest>"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr does not name %q:\n%s", want, errOut)
		}
	}
	if !strings.Contains(out, "image "+got) {
		t.Errorf("summary does not name the tag:\n%s", out)
	}
}

// deploy registers only a digest: a pushed image whose digest cannot be
// read stops it before the register.
func TestDeployStopsWhenThePushedDigestCannotBeRead(t *testing.T) {
	dir, builder, _ := deployFixture(t)
	fake := &deployServer{}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	_, errOut, err := run(t, "deploy", dir, "--server", srv.URL, "--token", "x", "--yes", "--force", "--builder", builder)
	if err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("deploy = %v, want the digest read failure", err)
	}
	if fake.registers != 0 {
		t.Errorf("registered %d time(s) without a digest", fake.registers)
	}
	if strings.Contains(errOut, "keeps its tag") {
		t.Errorf("deploy warned that it keeps the tag, which it never registers:\n%s", errOut)
	}
}

// --image is checked before anything is read or sent: an invalid reference,
// an @ that is not a full sha256 pin and a value FROM cannot carry are
// refused by name.
func TestPushRefusesAnInvalidImageFlag(t *testing.T) {
	f := writePushSpec(t, pushSpecImage)
	repo := &recordingVersions{}
	srv := httptest.NewServer(apitest.NewServer(api.Dependencies{Observability: api.ObservabilityDeps{Logger: discardTestLogger()}, Auth: api.AuthDeps{DevNoAuth: true}, Stores: api.StoreDeps{Versions: repo}}))
	defer srv.Close()
	for bad, why := range map[string]string{
		"ghcr.io/Acme/etl:v1":                                 "not a valid reference",
		"ghcr.io/acme/etl@sha256:abc":                         "not a full sha256 pin",
		"ghcr.io/acme/etl@sha512:" + strings.Repeat("a", 128): "not a full sha256 pin",
		"ghcr.io/acme/$etl:v1":                                `contains "$"`,
	} {
		_, _, err := run(t, "push", f, "--image", bad, "--server", srv.URL, "--token", "x")
		if err == nil || !strings.Contains(err.Error(), "--image") || !strings.Contains(err.Error(), why) ||
			strings.Contains(err.Error(), "server returned") || strings.Contains(err.Error(), "could not be pinned") {
			t.Errorf("push --image %q = %v, want the CLI's refusal naming --image and %q", bad, err, why)
		}
	}
	if got := repo.registered(); len(got) != 0 {
		t.Errorf("registered %q", got)
	}
}

// An --image tag the operator named explicitly must resolve: falling back to
// the tag would register something other than what was asked for.
func TestPushImageFlagTagThatDoesNotResolveFails(t *testing.T) {
	f := writePushSpec(t, pushSpecImage)
	srv, repo := enforcingServer(t)
	off := httptest.NewServer(apitest.NewServer(api.Dependencies{Observability: api.ObservabilityDeps{Logger: discardTestLogger()}, Auth: api.AuthDeps{DevNoAuth: true}, Stores: api.StoreDeps{Versions: repo}}))
	defer off.Close()
	for _, s := range []*httptest.Server{srv, off} {
		_, _, err := run(t, "push", f, "--image", missingRegistry(t)+"/team/etl:v9", "--server", s.URL, "--token", "x")
		if err == nil || !strings.Contains(err.Error(), "MANIFEST_UNKNOWN") || !strings.Contains(err.Error(), "--image <repository>@sha256:<digest>") {
			t.Errorf("push --image of a missing tag = %v, want the reason and the fix", err)
		}
	}
	if got := repo.registered(); len(got) != 0 {
		t.Errorf("registered %q", got)
	}
}

// A refusal that is not deploy.require_digest's is returned as the server
// gave it, even when the tag could not be pinned.
func TestPushOtherRefusalOfAnUnpinnedTagIsNotExplainedAsADigest(t *testing.T) {
	srv := conflictServer(t)
	f := writePushSpec(t, missingRegistry(t)+"/team/etl:v1")
	_, _, err := run(t, "push", f, "--server", srv.URL, "--token", "x")
	if err == nil || !strings.Contains(err.Error(), "hint:") {
		t.Fatalf("push on 409 = %v, want the conflict hint", err)
	}
	if strings.Contains(err.Error(), "could not pin") {
		t.Errorf("a 409 was explained as a digest refusal: %v", err)
	}
}

// The registry client of a push reads registry.auth_method from the project
// --project names, then the dag.json's directory, then the working
// directory; a project file that does not parse is warned about, not
// silently replaced by the defaults, and a deprecated auth_method says so.
func TestPushFindsTheProjectConfig(t *testing.T) {
	reg := newFakeOCIRegistry(t, singleManifest)
	srv, _ := enforcingServer(t)
	project := t.TempDir()
	writeProjectFile(t, project, "dexaflow.yaml", "dag_id: etl\nregistry:\n  auth_method: env\n")
	f := writePushSpec(t, reg.host+"/team/etl:v1")
	_, errOut, err := run(t, "push", f, "--project", project, "--server", srv.URL, "--token", "x")
	if err != nil {
		t.Fatalf("push --project: %v", err)
	}
	if !strings.Contains(errOut, "auth_method") {
		t.Errorf("stderr = %q, want the auth_method deprecation note of the --project config", errOut)
	}

	broken := filepath.Dir(f)
	writeProjectFile(t, broken, "dexaflow.yaml", "dag_id: [\n")
	_, errOut, err = run(t, "push", f, "--server", srv.URL, "--token", "x")
	if err != nil {
		t.Fatalf("push next to a broken dexaflow.yaml: %v", err)
	}
	if !strings.Contains(errOut, "warning:") || !strings.Contains(errOut, "dexaflow.yaml") {
		t.Errorf("stderr = %q, want a warning naming the file that does not parse", errOut)
	}
}

// pushProjectDir picks --project, then the dag.json's directory when it has
// a project file, then the working directory.
func TestPushProjectDir(t *testing.T) {
	t.Parallel()
	withCfg := t.TempDir()
	writeProjectFile(t, withCfg, "dexaflow.yaml", "dag_id: etl\n")
	bare := t.TempDir()
	if got := pushProjectDir("/explicit", filepath.Join(withCfg, "dag.json")); got != "/explicit" {
		t.Errorf("with --project = %q", got)
	}
	if got := pushProjectDir("", filepath.Join(withCfg, "dag.json")); got != withCfg {
		t.Errorf("dag.json next to a project = %q, want %q", got, withCfg)
	}
	if got := pushProjectDir("", filepath.Join(bare, "dag.json")); got != "." {
		t.Errorf("dag.json alone = %q, want the working directory", got)
	}
}
