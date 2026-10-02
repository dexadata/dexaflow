//go:build integration

package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// This file closes the wiring half of the DAG base-image pin. baseImageRef is
// unit-tested as a pure function (compile_build_baseimage_test.go), but nothing
// asserted that a REAL binary, stamped the way a release stamps it, actually
// emits the pinned FROM — and the version stamp is precisely what selects the
// branch:
//
//	a clean release version  → ghcr.io/…/leoflow-runtime:py<ver>-v<X.Y.Z>  (immutable)
//	a dev/dirty/describe one → ghcr.io/…/leoflow-runtime:py<ver>           (moving)
//
// The gap was not merely "untested": it was untestable by accident. Every test
// binary and every `go build` reports version "dev", so an in-process test of
// the compile chain can only ever observe the moving branch, and the k3d e2es
// hand `compile` their own Dockerfile (`--dockerfile Dockerfile`, FROM a
// locally-built base), so they never reach generatedDockerfile at all. Which
// branch the toolchain exercised therefore depended on how far main had drifted
// from the last tag — `make build` stamps `git describe`, so on the tag commit
// the pinned branch ran and one commit later the moving one did, silently
// alternating (#1032).
//
// The failure that would have shipped: a released CLI resolving the MOVING tag.
// Users get a base image that changes under them between releases, which
// defeats the immutability that makes a base bump safe to ship in a patch —
// and every existing signal stays green, because the unit test tests the
// function and the e2e may be exercising the other branch that week.
//
// So the stamping is not a detail of the setup, it IS the subject. Anything
// here that skips it tests nothing while looking like it tests everything: an
// unstamped binary yields the moving tag for EVERY case, so the moving
// assertion passes for the wrong reason and reads as proof that we never pin.
// assertVersionStamp exists to make that state impossible to mistake for a pass.

// publishedRuntimeRepo is spelled out rather than read from publishedBaseRepo so
// this test pins the observable contract — the repository release.yaml actually
// pushes tags to — instead of agreeing with the constant whatever it becomes.
const publishedRuntimeRepo = "ghcr.io/dexadata/leoflow-runtime"

// scaffoldPythonVersion is what `leoflow init` writes into leoflow.yaml, and
// what release.yaml's runtime-image matrix publishes a base for.
const scaffoldPythonVersion = "3.11"

// fromLineRe captures the argument of the generated Dockerfile's FROM.
var fromLineRe = regexp.MustCompile(`(?m)^FROM\s+(\S+)\s*$`)

// TestCompileFromPinsReleaseBaseImage drives the whole chain — link-time version
// stamp → version.Get() → resolveBaseImage → generatedDockerfile → the FROM the
// builder is handed — against a real binary, once per version shape a shipped
// CLI can carry.
//
// Both branches are asserted so neither can silently become the other, which is
// the actual regression risk: the two are one `if` apart, and a mistake in
// either direction is invisible in a diff review and invisible in CI.
//
// No cluster and no registry: a builder shim copies the Dockerfile the CLI told
// it to build, which is the only artifact under test. That keeps this in the
// per-PR integration job rather than in a k3d e2e nobody runs on a docs change.
func TestCompileFromPinsReleaseBaseImage(t *testing.T) {
	root := moduleRoot(t)
	versionPkg := versionPkgFromMakefile(t, root)

	cases := []struct {
		name string
		// stamp is the value linked into internal/version.version.
		stamp string
		want  string
	}{
		{
			// What a RELEASED CLI actually carries: GoReleaser stamps
			// `{{ .Version }}`, which drops the tag's leading `v`. The published
			// base tag always has it, so this is the case the normalization in
			// baseImageRef exists for, and the one users hit.
			name:  "goreleaser release stamp pins the immutable base",
			stamp: "9.9.9",
			want:  publishedRuntimeRepo + ":py" + scaffoldPythonVersion + "-v9.9.9",
		},
		{
			// `make build` on a clean tag: same tag, leading `v` kept. Must land
			// on the identical reference — a released CLI and a source build of
			// the same commit cannot disagree about their base.
			name:  "makefile stamp on a clean tag pins the same base",
			stamp: "v9.9.9",
			want:  publishedRuntimeRepo + ":py" + scaffoldPythonVersion + "-v9.9.9",
		},
		{
			// `git describe` after the tag. No versioned base was ever published
			// for an unreleased commit, so pinning here would produce a FROM that
			// cannot be pulled: the moving line tag is the only correct answer.
			name:  "git describe stamp falls back to the moving line tag",
			stamp: "v9.9.9-3-gdeadbee",
			want:  publishedRuntimeRepo + ":py" + scaffoldPythonVersion,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bin := buildStampedCLI(t, root, versionPkg, tc.stamp)
			assertVersionStamp(t, bin, tc.stamp)

			got := compiledDockerfileFrom(t, bin)
			if got != tc.want {
				t.Errorf("generated Dockerfile FROM = %q, want %q (CLI stamped %q)", got, tc.want, tc.stamp)
			}
		})
	}
}

// moduleRoot returns the repository root, resolved from the module file rather
// than from the test's working directory so the helper survives being moved to
// another package.
func moduleRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatalf("locating go.mod: %v", err)
	}
	gomod := strings.TrimSpace(string(out))
	if gomod == "" || gomod == os.DevNull {
		t.Fatalf("go env GOMOD = %q; this test must run inside the module", gomod)
	}
	return filepath.Dir(gomod)
}

// versionPkgFromMakefile reads the package path the Makefile stamps
// (VERSION_PKG) instead of hardcoding it here. Hardcoding would let this test
// keep stamping a symbol that shipped builds no longer stamp — it would still
// go red (assertVersionStamp would see "dev"), but red for a reason that reads
// as "the ldflag broke" rather than "the test is aimed at the wrong symbol".
// Reading it keeps the aim automatic and the failure honest.
func versionPkgFromMakefile(t *testing.T, root string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatalf("reading Makefile: %v", err)
	}
	m := regexp.MustCompile(`(?m)^VERSION_PKG\s*:?=\s*(\S+)\s*$`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("no VERSION_PKG assignment in the Makefile; this test stamps the same symbol `make build` does and cannot find it")
	}
	return string(m[1])
}

// buildStampedCLI builds cmd/dexaflow with the version linked in, exactly as
// `make build` and GoReleaser do. The stamp is the only thing that varies
// between cases.
func buildStampedCLI(t *testing.T, root, versionPkg, stamp string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "leoflow")
	//nolint:gosec // G204: versionPkg comes from the repo's own Makefile and stamp from this test's table.
	build := exec.CommandContext(t.Context(), "go", "build",
		"-ldflags", "-X "+versionPkg+".version="+stamp,
		"-o", bin, "github.com/dexadata/dexaflow/cmd/dexaflow")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building a CLI stamped %q: %v\n%s", stamp, err, out)
	}
	return bin
}

// assertVersionStamp fails BEFORE any base-image assertion if the link-time
// stamp did not take. An unstamped binary reports "dev" and takes the moving
// branch for every case, so the moving assertion would pass while proving
// nothing and the pinned one would fail pointing at baseImageRef — the wrong
// suspect. A `-X` that misses its symbol is silent by design: the build
// succeeds and the default stands.
func assertVersionStamp(t *testing.T, bin, want string) {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), bin, "version", "--json").Output()
	if err != nil {
		t.Fatalf("running `%s version --json`: %v", bin, err)
	}
	var info struct {
		Version string `json:"version"`
	}
	if uerr := json.Unmarshal(out, &info); uerr != nil {
		t.Fatalf("`version --json` is not JSON: %v (%q)", uerr, out)
	}
	if info.Version != want {
		t.Fatalf("the -X stamp did not take: binary reports version %q, want %q. "+
			"Every assertion below would measure an unstamped binary, which resolves the "+
			"moving base tag for every case", info.Version, want)
	}
}

// compiledDockerfileFrom scaffolds a project with NO base_image in leoflow.yaml
// (the default the pin governs), compiles it with the given binary, and returns
// the FROM argument of the Dockerfile the CLI generated and handed the builder.
//
// The generated Dockerfile is a temp file that compile deletes as soon as the
// build returns (ensureDockerfile's cleanup), so the shim has to copy it while
// the build is in flight — reading it afterwards is a race the CLI always wins.
func compiledDockerfileFrom(t *testing.T, bin string) string {
	t.Helper()
	home := t.TempDir()
	work := t.TempDir()
	dir := filepath.Join(work, "proj")

	if out, err := cliCmd(t, bin, home, "init", dir).CombinedOutput(); err != nil {
		t.Fatalf("leoflow init: %v\n%s", err, out)
	}
	// Guard the premise: the scaffold must not carry a base_image, because an
	// explicit one short-circuits resolveBaseImage and the FROM below would say
	// nothing about the pin.
	yaml, err := os.ReadFile(filepath.Join(dir, "leoflow.yaml"))
	if err != nil {
		t.Fatalf("reading the scaffolded leoflow.yaml: %v", err)
	}
	if strings.Contains(string(yaml), "base_image") {
		t.Fatalf("the scaffold now sets base_image; this test must compile a project WITHOUT one:\n%s", yaml)
	}
	if !strings.Contains(string(yaml), `python_version: "`+scaffoldPythonVersion+`"`) {
		t.Fatalf("the scaffold's python_version is no longer %s; the expected tags in this test are stale:\n%s",
			scaffoldPythonVersion, yaml)
	}

	// A parser stub: the Python parser is orthogonal to the base image, and
	// depending on an interpreter would make this test skip exactly where the
	// per-PR signal is wanted. Same shape as the fake parser in cli_test.go.
	parser := filepath.Join(work, "fake-parser.sh")
	writeScript(t, parser, "#!/usr/bin/env bash\n"+
		"out=\"\"\n"+
		"while [ $# -gt 0 ]; do case \"$1\" in --output) out=\"$2\"; shift 2;; *) shift;; esac; done\n"+
		"cat > \"$out\" <<'JSON'\n"+
		`{"schema_version":"1.0","dag_id":"proj","dag_version":"v0.0.0-test","image":"test:v1",`+
		`"tasks":[{"task_id":"hello","type":"python","entrypoint":"dag:hello"}]}`+"\n"+
		"JSON\n")

	// A builder shim standing in for docker. `compile --build` invokes
	// `<builder> build [--platform p] -t IMG -f DOCKERFILE CTX` (buildArgs), so
	// copying the -f argument captures exactly what would have been built.
	captured := filepath.Join(work, "captured.Dockerfile")
	builder := filepath.Join(work, "fake-builder.sh")
	writeScript(t, builder, "#!/usr/bin/env bash\n"+
		"while [ $# -gt 0 ]; do case \"$1\" in -f) cp \"$2\" \""+captured+"\"; shift 2;; *) shift;; esac; done\n")

	compile := cliCmd(t, bin, home, "compile", dir,
		"--output", filepath.Join(dir, "dag.json"),
		"--image", "test:v1",
		// Pinned so the compile never shells out to `git describe` in a temp
		// directory; the DAG version has no bearing on the base image.
		"--dag-version", "v0.0.0-test",
		"--parser-cmd", parser,
		"--build", "--builder", builder)
	out, cerr := compile.CombinedOutput()
	if cerr != nil {
		t.Fatalf("leoflow compile --build: %v\n%s", cerr, out)
	}

	raw, err := os.ReadFile(captured)
	if err != nil {
		t.Fatalf("the builder shim captured no Dockerfile (was it invoked with -f?): %v", err)
	}
	m := fromLineRe.FindSubmatch(raw)
	if m == nil {
		t.Fatalf("no FROM line in the generated Dockerfile:\n%s", raw)
	}
	return string(m[1])
}

// cliCmd builds a command that runs the stamped binary in a hermetic
// environment: its own HOME (compile self-heals ~/.leoflow/pysrc and reads
// ~/.leoflow/config.yaml, so a developer's real home must neither influence the
// result nor be written to) and no LEOFLOW_* variables leaked from the shell.
func cliCmd(t *testing.T, bin, home string, args ...string) *exec.Cmd {
	t.Helper()
	//nolint:gosec // G204: bin is this test's own build output and args are literals.
	cmd := exec.CommandContext(t.Context(), bin, args...)
	env := []string{"HOME=" + home}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "HOME=") || strings.HasPrefix(kv, "LEOFLOW_") {
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = env
	return cmd
}

func writeScript(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}
