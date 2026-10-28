package build

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
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
	// DaemonDepsImage is the dependency image the daemon builds, under the
	// key of its own builds (ADR 0077), when Wheels fails before anything is
	// pushed; empty when the daemon's key cannot cover the dependencies
	// (they are not pinned), and then the failure stands.
	DaemonDepsImage string
	// NoWheels says why the dependency layer does not apply, for the
	// builder line; empty when Wheels is set.
	NoWheels string
	// Note is said on the builder line after the reason, such as how long
	// the resolution of the dependencies took; empty when there is nothing
	// to say.
	Note string
}

// codeRegistry is what the code layer needs from a registry: reading a
// digest and pushing an image with a code layer appended.
type codeRegistry interface {
	Resolver
	AppendCode(ctx context.Context, base Pinned, files []LayerFile, image string) error
}

// CodeLayer builds the DAG image as Split says. With Deps set, the
// dependency image is already in the registry and no daemon runs (the
// code-layer path); without it, the dependency layer (Split.Wheels), the
// remote builder or the local daemon builds and pushes the dependency image
// first.
type CodeLayer struct {
	// Split names the dependency image and its Dockerfile.
	Split Split
	// Deps is the dependency image found in the registry, or nil when it is
	// built first.
	Deps *Pinned
	// Daemon builds the dependency image when Deps is nil and neither the
	// wheels nor Remote do.
	Daemon LocalDaemon
	// Remote builds the dependency image in the daemon's place when it is
	// set (path 4): when the wheels do not apply, or fail.
	Remote Remote
	// Registry reads digests and pushes the image.
	Registry codeRegistry
}

// Path is PathCodeLayer when the dependency image is reused,
// PathDependencyLayer when it is built from wheels, PathRemote when the
// remote builder builds it, PathLocalDaemon when the daemon does.
func (c CodeLayer) Path() Path {
	switch {
	case c.Deps != nil:
		return PathCodeLayer
	case c.Split.Wheels != nil:
		return PathDependencyLayer
	}
	return c.depsBuilder().Path()
}

// depsBuilder is the builder of the dependency image when no wheels build
// it: the remote builder when there is one, the daemon otherwise.
func (c CodeLayer) depsBuilder() Builder {
	if c.Remote != nil {
		return c.Remote
	}
	return c.Daemon
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
	return c.depsBuilder().Describe() + " for the dependency image, then " + string(PathCodeLayer)
}

// Build pushes the DAG image: the dependency image, built first when Deps is
// nil, with req.Code appended. The result is pinned to the digest the
// registry returns for the pushed image (ADR 0075).
func (c CodeLayer) Build(ctx context.Context, req Request) (Result, error) {
	if !req.Push {
		return Result{}, errors.New("the code layer is pushed through the registry API, so the build needs a push")
	}
	deps, path := c.Deps, c.Path()
	if deps == nil {
		built, by, err := c.buildDeps(ctx, req)
		if err != nil {
			return Result{}, err
		}
		deps, path = &built, by
	}
	if err := c.Registry.AppendCode(ctx, *deps, req.Code, req.Image); err != nil {
		return Result{}, fmt.Errorf("appending the DAG code to the dependency image %s: %w", deps.Ref, err)
	}
	res := Result{Path: path, Image: req.Image, Platforms: req.Platforms, Pushed: true}
	if !req.Pin {
		return res, nil
	}
	pinned, err := c.Registry.Resolve(ctx, req.Image)
	if err != nil {
		return res, fmt.Errorf("%w: reading the digest of the pushed image %q: %w", ErrPinRead, req.Image, err)
	}
	res.Ref = pinned.Ref
	return res, nil
}

// buildDeps builds and pushes the dependency image, from wheels, with the
// remote builder or with the daemon, reads back what the registry holds, and
// says which path built it. When the wheels fail, the remote builder, or the
// daemon when its tool is on PATH, builds the dependency image under
// Split.DaemonDepsImage, if there is one; otherwise the wheel failure is the
// error.
func (c CodeLayer) buildDeps(ctx context.Context, req Request) (Pinned, Path, error) {
	w := c.Split.Wheels
	if w == nil {
		pinned, err := c.daemonDeps(ctx, req, c.Split.DepsImage)
		return pinned, c.depsBuilder().Path(), err
	}
	pinned, err := c.wheelDeps(ctx, req, w)
	if err == nil {
		return pinned, PathDependencyLayer, nil
	}
	if c.Split.DaemonDepsImage == "" {
		return Pinned{}, "", err
	}
	if cerr := ctx.Err(); cerr != nil {
		return Pinned{}, "", fmt.Errorf("%w (the build was stopped: %w)", err, cerr)
	}
	return c.daemonFallback(ctx, req, err)
}

// daemonFallback gets the dependency image of the daemon's key after the
// wheels failed with why: the one the registry already holds, or one the
// remote builder builds, or the daemon when its tool is on PATH; otherwise
// why is the error.
func (c CodeLayer) daemonFallback(ctx context.Context, req Request, why error) (Pinned, Path, error) {
	image := c.Split.DaemonDepsImage
	if found, _, _ := reusable(ctx, c.Registry, image, req.Platforms, "dependency image "+image); found != nil { //nolint:errcheck // any read failure means it is not reused
		c.note(req, "%v\nthe registry already holds the dependency image %s, so the DAG code is appended to it\n", why, image)
		return *found, PathCodeLayer, nil
	}
	by := "the daemon"
	if c.Remote != nil {
		by = "the remote builder"
	} else if _, lerr := exec.LookPath(c.Daemon.tool()); lerr != nil {
		return Pinned{}, "", why
	}
	c.note(req, "%v\n%s builds the dependency image %s instead\n", why, by, image)
	pinned, err := c.daemonDeps(ctx, req, image)
	return pinned, c.depsBuilder().Path(), err
}

// note writes a progress note to the build's standard error, if it has one.
func (CodeLayer) note(req Request, format string, args ...any) {
	if req.Stderr != nil {
		fmt.Fprintf(req.Stderr, format, args...) //nolint:errcheck // a progress note
	}
}

// wheelDeps pushes the dependency image built from wheels and reads it back.
func (c CodeLayer) wheelDeps(ctx context.Context, req Request, w *DependencyLayer) (Pinned, error) {
	if err := w.Push(ctx, c.Split.DepsImage, req.Platforms); err != nil {
		return Pinned{}, fmt.Errorf("building the dependency image %s from wheels: %w", c.Split.DepsImage, err)
	}
	pinned, err := c.Registry.Resolve(ctx, c.Split.DepsImage)
	if err != nil {
		return Pinned{}, fmt.Errorf("reading the dependency image %s: %w", c.Split.DepsImage, err)
	}
	return pinned, nil
}

// depsRequest is the request that builds and pushes the dependency image
// under image from dockerfile in the empty directory dir, for the build
// req. It keeps the secrets and environment of req, since the generated
// Dockerfile mounts the package index secret for its pip install, and drops
// its build args, which no generated Dockerfile reads.
func depsRequest(req Request, image, dockerfile, dir string) Request {
	return Request{
		Image: image, Dockerfile: dockerfile, ContextDir: dir, ContextFiles: []LayerFile{}, Platforms: req.Platforms,
		Push: true, Pin: true, Secrets: req.Secrets, Env: req.Env, Stdout: req.Stdout, Stderr: req.Stderr,
	}
}

// daemonDeps has the daemon, or the remote builder in its place, build and
// push the dependency image under image, from its Dockerfile in an empty
// directory (an empty listed context for the remote builder), and reads it
// back.
func (c CodeLayer) daemonDeps(ctx context.Context, req Request, image string) (Pinned, error) {
	dir, err := os.MkdirTemp("", "dexaflow-deps-")
	if err != nil {
		return Pinned{}, fmt.Errorf("making the context of the dependency image: %w", err)
	}
	defer os.RemoveAll(dir) //nolint:errcheck // best-effort cleanup of a temporary directory
	dockerfile := filepath.Join(dir, "Dockerfile")
	if werr := os.WriteFile(dockerfile, []byte(c.Split.Dockerfile), 0o600); werr != nil {
		return Pinned{}, fmt.Errorf("writing the Dockerfile of the dependency image: %w", werr)
	}
	res, err := c.depsBuilder().Build(ctx, depsRequest(req, image, dockerfile, dir))
	if err != nil {
		return Pinned{}, fmt.Errorf("building the dependency image %s: %w", image, err)
	}
	pinned, err := c.Registry.Resolve(ctx, res.Ref)
	if err != nil {
		return Pinned{}, fmt.Errorf("reading the dependency image %s: %w", res.Ref, err)
	}
	return pinned, nil
}
