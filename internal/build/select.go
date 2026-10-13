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
	// Registry reads what the registry holds; the registry client when nil.
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
// reuse is allowed and the registry already holds image for every platform;
// otherwise the local daemon builds, as the daemonless and remote paths are
// not implemented yet.
func Select(ctx context.Context, o Options, image string, platforms []string) (b Builder, reason string) {
	reg := o.Registry
	if reg == nil {
		reg = Registry{}
	}
	daemon := LocalDaemon{Tool: o.Tool, Registry: reg}
	if !o.Reuse || len(platforms) == 0 {
		return daemon, fallbackReason
	}
	pinned, err := reg.Resolve(ctx, image)
	if err != nil {
		cause := errors.Unwrap(err)
		if cause == nil {
			cause = err
		}
		return daemon, fmt.Sprintf("no reusable image for this content in the registry (%v); %s", cause, fallbackReason)
	}
	if !isIndex(pinned.Manifest) {
		pr, ok := reg.(platformReader)
		if !ok {
			return daemon, fallbackReason
		}
		if pinned.Platform, err = pr.Platform(ctx, pinned.Ref); err != nil || pinned.Platform == "" {
			return daemon, "the platform of the image for this content in the registry is unknown; " + fallbackReason
		}
	}
	if missing := pinned.missing(platforms); len(missing) > 0 {
		return daemon, fmt.Sprintf("the image for this content in the registry has no %s; %s", strings.Join(missing, ","), fallbackReason)
	}
	return Existing{Pinned: pinned}, "the registry already holds the image for this content label (pass --rebuild to build it again)"
}
