package build

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// pushedDigest is the digest the fake tools report for what they pushed.
const pushedDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"

// pushTool configures a fake container tool that reports what it pushed the
// way the real one does.
type pushTool struct {
	// name is the executable's base name: docker, podman or nerdctl.
	name string
	// buildx makes `docker buildx version` succeed.
	buildx bool
	// metadata is written to the file passed as --metadata-file.
	metadata string
	// digestFile is written to the file passed as --digestfile.
	digestFile string
	// pushOutput is printed on stdout by `<tool> push`.
	pushOutput string
	// envVar, when set, is the variable whose value each call appends to
	// env.log, next to the log of calls, one line per call.
	envVar string
}

// fake writes the tool and returns its path and the log of its calls.
func (p pushTool) fake(t *testing.T) (tool, log string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX-only")
	}
	dir := t.TempDir()
	tool = filepath.Join(dir, p.name)
	log = filepath.Join(dir, "calls.log")
	buildxExit := "1"
	if p.buildx {
		buildxExit = "0"
	}
	for path, content := range map[string]string{"metadata": p.metadata, "digest": p.digestFile, "pushout": p.pushOutput} {
		if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := "#!/bin/sh\n" +
		"if [ \"$1 $2\" = \"buildx version\" ]; then exit " + buildxExit + "; fi\n" +
		"echo \"$*\" >> \"" + log + "\"\n" +
		envLine(p.envVar, filepath.Join(dir, "env.log")) +
		"prev=\n" +
		"for a in \"$@\"; do\n" +
		"  case \"$prev\" in\n" +
		"    --metadata-file) cat \"" + dir + "/metadata\" > \"$a\" ;;\n" +
		"    --digestfile) cat \"" + dir + "/digest\" > \"$a\" ;;\n" +
		"  esac\n" +
		"  prev=\"$a\"\n" +
		"done\n" +
		"if [ \"$1\" = push ]; then cat \"" + dir + "/pushout\"; fi\n" +
		"exit 0\n"
	if err := os.WriteFile(tool, []byte(script), 0o700); err != nil { //nolint:gosec // a test fixture that must be executable
		t.Fatal(err)
	}
	return tool, log
}

// envLine is the line of a fake tool's script that appends the value of
// envVar to log, or nothing without envVar.
func envLine(envVar, log string) string {
	if envVar == "" {
		return ""
	}
	return "printf '%s\\n' \"$" + envVar + "\" >> \"" + log + "\"\n"
}

// fileArg matches the temporary file a call names after --metadata-file or
// --digestfile, so the logged argv compares without it.
var fileArg = regexp.MustCompile(`(--metadata-file|--digestfile) \S+`)

// pushCalls returns the logged argv with the temporary files replaced by F.
func pushCalls(t *testing.T, log string) string {
	t.Helper()
	return fileArg.ReplaceAllString(strings.Join(calls(t, log), "\n"), "$1 F")
}

func pushRequest(pin bool, platforms ...string) Request {
	req := request(platforms...)
	req.Push, req.Pin = true, pin
	return req
}

// With buildx, docker builds and pushes in one call and writes the digest of
// what it pushed to the metadata file. A one-platform pin is taken from
// there: the registry is never read, so another push of the same tag between
// the push and a read cannot change what is registered.
func TestLocalDaemonPinsFromTheBuildxMetadataFile(t *testing.T) {
	tool, log := pushTool{name: "docker", buildx: true, metadata: `{"containerimage.digest":"` + pushedDigest + `","image.name":"reg.io/team/etl:v1"}`}.fake(t)
	reg := &fakeRegistry{err: errors.New("the registry must not be read")}

	res, err := LocalDaemon{Tool: tool, Registry: reg}.Build(context.Background(), pushRequest(true, "linux/arm64"))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := "buildx build --platform linux/arm64 --push --metadata-file F -t reg.io/team/etl:v1 -f /p/Dockerfile /p"
	if got := pushCalls(t, log); got != want {
		t.Fatalf("tool calls:\n%s\nwant:\n%s", got, want)
	}
	if len(reg.calls) != 0 {
		t.Errorf("registry reads = %q, want none", reg.calls)
	}
	if res.Ref != "reg.io/team/etl@"+pushedDigest || !res.Pushed {
		t.Errorf("result ref=%q pushed=%v, want the digest from the metadata file", res.Ref, res.Pushed)
	}
}

// A multi-platform push is still checked to hold every platform, but the
// registry is read at the digest the tool reported, which no later push can
// move, instead of at the tag.
func TestLocalDaemonVerifiesSeveralPlatformsAtTheReportedDigest(t *testing.T) {
	tool, log := pushTool{name: "docker", buildx: true, metadata: `{"containerimage.digest":"` + pushedDigest + `"}`}.fake(t)
	reg := &fakeRegistry{ref: "reg.io/team/etl@sha256:other", manifest: indexOf("linux/amd64", "linux/arm64")}

	res, err := LocalDaemon{Tool: tool, Registry: reg}.Build(context.Background(), pushRequest(true, "linux/amd64", "linux/arm64"))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := "buildx build --platform linux/amd64,linux/arm64 --push --metadata-file F -t reg.io/team/etl:v1 -f /p/Dockerfile /p"
	if got := pushCalls(t, log); got != want {
		t.Fatalf("tool calls:\n%s\nwant:\n%s", got, want)
	}
	if strings.Join(reg.calls, ",") != "reg.io/team/etl@"+pushedDigest {
		t.Errorf("registry reads = %q, want one read at the reported digest", reg.calls)
	}
	if res.Ref != "reg.io/team/etl@"+pushedDigest {
		t.Errorf("result ref = %q, want the reported digest", res.Ref)
	}
}

// The reported digest must still name a list holding every platform.
func TestLocalDaemonRefusesAReportedDigestMissingAPlatform(t *testing.T) {
	tool, _ := pushTool{name: "docker", buildx: true, metadata: `{"containerimage.digest":"` + pushedDigest + `"}`}.fake(t)
	reg := &fakeRegistry{manifest: indexOf("linux/amd64")}
	_, err := LocalDaemon{Tool: tool, Registry: reg}.Build(context.Background(), pushRequest(false, "linux/amd64", "linux/arm64"))
	if !errors.Is(err, ErrMultiPlatform) || !strings.Contains(err.Error(), "linux/arm64") || !strings.Contains(err.Error(), pushedDigest) {
		t.Fatalf("err = %v, want ErrMultiPlatform naming the missing platform and the digest", err)
	}
}

// A push without a pin of one platform reads nothing and pins nothing.
func TestLocalDaemonBuildxPushWithoutPin(t *testing.T) {
	tool, _ := pushTool{name: "docker", buildx: true, metadata: `{"containerimage.digest":"` + pushedDigest + `"}`}.fake(t)
	reg := &fakeRegistry{}
	res, err := LocalDaemon{Tool: tool, Registry: reg}.Build(context.Background(), pushRequest(false, "linux/amd64"))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(reg.calls) != 0 || res.Ref != "" || !res.Pushed {
		t.Errorf("registry reads=%q ref=%q pushed=%v, want no read, no ref, pushed", reg.calls, res.Ref, res.Pushed)
	}
}

// A build that is not pushed stays a plain `docker build`, buildx or not:
// a buildx builder with another driver would leave nothing in the image
// store.
func TestLocalDaemonBuildOnlyIgnoresBuildx(t *testing.T) {
	tool, log := pushTool{name: "docker", buildx: true}.fake(t)
	if _, err := (LocalDaemon{Tool: tool, Registry: &fakeRegistry{}}).Build(context.Background(), request("linux/amd64")); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := pushCalls(t, log); got != "build --platform linux/amd64 -t reg.io/team/etl:v1 -f /p/Dockerfile /p" {
		t.Errorf("tool calls = %q, want a plain build", got)
	}
}

// Without buildx, docker push prints the digest it pushed on its last line;
// that is the pin.
func TestLocalDaemonPinsFromTheDockerPushOutput(t *testing.T) {
	tool, log := pushTool{name: "docker", pushOutput: "abc: Pushed\nv1: digest: " + pushedDigest + " size: 1570\n"}.fake(t)
	reg := &fakeRegistry{err: errors.New("the registry must not be read")}
	res, err := LocalDaemon{Tool: tool, Registry: reg}.Build(context.Background(), pushRequest(true, "linux/amd64"))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := "build --platform linux/amd64 -t reg.io/team/etl:v1 -f /p/Dockerfile /p\npush reg.io/team/etl:v1"
	if got := pushCalls(t, log); got != want {
		t.Fatalf("tool calls:\n%s\nwant:\n%s", got, want)
	}
	if len(reg.calls) != 0 || res.Ref != "reg.io/team/etl@"+pushedDigest {
		t.Errorf("registry reads=%q ref=%q, want no read and the digest of the push output", reg.calls, res.Ref)
	}
}

// Only a tool named docker is asked for buildx: a wrapper script given with
// --builder keeps the build and push it was written for.
func TestLocalDaemonAsksOnlyDockerForBuildx(t *testing.T) {
	tool, log := pushTool{name: "docker-wrapper.sh", buildx: true, pushOutput: "v1: digest: " + pushedDigest + " size: 9\n"}.fake(t)
	res, err := LocalDaemon{Tool: tool, Registry: &fakeRegistry{}}.Build(context.Background(), pushRequest(true, "linux/amd64"))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := "build --platform linux/amd64 -t reg.io/team/etl:v1 -f /p/Dockerfile /p\npush reg.io/team/etl:v1"
	if got := pushCalls(t, log); got != want || res.Ref != "reg.io/team/etl@"+pushedDigest {
		t.Errorf("tool calls:\n%s\nref=%q, want:\n%s\nand the digest of the push output", got, res.Ref, want)
	}
}

// The push output still reaches the caller's writer.
func TestLocalDaemonKeepsThePushOutput(t *testing.T) {
	tool, _ := pushTool{name: "docker", pushOutput: "v1: digest: " + pushedDigest + " size: 1570\n"}.fake(t)
	req := pushRequest(true, "linux/amd64")
	var out strings.Builder
	req.Stdout = &out
	if _, err := (LocalDaemon{Tool: tool, Registry: &fakeRegistry{}}).Build(context.Background(), req); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.Contains(out.String(), "digest: "+pushedDigest) {
		t.Errorf("stdout = %q, want the push output", out.String())
	}
}

// podman writes the digest of a push to --digestfile. One platform is built
// with -t and pushed as an image.
func TestLocalDaemonPinsFromThePodmanDigestFile(t *testing.T) {
	tool, log := pushTool{name: "podman", digestFile: pushedDigest}.fake(t)
	reg := &fakeRegistry{err: errors.New("the registry must not be read")}
	res, err := LocalDaemon{Tool: tool, Registry: reg}.Build(context.Background(), pushRequest(true, "linux/amd64"))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := "build --platform linux/amd64 -t reg.io/team/etl:v1 -f /p/Dockerfile /p\npush --digestfile F reg.io/team/etl:v1"
	if got := pushCalls(t, log); got != want {
		t.Fatalf("tool calls:\n%s\nwant:\n%s", got, want)
	}
	if res.Ref != "reg.io/team/etl@"+pushedDigest {
		t.Errorf("result ref = %q, want the digest file's", res.Ref)
	}
}

// Several platforms with podman go into a manifest list, built fresh (a
// list left by an earlier build would keep its old images) and pushed with
// every image it holds.
func TestLocalDaemonBuildsAPodmanManifestList(t *testing.T) {
	tool, log := pushTool{name: "podman", digestFile: pushedDigest + "\n"}.fake(t)
	reg := &fakeRegistry{manifest: indexOf("linux/amd64", "linux/arm64")}
	res, err := LocalDaemon{Tool: tool, Registry: reg}.Build(context.Background(), pushRequest(true, "linux/amd64", "linux/arm64"))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := strings.Join([]string{
		"manifest rm reg.io/team/etl:v1",
		"build --platform linux/amd64,linux/arm64 --manifest reg.io/team/etl:v1 -f /p/Dockerfile /p",
		"manifest push --all --digestfile F reg.io/team/etl:v1 docker://reg.io/team/etl:v1",
	}, "\n")
	if got := pushCalls(t, log); got != want {
		t.Fatalf("tool calls:\n%s\nwant:\n%s", got, want)
	}
	if strings.Join(reg.calls, ",") != "reg.io/team/etl@"+pushedDigest || res.Ref != "reg.io/team/etl@"+pushedDigest {
		t.Errorf("registry reads=%q ref=%q, want the reported digest read and pinned", reg.calls, res.Ref)
	}
}

// nerdctl pushes every platform with --all-platforms and reports no digest
// in a form to rely on, so the registry is read at the tag, as before.
func TestLocalDaemonNerdctlPushesAllPlatformsAndReadsTheTag(t *testing.T) {
	tool, log := pushTool{name: "nerdctl"}.fake(t)
	reg := &fakeRegistry{ref: "reg.io/team/etl@sha256:abc", manifest: indexOf("linux/amd64", "linux/arm64")}
	res, err := LocalDaemon{Tool: tool, Registry: reg}.Build(context.Background(), pushRequest(true, "linux/amd64", "linux/arm64"))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := "build --platform linux/amd64,linux/arm64 -t reg.io/team/etl:v1 -f /p/Dockerfile /p\npush --all-platforms reg.io/team/etl:v1"
	if got := pushCalls(t, log); got != want {
		t.Fatalf("tool calls:\n%s\nwant:\n%s", got, want)
	}
	if strings.Join(reg.calls, ",") != "reg.io/team/etl:v1" || res.Ref != "reg.io/team/etl@sha256:abc" {
		t.Errorf("registry reads=%q ref=%q, want the tag read and pinned", reg.calls, res.Ref)
	}
}

// A tool that reports no digest, or one that is not a sha256 digest, falls
// back to reading the tag, and says so.
func TestLocalDaemonFallsBackToTheTagWithoutAReportedDigest(t *testing.T) {
	cases := map[string]pushTool{
		"metadata without digest": {name: "docker", buildx: true, metadata: `{"image.name":"x"}`},
		"metadata not json":       {name: "docker", buildx: true, metadata: "garbage"},
		"malformed digest":        {name: "podman", digestFile: "sha256:zz"},
		"push output without one": {name: "docker", pushOutput: "v1: Pushed\n"},
	}
	for name, pt := range cases {
		t.Run(name, func(t *testing.T) {
			tool, _ := pt.fake(t)
			reg := &fakeRegistry{ref: "reg.io/team/etl@sha256:abc", manifest: singleManifest}
			req := pushRequest(true, "linux/amd64")
			var stderr strings.Builder
			req.Stderr = &stderr
			res, err := LocalDaemon{Tool: tool, Registry: reg}.Build(context.Background(), req)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if strings.Join(reg.calls, ",") != "reg.io/team/etl:v1" || res.Ref != "reg.io/team/etl@sha256:abc" {
				t.Errorf("registry reads=%q ref=%q, want the tag read and pinned", reg.calls, res.Ref)
			}
			if !strings.Contains(stderr.String(), "reported no digest") {
				t.Errorf("stderr = %q, want a note that the digest is read from the registry", stderr.String())
			}
		})
	}
}

// parsePushDigest takes the last digest line of a docker push.
func TestParsePushDigest(t *testing.T) {
	other := strings.ReplaceAll(pushedDigest, "1", "2")
	for out, want := range map[string]string{
		"v1: digest: " + pushedDigest + " size: 9\n":                                   pushedDigest,
		"a: digest: " + other + " size: 1\nv1: digest: " + pushedDigest + " size: 9\n": pushedDigest,
		"v1: digest: sha256:abc size: 9\n":                                             "",
		"":                                                                             "",
	} {
		if got := parsePushDigest(out); got != want {
			t.Errorf("parsePushDigest(%q) = %q, want %q", out, got, want)
		}
	}
}
