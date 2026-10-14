package build

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Split describes a build of the DAG image as a dependency image plus a code
// layer (ADR 0073 section 4, ADR 0077): the dependency image holds the base,
// the system packages and the Python dependencies, and is reused for as long
// as its Dockerfile and platforms are unchanged; the code layer holds the DAG
// project files and is appended to it through the registry API.
type Split struct {
	// DepsImage is the dependency image, <repo>:deps-<key> in the DAG's own
	// repository.
	DepsImage string
	// Dockerfile is the content of the dependency image's Dockerfile. It
	// copies nothing, so it builds from an empty context.
	Dockerfile string
	// Wheels builds the dependency image without a daemon (ADR 0078), or is
	// nil when the daemon builds it from Dockerfile; then NoWheels says why.
	Wheels *DependencyLayer
	// NoWheels says why the dependency layer does not apply, for the
	// builder line; empty when Wheels is set.
	NoWheels string
}

// codeRegistry is what the code layer needs from a registry: reading a
// digest and pushing an image with a code layer appended.
type codeRegistry interface {
	Resolver
	AppendCode(ctx context.Context, base Pinned, files []LayerFile, image string) error
}

// CodeLayer builds the DAG image as Split says. With Deps set, the
// dependency image is already in the registry and no daemon runs (the
// code-layer path); without it, the dependency layer (Split.Wheels) or the
// local daemon builds and pushes the dependency image first.
type CodeLayer struct {
	// Split names the dependency image and its Dockerfile.
	Split Split
	// Deps is the dependency image found in the registry, or nil when the
	// daemon builds it.
	Deps *Pinned
	// Daemon builds the dependency image when Deps is nil.
	Daemon LocalDaemon
	// Registry reads digests and pushes the image.
	Registry codeRegistry
}

// Path is PathCodeLayer when the dependency image is reused,
// PathDependencyLayer when it is built from wheels, PathLocalDaemon when
// the daemon builds it.
func (c CodeLayer) Path() Path {
	switch {
	case c.Deps != nil:
		return PathCodeLayer
	case c.Split.Wheels != nil:
		return PathDependencyLayer
	}
	return PathLocalDaemon
}

// Describe names the path, and what builds the dependency image when it is
// built.
func (c CodeLayer) Describe() string {
	switch {
	case c.Deps != nil:
		return string(PathCodeLayer)
	case c.Split.Wheels != nil:
		return fmt.Sprintf("%s (%s), then %s", PathDependencyLayer, c.Split.Wheels.Installer.Describe(), PathCodeLayer)
	}
	return c.Daemon.Describe() + " for the dependency image, then " + string(PathCodeLayer)
}

// Build pushes the DAG image: the dependency image, built first when Deps is
// nil, with req.Code appended. The result is pinned to the digest the
// registry returns for the pushed image (ADR 0075).
func (c CodeLayer) Build(ctx context.Context, req Request) (Result, error) {
	if !req.Push {
		return Result{}, errors.New("the code layer is pushed through the registry API, so the build needs a push")
	}
	deps := c.Deps
	if deps == nil {
		built, err := c.buildDeps(ctx, req)
		if err != nil {
			return Result{}, err
		}
		deps = &built
	}
	if err := c.Registry.AppendCode(ctx, *deps, req.Code, req.Image); err != nil {
		return Result{}, fmt.Errorf("appending the DAG code to the dependency image %s: %w", deps.Ref, err)
	}
	res := Result{Path: c.Path(), Image: req.Image, Platforms: req.Platforms, Pushed: true}
	if !req.Pin {
		return res, nil
	}
	pinned, err := c.Registry.Resolve(ctx, req.Image)
	if err != nil {
		return Result{}, fmt.Errorf("reading the digest of the pushed image %q: %w", req.Image, err)
	}
	res.Ref = pinned.Ref
	return res, nil
}

// buildDeps builds and pushes the dependency image, from wheels or with the
// daemon from its Dockerfile in an empty directory, and reads back what the
// registry holds.
func (c CodeLayer) buildDeps(ctx context.Context, req Request) (Pinned, error) {
	if w := c.Split.Wheels; w != nil {
		if err := w.Push(ctx, c.Split.DepsImage, req.Platforms); err != nil {
			return Pinned{}, fmt.Errorf("building the dependency image %s from wheels: %w", c.Split.DepsImage, err)
		}
		pinned, err := c.Registry.Resolve(ctx, c.Split.DepsImage)
		if err != nil {
			return Pinned{}, fmt.Errorf("reading the dependency image %s: %w", c.Split.DepsImage, err)
		}
		return pinned, nil
	}
	dir, err := os.MkdirTemp("", "dexaflow-deps-")
	if err != nil {
		return Pinned{}, fmt.Errorf("making the context of the dependency image: %w", err)
	}
	defer os.RemoveAll(dir) //nolint:errcheck // best-effort cleanup of a temporary directory
	dockerfile := filepath.Join(dir, "Dockerfile")
	if werr := os.WriteFile(dockerfile, []byte(c.Split.Dockerfile), 0o600); werr != nil {
		return Pinned{}, fmt.Errorf("writing the Dockerfile of the dependency image: %w", werr)
	}
	res, err := c.Daemon.Build(ctx, Request{
		Image: c.Split.DepsImage, Dockerfile: dockerfile, ContextDir: dir, Platforms: req.Platforms,
		Push: true, Pin: true, Stdout: req.Stdout, Stderr: req.Stderr,
	})
	if err != nil {
		return Pinned{}, fmt.Errorf("building the dependency image %s: %w", c.Split.DepsImage, err)
	}
	pinned, err := c.Registry.Resolve(ctx, res.Ref)
	if err != nil {
		return Pinned{}, fmt.Errorf("reading the dependency image %s: %w", res.Ref, err)
	}
	return pinned, nil
}
