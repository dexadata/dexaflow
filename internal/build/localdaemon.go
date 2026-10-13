package build

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// LocalDaemon builds with a local container tool, docker, podman or nerdctl,
// by shelling out to it: `<tool> build`, `<tool> push`, and `<tool> inspect`
// for the digest. It is the last step of the resolution order, the fallback
// for when no daemonless or remote builder applies.
type LocalDaemon struct {
	// Tool is the executable to run, "docker" when empty.
	Tool string
}

// tool returns the executable, docker by default.
func (b LocalDaemon) tool() string {
	if b.Tool == "" {
		return "docker"
	}
	return b.Tool
}

// Path is PathLocalDaemon.
func (b LocalDaemon) Path() Path { return PathLocalDaemon }

// Describe names the path and the tool, such as "local-daemon (docker)".
func (b LocalDaemon) Describe() string {
	return fmt.Sprintf("%s (%s)", PathLocalDaemon, b.tool())
}

// multiPlatformHint wraps the failure of a build for several platforms, in
// the build or in verifyPlatforms. The list is passed to the tool as is,
// because docker with the containerd image store builds and pushes it as one
// manifest list; a tool that cannot fails with an error that does not say
// what to change, so this one does.
func (b LocalDaemon) multiPlatformHint(platforms []string, err error) error {
	return fmt.Errorf("%w: building for %s with %s needs a tool that pushes one image per platform under one manifest list, "+
		"such as docker with the containerd image store enabled. Otherwise build one platform: set build.platforms in dexaflow.yaml "+
		"(or pass --platform where the command has it), for example %s: %w",
		ErrMultiPlatform, strings.Join(platforms, ","), b.Describe(), platforms[0], err)
}

// Build runs the build, then the push when asked, then reads the pushed
// image's digest when a pin is asked for or several platforms were built,
// and checks that such a push holds every platform (verifyPlatforms).
func (b LocalDaemon) Build(ctx context.Context, req Request) (Result, error) {
	if req.Pin && !req.Push {
		return Result{}, errors.New("a digest pin needs a push: an image that is not in a registry has no registry digest")
	}
	res := Result{Path: PathLocalDaemon, Image: req.Image, Platforms: req.Platforms}
	if err := b.run(ctx, req.Stdout, req.Stderr, buildArgs(req.Image, req.Dockerfile, req.ContextDir, req.Platforms)...); err != nil {
		err = fmt.Errorf("building image %q with %q: %w", req.Image, b.tool(), err)
		if len(req.Platforms) > 1 {
			err = b.multiPlatformHint(req.Platforms, err)
		}
		return Result{}, err
	}
	if !req.Push {
		return res, nil
	}
	if err := b.run(ctx, req.Stdout, req.Stderr, "push", req.Image); err != nil {
		return Result{}, fmt.Errorf("pushing image %q with %q: %w", req.Image, b.tool(), err)
	}
	res.Pushed = true
	if !req.Pin && len(req.Platforms) < 2 {
		return res, nil
	}
	ref, err := b.Digest(ctx, req.Image, req.Stderr)
	if err != nil {
		return Result{}, err
	}
	if verr := b.verifyPlatforms(ctx, ref, req.Platforms, req.Stderr); verr != nil {
		return Result{}, verr
	}
	if req.Pin {
		res.Ref = ref
	}
	return res, nil
}

// verifyPlatforms checks, for a build of several platforms, that the pushed
// digest is a manifest list holding every one of them. A tool can succeed at
// build and push and still leave a partial image: podman without --manifest
// keeps only the last platform under the tag, and a push without
// --all-platforms can send part of a list. Registering that digest would run
// the wrong image, or none, on the missing platforms, so anything the check
// cannot confirm, including a tool that cannot run `manifest inspect`, fails
// the build. One platform is a plain image and is not checked.
func (b LocalDaemon) verifyPlatforms(ctx context.Context, ref string, platforms []string, stderr io.Writer) error {
	if len(platforms) < 2 {
		return nil
	}
	var out bytes.Buffer
	if err := b.run(ctx, &out, stderr, "manifest", "inspect", ref); err != nil {
		return b.multiPlatformHint(platforms, fmt.Errorf("could not check that %s lists every platform with `%s manifest inspect %s`: %w", ref, b.tool(), ref, err))
	}
	if missing := missingPlatforms(out.Bytes(), platforms); len(missing) > 0 {
		return b.multiPlatformHint(platforms, fmt.Errorf("the pushed image %s is not a manifest list holding every platform; missing %s", ref, strings.Join(missing, ",")))
	}
	return nil
}

// Digest reads the repo digest of an image the tool pushed, as a pinned
// reference. RepoDigests is set only by a push, so an image that was never
// pushed is an error, never an unpinned reference.
func (b LocalDaemon) Digest(ctx context.Context, image string, stderr io.Writer) (string, error) {
	var out bytes.Buffer
	if err := b.run(ctx, &out, stderr, inspectDigestArgs(image)...); err != nil {
		return "", fmt.Errorf("inspecting image %q with %q: %w", image, b.tool(), err)
	}
	ref, err := parseDigestRef(out.String())
	if err != nil {
		return "", fmt.Errorf("image %q: %w", image, err)
	}
	return ref, nil
}

// run executes the tool with args.
func (b LocalDaemon) run(ctx context.Context, stdout, stderr io.Writer, args ...string) error {
	//nolint:gosec // G204: the tool is the operator's choice by design (ADR 0015).
	c := exec.CommandContext(ctx, b.tool(), args...)
	c.Stdout, c.Stderr = stdout, stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("running %s %s: %w", b.tool(), args[0], err)
	}
	return nil
}

// buildArgs assembles the argv of an image build. With platforms it passes
// `--platform a,b`, so the image targets the cluster's architecture rather
// than the host's (ADR 0041).
func buildArgs(image, dockerfile, contextDir string, platforms []string) []string {
	args := []string{"build"}
	if len(platforms) > 0 {
		args = append(args, "--platform", strings.Join(platforms, ","))
	}
	return append(args, "-t", image, "-f", dockerfile, contextDir)
}

// inspectDigestArgs assembles the argv that prints a pushed image's repo
// digest (repo@sha256:...). docker, podman and nerdctl all expose RepoDigests
// through `inspect --format`.
func inspectDigestArgs(image string) []string {
	return []string{"inspect", "--format", "{{index .RepoDigests 0}}", image}
}

// parseDigestRef trims the inspect output into a pinned reference. An empty
// result means the image was never pushed; a result without an @sha256 digest
// is not a pin. Both are errors, so a deploy never registers an unpinned image.
func parseDigestRef(out string) (string, error) {
	ref := strings.TrimSpace(out)
	if ref == "" || ref == "<no value>" {
		return "", fmt.Errorf("image has no repo digest (was it pushed?)")
	}
	if !strings.Contains(ref, "@sha256:") {
		return "", fmt.Errorf("inspect returned %q, which is not a digest-pinned reference", ref)
	}
	return ref, nil
}
