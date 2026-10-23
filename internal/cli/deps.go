package cli

import (
	"context"
	"io"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/build"
)

// cliDeps are the calls a command makes into the world outside the process
// (registries, the release repository, the browser, Docker, k3d, uv, psql)
// and the waits it paces itself with. A command reads them from its context
// with depsFrom; a command run by NewRootCommand finds none there and uses
// productionDeps. The tests of this package run commands under a context
// that carries their own set (withDeps), so a fake reaches only the test
// that made it and tests can run in parallel.
type cliDeps struct {
	// imageDigest reads the digest an image reference names in its
	// registry, for `ci init` (lookupImageDigest).
	imageDigest func(ctx context.Context, image string) (string, error)
	// tagCommit finds the commit a release tag points to, for `ci init`
	// (lookupTagCommit).
	tagCommit func(ctx context.Context, tag string) (string, error)

	// watchInterval is how often deploy --watch polls the project files.
	watchInterval time.Duration
	// watchSettle is how long deploy --watch waits after a save for the
	// editor to finish writing before it redeploys.
	watchSettle time.Duration

	// devRun runs an external dev tool (k3d, docker, kubectl), streaming to
	// the command's output (runDevTool).
	devRun func(ctx context.Context, cmd *cobra.Command, name string, args ...string) error
	// devOutput runs an external dev tool and returns its combined output
	// (devToolOutput).
	devOutput func(ctx context.Context, name string, args ...string) ([]byte, error)
	// compileAndRegister compiles and registers one project of a `lite`
	// workspace (devCompileAndRegister).
	compileAndRegister func(ctx context.Context, cmd *cobra.Command, dir string, o compileOptions, token string, afterCompile func() error, uiURL string) error

	// devicePollWait waits one interval between two polls of the device
	// sign-in.
	devicePollWait func(time.Duration) <-chan time.Time
	// sessionHeadless reports whether this session looks like it has no
	// browser to open, and why (headlessHere).
	sessionHeadless func() (bool, string)
	// outputIsTerminal reports whether the command's output is a terminal,
	// where a person can read a printed code (writerIsTerminal).
	outputIsTerminal func(w io.Writer) bool
	// openBrowser opens a URL in the user's browser (systemOpenBrowser).
	openBrowser func(target string) error

	// dockerPing probes whether the Docker daemon answers (pingDocker).
	dockerPing func() error
	// composeProbe probes the Docker Compose v2 plugin (probeCompose).
	composeProbe func() error
	// dockerUsable reports whether the docker CLI is installed and its
	// daemon answers (hostDockerUsable).
	dockerUsable func(ctx context.Context) bool

	// verifyRegistryLogin checks a credential with its registry before it
	// is stored (build.VerifyLogin).
	verifyRegistryLogin func(ctx context.Context, host, user, secret string) error

	// psqlRestore replays a dump into the Lite database (runPsqlRestore).
	psqlRestore func(ctx context.Context, leoflowHome string, dump []byte) error
	// provisionForRestore brings up the datastore a restore replays into
	// and returns the stop to defer (provisionManagedForRestore).
	provisionForRestore func(ctx context.Context, cmd *cobra.Command) (func(), error)

	// followPollInterval is the shortest wait between two polls of a
	// followed run.
	followPollInterval time.Duration

	// baseReader turns the project's registry client into the reader of
	// its base image.
	baseReader func(r build.Registry) baseReader
	// lookUV finds the uv binary of the daemonless dependency layer
	// (ADR 0078).
	lookUV func() (string, error)
	// hostOS is the operating system the CLI runs on.
	hostOS string
	// registryCloud is the cloud session the registry client reads; nil
	// reads the cloud CLIs on PATH (build.CloudCLI).
	registryCloud build.CloudKeychain
}

// productionDeps are the dependencies of a command run from the shell.
func productionDeps() *cliDeps {
	return &cliDeps{
		imageDigest:         lookupImageDigest,
		tagCommit:           lookupTagCommit,
		watchInterval:       deployWatchInterval,
		watchSettle:         deployWatchSettle,
		devRun:              runDevTool,
		devOutput:           devToolOutput,
		compileAndRegister:  devCompileAndRegister,
		devicePollWait:      time.After,
		sessionHeadless:     headlessHere,
		outputIsTerminal:    writerIsTerminal,
		openBrowser:         systemOpenBrowser,
		dockerPing:          pingDocker,
		composeProbe:        probeCompose,
		dockerUsable:        hostDockerUsable,
		verifyRegistryLogin: build.VerifyLogin,
		psqlRestore:         runPsqlRestore,
		provisionForRestore: provisionManagedForRestore,
		followPollInterval:  followPollInterval,
		baseReader:          func(r build.Registry) baseReader { return r },
		lookUV:              lookPathUV,
		hostOS:              runtime.GOOS,
	}
}

// cliDepsKey is the context key of a command's cliDeps.
type cliDepsKey struct{}

// withDeps returns ctx carrying d, for the commands run under it.
func withDeps(ctx context.Context, d *cliDeps) context.Context {
	return context.WithValue(ctx, cliDepsKey{}, d)
}

// depsFrom returns the dependencies ctx carries, or productionDeps when it
// carries none (or ctx is nil, as for a command never executed).
func depsFrom(ctx context.Context) *cliDeps {
	if ctx != nil {
		if d, ok := ctx.Value(cliDepsKey{}).(*cliDeps); ok {
			return d
		}
	}
	return productionDeps()
}

// cmdDeps returns the dependencies of cmd's context.
func cmdDeps(cmd *cobra.Command) *cliDeps {
	return depsFrom(cmd.Context())
}
