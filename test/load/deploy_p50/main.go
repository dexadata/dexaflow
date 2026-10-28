// Command deploy_p50 is Load Experiment 7: the time of a code-only deploy
// and the warm-up of the pool after it (#1892).
//
// It drives the real `dexaflow deploy` binary, as a user does. It scaffolds a
// project with `dexaflow init`, gives it pinned dependencies and a registry,
// and deploys it once cold: that deploy builds the dependency image (or finds
// it) and is discarded. It then makes n code-only changes to dag.py, one per
// deploy, and times each deploy from the start of the command to its exit.
// Each measured deploy must take the code-layer path of ADR 0077 (the
// registry holds the dependency image, only the code layer is pushed); a
// deploy that took another path fails the measurement, because it did not
// measure a code-only deploy.
//
// With --warm it also measures the warm-up of each new version's pool on a
// cluster with warm pools on (ADR 0082): the project declares
// min_idle_workers: 1 and deploys with --active, and after each deploy the
// harness polls the warm worker pods for one running the new version's
// image, recording when it appeared, when it became Ready and the image pull
// time the kubelet reported.
//
// It prints a Markdown table and a PASS or FAIL line, and exits 1 on FAIL.
//
// Usage (see test/load/README.md, Experiment 7):
//
//	go run ./test/load/deploy_p50 --cli bin/dexaflow --config cli.yaml \
//	  --registry k3d-registry.localhost:5111 --n 10
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/go-containerregistry/pkg/registry"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// errFail is returned when the measurement ran and its verdict is FAIL.
var errFail = errors.New("the measurement failed its targets")

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "deploy_p50: %v\n", err)
		os.Exit(1)
	}
}

// listFlag is a flag that may repeat.
type listFlag []string

// String joins the values.
func (l *listFlag) String() string { return strings.Join(*l, ",") }

// Set adds one value.
func (l *listFlag) Set(v string) error {
	*l = append(*l, v)
	return nil
}

// flags are the command line.
type flags struct {
	cfg           config
	deps, extra   listFlag
	localRegistry bool
	kubeconfig    string
	workDir       string
	warmTimeout   time.Duration
}

// parseFlags reads the command line.
func parseFlags() flags {
	var f flags
	c := &f.cfg
	flag.StringVar(&c.cli, "cli", "bin/dexaflow", "the dexaflow binary to deploy with")
	flag.StringVar(&c.server, "server", "", "control plane URL, passed to deploy --server")
	flag.StringVar(&c.token, "token", "", "bearer token, passed to deploy --token")
	flag.StringVar(&c.cliConfig, "config", "", "CLI config file (from dexaflow auth login), passed to deploy --config")
	flag.StringVar(&c.registry, "registry", "", "registry prefix the image is pushed to, such as k3d-registry.localhost:5111")
	flag.BoolVar(&f.localRegistry, "local-registry", false, "push to an in-memory registry this process serves on the loopback, instead of --registry; without --base-image the scaffold builds on a stand-in runtime base pushed there")
	flag.StringVar(&c.project, "project", "", "deploy this existing project instead of a scaffold (its dexaflow.yaml must push to a registry)")
	flag.StringVar(&f.workDir, "workdir", "", "directory for the scaffolded project (default: a new temporary directory)")
	flag.StringVar(&c.dagID, "dag-id", "deploy_p50", "dag_id of the scaffolded project")
	flag.StringVar(&c.platform, "platform", "linux/"+runtime.GOARCH, "platform the image is built for")
	flag.StringVar(&c.python, "python", "3.12", "python_version of the scaffolded project")
	flag.StringVar(&c.baseImage, "base-image", "", "base_image of the scaffolded project (default: the published runtime base)")
	flag.Var(&f.deps, "dependency", "pinned dependency of the scaffolded project, name==version; repeat for more (default requests==2.32.3)")
	flag.IntVar(&c.n, "n", 10, "measured code-only deploys, after the discarded cold deploy")
	flag.DurationVar(&c.target, "target", 30*time.Second, "the p50 of the measured deploys must be under this")
	flag.BoolVar(&c.warm, "warm", false, "measure the warm-up of each new version's pool on the cluster of --kubeconfig")
	flag.StringVar(&f.kubeconfig, "kubeconfig", "", "kubeconfig of the cluster for --warm (default: the usual loading rules)")
	flag.StringVar(&c.namespace, "namespace", "leoflow", "namespace of the warm worker pods")
	flag.DurationVar(&c.warmTarget, "warm-target", time.Minute, "with --warm, the p50 from the deploy's end to a Ready warm worker of the new version must be under this")
	flag.DurationVar(&f.warmTimeout, "warm-timeout", 3*time.Minute, "with --warm, how long to wait for each new version's warm worker")
	flag.Var(&f.extra, "deploy-arg", "extra flag passed to every deploy; repeat for more")
	flag.Parse()
	c.dependencies, c.deployArgs = f.deps, f.extra
	if len(c.dependencies) == 0 {
		c.dependencies = []string{"requests==2.32.3"}
	}
	c.stamp = strconv.FormatInt(time.Now().Unix(), 36)
	return f
}

// checkFlags refuses flags that cannot work together.
func checkFlags(f flags) error {
	switch {
	case f.cfg.n < 1:
		return fmt.Errorf("--n must be at least 1")
	case f.localRegistry && f.cfg.registry != "":
		return fmt.Errorf("give --registry or --local-registry, not both")
	case f.localRegistry && f.cfg.warm:
		return fmt.Errorf("--warm needs a registry the cluster pulls from, not --local-registry")
	case !f.localRegistry && f.cfg.registry == "" && f.cfg.project == "":
		return fmt.Errorf("give --registry, --local-registry or --project")
	}
	return nil
}

func run() error {
	f := parseFlags()
	if err := checkFlags(f); err != nil {
		return err
	}
	cfg := f.cfg
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if f.localRegistry {
		host, closeReg, count, err := serveCountingRegistry(ctx)
		if err != nil {
			return err
		}
		defer closeReg()
		cfg.registry, cfg.requests = host+"/p50", count
		if cfg.baseImage == "" && cfg.project == "" {
			// No pull from a public registry: the measurement needs only
			// the CLI, the control plane and the package index.
			base, serr := seedStandInBase(host, cfg.python, cfg.platform)
			if serr != nil {
				return serr
			}
			cfg.baseImage = base
		}
	}
	if cfg.project == "" {
		dir, err := scaffoldDir(f.workDir)
		if err != nil {
			return err
		}
		cfg.workDir = dir
	}
	var watch watchFunc
	if cfg.warm {
		w, err := clusterWatch(f.kubeconfig, cfg.namespace, f.warmTimeout)
		if err != nil {
			return err
		}
		watch = w
	}
	fmt.Printf("deploy_p50: %d code-only deploys of %s to %s (%s)\n\n", cfg.n, cfg.what(), cfg.registryName(), cfg.platform)
	rep, err := measure(ctx, cfg, os.Stdout, watch)
	if err != nil {
		return err
	}
	if ok, _ := rep.verdict(cfg); !ok {
		return errFail
	}
	return nil
}

// what names what is deployed, for the header line.
func (c config) what() string {
	if c.project != "" {
		return c.project
	}
	return "a scaffolded project (" + strings.Join(c.dependencies, ", ") + ")"
}

// registryName is where the image goes, for the header line.
func (c config) registryName() string {
	if c.registry == "" {
		return "the project's registry"
	}
	return c.registry
}

// scaffoldDir is dir, or a new temporary directory when dir is empty.
func scaffoldDir(dir string) (string, error) {
	if dir != "" {
		return dir, nil
	}
	d, err := os.MkdirTemp("", "deploy_p50-")
	if err != nil {
		return "", fmt.Errorf("creating the work directory: %w", err)
	}
	return d, nil
}

// serveCountingRegistry serves an in-memory registry on a loopback port
// and returns its host:port, a func that closes it, and a func that counts
// the requests it served; the CLI talks plain HTTP to a loopback registry.
// It gives the lower bound of a code-only deploy, with no network between
// the CLI and the registry, and the number of registry round trips, which
// is what a remote registry multiplies by its latency.
func serveCountingRegistry(ctx context.Context) (host string, closeFn func(), count func() int64, err error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, nil, fmt.Errorf("listening for the local registry: %w", err)
	}
	var served atomic.Int64
	reg := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served.Add(1)
		reg.ServeHTTP(w, r)
	})
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }() //nolint:errcheck // Serve ends with ErrServerClosed when the measurement ends
	return ln.Addr().String(), func() { _ = srv.Close() }, served.Load, nil
}

// clusterWatch returns a watchFunc over the cluster kubeconfig names, each
// wait bounded by timeout.
func clusterWatch(kubeconfig, namespace string, timeout time.Duration) (watchFunc, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}
	rc, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("loading the kubeconfig for --warm: %w", err)
	}
	cs, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return nil, fmt.Errorf("building the Kubernetes client: %w", err)
	}
	w := warmWatch{client: cs, namespace: namespace, poll: 500 * time.Millisecond, now: time.Now, sleep: sleepCtx}
	return func(ctx context.Context, image string, start time.Time) (warmReading, error) {
		wctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return w.wait(wctx, image, start)
	}, nil
}
