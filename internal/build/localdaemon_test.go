package build

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeTool writes a stand-in for docker that appends its argv to a log, one
// call per line. failOn makes the call whose first word matches exit 1.
func fakeTool(t *testing.T, failOn string) (tool, log string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX-only")
	}
	dir := t.TempDir()
	tool = filepath.Join(dir, "docker")
	log = filepath.Join(dir, "calls.log")
	// No buildx: these tests drive the classic build-then-push argv, and a
	// push whose output names no digest, so the registry is read by tag.
	script := "#!/bin/sh\n" +
		"if [ \"$1 $2\" = \"buildx version\" ]; then exit 1; fi\n" +
		"echo \"$*\" >> \"" + log + "\"\n" +
		"if [ \"$1\" = \"" + failOn + "\" ]; then echo refused >&2; exit 1; fi\n" +
		"exit 0\n"
	if err := os.WriteFile(tool, []byte(script), 0o700); err != nil { //nolint:gosec // a test fixture that must be executable
		t.Fatal(err)
	}
	return tool, log
}

// indexOf is the manifest of an index holding one image per platform, plus
// an attestation entry like docker's.
func indexOf(platforms ...string) string {
	entries := make([]string, 0, len(platforms)+1)
	for _, p := range platforms {
		parts := strings.SplitN(p, "/", 3)
		e := `{"digest":"sha256:x","platform":{"os":"` + parts[0] + `","architecture":"` + parts[1] + `"`
		if len(parts) == 3 {
			e += `,"variant":"` + parts[2] + `"`
		}
		entries = append(entries, e+`}}`)
	}
	entries = append(entries, `{"digest":"sha256:att","platform":{"os":"unknown","architecture":"unknown"}}`)
	return `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[` + strings.Join(entries, ",") + `]}`
}

// fakeRegistry answers Resolve with a fixed pin, or err, and counts calls.
type fakeRegistry struct {
	ref, manifest string
	err           error
	calls         []string
}

func (f *fakeRegistry) Resolve(_ context.Context, image string) (Pinned, error) {
	f.calls = append(f.calls, image)
	if f.err != nil {
		return Pinned{}, f.err
	}
	return Pinned{Ref: f.ref, Manifest: []byte(f.manifest)}, nil
}

const singleManifest = `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{},"layers":[]}`

// calls returns the logged argv lines, or nil when the tool never ran.
func calls(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log) //nolint:gosec // a path this test created
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func request(platforms ...string) Request {
	return Request{
		Image:      "reg.io/team/etl:v1",
		Dockerfile: "/p/Dockerfile",
		ContextDir: "/p",
		Platforms:  platforms,
		Stdout:     &bytes.Buffer{},
		Stderr:     &bytes.Buffer{},
	}
}

// The deploy path: build for the platform, push, then read the digest of
// what the push left in the registry, and hand it back in the result. The
// tool is never asked for the digest (ADR 0075).
func TestLocalDaemonBuildsPushesAndPins(t *testing.T) {
	tool, log := fakeTool(t, "")
	reg := &fakeRegistry{ref: "reg.io/team/etl@sha256:abc123", manifest: singleManifest}
	req := request("linux/arm64")
	req.Push, req.Pin = true, true

	res, err := LocalDaemon{Tool: tool, Registry: reg}.Build(context.Background(), req)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := []string{
		"build --platform linux/arm64 -t reg.io/team/etl:v1 -f /p/Dockerfile /p",
		"push reg.io/team/etl:v1",
	}
	if got := calls(t, log); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("tool calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if strings.Join(reg.calls, ",") != "reg.io/team/etl:v1" {
		t.Errorf("registry reads = %q, want the pushed tag once", reg.calls)
	}
	if res.Path != PathLocalDaemon || !res.Pushed {
		t.Errorf("result path=%q pushed=%v, want %q and pushed", res.Path, res.Pushed, PathLocalDaemon)
	}
	if res.Ref != "reg.io/team/etl@sha256:abc123" || res.Digest() != "sha256:abc123" {
		t.Errorf("result ref=%q digest=%q, want the pinned reference and its digest", res.Ref, res.Digest())
	}
	if res.Image != req.Image || strings.Join(res.Platforms, ",") != "linux/arm64" {
		t.Errorf("result image=%q platforms=%v", res.Image, res.Platforms)
	}
}

// A push that asks for no pin reads no registry, and the
// result carries no digest.
func TestLocalDaemonPushWithoutPinDoesNotReadTheRegistry(t *testing.T) {
	tool, log := fakeTool(t, "")
	reg := &fakeRegistry{}
	req := request("linux/amd64")
	req.Push = true

	res, err := LocalDaemon{Tool: tool, Registry: reg}.Build(context.Background(), req)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	got := calls(t, log)
	if len(got) != 2 || !strings.HasPrefix(got[0], "build ") || got[1] != "push reg.io/team/etl:v1" {
		t.Fatalf("tool calls = %q, want build then push only", got)
	}
	if len(reg.calls) != 0 {
		t.Errorf("registry reads = %q, want none", reg.calls)
	}
	if res.Ref != "" || res.Digest() != "" || !res.Pushed {
		t.Errorf("result ref=%q pushed=%v, want no reference and pushed", res.Ref, res.Pushed)
	}
}

func TestLocalDaemonBuildOnly(t *testing.T) {
	tool, log := fakeTool(t, "")
	reg := &fakeRegistry{}
	res, err := LocalDaemon{Tool: tool, Registry: reg}.Build(context.Background(), request())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	got := calls(t, log)
	if len(got) != 1 || got[0] != "build -t reg.io/team/etl:v1 -f /p/Dockerfile /p" {
		t.Fatalf("tool calls = %q, want one build without --platform", got)
	}
	if res.Pushed || res.Ref != "" || len(reg.calls) != 0 {
		t.Errorf("result pushed=%v ref=%q registry reads=%q, want none", res.Pushed, res.Ref, reg.calls)
	}
}

// A pin is a promise that the image is pushed; asking for one without a push
// is a caller bug, refused before anything runs.
func TestLocalDaemonRefusesAPinWithoutAPush(t *testing.T) {
	tool, log := fakeTool(t, "")
	req := request()
	req.Pin = true
	if _, err := (LocalDaemon{Tool: tool, Registry: &fakeRegistry{}}).Build(context.Background(), req); err == nil {
		t.Fatal("a pin without a push must be refused")
	}
	if calls(t, log) != nil {
		t.Error("the tool ran for a refused request")
	}
}

// Several platforms reach the tool as one list: docker with the containerd
// image store builds and pushes them as one manifest list. After the push the
// manifest the registry holds is checked to be a list covering every
// platform.
func TestLocalDaemonPushesAndVerifiesSeveralPlatforms(t *testing.T) {
	tool, log := fakeTool(t, "")
	reg := &fakeRegistry{ref: "reg.io/team/etl@sha256:abc", manifest: indexOf("linux/amd64", "linux/arm64/v8")}
	req := request("linux/amd64", "linux/arm64")
	req.Push, req.Pin = true, true
	res, err := LocalDaemon{Tool: tool, Registry: reg}.Build(context.Background(), req)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := []string{
		"build --platform linux/amd64,linux/arm64 -t reg.io/team/etl:v1 -f /p/Dockerfile /p",
		"push reg.io/team/etl:v1",
	}
	if got := calls(t, log); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("tool calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if res.Ref != "reg.io/team/etl@sha256:abc" {
		t.Errorf("result ref = %q", res.Ref)
	}
}

// compile --push asks for no pin, but a multi-platform push is still checked:
// a partial image under the tag is wrong whoever reads it next.
func TestLocalDaemonVerifiesAMultiPlatformPushWithoutAPin(t *testing.T) {
	tool, _ := fakeTool(t, "")
	reg := &fakeRegistry{ref: "reg.io/team/etl@sha256:abc", manifest: indexOf("linux/amd64", "linux/arm64")}
	req := request("linux/amd64", "linux/arm64")
	req.Push = true
	res, err := LocalDaemon{Tool: tool, Registry: reg}.Build(context.Background(), req)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(reg.calls) != 1 {
		t.Errorf("registry reads = %q, want the push verified", reg.calls)
	}
	if res.Ref != "" {
		t.Errorf("result ref = %q, want none without a pin", res.Ref)
	}
}

// The silent wrong image: a tool that builds only the last platform under the
// tag (podman without --manifest), or pushes part of a list, succeeds at build
// and push. The check refuses what does not cover every requested platform,
// and refuses when it cannot read the registry at all.
func TestLocalDaemonRefusesAPushThatDoesNotCoverThePlatforms(t *testing.T) {
	cases := []struct {
		name, manifest string
		err            error
		missing        string
	}{
		{"single image", singleManifest, nil, "linux/amd64"},
		{"partial list", indexOf("linux/arm64"), nil, "linux/amd64"},
		{"variant mismatch", indexOf("linux/amd64", "linux/arm64/v7"), nil, "linux/arm64/v8"},
		{"not json", "garbage", nil, "linux/amd64"},
		{"registry unreadable", "", errors.New("401 unauthorized"), "401 unauthorized"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tool, _ := fakeTool(t, "")
			reg := &fakeRegistry{ref: "reg.io/team/etl@sha256:abc", manifest: c.manifest, err: c.err}
			req := request("linux/amd64", "linux/arm64/v8")
			req.Push, req.Pin = true, true
			_, err := LocalDaemon{Tool: tool, Registry: reg}.Build(context.Background(), req)
			if !errors.Is(err, ErrMultiPlatform) {
				t.Fatalf("err = %v, want ErrMultiPlatform", err)
			}
			for _, want := range []string{c.missing, "reg.io/team/etl", "build.platforms"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not say %q", err, want)
				}
			}
		})
	}
}

// One platform is never checked: a plain image is the right answer, and an
// index with attestations is too.
func TestLocalDaemonDoesNotVerifyOnePlatform(t *testing.T) {
	for _, manifest := range []string{singleManifest, indexOf("linux/arm64")} {
		tool, _ := fakeTool(t, "")
		reg := &fakeRegistry{ref: "reg.io/team/etl@sha256:abc", manifest: manifest}
		req := request("linux/amd64")
		req.Push, req.Pin = true, true
		if _, err := (LocalDaemon{Tool: tool, Registry: reg}).Build(context.Background(), req); err != nil {
			t.Fatalf("Build with manifest %s: %v", manifest, err)
		}
	}
}

// When a multi-platform build fails, the tool's error is kept and a hint says
// what the tool needs and how to build one platform instead.
func TestLocalDaemonHintsWhenAMultiPlatformBuildFails(t *testing.T) {
	tool, _ := fakeTool(t, "build")
	_, err := LocalDaemon{Tool: tool, Registry: &fakeRegistry{}}.Build(context.Background(), request("linux/amd64", "linux/arm64"))
	if !errors.Is(err, ErrMultiPlatform) {
		t.Fatalf("err = %v, want ErrMultiPlatform", err)
	}
	for _, want := range []string{"linux/amd64,linux/arm64", "build.platforms", "containerd image store", "running " + tool + " build"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not say %q", err, want)
		}
	}
}

// A one-platform failure is the tool's error alone: the hint would mislead.
func TestLocalDaemonGivesNoMultiPlatformHintForOnePlatform(t *testing.T) {
	tool, _ := fakeTool(t, "build")
	_, err := LocalDaemon{Tool: tool, Registry: &fakeRegistry{}}.Build(context.Background(), request("linux/amd64"))
	if err == nil || errors.Is(err, ErrMultiPlatform) {
		t.Fatalf("err = %v, want a plain build failure", err)
	}
}

func TestLocalDaemonNamesTheFailingStep(t *testing.T) {
	for _, step := range []string{"build", "push"} {
		t.Run(step, func(t *testing.T) {
			tool, _ := fakeTool(t, step)
			req := request()
			req.Push, req.Pin = true, true
			_, err := LocalDaemon{Tool: tool, Registry: &fakeRegistry{ref: "reg.io/team/etl@sha256:abc"}}.Build(context.Background(), req)
			if err == nil || !strings.Contains(err.Error(), tool) || !strings.Contains(err.Error(), "reg.io/team/etl:v1") {
				t.Fatalf("err = %v, want the tool and the image named", err)
			}
		})
	}
}

// A registry that cannot be read after a push fails the pin, naming the
// image: a deploy must never register an unpinned image.
func TestLocalDaemonFailsWhenTheRegistryCannotPin(t *testing.T) {
	tool, _ := fakeTool(t, "")
	req := request()
	req.Push, req.Pin = true, true
	res, err := LocalDaemon{Tool: tool, Registry: &fakeRegistry{err: errors.New("manifest unknown")}}.Build(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "manifest unknown") || !strings.Contains(err.Error(), "reg.io/team/etl:v1") {
		t.Fatalf("err = %v, want the registry failure and the image named", err)
	}
	// The push happened: the error says only the read failed, and the
	// result names what was pushed, for a caller that can keep the tag.
	if !errors.Is(err, ErrPinRead) {
		t.Errorf("err = %v, want ErrPinRead", err)
	}
	if !res.Pushed || res.Image != req.Image || res.Ref != "" {
		t.Errorf("result = %+v, want the pushed tag, unpinned", res)
	}
}

// A multi-platform push whose read fails cannot be verified, which is not a
// pin read the caller may skip.
func TestLocalDaemonUnverifiedMultiPlatformPushIsNotAPinRead(t *testing.T) {
	tool, _ := fakeTool(t, "")
	req := request("linux/amd64", "linux/arm64")
	req.Push, req.Pin = true, true
	_, err := LocalDaemon{Tool: tool, Registry: &fakeRegistry{err: errors.New("manifest unknown")}}.Build(context.Background(), req)
	if err == nil || errors.Is(err, ErrPinRead) {
		t.Fatalf("err = %v, want a failure that is not ErrPinRead", err)
	}
}

// Without a Registry the local daemon reads through the real client.
func TestLocalDaemonDefaultsToTheRegistryClient(t *testing.T) {
	if _, ok := (LocalDaemon{}).registry().(Registry); !ok {
		t.Error("the default resolver is not the registry client")
	}
}

func TestLocalDaemonDescribe(t *testing.T) {
	b := LocalDaemon{Tool: "podman"}
	if b.Path() != PathLocalDaemon || b.Describe() != "local-daemon (podman)" {
		t.Errorf("Path=%q Describe=%q", b.Path(), b.Describe())
	}
}

func TestResultDigestOfAnEmptyResult(t *testing.T) {
	if d := (Result{}).Digest(); d != "" {
		t.Errorf("Digest of an unpinned result = %q, want empty", d)
	}
}

// A failed push names the command that signs the tool in to the registry:
// a push the daemonless paths make with a cloud session still needs the
// tool's own login when the daemon builds.
func TestLocalDaemonNamesTheLoginWhenThePushFails(t *testing.T) {
	tool, _ := fakeTool(t, "push")
	req := request()
	req.Image = "123456789012.dkr.ecr.eu-west-1.amazonaws.com/team/etl:v1"
	req.Push = true
	_, err := LocalDaemon{Tool: tool, Registry: &fakeRegistry{}}.Build(context.Background(), req)
	want := "aws ecr get-login-password --region eu-west-1 | docker login --username AWS --password-stdin 123456789012.dkr.ecr.eu-west-1.amazonaws.com"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want the login command %q", err, want)
	}
}

// A stopped build (Ctrl-C reaching the CLI, #1061) interrupts the tool, the
// signal `docker build` handles by canceling the build cleanly, rather than
// killing it outright.
func TestLocalDaemonInterruptsTheToolWhenStopped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX-only")
	}
	dir := t.TempDir()
	tool := filepath.Join(dir, "docker")
	started, interrupted := filepath.Join(dir, "started"), filepath.Join(dir, "interrupted")
	script := "#!/bin/sh\n" +
		"trap ': > \"" + interrupted + "\"; exit 130' INT\n" +
		": > \"" + started + "\"\n" +
		"while :; do sleep 0.05; done\n"
	if err := os.WriteFile(tool, []byte(script), 0o700); err != nil { //nolint:gosec // a test fixture that must be executable
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := LocalDaemon{Tool: tool}.Build(ctx, request())
		done <- err
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the tool never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a stopped build reported success")
		}
	case <-time.After(localDaemonWaitDelay + 5*time.Second):
		t.Fatal("the build did not return after the stop")
	}
	if _, err := os.Stat(interrupted); err != nil {
		t.Errorf("the tool was not sent an interrupt before it ended: %v", err)
	}
}
