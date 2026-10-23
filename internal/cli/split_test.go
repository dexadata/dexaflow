package cli

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/dexadata/dexaflow/internal/build"
	"github.com/dexadata/dexaflow/internal/domain"
)

// withoutCopies drops the COPY lines of a Dockerfile.
func withoutCopies(dockerfile string) string {
	var keep []string
	for _, l := range strings.SplitAfter(dockerfile, "\n") {
		if !strings.HasPrefix(l, "COPY ") {
			keep = append(keep, l)
		}
	}
	return strings.Join(keep, "")
}

// The dependency image is the generated Dockerfile without its COPY lines,
// so the dependency image plus the code layer is the image a full build
// makes: same base, packages, environment and user.
func TestDependencyDockerfileIsTheGeneratedOneWithoutItsCopies(t *testing.T) {
	t.Parallel()
	cases := map[string]*domain.LeoflowConfig{
		"dag.py with packages": {DagID: "etl", DagSource: "dag.py", BaseImage: "base:1", Dependencies: []string{"pandas==2.2.3"}, SystemPackages: []string{"libpq5"}, IncludePaths: []string{"sql"}},
		"dag.py alone":         {DagID: "etl", DagSource: "dag.py", BaseImage: "base:1"},
		"dbt project":          {DagID: "etl", BaseImage: "base:1", Dependencies: []string{"dbt-duckdb==1.9.1"}, Dbt: &domain.DbtConfig{Project: "transform"}},
	}
	for name, cfg := range cases {
		full, err := generatedDockerfile(cfg, cfg.DagSource)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		deps, err := dependencyDockerfile(cfg, "base:1")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.Contains(deps, "COPY") || deps != withoutCopies(full) {
			t.Errorf("%s: dependency Dockerfile\n%s\nwant the generated one without its copies\n%s", name, deps, withoutCopies(full))
		}
	}
}

func TestDependencyDockerfileRefusesWhatTheGeneratedOneRefuses(t *testing.T) {
	t.Parallel()
	cfg := &domain.LeoflowConfig{DagID: "etl", DagSource: "dag.py", Dependencies: []string{"pandas==1\nRUN curl x"}}
	if _, err := dependencyDockerfile(cfg, "base:1"); err == nil {
		t.Fatal("a dependency carrying a newline was rendered")
	}
}

func TestDepsKeyCoversTheDockerfileAndThePlatforms(t *testing.T) {
	t.Parallel()
	k := depsKey("FROM a\n", []string{"linux/amd64", "linux/arm64"})
	if len(k) != 64 {
		t.Fatalf("key %q is not a sha256", k)
	}
	if depsKey("FROM a\n", []string{"linux/ARM64", "linux/amd64"}) != k {
		t.Error("the order or case of the platforms changed the key")
	}
	for _, other := range []string{depsKey("FROM b\n", []string{"linux/amd64", "linux/arm64"}), depsKey("FROM a\n", []string{"linux/amd64"})} {
		if other == k {
			t.Error("a different Dockerfile or platform list kept the key")
		}
	}
}

func TestDepsImageIsATagInTheDAGRepository(t *testing.T) {
	t.Parallel()
	key := strings.Repeat("ab", 32)
	cases := map[string]string{
		"reg.io/team/etl:v1-abc":           "reg.io/team/etl:deps-" + key[:32],
		"127.0.0.1:5000/etl:v1":            "127.0.0.1:5000/etl:deps-" + key[:32],
		"127.0.0.1:5000/etl@sha256:00":     "127.0.0.1:5000/etl:deps-" + key[:32],
		"localhost:5000/a/b/etl:latest-12": "localhost:5000/a/b/etl:deps-" + key[:32],
	}
	for image, want := range cases {
		if got := depsImage(image, key); got != want {
			t.Errorf("depsImage(%q) = %q, want %q", image, got, want)
		}
	}
}

func TestUnpinnedDependencies(t *testing.T) {
	t.Parallel()
	deps := []string{
		"pandas==2.2.3", "requests[socks]==2.32.3", "numpy>=2", "polars", "scipy==1.*",
		"apache-airflow-providers-amazon", "Apache_Airflow.Providers-Amazon==9.1.0",
		"https://example.com/pkg.whl", "./local", "attrs==23.1; python_version<'3.12'",
	}
	want := []string{"numpy>=2", "polars", "scipy==1.*", "https://example.com/pkg.whl", "./local", "attrs==23.1; python_version<'3.12'"}
	if got := unpinned(deps); !slices.Equal(got, want) {
		t.Errorf("unpinned = %q, want %q", got, want)
	}
}

// layerProject writes a dag.py project with include_paths, exclusions and a
// symlink, and returns its directory.
func layerProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeProjectFile(t, dir, "dag.py", "# dag\n")
	writeProjectFile(t, dir, "other.py", "# not copied\n")
	writeProjectFile(t, dir, "scripts/run.sh", "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(dir, "scripts", "run.sh"), 0o700); err != nil { //nolint:gosec // an executable fixture
		t.Fatal(err)
	}
	writeProjectFile(t, dir, "scripts/token.secret", "x\n")
	writeProjectFile(t, dir, "scripts/.git/config", "x\n")
	writeProjectFile(t, dir, "scripts/skip.tmp", "x\n")
	writeProjectFile(t, dir, ".dockerignore", "**/*.tmp\n")
	writeProjectFile(t, dir, "conf/app/settings.yaml", "a: 1\n")
	if err := os.Symlink("run.sh", filepath.Join(dir, "scripts", "latest")); err != nil {
		t.Fatal(err)
	}
	return dir
}

// layerPaths renders the layer files as path[/][*][ -> link], sorted.
func layerPaths(files []build.LayerFile) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		p := f.Path
		switch {
		case f.Dir:
			p += "/"
		case f.Link != "":
			p += " -> " + f.Link
		case f.Exec:
			p += "*"
		}
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

// The code layer holds what the COPY lines of the generated Dockerfile take:
// the DAG source and include_paths, without .git, exclude_paths or
// .dockerignore matches, with links kept and the execute bit known.
func TestCodeLayerFilesAreWhatTheCopiesTake(t *testing.T) {
	t.Parallel()
	dir := layerProject(t)
	cfg := &domain.LeoflowConfig{DagID: "etl", DagSource: "dag.py", IncludePaths: []string{".", "scripts", "conf/app/settings.yaml"}, ExcludePaths: []string{"*.secret"}}
	files, err := codeLayerFiles(dir, cfg, filepath.Join(dir, "dag.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"conf/", "conf/app/", "conf/app/settings.yaml", "dag.py", "scripts/", "scripts/latest -> run.sh", "scripts/run.sh*"}
	if got := layerPaths(files); !slices.Equal(got, want) {
		t.Errorf("code layer = %q, want %q", got, want)
	}
}

// A dbt project ships its directory, with the files `dbt parse` writes
// under target/ (as the COPY ships them) but without logs/ and .user.yml.
func TestCodeLayerFilesOfADbtProject(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, f := range []string{"transform/dbt_project.yml", "transform/models/a.sql", "transform/target/manifest.json", "transform/logs/dbt.log", "transform/.user.yml", "dag.py"} {
		writeProjectFile(t, dir, f, "x\n")
	}
	cfg := &domain.LeoflowConfig{DagID: "etl", Dbt: &domain.DbtConfig{Project: "transform", Manifest: "target/manifest.json"}}
	files, err := codeLayerFiles(dir, cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"transform/", "transform/dbt_project.yml", "transform/models/", "transform/models/a.sql", "transform/target/", "transform/target/manifest.json"}
	if got := layerPaths(files); !slices.Equal(got, want) {
		t.Errorf("code layer = %q, want %q", got, want)
	}
}

func TestCodeLayerFilesFailOnAMissingRoot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeProjectFile(t, dir, "dag.py", "# dag\n")
	cfg := &domain.LeoflowConfig{DagID: "etl", DagSource: "dag.py", IncludePaths: []string{"missing"}}
	if _, err := codeLayerFiles(dir, cfg, ""); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("err = %v, want one naming the missing root", err)
	}
}

// splitRegistryWith starts an in-memory registry holding a one-platform
// base image and returns its host and the base reference. Its blob uploads
// are refused with a 401 while deny is set, as a registry refuses a
// credential that may not push; nil never refuses.
func splitRegistryWith(t *testing.T, deny *atomic.Bool) (host, base string) {
	t.Helper()
	reg := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if deny != nil && deny.Load() && req.Method == http.MethodPost && strings.Contains(req.URL.Path, "/blobs/uploads/") {
			http.Error(w, `{"errors":[{"code":"DENIED","message":"requested access to the resource is denied"}]}`, http.StatusUnauthorized)
			return
		}
		reg.ServeHTTP(w, req)
	}))
	t.Cleanup(srv.Close)
	host = strings.TrimPrefix(srv.URL, "http://")
	base = host + "/runtime:py3.12"
	pushPlatformIndex(t, base, "linux/amd64")
	return host, base
}

// pushPlatformIndex pushes an index of one random image per platform.
func pushPlatformIndex(t *testing.T, ref string, platforms ...string) {
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
}

// splitProject is a stubbed-parser project whose image and base live on an
// in-memory registry, and the dependency image its build uses.
func splitProject(t *testing.T, extraYAML string) (dir, host, deps string) {
	t.Helper()
	return splitProjectWith(t, extraYAML, nil)
}

// splitProjectWith is splitProject on splitRegistryWith(deny).
func splitProjectWith(t *testing.T, extraYAML string, deny *atomic.Bool) (dir, host, deps string) {
	t.Helper()
	host, base := splitRegistryWith(t, deny)
	dir = versionProject(t, "base_image: "+base+"\ndependencies: [pandas==2.2.3]\nregistry:\n  url: "+host+"/team\n  image_name: etl\n"+
		"build:\n  platforms: [linux/amd64]\n"+extraYAML)
	t.Setenv("LEOFLOW_PARSER_CMD", recordingParser(t, filepath.Join(t.TempDir(), "parser.log")))
	cfg, err := loadProjectConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := baseReaderFor(testCtx(nil), cfg).Resolve(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	content, err := dependencyDockerfile(cfg, pinned.Ref)
	if err != nil {
		t.Fatal(err)
	}
	return dir, host, depsImage(host+"/team/etl:x", depsKey(content, []string{"linux/amd64"}))
}

// lastLayerNames lists the entries of the last layer of the image ref names,
// the single image of an index for one platform.
func lastLayerNames(t *testing.T, ref string) []string {
	t.Helper()
	r, err := name.ParseReference(ref)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := remote.Index(r)
	if err != nil {
		t.Fatal(err)
	}
	im, err := idx.IndexManifest()
	if err != nil || len(im.Manifests) != 1 {
		t.Fatalf("index %s: %v, %d manifests", ref, err, len(im.Manifests))
	}
	img, err := idx.Image(im.Manifests[0].Digest)
	if err != nil {
		t.Fatal(err)
	}
	layers, err := img.Layers()
	if err != nil {
		t.Fatal(err)
	}
	rc, err := layers[len(layers)-1].Uncompressed()
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close() //nolint:errcheck // test reader
	var names []string
	tr := tar.NewReader(rc)
	for {
		h, nerr := tr.Next()
		if errors.Is(nerr, io.EOF) {
			return names
		}
		if nerr != nil {
			t.Fatal(nerr)
		}
		names = append(names, h.Name)
	}
}

// With the dependency image for this content in the registry, a deploy
// pushes only the code, runs no daemon, and registers the digest the
// registry gives the new image.
func TestDeployAppendsTheCodeToTheDependencyImage(t *testing.T) {
	dir, host, deps := splitProject(t, "")
	pushPlatformIndex(t, deps, "linux/amd64")
	builder, argv := argvBuilder(t)
	fake := &deployServer{}
	out, _, err := deployDir(t, fake, dir, builder)
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	if calls := argvLines(t, argv); calls != nil {
		t.Errorf("the daemon ran: %q", calls)
	}
	if !strings.Contains(out, "builder:    code-layer, the registry holds the dependency image "+deps) {
		t.Errorf("output does not name the code-layer path:\n%s", out)
	}
	image := host + "/team/etl:" + labelOf(t, dir)
	pinned, err := build.Registry{}.Resolve(context.Background(), image)
	if err != nil {
		t.Fatal(err)
	}
	if fake.registers != 1 || !strings.Contains(out, strings.TrimPrefix(pinned.Ref, host+"/team/etl@")) {
		t.Errorf("registers=%d, want the pushed digest %s registered:\n%s", fake.registers, pinned.Ref, out)
	}
	if names := lastLayerNames(t, image); !slices.Equal(names, []string{"home/leoflow/dag.py"}) {
		t.Errorf("the code layer holds %q, want the DAG source", names)
	}
}

// --rebuild has the daemon build and push the dependency image again, from
// an empty context, before the code is appended.
func TestDeployRebuildBuildsTheDependencyImage(t *testing.T) {
	dir, _, deps := splitProject(t, "")
	// What the daemon's push leaves in the registry.
	pushPlatformIndex(t, deps, "linux/amd64")
	builder, argv := argvBuilder(t)
	out, _, err := deployDir(t, &deployServer{}, dir, builder, "--rebuild")
	if err != nil {
		t.Fatalf("deploy --rebuild: %v\n%s", err, out)
	}
	calls := argvLines(t, argv)
	if len(calls) != 2 || !strings.HasPrefix(calls[0], "build --platform linux/amd64 -t "+deps+" -f ") || calls[1] != "push "+deps {
		t.Fatalf("daemon calls = %q, want a build and a push of %s", calls, deps)
	}
	if strings.Contains(calls[0], dir) {
		t.Errorf("the dependency image was built from the project directory: %q", calls[0])
	}
	if !strings.Contains(out, "for the dependency image, then code-layer") {
		t.Errorf("output does not say the daemon builds the dependency image:\n%s", out)
	}
}

// Each reason not to split reaches the builder line, and the whole image is
// built as before.
func TestDeploySaysWhyTheBuildIsNotSplit(t *testing.T) {
	cases := map[string]struct {
		edit func(t *testing.T, dir string)
		want string
	}{
		"unpinned": {func(t *testing.T, dir string) {
			editYAML(t, dir, "dependencies: [pandas==2.2.3]", "dependencies: [pandas>=2]")
		}, `fallback: "pandas>=2" is not pinned to one version`},
		"own Dockerfile": {func(t *testing.T, dir string) {
			writeProjectFile(t, dir, "Dockerfile", "FROM scratch\n")
		}, "fallback: the project ships its own Dockerfile"},
		"pattern in include_paths": {func(t *testing.T, dir string) {
			writeProjectFile(t, dir, "lib/a.py", "x\n")
			editYAML(t, dir, "dependencies:", "include_paths: [\"lib/*.py\"]\ndependencies:")
		}, `fallback: include_paths entry "lib/*.py" is a pattern`},
		"public base": {func(t *testing.T, dir string) {
			editYAML(t, dir, "base_image: ", "base_image: ghcr.io/x/y:1\n# was: ")
		}, "fallback: the digest of the base image ghcr.io/x/y:1 could not be read"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir, _, _ := splitProject(t, "")
			tc.edit(t, dir)
			builder, argv := argvBuilder(t)
			out, _, err := deployDir(t, &deployServer{}, dir, builder, "--dag-version", "v1")
			if !strings.Contains(out, tc.want) {
				t.Errorf("output does not say %q (err %v):\n%s", tc.want, err, out)
			}
			if calls := argvLines(t, argv); len(calls) < 1 || strings.Contains(calls[0], ":deps-") {
				t.Errorf("daemon calls = %q, want the whole image built", calls)
			}
		})
	}
}

// editYAML replaces old with new in the project's dexaflow.yaml.
func editYAML(t *testing.T, dir, old, replacement string) {
	t.Helper()
	body := readProjectFile(t, dir, "dexaflow.yaml")
	if !strings.Contains(body, old) {
		t.Fatalf("dexaflow.yaml has no %q:\n%s", old, body)
	}
	writeProjectFile(t, dir, "dexaflow.yaml", strings.Replace(body, old, replacement, 1))
}

// exclude_paths reach the code layer with Docker's rules, as the generated
// .dockerignore applies them: `**` at any depth, a pattern with a slash only
// from the project root, a bare name at any depth.
func TestCodeLayerFilesApplyExcludePathsAsDockerDoes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, f := range []string{"dag.py", "lib/a.py", "lib/certs/deep/k.pem", "lib/data/schema.json", "lib/cache/x.bin", "lib/sub/cache/y.bin"} {
		writeProjectFile(t, dir, f, "x\n")
	}
	cfg := &domain.LeoflowConfig{DagID: "etl", DagSource: "dag.py", IncludePaths: []string{"lib"}, ExcludePaths: []string{"**/*.pem", "data/", "cache"}}
	files, err := codeLayerFiles(dir, cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"dag.py", "lib/", "lib/a.py", "lib/certs/", "lib/certs/deep/", "lib/data/", "lib/data/schema.json", "lib/sub/"}
	if got := layerPaths(files); !slices.Equal(got, want) {
		t.Errorf("code layer = %q, want %q", got, want)
	}
}

// A root the .dockerignore excludes fails, as its COPY would.
func TestCodeLayerFilesFailOnAnExcludedRoot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeProjectFile(t, dir, "dag.py", "# dag\n")
	writeProjectFile(t, dir, "secrets/k", "x\n")
	cfg := &domain.LeoflowConfig{DagID: "etl", DagSource: "dag.py", IncludePaths: []string{"secrets"}, ExcludePaths: []string{"/secrets"}}
	if _, err := codeLayerFiles(dir, cfg, ""); err == nil || !strings.Contains(err.Error(), "excluded") {
		t.Fatalf("err = %v, want one saying the root is excluded", err)
	}
}

// compile --push --rebuild builds the dependency image again, as deploy
// --rebuild does.
func TestCompileRebuildBuildsTheDependencyImage(t *testing.T) {
	dir, _, deps := splitProject(t, "")
	pushPlatformIndex(t, deps, "linux/amd64")
	builder, argv := argvBuilder(t)
	out, _, err := run(t, "compile", dir, "--build", "--push", "--rebuild", "--builder", builder)
	if err != nil {
		t.Fatalf("compile --rebuild: %v\n%s", err, out)
	}
	if calls := argvLines(t, argv); len(calls) != 2 || !strings.Contains(calls[0], "-t "+deps+" ") {
		t.Fatalf("daemon calls = %q, want the dependency image built and pushed", calls)
	}
	out, _, err = run(t, "compile", dir, "--build", "--push", "--builder", builder)
	if err != nil || !strings.Contains(out, "builder:    code-layer") {
		t.Fatalf("compile without --rebuild: %v, want the code-layer path:\n%s", err, out)
	}
}
