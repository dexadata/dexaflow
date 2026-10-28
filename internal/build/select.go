package build

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strings"

	ggcrtransport "github.com/google/go-containerregistry/pkg/v1/remote/transport"
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
	// Remote is the remote builder the project or its target configured
	// (path 4), or nil when none is.
	Remote Remote
	// RemoteSource names where Remote was configured, for the builder line,
	// such as the project's dexaflow.yaml.
	RemoteSource string
	// Push says the build is pushed. The remote builder pushes what it
	// builds, so it applies only to a pushed build.
	Push bool
	// LocalOnly says why the build must run on this machine (its build
	// secrets or package index come from this machine's environment), or ""
	// when the remote builder may take it.
	LocalOnly string
}

// remote returns the remote builder when it applies to a build for
// platforms, or nil and why not ("" when none is configured). One that is
// not ready (its CLI is missing, or the person deploying may not use it) is
// passed over only when the daemon's tool is on PATH; otherwise it is kept,
// so the build fails on what was configured.
func (o Options) remote(ctx context.Context, platforms []string, daemon LocalDaemon) (rem Remote, why string) {
	if o.Remote == nil {
		return nil, ""
	}
	from := "the remote builder"
	if o.RemoteSource != "" {
		from += " from " + o.RemoteSource
	}
	if !o.Push {
		return nil, from + " pushes what it builds, and this build is not pushed"
	}
	if o.LocalOnly != "" {
		return nil, from + " does not apply: " + o.LocalOnly
	}
	if why := o.Remote.Decline(platforms); why != "" {
		return nil, from + " does not apply: " + why
	}
	if err := o.Remote.Ready(ctx); err != nil {
		if _, lerr := exec.LookPath(daemon.tool()); lerr == nil {
			return nil, from + " does not apply: " + err.Error()
		}
	}
	return o.Remote, ""
}

// platformReader reads the platform of a single image. The registry client
// implements it; a Resolver that does not cannot vouch for a single image.
type platformReader interface {
	Platform(ctx context.Context, ref string) (string, error)
}

// fallbackReason says why the local daemon builds when nothing more precise
// can be said.
const fallbackReason = "fallback: no daemonless path applies and no remote builder is configured (build.remote_builder)"

// Select resolves the builder for a build of image for platforms in
// ResolutionOrder, and says why it was chosen. The skip path applies when
// reuse is allowed and the registry already holds image for every platform
// (ADR 0076). Then a split build (ADR 0077) appends the DAG code to the
// dependency image when the registry holds it for every platform, or first
// builds that dependency image, from wheels (Split.Wheels, ADR 0078), with
// the remote builder or with the local daemon. Otherwise the remote builder
// builds the whole image when one is configured and applies (Options.Remote,
// ADR 0083), and the local daemon when not.
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
		// Any failure to reuse the whole image only means a build: the read
		// error is in skipWhy.
		pinned, skipWhy, _ := reusable(ctx, reg, image, platforms, "reusable image for this content") //nolint:errcheck // see above
		if pinned != nil {
			return Existing{Pinned: *pinned}, "the registry already holds the image for this content label (pass --rebuild to build it again)"
		}
		why = skipWhy + "; "
	}
	rem, remWhy := o.remote(ctx, platforms, daemon)
	cr, ok := reg.(codeRegistry)
	if o.Split == nil || !ok {
		return whole(daemon, rem, why, o.NoSplit, remWhy, o.RemoteSource)
	}
	split := CodeLayer{Split: *o.Split, Daemon: daemon, Remote: rem, Registry: cr}
	how := depsHow(o.Split, rem, remWhy)
	if o.Rebuild {
		return split, withNote(fmt.Sprintf("--rebuild builds the dependency image %s again (%s), then the DAG code is appended to it", o.Split.DepsImage, how), o.Split.Note)
	}
	deps, depsWhy, err := reusable(ctx, reg, o.Split.DepsImage, platforms, "dependency image "+o.Split.DepsImage)
	if err != nil {
		// The daemon may reach a registry this client cannot (a credential
		// helper off PATH, an insecure registry); the whole build does not
		// need the client.
		return daemon, why + fmt.Sprintf("fallback: the registry could not be read for the dependency image %s (%v)", o.Split.DepsImage, unwrap(err))
	}
	if deps == nil {
		return split, withNote(fmt.Sprintf("%s: %s, then the DAG code is appended to it", depsWhy, how), o.Split.Note)
	}
	split.Deps = deps
	return split, withNote(fmt.Sprintf("the registry holds the dependency image %s for this build, so only the DAG code is pushed, "+
		"as a layer on it (pass --rebuild to build the dependency image again)", o.Split.DepsImage), o.Split.Note)
}

// whole is the builder of a build that is not split: the remote builder
// when it applies, the local daemon otherwise. why is what came before
// (the skip path's reason), noSplit why the build is not split, remWhy why
// the remote builder does not apply.
func whole(daemon LocalDaemon, rem Remote, why, noSplit, remWhy, source string) (b Builder, reason string) {
	if rem != nil {
		from := "the remote builder"
		if source != "" {
			from += " from " + source
		}
		if noSplit != "" {
			return rem, why + noSplit + ", so " + from + " builds the whole image"
		}
		return rem, why + from + " builds the whole image"
	}
	reason = why + fallbackReason
	if noSplit != "" {
		reason = why + "fallback: " + noSplit
	}
	return daemon, withNote(reason, remWhy)
}

// depsHow says how a missing dependency image is built: from wheels, by the
// remote builder or by the daemon, and why not from wheels or remotely.
func depsHow(split *Split, rem Remote, remWhy string) string {
	if split.Wheels != nil {
		return "it is built from wheels without a daemon and pushed"
	}
	by := "the daemon builds and pushes it"
	if rem != nil {
		by = "the remote builder builds and pushes it"
	}
	var why []string
	if split.NoWheels != "" {
		why = append(why, "no daemonless dependency layer: "+split.NoWheels)
	}
	if remWhy != "" {
		why = append(why, remWhy)
	}
	if len(why) == 0 {
		return by
	}
	return fmt.Sprintf("%s (%s)", by, strings.Join(why, "; "))
}

// withNote ends reason with note, when there is one.
func withNote(reason, note string) string {
	if note == "" {
		return reason
	}
	return reason + "; " + note
}

// reusable returns what the registry holds under image when it serves every
// platform, or nil and why not; what names the image in that reason.
// unreadable is the error of a registry read that failed for another reason
// than a missing tag; nil otherwise.
func reusable(ctx context.Context, reg Resolver, image string, platforms []string, what string) (found *Pinned, why string, unreadable error) {
	pinned, err := reg.Resolve(ctx, image)
	if err != nil {
		why = fmt.Sprintf("no %s in the registry (%v)", what, unwrap(err))
		if !notFound(err) {
			unreadable = err
		}
		return nil, why, unreadable
	}
	if !isIndex(pinned.Manifest) {
		pr, ok := reg.(platformReader)
		if !ok {
			return nil, fmt.Sprintf("the platform of the %s is unknown", what), nil
		}
		if p, perr := pr.Platform(ctx, pinned.Ref); perr == nil && p != "" {
			pinned.Platform = p
		} else {
			return nil, fmt.Sprintf("the platform of the %s is unknown", what), nil
		}
	}
	if missing := pinned.missing(platforms); len(missing) > 0 {
		return nil, fmt.Sprintf("the %s has no %s", what, strings.Join(missing, ",")), nil
	}
	return &pinned, "", nil
}

// unwrap returns the cause of err without the context Resolve adds, or err
// when it wraps nothing.
func unwrap(err error) error {
	if cause := errors.Unwrap(err); cause != nil {
		return cause
	}
	return err
}

// notFound reports whether err says the registry has no such manifest, as
// opposed to a registry that could not be read.
func notFound(err error) bool {
	var te *ggcrtransport.Error
	if !errors.As(err, &te) {
		return false
	}
	if te.StatusCode == http.StatusNotFound {
		return true
	}
	for _, d := range te.Errors {
		if d.Code == ggcrtransport.ManifestUnknownErrorCode || d.Code == ggcrtransport.NameUnknownErrorCode {
			return true
		}
	}
	return false
}
