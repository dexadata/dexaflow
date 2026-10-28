package build

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// The remote path of ADR 0073 (path 4) on the in-cluster builder the Helm
// chart runs (builder.enabled, #1883): one rootless BuildKit daemon in a
// namespace of its own, listening on a unix socket only. The CLI finds a
// ready pod of it with kubectl and runs `buildctl --addr kube-pod://<pod>`,
// which reaches the daemon with `kubectl exec` as the person who deploys, so
// the Role the chart binds to builder.subjects decides who may build. No
// Kubernetes or BuildKit module is linked (ADR 0015). buildctl sends the
// project directory as the context with the .dockerignore the build reads,
// as a local `docker build` does, and BuildKit pushes the image with the
// credentials of this machine's docker config, which it asks buildctl for
// over the session: the pod holds no secret. The digest is the one BuildKit
// reports for the push (ADR 0087).

// buildKitCheckTimeout bounds the kubectl calls that find the builder and
// check the permission to use it.
const buildKitCheckTimeout = 20 * time.Second

// buildKitWaitDelay bounds how long buildctl has to exit after SIGINT
// before it is killed.
const buildKitWaitDelay = 10 * time.Second

// podName is a pod name (an RFC 1123 subdomain). A name kubectl lists is
// checked against it before it reaches buildctl's address.
var podName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)

// InCluster builds on the in-cluster BuildKit daemon, through buildctl over
// kubectl exec.
type InCluster struct {
	// Namespace is the namespace of the builder.
	Namespace string
	// PodSelector is the label selector of the builder pod; the chart's
	// (domain.DefaultBuildKitPodSelector) when empty.
	PodSelector string
	// Context is the kubeconfig context; kubectl's current one when empty.
	Context string
	// Registry reads the pushed digest when BuildKit reports none; the
	// registry client when nil.
	Registry Resolver
}

// selector returns the label selector of the builder pod.
func (b InCluster) selector() string {
	if b.PodSelector == "" {
		return "app.kubernetes.io/component=builder"
	}
	return b.PodSelector
}

// registry returns the resolver, the registry client by default.
func (b InCluster) registry() Resolver {
	if b.Registry == nil {
		return Registry{}
	}
	return b.Registry
}

// Path is PathRemote.
func (InCluster) Path() Path { return PathRemote }

// Describe names the path, the backend and where it builds, such as
// "remote (buildkit, namespace dexaflow-builder)".
func (b InCluster) Describe() string {
	where := "namespace " + b.Namespace
	if b.Context != "" {
		where += ", context " + b.Context
	}
	return fmt.Sprintf("%s (buildkit, %s)", PathRemote, where)
}

// kubectlArgs are the --context and --namespace of every kubectl call,
// followed by args.
func (b InCluster) kubectlArgs(args ...string) []string {
	var out []string
	if b.Context != "" {
		out = append(out, "--context="+b.Context)
	}
	return append(append(out, "--namespace="+b.Namespace), args...)
}

// Ready reports whether buildctl and kubectl are on PATH and whether the
// person deploying may exec into pods of the builder namespace, which is
// what the chart's Role grants to builder.subjects. A builder this person
// may not use is passed over for the local daemon when its tool is on PATH.
func (b InCluster) Ready(ctx context.Context) error {
	if _, err := exec.LookPath("buildctl"); err != nil {
		return errors.New("the remote builder buildkit runs buildctl, and buildctl is not on PATH; " +
			"install it from https://github.com/moby/buildkit/releases")
	}
	if _, err := exec.LookPath("kubectl"); err != nil {
		return errors.New("the remote builder buildkit reaches the builder with kubectl, and kubectl is not on PATH; " +
			"install it (https://kubernetes.io/docs/tasks/tools/) and point it at the cluster")
	}
	ctx, cancel := context.WithTimeout(ctx, buildKitCheckTimeout)
	defer cancel()
	var out bytes.Buffer
	//nolint:gosec // G204: kubectl with validated values passed as --flag=value
	c := exec.CommandContext(ctx, "kubectl", b.kubectlArgs("auth", "can-i", "create", "pods/exec")...)
	c.Stdout, c.Stderr = &out, &out
	err := c.Run()
	if strings.TrimSpace(out.String()) == "yes" && err == nil {
		return nil
	}
	return fmt.Errorf("you may not use the in-cluster builder in namespace %s (`kubectl auth can-i create pods/exec` answered %q); "+
		"ask for your user, group or ServiceAccount to be added to the chart's builder.subjects", b.Namespace, lastLine(out.String()))
}

// Decline says why platforms are not built here: the rootless builder sets
// up no emulation, so it builds one platform, its node's.
func (InCluster) Decline(platforms []string) string {
	if len(platforms) <= 1 {
		return ""
	}
	return fmt.Sprintf("buildkit builds one platform, the architecture of its node, not %s", strings.Join(platforms, ","))
}

// Build finds the ready builder pod, builds req on it with buildctl, which
// pushes the image, and pins the digest BuildKit reports for the push, or,
// when it reports none, the digest the registry holds.
func (b InCluster) Build(ctx context.Context, req Request) (Result, error) {
	if !req.Push {
		return Result{}, errors.New("the remote builder pushes the image it builds, so the build needs a push (--push)")
	}
	if len(req.Secrets) > 0 {
		return Result{}, errors.New("the build has secrets, which are read from this machine's environment; " +
			"the in-cluster builder is shared by everyone bound to it, so a builder on this machine builds it (ADR 0088)")
	}
	if err := b.Ready(ctx); err != nil {
		return Result{}, err
	}
	pod, err := b.pod(ctx)
	if err != nil {
		return Result{}, err
	}
	dir, err := os.MkdirTemp("", "dexaflow-buildkit-")
	if err != nil {
		return Result{}, fmt.Errorf("making the metadata file of the build: %w", err)
	}
	defer os.RemoveAll(dir) //nolint:errcheck // best-effort cleanup of a temporary directory
	metadata := filepath.Join(dir, "metadata.json")
	notef(req.Stderr, "building on the in-cluster builder in namespace %s, on pod %s\n", b.Namespace, pod)
	if rerr := b.run(ctx, req, b.buildctlArgs(req, pod, metadata)); rerr != nil {
		return Result{}, fmt.Errorf("building image %q on the in-cluster builder (pod %s in namespace %s): %w\n"+
			"  buildctl reaches it with kubectl exec as you (the chart's builder.subjects), and BuildKit pushes with the "+
			"credentials of your docker config; if the registry refused them, sign in once with `dexaflow registry login`",
			req.Image, pod, b.Namespace, rerr)
	}
	res := Result{Path: PathRemote, Image: req.Image, Platforms: req.Platforms, Pushed: true}
	if !req.Pin {
		return res, nil
	}
	if digest := metadataDigest(metadata); digest != "" {
		res.Ref = PinnedRef(req.Image, digest)
		return res, nil
	}
	pinned, err := b.registry().Resolve(ctx, req.Image)
	if err != nil {
		return res, fmt.Errorf("%w: reading the digest of the image %q BuildKit pushed: %w", ErrPinRead, req.Image, err)
	}
	res.Ref = pinned.Ref
	return res, nil
}

// podList is the part of `kubectl get pods --output=json` the CLI reads.
type podList struct {
	Items []struct {
		Metadata struct {
			Name              string  `json:"name"`
			DeletionTimestamp *string `json:"deletionTimestamp"` //nolint:tagliatelle // the Kubernetes field name
		} `json:"metadata"`
		Status struct {
			Phase      string `json:"phase"`
			Conditions []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
			} `json:"conditions"`
		} `json:"status"`
	} `json:"items"`
}

// ready returns the names of the pods that run, are ready and are not
// being deleted, in name order, leaving out a name that is not a pod name.
func (p podList) ready() []string {
	var names []string
	for _, it := range p.Items {
		if it.Metadata.DeletionTimestamp != nil || it.Status.Phase != "Running" || !podName.MatchString(it.Metadata.Name) {
			continue
		}
		for _, c := range it.Status.Conditions {
			if c.Type == "Ready" && c.Status == "True" {
				names = append(names, it.Metadata.Name)
				break
			}
		}
	}
	slices.Sort(names)
	return names
}

// pod returns the first ready pod of the builder.
func (b InCluster) pod(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, buildKitCheckTimeout)
	defer cancel()
	var out, errOut bytes.Buffer
	//nolint:gosec // G204: kubectl with validated values passed as --flag=value
	c := exec.CommandContext(ctx, "kubectl", b.kubectlArgs("get", "pods", "--selector="+b.selector(), "--output=json")...)
	c.Stdout, c.Stderr = &out, &errOut
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("listing the pods of the in-cluster builder in namespace %s: %w: %s", b.Namespace, err, lastLine(errOut.String()))
	}
	var pods podList
	if err := json.Unmarshal(out.Bytes(), &pods); err != nil {
		return "", fmt.Errorf("reading the pods of the in-cluster builder in namespace %s: %w", b.Namespace, err)
	}
	names := pods.ready()
	if len(names) == 0 {
		return "", fmt.Errorf("no ready pod of the in-cluster builder matches %s in namespace %s; "+
			"check that the chart has builder.enabled (`kubectl --namespace=%s get pods --selector=%s`)",
			b.selector(), b.Namespace, b.Namespace, b.selector())
	}
	return names[0], nil
}

// address is the buildctl address of pod: kube-pod://, which buildctl
// dials with `kubectl exec ... buildctl dial-stdio`.
func (b InCluster) address(pod string) string {
	q := url.Values{"namespace": {b.Namespace}}
	if b.Context != "" {
		q.Set("context", b.Context)
	}
	return (&url.URL{Scheme: "kube-pod", Host: pod, RawQuery: q.Encode()}).String()
}

// buildctlArgs is the argv of the build of req on pod: the Dockerfile
// frontend, the project directory as the context (filtered by the
// .dockerignore the frontend reads), the Dockerfile's directory and name,
// the platform and build arguments as options, and an image output BuildKit
// pushes, its digest written to metadata.
func (b InCluster) buildctlArgs(req Request, pod, metadata string) []string {
	args := []string{"--addr", b.address(pod), "build",
		"--frontend", "dockerfile.v0",
		"--local", "context=" + req.ContextDir,
		"--local", "dockerfile=" + filepath.Dir(req.Dockerfile),
		"--opt", "filename=" + filepath.Base(req.Dockerfile)}
	if len(req.Platforms) > 0 {
		args = append(args, "--opt", "platform="+strings.Join(req.Platforms, ","))
	}
	ba := buildArgFlags(req.BuildArgs)
	for i := 1; i < len(ba); i += 2 {
		args = append(args, "--opt", "build-arg:"+ba[i])
	}
	return append(args, "--output", "type=image,name="+req.Image+",push=true", "--metadata-file", metadata)
}

// run executes buildctl with args. A stopped command sends it SIGINT, so it
// ends the session and BuildKit cancels the build.
func (b InCluster) run(ctx context.Context, req Request, args []string) error {
	var errTail bytes.Buffer
	//nolint:gosec // G204: buildctl with validated values; the image and paths are this build's own
	c := exec.CommandContext(ctx, "buildctl", args...)
	c.Stdout = orDiscard(req.Stdout)
	c.Stderr = teeWriter(req.Stderr, &errTail)
	c.Cancel = func() error { return c.Process.Signal(os.Interrupt) }
	c.WaitDelay = buildKitWaitDelay
	if err := c.Run(); err != nil {
		if line := lastLine(errTail.String()); line != "" {
			return fmt.Errorf("buildctl build: %w: %s", err, line)
		}
		return fmt.Errorf("buildctl build: %w", err)
	}
	return nil
}
