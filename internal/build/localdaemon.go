package build

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
)

// LocalDaemon builds with a local container tool, docker, podman or nerdctl,
// by shelling out to it: `<tool> build` and `<tool> push`, or one
// `docker buildx build --push` when docker has buildx. The digest of what it
// pushed is taken from the tool's report of the push (the buildx metadata
// file, podman's --digestfile, the last line of docker push), never from the
// tool's image store; when the tool reports none it is read from the
// registry (ADR 0075, ADR 0087). It is the last step of the resolution order,
// the fallback for when no daemonless or remote builder applies.
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
// because docker buildx, docker with the containerd image store and podman
// with a manifest list build and push it as one manifest list; a tool that
// cannot fails with an error that does not say what to change, so this one
// does.
func (b LocalDaemon) multiPlatformHint(platforms []string, err error) error {
	return fmt.Errorf("%w: building for %s with %s needs a tool that pushes one image per platform under one manifest list, "+
		"such as docker with buildx or with the containerd image store enabled, or podman. Otherwise build one platform: set build.platforms in dexaflow.yaml "+
		"(or pass --platform where the command has it), for example %s: %w",
		ErrMultiPlatform, strings.Join(platforms, ","), b.Describe(), platforms[0], err)
}

// Build runs the build, then the push when asked, and takes the digest of
// what it pushed from the tool's own report of the push (pushImage). The
// registry is read only to check that a push of several platforms holds every
// one of them (verifyPlatforms), at that digest, or, when the tool reported
// no digest, at the tag, as ADR 0075 decided (ADR 0087).
func (b LocalDaemon) Build(ctx context.Context, req Request) (Result, error) {
	if req.Pin && !req.Push {
		return Result{}, errors.New("a digest pin needs a push: an image that is not in a registry has no registry digest")
	}
	res := Result{Path: PathLocalDaemon, Image: req.Image, Platforms: req.Platforms}
	if !req.Push {
		if err := b.runEnv(ctx, req.Stdout, req.Stderr, req.Env, buildArgs(req.Image, req.Dockerfile, req.ContextDir, req.Platforms, inputArgs(req)...)...); err != nil {
			return Result{}, b.buildError(req, err)
		}
		return res, nil
	}
	digest, err := b.pushImage(ctx, req)
	if err != nil {
		return Result{}, err
	}
	res.Pushed = true
	if digest != "" {
		return b.pinReported(ctx, req, res, digest)
	}
	if !req.Pin && len(req.Platforms) < 2 {
		return res, nil
	}
	notef(req.Stderr, "note: %s reported no digest for the push of %s; reading it from the registry at the tag\n", filepath.Base(b.tool()), req.Image)
	pinned, err := b.registry().Resolve(ctx, req.Image)
	if err != nil {
		if len(req.Platforms) > 1 {
			return Result{}, b.multiPlatformHint(req.Platforms, fmt.Errorf("reading the digest of the pushed image %q: %w", req.Image, err))
		}
		return res, fmt.Errorf("%w: reading the digest of the pushed image %q: %w", ErrPinRead, req.Image, err)
	}
	if verr := b.verifyPlatforms(pinned, req.Platforms); verr != nil {
		return Result{}, verr
	}
	if req.Pin {
		res.Ref = pinned.Ref
	}
	return res, nil
}

// pinReported finishes a push whose digest the tool reported. A push of
// several platforms is checked at that digest, which no later push of the
// tag can move; one platform needs no registry read at all.
func (b LocalDaemon) pinReported(ctx context.Context, req Request, res Result, digest string) (Result, error) {
	ref := PinnedRef(req.Image, digest)
	if len(req.Platforms) > 1 {
		pinned, err := b.registry().Resolve(ctx, ref)
		if err != nil {
			return Result{}, b.multiPlatformHint(req.Platforms, fmt.Errorf("reading the pushed image %q: %w", ref, err))
		}
		pinned.Ref = ref
		if verr := b.verifyPlatforms(pinned, req.Platforms); verr != nil {
			return Result{}, verr
		}
	}
	if req.Pin {
		res.Ref = ref
	}
	return res, nil
}

// buildError wraps the failure of the build step, with the multi-platform
// hint when several platforms were asked for, and what build secrets need
// when the build has some: a tool that cannot take them fails with its own
// terse message, such as the legacy docker builder's "unknown flag".
func (b LocalDaemon) buildError(req Request, err error) error {
	err = fmt.Errorf("building image %q with %q: %w", req.Image, b.tool(), err)
	if len(req.Secrets) > 0 {
		err = fmt.Errorf("%w\n  the build passes its secrets as --secret id=<id>,env=<variable>, which needs BuildKit: "+
			"docker with buildx (the legacy builder has no --secret), podman or nerdctl with env= secret support (ADR 0088)", err)
	}
	if len(req.Platforms) > 1 {
		err = b.multiPlatformHint(req.Platforms, err)
	}
	return err
}

// pushError wraps the failure of the push step with the command that signs
// the tool in to the registry.
func (b LocalDaemon) pushError(req Request, err error) error {
	return fmt.Errorf("pushing image %q with %q: %w\n  %s pushes with its own credentials; "+
		"if the registry refused them, sign in once:  %s", req.Image, b.tool(), err, filepath.Base(b.tool()), b.loginCommand(req.Image))
}

// verifyPlatforms checks, for a build of several platforms, that the pushed
// digest is a manifest list holding every one of them. A tool can succeed at
// build and push and still leave a partial image: a docker build without the
// containerd image store keeps one platform, and a push of part of a list
// succeeds. Registering that digest would run
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

// loginCommand is the command that signs the tool in to the registry of
// image (LoginCommand), or the tool's own login when image does not parse.
func (b LocalDaemon) loginCommand(image string) string {
	tool := filepath.Base(b.tool())
	ref, err := name.ParseReference(image)
	if err != nil {
		return tool + " login"
	}
	return LoginCommand(ref.Context().RegistryStr(), tool)
}

// localDaemonWaitDelay bounds how long the tool has to exit after the
// interrupt a stop sends it, before it is killed.
const localDaemonWaitDelay = 10 * time.Second

// run executes the tool with args. A stop (ctx canceled, as Ctrl-C does to the
// CLI) sends the tool an interrupt, which `docker build` and `docker push`
// handle by canceling cleanly, and kills it after localDaemonWaitDelay, or at
// once when the interrupt cannot be sent (Windows).
func (b LocalDaemon) run(ctx context.Context, stdout, stderr io.Writer, args ...string) error {
	return b.runEnv(ctx, stdout, stderr, nil, args...)
}

// runEnv executes the tool with args and env added to the CLI's
// environment.
func (b LocalDaemon) runEnv(ctx context.Context, stdout, stderr io.Writer, env []string, args ...string) error {
	//nolint:gosec // G204: the tool is the operator's choice by design (ADR 0015).
	c := exec.CommandContext(ctx, b.tool(), args...)
	c.Cancel = func() error {
		if err := c.Process.Signal(os.Interrupt); err != nil {
			return c.Process.Kill()
		}
		return nil
	}
	c.WaitDelay = localDaemonWaitDelay
	c.Stdout, c.Stderr = stdout, stderr
	if len(env) > 0 {
		c.Env = append(os.Environ(), env...)
	}
	if err := c.Run(); err != nil {
		return fmt.Errorf("running %s %s: %w", b.tool(), args[0], err)
	}
	return nil
}

// buildArgs assembles the argv of an image build. With platforms it passes
// `--platform a,b`, so the image targets the cluster's architecture rather
// than the host's (ADR 0041); opts follow it (inputArgs).
func buildArgs(image, dockerfile, contextDir string, platforms []string, opts ...string) []string {
	args := []string{"build"}
	if len(platforms) > 0 {
		args = append(args, "--platform", strings.Join(platforms, ","))
	}
	args = append(args, opts...)
	return append(args, "-t", image, "-f", dockerfile, contextDir)
}

// inputArgs are the --build-arg options of req, in name order, then its
// --secret options. A secret names the variable that holds its value, never
// the value (ADR 0088).
func inputArgs(req Request) []string {
	args := buildArgFlags(req.BuildArgs)
	for _, s := range req.Secrets {
		args = append(args, "--secret", "id="+s.ID+",env="+s.Env)
	}
	return args
}

// buildArgFlags are the --build-arg options of args, in name order.
func buildArgFlags(args map[string]string) []string {
	names := make([]string, 0, len(args))
	for k := range args {
		names = append(names, k)
	}
	slices.Sort(names)
	out := make([]string, 0, 2*len(names))
	for _, k := range names {
		out = append(out, "--build-arg", k+"="+args[k])
	}
	return out
}
