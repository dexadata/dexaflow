package build

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Options are the inputs to builder selection.
type Options struct {
	// Tool is the local container tool for the fallback path (--builder).
	Tool string
	// Reuse allows the skip path: the image tag names the build's content
	// (the default version label with tag_strategy version), so an image the
	// registry already holds under it was built from the same inputs. The
	// caller leaves it off for any other tag and for --rebuild.
	Reuse bool
	// Split is how this build divides into a dependency image and a code
	// layer (ADR 0077), or nil when it cannot: then NoSplit says why.
	Split *Split
	// NoSplit says why the build is not split, for the builder line; empty
	// when there is nothing to say (a build that is not pushed).
	NoSplit string
	// Rebuild builds the dependency image again even when the registry holds
	// one for its key (--rebuild).
	Rebuild bool
	// Registry reads what the registry holds; the registry client when nil.
	// A split build also needs it to push (codeRegistry).
	Registry Resolver
}

// platformReader reads the platform of a single image. The registry client
// implements it; a Resolver that does not cannot vouch for a single image.
type platformReader interface {
	Platform(ctx context.Context, ref string) (string, error)
}

// fallbackReason says why the local daemon builds.
const fallbackReason = "fallback: no daemonless or remote builder is available yet"

// Select resolves the builder for a build of image for platforms in
// ResolutionOrder, and says why it was chosen. The skip path applies when
// reuse is allowed and the registry already holds image for every platform
// (ADR 0076). Then a split build (ADR 0077) appends the DAG code to the
// dependency image when the registry holds it for every platform, or has the
// local daemon build that dependency image first. Otherwise the local daemon
// builds the whole image, as the daemonless dependency and remote paths are
// not implemented yet.
func Select(ctx context.Context, o Options, image string, platforms []string) (b Builder, reason string) {
	reg := o.Registry
	if reg == nil {
		reg = Registry{}
	}
	daemon := LocalDaemon{Tool: o.Tool, Registry: reg}
	if len(platforms) == 0 {
		return daemon, fallbackReason
	}
	var why string
	if o.Reuse {
		pinned, skipWhy := reusable(ctx, reg, image, platforms, "reusable image for this content")
		if pinned != nil {
			return Existing{Pinned: *pinned}, "the registry already holds the image for this content label (pass --rebuild to build it again)"
		}
		why = skipWhy + "; "
	}
	cr, ok := reg.(codeRegistry)
	if o.Split == nil || !ok {
		if o.NoSplit != "" {
			return daemon, why + "fallback: " + o.NoSplit
		}
		return daemon, why + fallbackReason
	}
	split := CodeLayer{Split: *o.Split, Daemon: daemon, Registry: cr}
	if o.Rebuild {
		return split, fmt.Sprintf("--rebuild builds the dependency image %s again, then the DAG code is appended to it", o.Split.DepsImage)
	}
	deps, depsWhy := reusable(ctx, reg, o.Split.DepsImage, platforms, "dependency image "+o.Split.DepsImage)
	if deps == nil {
		return split, fmt.Sprintf("%s: the daemon builds and pushes it, then the DAG code is appended to it", depsWhy)
	}
	split.Deps = deps
	return split, fmt.Sprintf("the registry holds the dependency image %s for this build, so only the DAG code is pushed, "+
		"as a layer on it (pass --rebuild to build the dependency image again)", o.Split.DepsImage)
}

// reusable returns what the registry holds under image when it serves every
// platform, or nil and why not; what names the image in that reason.
func reusable(ctx context.Context, reg Resolver, image string, platforms []string, what string) (found *Pinned, why string) {
	pinned, err := reg.Resolve(ctx, image)
	if err != nil {
		cause := errors.Unwrap(err)
		if cause == nil {
			cause = err
		}
		return nil, fmt.Sprintf("no %s in the registry (%v)", what, cause)
	}
	if !isIndex(pinned.Manifest) {
		pr, ok := reg.(platformReader)
		if !ok {
			return nil, fmt.Sprintf("the platform of the %s is unknown", what)
		}
		if pinned.Platform, err = pr.Platform(ctx, pinned.Ref); err != nil || pinned.Platform == "" {
			return nil, fmt.Sprintf("the platform of the %s is unknown", what)
		}
	}
	if missing := pinned.missing(platforms); len(missing) > 0 {
		return nil, fmt.Sprintf("the %s has no %s", what, strings.Join(missing, ","))
	}
	return &pinned, ""
}
