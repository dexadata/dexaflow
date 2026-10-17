package build

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// codeFixture writes a small project and returns the layer files for it: a
// dag.py, an executable script in a directory, and a symlink.
func codeFixture(t *testing.T) []LayerFile {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, body string, mode os.FileMode) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
		// A fixed, recent mtime that must not reach the layer.
		if err := os.Chtimes(p, time.Now(), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	write("dag.py", "print('dag')\n", 0o600)
	write("scripts/run.sh", "#!/bin/sh\n", 0o700)
	return []LayerFile{
		{Path: "scripts/run.sh", Source: filepath.Join(dir, "scripts", "run.sh"), Exec: true},
		{Path: "dag.py", Source: filepath.Join(dir, "dag.py")},
		{Path: "scripts", Dir: true},
		{Path: "scripts/latest", Link: "run.sh"},
	}
}

// layerEntries reads the tar of layer, in order.
func layerEntries(t *testing.T, layer v1.Layer) []*tar.Header {
	t.Helper()
	rc, err := layer.Uncompressed()
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close() //nolint:errcheck // test reader
	tr := tar.NewReader(rc)
	var out []*tar.Header
	for {
		h, nerr := tr.Next()
		if errors.Is(nerr, io.EOF) {
			return out
		}
		if nerr != nil {
			t.Fatal(nerr)
		}
		out = append(out, h)
	}
}

func TestCodeLayerIsDeterministic(t *testing.T) {
	files := codeFixture(t)
	a, err := makeLayer(t, files, types.OCILayer)
	if err != nil {
		t.Fatal(err)
	}
	reversed := []LayerFile{files[3], files[2], files[1], files[0]}
	b, err := makeLayer(t, reversed, types.OCILayer)
	if err != nil {
		t.Fatal(err)
	}
	da, _ := a.Digest()
	db, _ := b.Digest()
	if da != db {
		t.Fatalf("the same files in another order gave another layer: %s and %s", da, db)
	}
	if mt, _ := a.MediaType(); mt != types.OCILayer {
		t.Fatalf("media type = %s, want %s", mt, types.OCILayer)
	}
}

func TestCodeLayerEntriesAreNormalized(t *testing.T) {
	layer, err := makeLayer(t, codeFixture(t), types.DockerLayer)
	if err != nil {
		t.Fatal(err)
	}
	got := layerEntries(t, layer)
	want := []struct {
		name string
		typ  byte
		mode int64
		link string
	}{
		{"home/leoflow/dag.py", tar.TypeReg, 0o644, ""},
		{"home/leoflow/scripts/", tar.TypeDir, 0o755, ""},
		{"home/leoflow/scripts/latest", tar.TypeSymlink, 0o777, "run.sh"},
		{"home/leoflow/scripts/run.sh", tar.TypeReg, 0o755, ""},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	for i, w := range want {
		h := got[i]
		if h.Name != w.name || h.Typeflag != w.typ || h.Mode != w.mode || h.Linkname != w.link {
			t.Errorf("entry %d = %s type %c mode %o link %q, want %s type %c mode %o link %q",
				i, h.Name, h.Typeflag, h.Mode, h.Linkname, w.name, w.typ, w.mode, w.link)
		}
		if h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" {
			t.Errorf("entry %s owner = %d:%d (%q:%q), want root", h.Name, h.Uid, h.Gid, h.Uname, h.Gname)
		}
		if !h.ModTime.Equal(layerTime) {
			t.Errorf("entry %s mtime = %s, want %s", h.Name, h.ModTime, layerTime)
		}
	}
}

func TestCodeLayerRefusesAPathOutsideTheWorkdir(t *testing.T) {
	for _, p := range []string{"../etc/passwd", "/etc/passwd", "a/../../b", ""} {
		if _, err := makeLayer(t, []LayerFile{{Path: p, Dir: true}}, types.OCILayer); err == nil {
			t.Errorf("codeLayer accepted the path %q", p)
		}
	}
}

func TestCodeLayerReadsEachFileOnce(t *testing.T) {
	files := codeFixture(t)
	layer, err := makeLayer(t, files[1:2], types.OCILayer)
	if err != nil {
		t.Fatal(err)
	}
	// The source is read once, when the layer is made: a later change does
	// not reach a layer already made.
	if werr := os.WriteFile(files[1].Source, []byte("longer content now\n"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	again := layerEntries(t, layer)
	if again[0].Size != int64(len("print('dag')\n")) {
		t.Fatalf("the layer re-read the file: size %d", again[0].Size)
	}
}

func TestAppendCodeToASingleImage(t *testing.T) {
	host := testRegistry(t)
	deps := host + "/dags/etl:deps-1"
	pushImage(t, deps)
	base, err := Registry{}.Resolve(context.Background(), deps)
	if err != nil {
		t.Fatal(err)
	}
	image := host + "/dags/etl:v1"
	if aerr := (Registry{}).AppendCode(context.Background(), base, codeFixture(t), image); aerr != nil {
		t.Fatal(aerr)
	}
	baseImg := remoteImage(t, base.Ref)
	got := remoteImage(t, image)
	bl, _ := baseImg.Layers()
	gl, _ := got.Layers()
	if len(gl) != len(bl)+1 {
		t.Fatalf("the image has %d layers, want the %d of the dependency image plus one", len(gl), len(bl))
	}
	if names := entryNames(t, gl[len(gl)-1]); !strings.Contains(names, "home/leoflow/dag.py") {
		t.Fatalf("the last layer holds %s, want the DAG code", names)
	}
	cfg, _ := got.ConfigFile()
	if h := cfg.History[len(cfg.History)-1]; !strings.Contains(h.CreatedBy, "code layer") {
		t.Fatalf("the last history entry is %q, want the code layer", h.CreatedBy)
	}
}

func TestAppendCodeToAnIndexKeepsEveryPlatform(t *testing.T) {
	host := testRegistry(t)
	deps := host + "/dags/etl:deps-2"
	pushIndexWithAttestation(t, deps, "linux/amd64", "linux/arm64")
	base, err := Registry{}.Resolve(context.Background(), deps)
	if err != nil {
		t.Fatal(err)
	}
	image := host + "/dags/etl:v2"
	if aerr := (Registry{}).AppendCode(context.Background(), base, codeFixture(t), image); aerr != nil {
		t.Fatal(aerr)
	}
	ref, _ := name.ParseReference(image)
	idx, err := remote.Index(ref)
	if err != nil {
		t.Fatal(err)
	}
	im, _ := idx.IndexManifest()
	platforms := make([]string, 0, len(im.Manifests))
	for _, d := range im.Manifests {
		platforms = append(platforms, d.Platform.String())
		img, ierr := idx.Image(d.Digest)
		if ierr != nil {
			t.Fatal(ierr)
		}
		layers, _ := img.Layers()
		if names := entryNames(t, layers[len(layers)-1]); !strings.Contains(names, "home/leoflow/dag.py") {
			t.Errorf("%s: the last layer holds %s, want the DAG code", d.Platform, names)
		}
	}
	if strings.Join(platforms, ",") != "linux/amd64,linux/arm64" {
		t.Fatalf("the index holds %v, want the two platforms and no attestation", platforms)
	}
}

func TestAppendCodeNamesTheImageItCouldNotWrite(t *testing.T) {
	host := testRegistry(t)
	deps := host + "/dags/etl:deps-3"
	pushImage(t, deps)
	base, err := Registry{}.Resolve(context.Background(), deps)
	if err != nil {
		t.Fatal(err)
	}
	err = Registry{}.AppendCode(context.Background(), base, codeFixture(t), "not a reference")
	if err == nil || !strings.Contains(err.Error(), "not a reference") {
		t.Fatalf("err = %v, want one naming the image", err)
	}
}

// remoteImage reads the single image ref names.
func remoteImage(t *testing.T, ref string) v1.Image {
	t.Helper()
	r, err := name.ParseReference(ref)
	if err != nil {
		t.Fatal(err)
	}
	img, err := remote.Image(r)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// entryNames lists the entry names of layer, comma-separated.
func entryNames(t *testing.T, layer v1.Layer) string {
	t.Helper()
	entries := layerEntries(t, layer)
	names := make([]string, 0, len(entries))
	for _, h := range entries {
		names = append(names, h.Name)
	}
	return strings.Join(names, ",")
}

// pushIndexWithAttestation pushes an index holding one random image per
// platform plus an attestation manifest, as docker with BuildKit pushes one.
func pushIndexWithAttestation(t *testing.T, ref string, platforms ...string) {
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
	att, err := random.Image(8, 1)
	if err != nil {
		t.Fatal(err)
	}
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
}

// makeLayer is codeLayer with its temporary file removed after the test.
func makeLayer(t *testing.T, files []LayerFile, mediaType types.MediaType) (v1.Layer, error) {
	t.Helper()
	layer, remove, err := codeLayer(files, mediaType)
	if err == nil {
		t.Cleanup(remove)
	}
	return layer, err
}

func TestCodeLayerRefusesAWhiteoutName(t *testing.T) {
	for _, p := range []string{".wh.dag.py", "lib/.wh..wh..opq"} {
		if _, err := makeLayer(t, []LayerFile{{Path: p, Dir: true}}, types.OCILayer); err == nil || !strings.Contains(err.Error(), ".wh.") {
			t.Errorf("%s: err = %v, want a refusal naming .wh.", p, err)
		}
	}
}

func TestCodeLayerRemovesItsTemporaryFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	_, remove, err := codeLayer(codeFixture(t), types.OCILayer)
	if err != nil {
		t.Fatal(err)
	}
	if left, _ := filepath.Glob(filepath.Join(tmp, "dexaflow-code-*")); len(left) != 1 {
		t.Fatalf("temporary files = %v, want the layer's", left)
	}
	remove()
	if left, _ := filepath.Glob(filepath.Join(tmp, "dexaflow-code-*")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

// An index with no image for a platform is refused rather than pushed empty.
func TestAppendCodeRefusesAnIndexWithNoPlatformImage(t *testing.T) {
	host := testRegistry(t)
	deps := host + "/dags/etl:deps-4"
	pushIndexWithAttestation(t, deps)
	base, err := Registry{}.Resolve(context.Background(), deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := (Registry{}).AppendCode(context.Background(), base, codeFixture(t), host+"/dags/etl:v4"); err == nil || !strings.Contains(err.Error(), "no image for a platform") {
		t.Fatalf("err = %v, want a refusal", err)
	}
}

// A layer is packed under the root it is given, and only the entries listed
// are emitted: the directories of the root itself exist in the base image
// with their own modes and owners, so the layer never carries them.
func TestPackLayerPlacesEntriesUnderItsRoot(t *testing.T) {
	files := codeFixture(t)
	layer, remove, err := packLayer(kindCode, "/usr/local", files, types.OCILayer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(remove)
	entries := layerEntries(t, layer)
	names := make([]string, 0, len(entries))
	for _, h := range entries {
		names = append(names, h.Name)
	}
	want := "usr/local/dag.py usr/local/scripts/ usr/local/scripts/latest usr/local/scripts/run.sh"
	if got := strings.Join(names, " "); got != want {
		t.Errorf("entries = %q, want %q", got, want)
	}
}
