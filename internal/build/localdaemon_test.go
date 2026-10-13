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
)

// fakeTool writes a stand-in for docker that appends its argv to a log, one
// call per line, and answers `inspect` with inspectOut. failOn makes the call
// whose first word matches exit 1.
func fakeTool(t *testing.T, inspectOut, failOn string) (tool, log string) {
	t.Helper()
	return fakeToolWithManifest(t, inspectOut, "", failOn)
}

// indexOf is the `manifest inspect` answer for a manifest list holding one
// image per platform, plus an attestation entry like docker's.
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

// fakeToolWithManifest is fakeTool that also answers `manifest inspect` with
// manifestOut.
func fakeToolWithManifest(t *testing.T, inspectOut, manifestOut, failOn string) (tool, log string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX-only")
	}
	dir := t.TempDir()
	tool = filepath.Join(dir, "docker")
	log = filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> \"" + log + "\"\n" +
		"if [ \"$1\" = \"" + failOn + "\" ]; then echo refused >&2; exit 1; fi\n" +
		"if [ \"$1\" = inspect ]; then printf '%s\\n' '" + inspectOut + "'; fi\n" +
		"if [ \"$1\" = manifest ]; then printf '%s\\n' '" + manifestOut + "'; fi\n" +
		"exit 0\n"
	if err := os.WriteFile(tool, []byte(script), 0o700); err != nil { //nolint:gosec // a test fixture that must be executable
		t.Fatal(err)
	}
	return tool, log
}

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

// The deploy path: build for the platform, push, then read the digest the
// push produced, and hand it back in the result.
func TestLocalDaemonBuildsPushesAndPins(t *testing.T) {
	tool, log := fakeTool(t, "reg.io/team/etl@sha256:abc123", "")
	req := request("linux/arm64")
	req.Push, req.Pin = true, true

	res, err := LocalDaemon{Tool: tool}.Build(context.Background(), req)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := []string{
		"build --platform linux/arm64 -t reg.io/team/etl:v1 -f /p/Dockerfile /p",
		"push reg.io/team/etl:v1",
		"inspect --format {{index .RepoDigests 0}} reg.io/team/etl:v1",
	}
	if got := calls(t, log); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("tool calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
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

// compile --build --push asks for no pin: nothing is inspected, exactly as
// before the seam, and the result carries no digest.
func TestLocalDaemonPushWithoutPinDoesNotInspect(t *testing.T) {
	tool, log := fakeTool(t, "", "")
	req := request("linux/amd64")
	req.Push = true

	res, err := LocalDaemon{Tool: tool}.Build(context.Background(), req)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	got := calls(t, log)
	if len(got) != 2 || !strings.HasPrefix(got[0], "build ") || got[1] != "push reg.io/team/etl:v1" {
		t.Fatalf("tool calls = %q, want build then push only", got)
	}
	if res.Ref != "" || res.Digest() != "" || !res.Pushed {
		t.Errorf("result ref=%q pushed=%v, want no reference and pushed", res.Ref, res.Pushed)
	}
}

func TestLocalDaemonBuildOnly(t *testing.T) {
	tool, log := fakeTool(t, "", "")
	res, err := LocalDaemon{Tool: tool}.Build(context.Background(), request())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	got := calls(t, log)
	if len(got) != 1 || got[0] != "build -t reg.io/team/etl:v1 -f /p/Dockerfile /p" {
		t.Fatalf("tool calls = %q, want one build without --platform", got)
	}
	if res.Pushed || res.Ref != "" {
		t.Errorf("result pushed=%v ref=%q, want neither", res.Pushed, res.Ref)
	}
}

// A pin is a promise that the image is pushed; asking for one without a push
// is a caller bug, refused before anything runs.
func TestLocalDaemonRefusesAPinWithoutAPush(t *testing.T) {
	tool, log := fakeTool(t, "", "")
	req := request()
	req.Pin = true
	if _, err := (LocalDaemon{Tool: tool}).Build(context.Background(), req); err == nil {
		t.Fatal("a pin without a push must be refused")
	}
	if calls(t, log) != nil {
		t.Error("the tool ran for a refused request")
	}
}

// Several platforms reach the tool as one list: docker with the containerd
// image store builds and pushes them as one manifest list. After the push the
// pinned digest is checked to be a list covering every platform.
func TestLocalDaemonPushesAndVerifiesSeveralPlatforms(t *testing.T) {
	tool, log := fakeToolWithManifest(t, "reg.io/team/etl@sha256:abc", indexOf("linux/amd64", "linux/arm64/v8"), "")
	req := request("linux/amd64", "linux/arm64")
	req.Push, req.Pin = true, true
	res, err := LocalDaemon{Tool: tool}.Build(context.Background(), req)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := []string{
		"build --platform linux/amd64,linux/arm64 -t reg.io/team/etl:v1 -f /p/Dockerfile /p",
		"push reg.io/team/etl:v1",
		"inspect --format {{index .RepoDigests 0}} reg.io/team/etl:v1",
		"manifest inspect reg.io/team/etl@sha256:abc",
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
	tool, log := fakeToolWithManifest(t, "reg.io/team/etl@sha256:abc", indexOf("linux/amd64", "linux/arm64"), "")
	req := request("linux/amd64", "linux/arm64")
	req.Push = true
	res, err := LocalDaemon{Tool: tool}.Build(context.Background(), req)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := calls(t, log); len(got) != 4 || got[3] != "manifest inspect reg.io/team/etl@sha256:abc" {
		t.Errorf("tool calls = %q, want the push verified", got)
	}
	if res.Ref != "" {
		t.Errorf("result ref = %q, want none without a pin", res.Ref)
	}
}

// The silent wrong image: a tool that builds only the last platform under the
// tag (podman without --manifest), or pushes part of a list, succeeds at build
// and push. The check refuses what does not cover every requested platform,
// and refuses when it cannot check at all.
func TestLocalDaemonRefusesAPushThatDoesNotCoverThePlatforms(t *testing.T) {
	single := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{},"layers":[]}`
	cases := []struct{ name, manifest, failOn, missing string }{
		{"single image", single, "", "linux/amd64"},
		{"partial list", indexOf("linux/arm64"), "", "linux/amd64"},
		{"variant mismatch", indexOf("linux/amd64", "linux/arm64/v7"), "", "linux/arm64/v8"},
		{"not json", "garbage", "", "linux/amd64"},
		{"inspect refused", "", "manifest", "manifest inspect"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tool, _ := fakeToolWithManifest(t, "reg.io/team/etl@sha256:abc", c.manifest, c.failOn)
			req := request("linux/amd64", "linux/arm64/v8")
			req.Push, req.Pin = true, true
			_, err := LocalDaemon{Tool: tool}.Build(context.Background(), req)
			if !errors.Is(err, ErrMultiPlatform) {
				t.Fatalf("err = %v, want ErrMultiPlatform", err)
			}
			for _, want := range []string{c.missing, "reg.io/team/etl@sha256:abc", "build.platforms"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not say %q", err, want)
				}
			}
		})
	}
}

// One platform is never checked: a plain image is the right answer.
func TestLocalDaemonDoesNotVerifyOnePlatform(t *testing.T) {
	tool, log := fakeToolWithManifest(t, "reg.io/team/etl@sha256:abc", "", "manifest")
	req := request("linux/amd64")
	req.Push, req.Pin = true, true
	if _, err := (LocalDaemon{Tool: tool}).Build(context.Background(), req); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := calls(t, log); len(got) != 3 {
		t.Errorf("tool calls = %q, want build, push and inspect only", got)
	}
}

// When a multi-platform build fails, the tool's error is kept and a hint says
// what the tool needs and how to build one platform instead.
func TestLocalDaemonHintsWhenAMultiPlatformBuildFails(t *testing.T) {
	tool, _ := fakeTool(t, "", "build")
	_, err := LocalDaemon{Tool: tool}.Build(context.Background(), request("linux/amd64", "linux/arm64"))
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
	tool, _ := fakeTool(t, "", "build")
	_, err := LocalDaemon{Tool: tool}.Build(context.Background(), request("linux/amd64"))
	if err == nil || errors.Is(err, ErrMultiPlatform) {
		t.Fatalf("err = %v, want a plain build failure", err)
	}
}

func TestLocalDaemonNamesTheFailingStep(t *testing.T) {
	for _, step := range []string{"build", "push", "inspect"} {
		t.Run(step, func(t *testing.T) {
			tool, _ := fakeTool(t, "reg.io/team/etl@sha256:abc", step)
			req := request()
			req.Push, req.Pin = true, true
			_, err := LocalDaemon{Tool: tool}.Build(context.Background(), req)
			if err == nil || !strings.Contains(err.Error(), tool) || !strings.Contains(err.Error(), "reg.io/team/etl:v1") {
				t.Fatalf("err = %v, want the tool and the image named", err)
			}
		})
	}
}

// An inspect that answers with no digest (the image was never pushed) or a
// tag is not a pin: a deploy must never register an unpinned image.
func TestLocalDaemonRefusesAnUnpinnedInspect(t *testing.T) {
	for _, out := range []string{"", "<no value>", "reg.io/team/etl:v1"} {
		tool, _ := fakeTool(t, out, "")
		req := request()
		req.Push, req.Pin = true, true
		if _, err := (LocalDaemon{Tool: tool}).Build(context.Background(), req); err == nil {
			t.Errorf("inspect output %q accepted as a pin", out)
		}
	}
}

// Digest resolves an image built elsewhere (deploy --skip-build).
func TestLocalDaemonDigest(t *testing.T) {
	tool, log := fakeTool(t, "reg.io/team/etl@sha256:feed", "")
	ref, err := LocalDaemon{Tool: tool}.Digest(context.Background(), "reg.io/team/etl:v1", &bytes.Buffer{})
	if err != nil || ref != "reg.io/team/etl@sha256:feed" {
		t.Fatalf("Digest = %q, %v", ref, err)
	}
	if got := calls(t, log); len(got) != 1 || !strings.HasPrefix(got[0], "inspect ") {
		t.Errorf("tool calls = %q, want one inspect", got)
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
