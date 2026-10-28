package cli

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/dexadata/dexaflow/internal/build"
	"github.com/dexadata/dexaflow/internal/domain"
)

// fakeUVScript stands in for uv. It logs every call to calls.log in its
// directory, and the UV_* and PIP_* variables it runs with to env.log, and
// answers --version and `python find` (failing with a nopython file).
// `pip compile` pins every requirement of
// its last argument to the version it names, or to the content of the
// version file (1.0 without one), with a hash of the pin, and keeps a copy
// of its constraints in constraints.txt; a fail file makes
// it fail with that file as its standard error. `pip install` lays out, for
// each line of -r, a package, its dist-info with a RECORD, and a console
// script with the shebang uv writes.
const fakeUVScript = `#!/bin/sh
d=DIR
echo "$*" >> "$d/calls.log"
env | grep -E '^(UV|PIP)_' >> "$d/env.log"
if [ "$1" = "--version" ]; then echo "uv 0.11.32 (x86_64-unknown-linux-gnu)"; exit 0; fi
if [ "$1" = "python" ]; then
  pwd > "$d/python.pwd"
  if [ -f "$d/nopython" ]; then echo "error: No interpreter found in virtual environments, managed installations, or search path" >&2; exit 2; fi
  echo /usr/bin/python3; exit 0
fi
if [ -f "$d/fail" ]; then cat "$d/fail" >&2; exit 2; fi
sub="$2"; out=""; target=""; reqs=""; last=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2;;
    --target) target="$2"; shift 2;;
    -r) reqs="$2"; shift 2;;
    -c) cp "$2" "$d/constraints.txt"; shift 2;;
    --python-platform|--python-version|--only-binary) shift 2;;
    *) last="$1"; shift;;
  esac
done
if [ "$sub" = compile ]; then
  : > "$out"
  while IFS= read -r line; do
    [ -z "$line" ] && continue
    n=$(echo "$line" | sed 's/[^A-Za-z0-9._-].*//')
    v=$(echo "$line" | sed -n 's/.*==\([^ ;]*\).*/\1/p')
    [ -z "$v" ] && v=$(cat "$d/version" 2>/dev/null || echo 1.0)
    printf '%s==%s \\\n    --hash=sha256:%s\n' "$n" "$v" "$(printf '%s' "$n$v" | sha256sum | cut -c1-64)" >> "$out"
  done < "$last"
  exit 0
fi
while IFS= read -r line; do
  n=${line%%==*}; rest=${line#*==}; v=${rest%% *}
  mkdir -p "$target/$n" "$target/$n-$v.dist-info" "$target/bin"
  echo "# $n" > "$target/$n/__init__.py"
  printf '#!/usr/bin/python\nimport %s\n' "$n" > "$target/bin/$n"
  chmod 755 "$target/bin/$n"
  printf '%s/__init__.py,,\nbin/%s,,\n' "$n" "$n" > "$target/$n-$v.dist-info/RECORD"
done < "$reqs"
: > "$target/.lock"
`

// fakeUV puts the fake uv where the commands run under d find it and
// returns its directory, which holds calls.log.
func fakeUV(t *testing.T, d *cliDeps) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX-only")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "uv")
	if err := os.WriteFile(p, []byte(strings.ReplaceAll(fakeUVScript, "DIR", dir)), 0o700); err != nil { //nolint:gosec // a test fixture that must be executable
		t.Fatal(err)
	}
	d.lookUV = func() (string, error) { return p, nil }
	return dir
}

// uvCalls returns the logged uv calls, without --version and python find.
func uvCalls(t *testing.T, dir string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "calls.log")) //nolint:gosec // a path this test created
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if l != "--version" && !strings.HasPrefix(l, "python find") {
			out = append(out, l)
		}
	}
	return out
}

// runtimeBaseLayer is the layer of a fake Python 3.11 runtime base: glibc
// 2.41 in the dpkg status, and six 1.16.0 in site-packages.
func runtimeBaseLayer(t *testing.T) v1.Layer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	status := "Package: libc6\nStatus: install ok installed\nVersion: 2.41-12\n\n"
	for _, e := range [][2]string{
		{"var/lib/dpkg/status", status},
		{"usr/local/lib/python3.11/site-packages/six-1.16.0.dist-info/RECORD", "x"},
	} {
		if err := tw.WriteHeader(&tar.Header{Name: e[0], Mode: 0o644, Typeflag: tar.TypeReg, Size: int64(len(e[1]))}); err != nil {
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

// pushRuntimeBaseIndex pushes an index of one fake runtime base image per
// platform under ref.
func pushRuntimeBaseIndex(t *testing.T, ref string, platforms ...string) {
	t.Helper()
	pushRuntimeBaseWithEnv(t, ref, nil, platforms...)
}

// pushRuntimeBaseWithEnv is pushRuntimeBaseIndex with env added to the
// config of every image.
func pushRuntimeBaseWithEnv(t *testing.T, ref string, env []string, platforms ...string) {
	t.Helper()
	var idx v1.ImageIndex = empty.Index
	for _, p := range platforms {
		plat, err := v1.ParsePlatform(p)
		if err != nil {
			t.Fatal(err)
		}
		img, err := mutate.AppendLayers(empty.Image, runtimeBaseLayer(t))
		if err != nil {
			t.Fatal(err)
		}
		cf, err := img.ConfigFile()
		if err != nil {
			t.Fatal(err)
		}
		cf = cf.DeepCopy()
		cf.OS, cf.Architecture = plat.OS, plat.Architecture
		cf.Config.Env = append([]string{"PATH=/usr/local/bin:/usr/bin", "PYTHON_VERSION=3.11.9"}, env...)
		// No user, as a python image runs: the dependency image must set it.
		cf.Config.User, cf.Config.WorkingDir = "", build.Workdir
		if img, err = mutate.ConfigFile(img, cf); err != nil {
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

// wheelProject is a split project (splitProject) whose base is a fake
// runtime base for linux/amd64, with deps as its dependencies, and a fake
// uv for the commands run under d; it returns the project, the registry
// host and the uv directory.
func wheelProject(t *testing.T, d *cliDeps, deps string) (dir, host, uv string) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir, host, _ = splitProject(t, "")
	pushRuntimeBaseIndex(t, host+"/runtime:py3.11", "linux/amd64")
	editYAML(t, dir, "runtime:py3.12", "runtime:py3.11")
	editYAML(t, dir, "dependencies: [pandas==2.2.3]", "dependencies: "+deps)
	return dir, host, fakeUV(t, d)
}

// depsTags lists the dependency image tags of the DAG repository.
func depsTags(t *testing.T, host string) []string {
	t.Helper()
	repo, err := name.NewRepository(host + "/team/etl")
	if err != nil {
		t.Fatal(err)
	}
	tags, err := remote.List(repo)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, tag := range tags {
		if strings.HasPrefix(tag, depsTagPrefix) {
			out = append(out, tag)
		}
	}
	return out
}

// With uv on PATH and a runtime base, a deploy whose dependency image is
// missing builds it from wheels and pushes it, then the DAG image, with no
// daemon; a second deploy that changed only dag.py appends the code to it.
func TestDeployBuildsTheDependencyImageFromWheels(t *testing.T) {
	d := testDeps()
	dir, host, uv := wheelProject(t, d, "[pandas==2.2.3, six==1.16.0]")
	t.Setenv("UV_INDEX_URL", "https://evil.example/simple")
	t.Setenv("PIP_INDEX_URL", "https://evil.example/simple")
	builder, argv := argvBuilder(t)
	out, _, err := deployDirDeps(t, d, &deployServer{}, dir, builder)
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	if calls := argvLines(t, argv); calls != nil {
		t.Errorf("the daemon ran: %q", calls)
	}
	if !strings.Contains(out, "builder:    dependency-layer (uv 0.11.32), then code-layer, no dependency image") {
		t.Errorf("output does not name the dependency layer:\n%s", out)
	}
	tags := depsTags(t, host)
	if len(tags) != 1 {
		t.Fatalf("dependency tags = %q, want one", tags)
	}
	calls := uvCalls(t, uv)
	if len(calls) != 2 || !strings.HasPrefix(calls[0], "pip compile --no-config --quiet --python-platform x86_64-manylinux_2_36 --python-version 3.11.9 --only-binary :all: --generate-hashes") ||
		!strings.HasPrefix(calls[1], "pip install --no-config --quiet --target ") ||
		!strings.Contains(calls[1], "--python-platform x86_64-manylinux_2_36 --python-version 3.11.9 --only-binary :all: --require-hashes --no-deps -r ") {
		t.Fatalf("uv calls = %q, want one compile and one install for linux/amd64", calls)
	}
	if c, rerr := os.ReadFile(filepath.Join(uv, "constraints.txt")); rerr != nil || string(c) != "six==1.16.0\n" { //nolint:gosec // a path this test created
		t.Errorf("constraints = %q (%v), want the distributions of the base", c, rerr)
	}
	if env := readProjectFile(t, uv, "env.log"); strings.Contains(env, "INDEX_URL") || !strings.Contains(env, "UV_PYTHON_DOWNLOADS=never") {
		t.Errorf("uv ran with:\n%s\nwant no index variable and Python downloads off", env)
	}
	deps := host + "/team/etl:" + tags[0]
	if cfg := depsConfig(t, deps); !slices.Contains(cfg.Env, "PYTHONPATH=/home/leoflow") || cfg.User != "65532:65532" {
		t.Errorf("dependency image config = %+v, want PYTHONPATH and the non-root user", cfg)
	}
	names := lastLayerNames(t, deps)
	sp := "usr/local/lib/python3.11/site-packages/"
	want := []string{"usr/local/bin/pandas", sp + "pandas/", sp + "pandas-2.2.3.dist-info/", sp + "pandas-2.2.3.dist-info/RECORD", sp + "pandas/__init__.py"}
	if !slices.Equal(names, want) {
		t.Errorf("the dependency layer holds %q, want %q (six is in the base)", names, want)
	}
	image := host + "/team/etl:" + labelOf(t, dir)
	if names := lastLayerNames(t, image); !slices.Equal(names, []string{"home/leoflow/dag.py"}) {
		t.Errorf("the code layer holds %q, want the DAG source", names)
	}

	writeProjectFile(t, dir, "dag.py", "# dag, edited\n")
	out, _, err = deployDirDeps(t, d, &deployServer{}, dir, builder)
	if err != nil {
		t.Fatalf("second deploy: %v\n%s", err, out)
	}
	if !strings.Contains(out, "builder:    code-layer, the registry holds the dependency image "+deps) {
		t.Errorf("the second deploy did not take the code-layer path:\n%s", out)
	}
	if again := uvCalls(t, uv); len(again) != 3 || !strings.HasPrefix(again[2], "pip compile ") {
		t.Errorf("uv calls after the second deploy = %q, want one more compile and no install", again)
	}
}

// depsConfig reads the config of the single image of the index ref names.
func depsConfig(t *testing.T, ref string) v1.Config {
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
	if err != nil {
		t.Fatal(err)
	}
	img, err := idx.Image(im.Manifests[0].Digest)
	if err != nil {
		t.Fatal(err)
	}
	cf, err := img.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	return cf.Config
}

// The builder line says how long the resolution took only when it took a
// second or more.
func TestWheelPlanNoteSaysASlowResolution(t *testing.T) {
	t.Parallel()
	if note := (&wheelPlan{took: 900 * time.Millisecond}).note(); note != "" {
		t.Errorf("a fast resolution says %q", note)
	}
	if note := (&wheelPlan{took: 2300 * time.Millisecond}).note(); note != "uv resolved the dependencies in 2.3s" {
		t.Errorf("a slow resolution says %q", note)
	}
}

// Dependencies that are not pinned take the split with uv, because the key
// covers what uv resolved: a new release that satisfies the range gives a
// new dependency image.
func TestUnpinnedDependenciesTakeTheDependencyLayer(t *testing.T) {
	d := testDeps()
	dir, host, uv := wheelProject(t, d, "[pandas>=2]")
	builder, _ := argvBuilder(t)
	out, _, err := runDeps(t, d, "compile", dir, "--build", "--push", "--builder", builder)
	if err != nil || !strings.Contains(out, "builder:    dependency-layer (uv 0.11.32)") {
		t.Fatalf("compile: %v, want the dependency layer:\n%s", err, out)
	}
	if werr := os.WriteFile(filepath.Join(uv, "version"), []byte("2.3.0"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	out, _, err = runDeps(t, d, "compile", dir, "--build", "--push", "--builder", builder)
	if err != nil || !strings.Contains(out, "builder:    dependency-layer (uv 0.11.32)") {
		t.Fatalf("compile after a new release: %v, want the dependency layer again:\n%s", err, out)
	}
	if tags := depsTags(t, host); len(tags) != 2 {
		t.Errorf("dependency tags = %q, want one per resolution", tags)
	}
}

// splitOf runs splitFor on the project in dir as a pushed compile run
// under d does.
func splitOf(t *testing.T, d *cliDeps, dir string) (split *build.Split, why string) {
	t.Helper()
	cfg, err := loadProjectConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	return splitFor(testCtx(d), dir, "Dockerfile", compileOptions{push: true}, cfg, "127.0.0.1:1/team/etl:v1")
}

// Each reason the dependency layer does not apply is said: on the split the
// daemon builds when the dependencies are pinned, as the reason not to split
// when they are not.
func TestTheDependencyLayerSaysWhyItDoesNotApply(t *testing.T) {
	noWheel := "  × No solution found when resolving dependencies:\n  ╰─▶ Because pyyaml==5.3 has no usable wheels and you require pyyaml==5.3, we\n" +
		"      can conclude that your requirements are unsatisfiable.\n\nhint: Wheels are required for `pyyaml`\n"
	cases := map[string]struct {
		edit func(t *testing.T, d *cliDeps, dir, uv, host string)
		want string
	}{
		"system packages": {func(t *testing.T, _ *cliDeps, dir, _, _ string) {
			editYAML(t, dir, "dependencies:", "system_packages: [libpq5]\ndependencies:")
		}, "system_packages need a real build"},
		"platform": {func(t *testing.T, _ *cliDeps, dir, _, _ string) {
			editYAML(t, dir, "[linux/amd64]", "[linux/arm/v7]")
		}, "the platform linux/arm/v7 has no wheel tag"},
		"no uv": {func(_ *testing.T, d *cliDeps, _, _, _ string) {
			d.lookUV = func() (string, error) { return "", os.ErrNotExist }
		}, "uv is not on PATH"},
		"not a runtime base": {func(t *testing.T, _ *cliDeps, dir, _, _ string) {
			editYAML(t, dir, "runtime:py3.11", "runtime:py3.12")
		}, "the base image is not a Dexaflow runtime base"},
		"python version": {func(t *testing.T, _ *cliDeps, dir, _, _ string) {
			editYAML(t, dir, "dependencies:", "python_version: \"3.12\"\ndependencies:")
		}, "python_version 3.12 in dexaflow.yaml does not match Python 3.11.9 of the base image"},
		"no python": {func(t *testing.T, _ *cliDeps, _, uv, _ string) {
			if err := os.WriteFile(filepath.Join(uv, "nopython"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}, "uv finds no Python on this machine"},
		"pip configuration": {func(t *testing.T, _ *cliDeps, dir, _, host string) {
			pushRuntimeBaseWithEnv(t, host+"/runtime:pip", []string{"PIP_INDEX_URL=https://pypi.internal/simple"}, "linux/amd64")
			editYAML(t, dir, "runtime:py3.11", "runtime:pip")
		}, "the base image configures pip (PIP_INDEX_URL)"},
		"windows": {func(_ *testing.T, d *cliDeps, _, _, _ string) {
			d.hostOS = "windows"
		}, "the dependency layer does not run on Windows yet"},
		"no wheel": {func(t *testing.T, _ *cliDeps, _, uv, _ string) {
			if err := os.WriteFile(filepath.Join(uv, "fail"), []byte(noWheel), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "uv could not resolve the dependencies for linux/amd64: Because pyyaml==5.3 has no usable wheels and you require pyyaml==5.3, we can conclude that your requirements are unsatisfiable."},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			d := testDeps()
			dir, host, uv := wheelProject(t, d, "[pandas==2.2.3]")
			tc.edit(t, d, dir, uv, host)
			split, why := splitOf(t, d, dir)
			if split == nil || split.Wheels != nil || !strings.Contains(split.NoWheels, tc.want) {
				t.Fatalf("pinned: split = %+v (%q), want the daemon's, saying %q", split, why, tc.want)
			}
			editYAML(t, dir, "[pandas==2.2.3]", "[pandas>=2]")
			split, why = splitOf(t, d, dir)
			if split != nil || !strings.Contains(why, `"pandas>=2" is not pinned`) || !strings.Contains(why, tc.want) {
				t.Errorf("unpinned: split = %+v, reason %q, want none, saying %q", split, why, tc.want)
			}
		})
	}
}

// A path, a URL or a VCS reference, and anything a requirements file would
// read as an option, keep the dependency layer out.
func TestWheelBlockerRefusesWhatIsNotARequirementByName(t *testing.T) {
	t.Parallel()
	cfg := &domain.LeoflowConfig{}
	for _, d := range []string{"./lib", "/opt/pkg", "pkg @ https://x/y.whl", "pkg @ ./local", "git+https://x/y.git", "file:///x.whl", "https://x/y.whl"} {
		if why := wheelBlocker(context.Background(), cfg, []string{d}, []string{"linux/amd64"}); !strings.Contains(why, "a URL, a path or a VCS reference") {
			t.Errorf("%q: %q", d, why)
		}
	}
	for _, d := range []string{"--pre", "pkg==1 --hash=sha256:00", "-e .", "pkg #x"} {
		if why := wheelBlocker(context.Background(), cfg, []string{d}, []string{"linux/amd64"}); !strings.Contains(why, "not a requirement by name") {
			t.Errorf("%q: %q", d, why)
		}
	}
	for _, d := range []string{"pandas", "requests[socks]>=2,<3", "attrs==23.1; python_version < \"3.12\"", "Apache_Airflow.Providers-Amazon~=9.1"} {
		if why := wheelBlocker(context.Background(), cfg, []string{d}, []string{"linux/amd64", "linux/arm64"}); why != "" {
			t.Errorf("%q was refused: %q", d, why)
		}
	}
}

// keyPlan is a resolved plan for two platforms.
func keyPlan() *wheelPlan {
	dist := func(name, version string, hashes ...string) resolvedDist {
		return resolvedDist{name: name, version: version, hashes: hashes}
	}
	return &wheelPlan{
		uv: &uvTool{version: "uv 0.11.32"}, base: build.Pinned{Ref: "reg.io/runtime@sha256:aa"}, python: "3.11.9",
		env: []string{"PYTHONPATH=/home/leoflow"}, user: "65532:65532",
		platforms: []wheelPlatform{
			{platform: "linux/amd64", tag: "x86_64-manylinux_2_36", resolved: []resolvedDist{dist("numpy", "2.1.0", "sha256:01"), dist("pandas", "2.2.3", "sha256:02", "sha256:03")}},
			{platform: "linux/arm64", tag: "aarch64-manylinux_2_36", resolved: []resolvedDist{dist("numpy", "2.1.0", "sha256:01"), dist("pandas", "2.2.3", "sha256:02", "sha256:03")}},
		},
	}
}

// The key covers the resolution, the base, the platforms, Python, the wheel
// tag, uv and the image config, and nothing else: the order of the
// platforms does not change it.
func TestWheelKeyCoversWhatTheImageHolds(t *testing.T) {
	t.Parallel()
	k := keyPlan().key()
	if len(k) != 64 || keyPlan().key() != k {
		t.Fatalf("key %q is not a stable sha256", k)
	}
	swapped := keyPlan()
	swapped.platforms[0], swapped.platforms[1] = swapped.platforms[1], swapped.platforms[0]
	if swapped.key() != k {
		t.Error("the order of the platforms changed the key")
	}
	edits := map[string]func(p *wheelPlan){
		"version":   func(p *wheelPlan) { p.platforms[0].resolved[1].version = "2.2.4" },
		"hash":      func(p *wheelPlan) { p.platforms[1].resolved[0].hashes = []string{"sha256:09"} },
		"base":      func(p *wheelPlan) { p.base.Ref = "reg.io/runtime@sha256:bb" },
		"platform":  func(p *wheelPlan) { p.platforms = p.platforms[:1] },
		"python":    func(p *wheelPlan) { p.python = "3.11.10" },
		"uv":        func(p *wheelPlan) { p.uv = &uvTool{version: "uv 0.12.0"} },
		"wheel tag": func(p *wheelPlan) { p.platforms[0].tag = "x86_64-manylinux_2_31" },
		"env":       func(p *wheelPlan) { p.env = nil },
		"user":      func(p *wheelPlan) { p.user = "" },
		"added dist": func(p *wheelPlan) {
			p.platforms[0].resolved = append(p.platforms[0].resolved, resolvedDist{name: "pytz", version: "1"})
		},
	}
	for name, edit := range edits {
		p := keyPlan()
		edit(p)
		if p.key() == k {
			t.Errorf("a different %s kept the key", name)
		}
	}
	format1 := depsKey("FROM reg.io/runtime@sha256:aa\n", []string{"linux/amd64", "linux/arm64"})
	if format1 == k {
		t.Error("a format 2 key equals a format 1 key")
	}
}

func TestManylinuxTag(t *testing.T) {
	t.Parallel()
	cases := map[string]string{"2.41": "manylinux_2_36", "2.36": "manylinux_2_36", "2.31": "manylinux_2_31", "2.30": "manylinux_2_28", "2.17": "manylinux_2_17"}
	for glibc, want := range cases {
		if got, why := manylinuxTag(glibc); got != want {
			t.Errorf("manylinuxTag(%s) = %q (%s), want %q", glibc, got, why, want)
		}
	}
	for _, glibc := range []string{"", "2.12", "musl"} {
		if got, why := manylinuxTag(glibc); got != "" || why == "" {
			t.Errorf("manylinuxTag(%q) = %q, want a refusal", glibc, got)
		}
	}
}

func TestParseResolved(t *testing.T) {
	t.Parallel()
	text := "anyio==4.15.1 \\\n    --hash=sha256:bb \\\n    --hash=sha256:aa\n# a comment\nIDNA==3.10 \\\n    --hash=sha256:cc\n"
	got, err := parseResolved(text)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].line() != "anyio==4.15.1 --hash=sha256:aa --hash=sha256:bb" || got[1].line() != "idna==3.10 --hash=sha256:cc" {
		t.Errorf("resolved = %+v", got)
	}
	for _, bad := range []string{"anyio>=4 \\\n  --hash=sha256:aa\n", "anyio==4.* --hash=sha256:aa\n", "anyio==4.1\n", "anyio==4.1 --index-url x\n"} {
		if _, err := parseResolved(bad); err == nil {
			t.Errorf("parseResolved(%q) accepted it", bad)
		}
	}
}

// What the base holds at the resolved version is not installed again; a
// different version is.
func TestToInstallLeavesOutWhatTheBaseHolds(t *testing.T) {
	t.Parallel()
	wp := wheelPlatform{
		base:     build.BaseImage{Dists: []string{"six==1.16.0", "pip==26.2.1"}},
		resolved: []resolvedDist{{name: "pandas", version: "2.2.3", hashes: []string{"sha256:01"}}, {name: "six", version: "1.16.0", hashes: []string{"sha256:02"}}, {name: "pip", version: "26.2.2", hashes: []string{"sha256:03"}}},
	}
	if got := toInstall(wp); !slices.Equal(got, []string{"pandas==2.2.3 --hash=sha256:01", "pip==26.2.2 --hash=sha256:03"}) {
		t.Errorf("to install = %q", got)
	}
}

// uv runs with the proxy, CA and cache variables of the CLI, and without
// any uv or pip configuration.
func TestUVEnvironmentDropsInstallerConfiguration(t *testing.T) {
	t.Setenv("UV_INDEX_URL", "https://evil.example/simple")
	t.Setenv("PIP_INDEX_URL", "https://evil.example/simple")
	t.Setenv("UV_CACHE_DIR", "/tmp/uv-cache")
	t.Setenv("HTTPS_PROXY", "http://proxy:3128")
	t.Setenv("SSL_CERT_FILE", "/etc/ca.pem")
	t.Setenv("UV_NATIVE_TLS", "1")
	t.Setenv("ALL_PROXY", "")
	if err := os.Unsetenv("ALL_PROXY"); err != nil {
		t.Fatal(err)
	}
	env := uvEnv("")
	for _, kv := range env {
		if strings.HasPrefix(kv, "UV_INDEX_URL=") || strings.HasPrefix(kv, "PIP_") || strings.HasPrefix(kv, "ALL_PROXY=") {
			t.Errorf("uv runs with %s", kv)
		}
	}
	for _, kv := range []string{"UV_CACHE_DIR=/tmp/uv-cache", "HTTPS_PROXY=http://proxy:3128", "SSL_CERT_FILE=/etc/ca.pem", "UV_NATIVE_TLS=1"} {
		if !slices.Contains(env, kv) {
			t.Errorf("uv runs without %s", kv)
		}
	}
}

func TestUVMessageKeepsTheReason(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"error: no network\nhint: try again\n": "error: no network",
		"  × No solution found when resolving dependencies:\n  ╰─▶ Because you require pip==25.0 and pip==26.0, we can\n      conclude that your requirements are unsatisfiable.\n": "Because you require pip==25.0 and pip==26.0, we can conclude that your requirements are unsatisfiable.",
		"error: Failed to fetch: `https://pypi.org/simple/x/`\n  Caused by: connection refused\n\nmore\n":                                                                           "error: Failed to fetch: `https://pypi.org/simple/x/` Caused by: connection refused",
		"": "",
	}
	for in, want := range cases {
		if got := uvMessage(in); got != want {
			t.Errorf("uvMessage(%q) = %q, want %q", in, got, want)
		}
	}
}

// With DEXAFLOW_TEST_REAL_UV=1 and uv on PATH, the real uv resolves and
// installs a small set from PyPI, and the tree takes the layout the layer
// expects. CI does not run it: it needs PyPI.
func TestRealUVResolvesAndInstalls(t *testing.T) {
	t.Parallel()
	d := testDeps()
	if os.Getenv("DEXAFLOW_TEST_REAL_UV") != "1" {
		t.Skip("set DEXAFLOW_TEST_REAL_UV=1 to run uv against PyPI")
	}
	p, err := exec.LookPath("uv")
	if err != nil {
		t.Skip("uv is not on PATH")
	}
	d.lookUV = func() (string, error) { return p, nil }
	u, why := findUV(testCtx(d))
	if u == nil {
		t.Fatal(why)
	}
	resolved, err := u.compile(context.Background(), "aarch64-manylinux_2_36", "3.11.9", []string{"idna==3.10"}, []string{"httpx==0.27.2"})
	if err != nil {
		t.Fatal(err)
	}
	pins := make([]string, 0, len(resolved))
	for _, r := range resolved {
		pins = append(pins, r.pin())
	}
	if !slices.Contains(pins, "idna==3.10") || !slices.Contains(pins, "httpx==0.27.2") {
		t.Fatalf("resolved %q, want httpx and the base's idna", pins)
	}
	target := t.TempDir()
	if err := u.install(context.Background(), "aarch64-manylinux_2_36", "3.11.9", toInstall(wheelPlatform{resolved: resolved}), target); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"httpx/__init__.py", "httpx-0.27.2.dist-info/RECORD", "bin/httpx"} {
		if _, err := os.Stat(filepath.Join(target, f)); err != nil {
			t.Errorf("the install has no %s: %v", f, err)
		}
	}
}

// A uv that does not say it is uv is not used.
func TestFindUVChecksTheVersion(t *testing.T) {
	t.Parallel()
	d := testDeps()
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX-only")
	}
	p := filepath.Join(t.TempDir(), "uv")
	if err := os.WriteFile(p, []byte("#!/bin/sh\necho 'pip 24.0'\n"), 0o700); err != nil { //nolint:gosec // a test fixture that must be executable
		t.Fatal(err)
	}
	d.lookUV = func() (string, error) { return p, nil }
	if u, why := findUV(testCtx(d)); u != nil || !strings.Contains(why, "not a uv version") {
		t.Errorf("findUV = %+v, %q, want a refusal", u, why)
	}
}

// Without uv, unpinned dependencies keep the whole build before the base
// image is read: no registry request is made for nothing.
func TestUnpinnedDependenciesWithoutUVDoNotReadTheBase(t *testing.T) {
	d := testDeps()
	dir, _, _ := splitProject(t, "")
	editYAML(t, dir, "dependencies: [pandas==2.2.3]", "dependencies: [pandas>=2]")
	editYAML(t, dir, "base_image: ", "base_image: ghcr.io/x/y:1\n# was: ")
	if split, why := splitOf(t, d, dir); split != nil || !strings.Contains(why, "not pinned") || strings.Contains(why, "could not be read") {
		t.Errorf("split = %+v, reason %q, want the unpinned reason alone", split, why)
	}
}

// Each platform is resolved and installed with its own wheel tag, and the
// dependency image is an index of both.
func TestTheDependencyLayerBuildsEveryPlatform(t *testing.T) {
	d := testDeps()
	dir, host, uv := wheelProject(t, d, "[pandas==2.2.3]")
	pushRuntimeBaseIndex(t, host+"/runtime:py3.11", "linux/amd64", "linux/arm64")
	editYAML(t, dir, "[linux/amd64]", "[linux/arm64, linux/amd64]")
	builder, _ := argvBuilder(t)
	out, _, err := runDeps(t, d, "compile", dir, "--build", "--push", "--builder", builder)
	if err != nil || !strings.Contains(out, "builder:    dependency-layer (uv 0.11.32)") {
		t.Fatalf("compile: %v, want the dependency layer:\n%s", err, out)
	}
	calls := uvCalls(t, uv)
	tags := make([]string, 0, len(calls))
	for _, c := range calls {
		f := strings.Fields(c)
		tags = append(tags, f[1]+" "+f[slices.Index(f, "--python-platform")+1])
	}
	want := []string{"compile x86_64-manylinux_2_36", "compile aarch64-manylinux_2_36", "install x86_64-manylinux_2_36", "install aarch64-manylinux_2_36"}
	if !slices.Equal(tags, want) {
		t.Errorf("uv calls = %q, want %q", tags, want)
	}
	pinned, err := build.Registry{}.Resolve(context.Background(), host+"/team/etl:"+depsTags(t, host)[0])
	if err != nil {
		t.Fatal(err)
	}
	idx, err := v1.ParseIndexManifest(bytes.NewReader(pinned.Manifest))
	if err != nil || len(idx.Manifests) != 2 {
		t.Fatalf("dependency index: %v, %+v", err, idx)
	}
}

// A split built from wheels names the dependency image the daemon would
// build when the wheels fail, under the daemon's key, only when the
// dependencies are pinned: for a range, the daemon's key would keep the
// first resolution.
func TestTheDaemonFallbackIsOnlyForPinnedDependencies(t *testing.T) {
	d := testDeps()
	dir, _, _ := wheelProject(t, d, "[pandas==2.2.3]")
	split, why := splitOf(t, d, dir)
	if split == nil || split.Wheels == nil || !strings.Contains(split.DaemonDepsImage, ":deps-") || split.DaemonDepsImage == split.DepsImage {
		t.Fatalf("pinned: split = %+v (%q), want a daemon fallback under its own tag", split, why)
	}
	cfg, err := loadProjectConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := baseReaderFor(testCtx(d), cfg).Resolve(context.Background(), cfg.BaseImage)
	if err != nil {
		t.Fatal(err)
	}
	content, err := dependencyDockerfile(cfg, pinned.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if want := depsImage("127.0.0.1:1/team/etl:v1", depsKey(content, []string{"linux/amd64"})); split.DaemonDepsImage != want {
		t.Errorf("daemon fallback = %s, want the format 1 image %s", split.DaemonDepsImage, want)
	}
	editYAML(t, dir, "[pandas==2.2.3]", "[pandas>=2]")
	if split, _ = splitOf(t, d, dir); split == nil || split.Wheels == nil || split.DaemonDepsImage != "" {
		t.Errorf("unpinned: split = %+v, want wheels with no daemon fallback", split)
	}
}

// uv looks for a Python with --no-config in an empty directory of its own,
// so a .python-version or uv.toml where the CLI runs changes nothing.
func TestUVFindsPythonInAnEmptyDirectory(t *testing.T) {
	t.Parallel()
	d := testDeps()
	uv := fakeUV(t, d)
	u, why := findUV(testCtx(d))
	if u == nil {
		t.Fatal(why)
	}
	if err := u.findPython(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls := readProjectFile(t, uv, "calls.log"); !strings.Contains(calls, "python find --no-config\n") {
		t.Errorf("uv calls = %q, want python find --no-config", calls)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := strings.TrimSpace(readProjectFile(t, uv, "python.pwd"))
	if dir == cwd || !strings.HasPrefix(filepath.Base(dir), "dexaflow-uv-") {
		t.Errorf("uv ran in %s, want an empty temporary directory", dir)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the directory %s is left behind (%v)", dir, err)
	}
}
