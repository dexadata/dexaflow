// Package build produces the container image of a DAG project and reports
// what it pushed.
//
// A Builder is one way of producing that image. ADR 0073 decides the order
// builders are tried in (ResolutionOrder): skip the build when the dependency
// image already exists, append a code layer, pack a daemonless dependency
// layer, use a remote builder, and only then fall back to a local container
// daemon. Three exist today: Existing, the skip path in its first form (the
// whole image for this content label is already in the registry, see Select);
// CodeLayer, which appends the DAG code to a dependency image the registry
// holds, or has the daemon build that dependency image first (ADR 0077); and
// LocalDaemon. Every builder returns a Result that carries the digest of the image it
// pushed, so the caller pins dag.json to exactly those bytes (ADR 0003,
// ADR 0041).
//
// Builders run out of process or through a registry API; none links the
// Docker SDK (ADR 0015).
package build

import (
	"context"
	"errors"
	"io"
	"strings"
)

// Path names one step of the builder resolution order.
type Path string

// The builder resolution order of ADR 0073, first to last.
const (
	// PathSkip reuses the whole image of the content label already in the
	// registry (ADR 0076). A cached dependency image is reused by
	// PathCodeLayer (ADR 0077).
	PathSkip Path = "skip"
	// PathCodeLayer appends the DAG code as a layer to a cached dependency
	// image through the registry API.
	PathCodeLayer Path = "code-layer"
	// PathDependencyLayer installs wheels for the target platform without a
	// daemon and packs them as a layer on the runtime base.
	PathDependencyLayer Path = "dependency-layer"
	// PathRemote builds in a remote builder: an in-cluster build pod or a
	// cloud builder.
	PathRemote Path = "remote"
	// PathLocalDaemon shells out to a local container tool (docker, podman,
	// nerdctl). It is the fallback, never the default.
	PathLocalDaemon Path = "local-daemon"
)

// ResolutionOrder returns the builder paths in the order the CLI tries them.
func ResolutionOrder() []Path {
	return []Path{PathSkip, PathCodeLayer, PathDependencyLayer, PathRemote, PathLocalDaemon}
}

// ErrMultiPlatform marks the failure of a build for several platforms; the
// error wraps the builder's own failure and says how to build one platform.
var ErrMultiPlatform = errors.New("the multi-platform build failed")

// Request is one image to produce.
type Request struct {
	// Image is the tag reference to build, <registry>/<name>:<tag>.
	Image string
	// Dockerfile is the path of the Dockerfile the build reads.
	Dockerfile string
	// ContextDir is the build context, the DAG project directory.
	ContextDir string
	// Platforms are the os/arch targets; empty means the builder's default.
	Platforms []string
	// Push pushes the image to its registry after the build.
	Push bool
	// Pin asks for the digest of the pushed image in Result.Ref. It needs
	// Push: an image that is not pushed has no registry digest.
	Pin bool
	// Code is the DAG project files of the code layer, for a split build
	// (Split); other builders read ContextDir.
	Code []LayerFile
	// Stdout and Stderr receive the builder's own output.
	Stdout io.Writer
	Stderr io.Writer
}

// Result is what a builder produced.
type Result struct {
	// Path is the resolution step that produced the image.
	Path Path
	// Image is the tag reference that was built.
	Image string
	// Platforms are the platforms the image was built for.
	Platforms []string
	// Pushed reports that the image is in its registry.
	Pushed bool
	// Ref is the digest-pinned reference, <repo>@sha256:<hex>, when the
	// request asked for a pin; empty otherwise.
	Ref string
}

// Digest returns the sha256:<hex> part of Ref, or "" when the result is not
// pinned.
func (r Result) Digest() string {
	if i := strings.LastIndex(r.Ref, "@"); i >= 0 {
		return r.Ref[i+1:]
	}
	return ""
}

// Builder produces a DAG image.
type Builder interface {
	// Path is the resolution step this builder implements.
	Path() Path
	// Describe names the builder for the command output.
	Describe() string
	// Build produces the image described by req.
	Build(ctx context.Context, req Request) (Result, error)
}
