package build

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// recordHash is the hash field of a RECORD line for body.
func recordHash(body string) string {
	sum := sha256.Sum256([]byte(body))
	return "sha256=" + base64.RawURLEncoding.EncodeToString(sum[:])
}

// writeTree writes files (path to body) under dir; a path ending in * is
// made executable.
func writeTree(dir string, files map[string]string) error {
	for p, body := range files {
		mode := os.FileMode(0o644)
		if strings.HasSuffix(p, "*") {
			p, mode = strings.TrimSuffix(p, "*"), 0o755
		}
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(body), mode); err != nil { //nolint:gosec // test fixture modes
			return err
		}
	}
	return nil
}

// fakeInstaller lays out what `uv pip install --target` writes for one
// package with a console script, per platform, and records the platforms.
type fakeInstaller struct {
	installed []string
	fail      error
}

func (f *fakeInstaller) Describe() string { return "uv 0.11.32" }

func (f *fakeInstaller) Install(_ context.Context, platform, dir string) error {
	if f.fail != nil {
		return f.fail
	}
	f.installed = append(f.installed, platform)
	tool := "#!/usr/bin/python\nimport sys\nfrom tool import main\nsys.exit(main())\n"
	return writeTree(dir, map[string]string{
		".lock":                       "",
		"tool/__init__.py":            "def main(): return 0\n",
		"tool/_speed.so*":             "ELF " + platform,
		"tool-1.0.dist-info/METADATA": "Name: tool\nVersion: 1.0\n",
		"tool-1.0.dist-info/RECORD": "tool/__init__.py," + recordHash("def main(): return 0\n") + ",21\n" +
			"bin/tool," + recordHash(tool) + "," + fmt.Sprint(len(tool)) + "\n" +
			"bin/run.sh," + recordHash("#!/bin/sh\n") + ",10\n" +
			"tool-1.0.dist-info/RECORD,,\n",
		"bin/tool*":   tool,
		"bin/run.sh*": "#!/bin/sh\n",
	})
}

// The installed tree becomes the layer files under /usr/local: packages and
// dist-info in site-packages with their directories, scripts in bin with a
// shebang naming the image's Python, and the RECORD pointing at where the
// scripts are, with their new hash and size. Neither site-packages nor bin
// is an entry, nor uv's lock file.
func TestDependencyFilesLayOutAnInstalledTree(t *testing.T) {
	dir := t.TempDir()
	if err := (&fakeInstaller{}).Install(context.Background(), "linux/amd64", dir); err != nil {
		t.Fatal(err)
	}
	files, err := dependencyFiles(dir, "3.11")
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(files))
	for _, f := range files {
		p := f.Path
		if f.Dir {
			p += "/"
		} else if f.Exec {
			p += "*"
		}
		got = append(got, p)
	}
	slices.Sort(got)
	sp := "lib/python3.11/site-packages/"
	want := []string{"bin/run.sh*", "bin/tool*", sp + "tool-1.0.dist-info/", sp + "tool-1.0.dist-info/METADATA", sp + "tool-1.0.dist-info/RECORD",
		sp + "tool/", sp + "tool/__init__.py", sp + "tool/_speed.so*"}
	if !slices.Equal(got, want) {
		t.Fatalf("files = %q\nwant    %q", got, want)
	}
	script := readFile(t, filepath.Join(dir, "bin", "tool"))
	wantScript := "#!/usr/local/bin/python3.11\nimport sys\nfrom tool import main\nsys.exit(main())\n"
	if script != wantScript {
		t.Errorf("script = %q, want %q", script, wantScript)
	}
	if sh := readFile(t, filepath.Join(dir, "bin", "run.sh")); sh != "#!/bin/sh\n" {
		t.Errorf("a shell script was rewritten: %q", sh)
	}
	record := readFile(t, filepath.Join(dir, "tool-1.0.dist-info", "RECORD"))
	for _, line := range []string{
		"../../../bin/tool," + recordHash(wantScript) + "," + fmt.Sprint(len(wantScript)),
		"../../../bin/run.sh," + recordHash("#!/bin/sh\n") + ",10",
		"tool/__init__.py," + recordHash("def main(): return 0\n") + ",21",
	} {
		if !strings.Contains(record, line+"\n") {
			t.Errorf("RECORD has no line %q:\n%s", line, record)
		}
	}
	if strings.Contains(record, "\nbin/") || strings.HasPrefix(record, "bin/") {
		t.Errorf("RECORD still lists a script under bin/:\n%s", record)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p) //nolint:gosec // a test file
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// depsLayer is the dependency layer of a fake runtime base for a dag.py
// project.
func depsLayer(t *testing.T, host string, inst Installer, platforms ...string) DependencyLayer {
	t.Helper()
	return DependencyLayer{
		Base: pushRuntimeBase(t, host+"/runtime:py3.11", platforms...), Python: "3.11",
		Env: []string{"PYTHONPATH=" + Workdir}, User: "65532:65532", Installer: inst,
	}
}

// The dependency image is the base for every platform asked, attestations
// dropped, each with the packed tree appended and the image config of the
// generated Dockerfile.
func TestDependencyLayerPushesTheBaseWithTheInstalledTree(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	host := testRegistry(t)
	inst := &fakeInstaller{}
	d := depsLayer(t, host, inst, "linux/amd64", "linux/arm64", "linux/s390x")
	image := host + "/dags/etl:deps-2"
	if err := d.Push(context.Background(), image, []string{"linux/arm64", "linux/amd64"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(inst.installed, []string{"linux/amd64", "linux/arm64"}) {
		t.Errorf("installed for %q, want amd64 then arm64 (the base's order)", inst.installed)
	}
	pinned, err := Registry{}.Resolve(context.Background(), image)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := v1.ParseIndexManifest(bytes.NewReader(pinned.Manifest))
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Manifests) != 2 || idx.Manifests[0].Platform.String() != "linux/amd64" || idx.Manifests[1].Platform.String() != "linux/arm64" {
		t.Fatalf("index = %+v, want linux/amd64 and linux/arm64 only", idx.Manifests)
	}
	img := remoteImage(t, strings.Split(image, ":deps")[0]+"@"+idx.Manifests[1].Digest.String())
	layers, err := img.Layers()
	if err != nil {
		t.Fatal(err)
	}
	if len(layers) != len(runtimeLayers(t))+1 {
		t.Fatalf("%d layers, want the base's plus one", len(layers))
	}
	names := entryNames(t, layers[len(layers)-1])
	sp := "usr/local/lib/python3.11/site-packages/"
	want := strings.Join([]string{"usr/local/bin/run.sh", "usr/local/bin/tool", sp + "tool/", sp + "tool-1.0.dist-info/",
		sp + "tool-1.0.dist-info/METADATA", sp + "tool-1.0.dist-info/RECORD", sp + "tool/__init__.py", sp + "tool/_speed.so"}, ",")
	if names != want {
		t.Errorf("layer entries = %s\nwant %s", names, want)
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cfg.Config.Env, "PYTHONPATH=/home/leoflow") || !slices.Contains(cfg.Config.Env, "PYTHON_VERSION=3.11.9") ||
		cfg.Config.User != "65532:65532" || cfg.Config.WorkingDir != Workdir {
		t.Errorf("config = %+v, want the base's with PYTHONPATH and the non-root user", cfg.Config)
	}
	if last := cfg.History[len(cfg.History)-1]; !strings.Contains(last.CreatedBy, "uv 0.11.32") {
		t.Errorf("history = %+v, want an entry naming the installer", last)
	}
}

// The same installed tree on the same base makes the same image, so a
// dependency image can be rebuilt byte for byte.
func TestDependencyLayerIsDeterministic(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	host := testRegistry(t)
	d := depsLayer(t, host, &fakeInstaller{}, "linux/amd64")
	digests := make([]string, 0, 2)
	for _, tag := range []string{"deps-a", "deps-b"} {
		if err := d.Push(context.Background(), host+"/dags/etl:"+tag, []string{"linux/amd64"}); err != nil {
			t.Fatal(err)
		}
		pinned, err := Registry{}.Resolve(context.Background(), host+"/dags/etl:"+tag)
		if err != nil {
			t.Fatal(err)
		}
		digests = append(digests, pinned.Ref[strings.LastIndex(pinned.Ref, "@"):])
	}
	if digests[0] != digests[1] {
		t.Errorf("two pushes of the same tree made %s and %s", digests[0], digests[1])
	}
}

// An existing PYTHONPATH of the base is replaced, as ENV replaces it.
func TestSetEnvReplacesAVariable(t *testing.T) {
	got := setEnv([]string{"PATH=/bin", "PYTHONPATH=/old"}, []string{"PYTHONPATH=/home/leoflow", "NEW=1"})
	if want := []string{"PATH=/bin", "PYTHONPATH=/home/leoflow", "NEW=1"}; !slices.Equal(got, want) {
		t.Errorf("env = %q, want %q", got, want)
	}
}

// A platform the base does not serve, and an installer failure, fail the
// push before anything is written.
func TestDependencyLayerFailures(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	host := testRegistry(t)
	d := depsLayer(t, host, &fakeInstaller{}, "linux/amd64")
	if err := d.Push(context.Background(), host+"/dags/etl:deps-x", []string{"linux/arm64"}); err == nil || !strings.Contains(err.Error(), "linux/arm64") {
		t.Errorf("missing platform: err = %v, want one naming linux/arm64", err)
	}
	d.Installer = &fakeInstaller{fail: errors.New("no wheel")}
	if err := d.Push(context.Background(), host+"/dags/etl:deps-y", []string{"linux/amd64"}); err == nil || !strings.Contains(err.Error(), "no wheel") {
		t.Errorf("installer failure: err = %v", err)
	}
	for _, tag := range []string{"deps-x", "deps-y"} {
		if _, err := (Registry{}).Resolve(context.Background(), host+"/dags/etl:"+tag); err == nil {
			t.Errorf("%s was pushed", tag)
		}
	}
}

// With the dependency layer available and no dependency image, Select
// builds the dependency image from wheels, then appends the code: no daemon
// runs, and the builder line names the installer.
func TestSelectBuildsTheDependencyImageFromWheels(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	host, split := splitFixture(t)
	d := depsLayer(t, host, &fakeInstaller{}, "linux/amd64")
	split.Wheels = &d
	tool, log := fakeTool(t, "")
	b, reason := Select(context.Background(), Options{Tool: tool, Split: split}, host+"/dags/etl:v1", []string{"linux/amd64"})
	if b.Path() != PathDependencyLayer || b.Describe() != "dependency-layer (uv 0.11.32), then code-layer" {
		t.Fatalf("path = %s (%s), want the dependency layer (reason %q)", b.Path(), b.Describe(), reason)
	}
	if !strings.Contains(reason, "built from wheels without a daemon") {
		t.Errorf("reason %q does not say how the dependency image is built", reason)
	}
	res, err := b.Build(context.Background(), codeRequest(t, host+"/dags/etl:v1", "linux/amd64"))
	if err != nil {
		t.Fatal(err)
	}
	if got := calls(t, log); got != nil {
		t.Errorf("the daemon ran %v", got)
	}
	if res.Path != PathDependencyLayer || res.Ref == "" {
		t.Errorf("result = %+v", res)
	}
	if _, err := (Registry{}).Resolve(context.Background(), split.DepsImage); err != nil {
		t.Errorf("the dependency image was not pushed: %v", err)
	}
	if names := entryNames(t, lastLayer(t, host+"/dags/etl:v1")); !strings.Contains(names, "home/leoflow/dag.py") {
		t.Errorf("the DAG image's last layer is not the code: %s", names)
	}
	// --rebuild builds it from wheels again.
	b, reason = Select(context.Background(), Options{Tool: tool, Split: split, Rebuild: true}, host+"/dags/etl:v1", []string{"linux/amd64"})
	if b.Path() != PathDependencyLayer || !strings.Contains(reason, "--rebuild") {
		t.Errorf("rebuild: path = %s, reason %q", b.Path(), reason)
	}
	// Once it is there, only the code is pushed.
	if b, _ = Select(context.Background(), Options{Tool: tool, Split: split}, host+"/dags/etl:v1", []string{"linux/amd64"}); b.Path() != PathCodeLayer {
		t.Errorf("second build: path = %s, want code-layer", b.Path())
	}
}

// When the daemon builds the dependency image, the builder line says why
// the dependency layer could not.
func TestSelectSaysWhyTheDependencyLayerDoesNotApply(t *testing.T) {
	host, split := splitFixture(t)
	split.NoWheels = "uv is not on PATH"
	_, reason := Select(context.Background(), Options{Split: split}, host+"/dags/etl:v1", []string{"linux/amd64"})
	if !strings.Contains(reason, "uv is not on PATH") {
		t.Errorf("reason %q does not say why the dependency layer does not apply", reason)
	}
}

// layerType follows the media type of the image it is appended to.
func TestDependencyLayerMediaType(t *testing.T) {
	if layerTypeFor(types.DockerManifestSchema2) != types.DockerLayer || layerTypeFor(types.OCIManifestSchema1) != types.OCILayer {
		t.Error("the layer media type does not follow the image")
	}
}
