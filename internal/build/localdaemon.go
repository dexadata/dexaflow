package build

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// LocalDaemon builds with a local container tool, docker, podman or nerdctl,
// by shelling out to it: `<tool> build` and `<tool> push`. The digest of what
// it pushed is read from the registry, never from the tool's image store
// (ADR 0075). It is the last step of the resolution order, the fallback for
// when no daemonless or remote builder applies.
type LocalDaemon struct {
	// Tool is the executable to run, "docker" when empty.
	Tool string
	// Registry reads the pushed digest; the registry client when nil.
	Registry Resolver
}

// registry returns the resolver, the registry client by default.
func (b LocalDaemon) registry() Resolver {
	if b.Registry == nil {
		return Registry{}
	}
	return b.Registry
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
// image's digest from the registry when a pin is asked for or several
// platforms were built, and checks that such a push holds every platform
// (verifyPlatforms).
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
	pinned, err := b.registry().Resolve(ctx, req.Image)
	if err != nil {
		err = fmt.Errorf("reading the digest of the pushed image %q: %w", req.Image, err)
		if len(req.Platforms) > 1 {
			err = b.multiPlatformHint(req.Platforms, err)
		}
		return Result{}, err
	}
	if verr := b.verifyPlatforms(pinned, req.Platforms); verr != nil {
		return Result{}, verr
	}
	if req.Pin {
		res.Ref = pinned.Ref
	}
	return res, nil
}

// verifyPlatforms checks, for a build of several platforms, that the pushed
// digest is a manifest list holding every one of them. A tool can succeed at
// build and push and still leave a partial image: podman without --manifest
// keeps only the last platform under the tag, and a push without
// --all-platforms can send part of a list. Registering that digest would run
// the wrong image, or none, on the missing platforms, so anything the check
// cannot confirm fails the build. One platform is a plain image, or an index
// docker adds attestations to, and is not checked.
func (b LocalDaemon) verifyPlatforms(pinned Pinned, platforms []string) error {
	if len(platforms) < 2 {
		return nil
	}
	if missing := missingPlatforms(pinned.Manifest, platforms); len(missing) > 0 {
		return b.multiPlatformHint(platforms, fmt.Errorf("the pushed image %s is not a manifest list holding every platform; missing %s", pinned.Ref, strings.Join(missing, ",")))
	}
	return nil
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
