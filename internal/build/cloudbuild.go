package build

import (
	"compress/gzip"
	"context"
	"encoding/json"
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

// The remote path of ADR 0073 (path 4) on Google Cloud Build (ADR 0083). The
// CLI packs the build context itself, from the files the .dockerignore keeps
// (Request.ContextFiles), and submits it with `gcloud builds submit`: no
// cloud SDK is linked, as for the registry credentials of ADR 0080, and the
// build runs on Cloud Build's workers with the service account of the
// project, never on this machine and never in the control plane (ADR 0048).
// Cloud Build pushes the image; the digest is read back from the registry
// (ADR 0075).

// Remote is a builder of the remote path: it builds a whole image from a
// Dockerfile and a listed context somewhere else and pushes it. Select asks
// it whether it can before it picks it.
type Remote interface {
	Builder
	// Ready says why the builder cannot run on this machine (its CLI is not
	// on PATH, or the person deploying may not use it), or nil when it can.
	Ready(ctx context.Context) error
	// Decline says why the builder does not build for platforms, or "" when
	// it does.
	Decline(platforms []string) string
}

// remoteDockerfile is the name the Dockerfile of the build takes in the
// archive a remote builder receives, wherever it was on this machine. The
// leading dot and the prefix keep it apart from the project's own files.
const remoteDockerfile = ".dexaflow-remote.Dockerfile"

// cloudBuildDockerImage is the build step image: the docker builder Google
// publishes for Cloud Build.
const cloudBuildDockerImage = "gcr.io/cloud-builders/docker"

// cloudBuildPlatform is the one platform the Cloud Build backend builds:
// Cloud Build's default workers are amd64 and the build sets up no emulation.
const cloudBuildPlatform = "linux/amd64"

// CloudBuild builds on Google Cloud Build through the gcloud CLI.
type CloudBuild struct {
	// Project is the Google Cloud project the build runs in.
	Project string
	// Region is the Cloud Build region; gcloud's builds/region, else global,
	// when empty.
	Region string
	// WorkerPool is a private pool, projects/<p>/locations/<r>/workerPools/<n>;
	// the default pool when empty.
	WorkerPool string
	// ServiceAccount is the service account the build runs as,
	// projects/<p>/serviceAccounts/<email>; Cloud Build's default when empty.
	ServiceAccount string
	// Tool is the gcloud executable, "gcloud" from PATH when empty.
	Tool string
	// Registry reads the pushed digest; the registry client when nil.
	Registry Resolver
	// Poll is how often a build whose log cannot be streamed is described
	// until it ends, and the first wait before a failed describe is tried
	// again; cloudBuildPoll when zero.
	Poll time.Duration
	// MaxContext is the largest compressed build context uploaded, in
	// bytes; defaultMaxContext (2 GiB) when zero.
	MaxContext int64
}

// maxContext returns the archive cap.
func (b CloudBuild) maxContext() int64 {
	if b.MaxContext > 0 {
		return b.MaxContext
	}
	return defaultMaxContext
}

// tool returns the executable, gcloud by default.
func (b CloudBuild) tool() string {
	if b.Tool == "" {
		return "gcloud"
	}
	return b.Tool
}

// registry returns the resolver, the registry client by default.
func (b CloudBuild) registry() Resolver {
	if b.Registry == nil {
		return Registry{}
	}
	return b.Registry
}

// Path is PathRemote.
func (CloudBuild) Path() Path { return PathRemote }

// Describe names the path, the backend and where it builds, such as
// "remote (cloud-build, project acme-ci, region us-central1)".
func (b CloudBuild) Describe() string {
	where := "project " + b.Project
	if b.Region != "" {
		where += ", region " + b.Region
	}
	return fmt.Sprintf("%s (cloud-build, %s)", PathRemote, where)
}

// Ready reports whether gcloud is on PATH.
func (b CloudBuild) Ready(_ context.Context) error {
	if _, err := exec.LookPath(b.tool()); err != nil {
		return fmt.Errorf("the remote builder cloud-build runs gcloud, and %s is not on PATH; "+
			"install the Google Cloud CLI (https://cloud.google.com/sdk/docs/install) and sign in with `gcloud auth login`", filepath.Base(b.tool()))
	}
	return nil
}

// Decline says why platforms are not built here: anything but one
// linux/amd64 image.
func (CloudBuild) Decline(platforms []string) string {
	if len(platforms) == 1 && strings.EqualFold(platforms[0], cloudBuildPlatform) {
		return ""
	}
	return fmt.Sprintf("cloud-build builds %s only, not %s", cloudBuildPlatform, strings.Join(platforms, ","))
}

// cloudBuildConfig is the build config gcloud submits, in Cloud Build's
// JSON form.
type cloudBuildConfig struct {
	Steps   []cloudBuildStep   `json:"steps"`
	Images  []string           `json:"images"`
	Options *cloudBuildOptions `json:"options,omitempty"`
	// Tags label the build in Cloud Build's history, so builds the CLI
	// submitted can be filtered (`gcloud builds list --filter=tags=dexaflow`).
	Tags []string `json:"tags,omitempty"`
}

// cloudBuildStep is one step of a build config.
type cloudBuildStep struct {
	Name string   `json:"name"`
	Args []string `json:"args"`
}

// cloudBuildOptions are the options of a build config.
type cloudBuildOptions struct {
	// Logging is where the logs go. A build with a service account of its
	// own must set it, or Cloud Build refuses the build.
	Logging string `json:"logging,omitempty"`
}

// config is the build config of req: one docker build for the platform,
// tagged with the image, which Cloud Build pushes when the step succeeds.
func (b CloudBuild) config(req Request) cloudBuildConfig {
	args := []string{"build"}
	if len(req.Platforms) > 0 {
		args = append(args, "--platform", strings.Join(req.Platforms, ","))
	}
	args = append(args, buildArgFlags(req.BuildArgs)...)
	args = append(args, "--tag", req.Image, "--file", remoteDockerfile, ".")
	cfg := cloudBuildConfig{
		Steps:  []cloudBuildStep{{Name: cloudBuildDockerImage, Args: args}},
		Images: []string{req.Image},
		Tags:   []string{"dexaflow"},
	}
	if b.ServiceAccount != "" {
		cfg.Options = &cloudBuildOptions{Logging: "CLOUD_LOGGING_ONLY"}
	}
	return cfg
}

// Build packs the listed context with the Dockerfile, submits it to Cloud
// Build, follows the build until it ends (it pushes the image), and, when a
// pin is asked for, reads the pushed image's digest from the registry and
// checks it is the one the build reports.
func (b CloudBuild) Build(ctx context.Context, req Request) (Result, error) {
	if !req.Push {
		return Result{}, errors.New("the remote builder pushes the image it builds, so the build needs a push (--push)")
	}
	if req.ContextFiles == nil {
		return Result{}, errors.New("the remote builder sends the build context to Cloud Build, and this build listed none")
	}
	if len(req.Secrets) > 0 {
		return Result{}, errors.New("the build has secrets, which are read from this machine's environment; " +
			"Cloud Build would have to store them in its build record, so a builder on this machine builds it (ADR 0088)")
	}
	if err := b.Ready(ctx); err != nil {
		return Result{}, err
	}
	dir, err := os.MkdirTemp("", "dexaflow-cloudbuild-")
	if err != nil {
		return Result{}, fmt.Errorf("making the files of the Cloud Build submission: %w", err)
	}
	defer os.RemoveAll(dir) //nolint:errcheck // best-effort cleanup of a temporary directory
	archive, config, err := b.prepare(dir, req)
	if err != nil {
		return Result{}, err
	}
	b.noteRegistry(req)
	built, err := b.run(ctx, req, archive, config)
	if err != nil {
		return Result{}, err
	}
	res := Result{Path: PathRemote, Image: req.Image, Platforms: req.Platforms, Pushed: true}
	if !req.Pin {
		return res, nil
	}
	pinned, err := b.registry().Resolve(ctx, req.Image)
	if err != nil {
		return res, fmt.Errorf("%w: reading the digest of the image %q Cloud Build pushed: %w", ErrPinRead, req.Image, err)
	}
	if err := built.checkDigest(req.Image, pinned); err != nil {
		return Result{}, err
	}
	res.Ref = pinned.Ref
	return res, nil
}

// noteRegistry says, for an image outside Google Cloud's registries, that
// the push may be refused: Cloud Build pushes the images of a build with its
// service account, which Artifact Registry and GCR accept and other
// registries do not know.
func (b CloudBuild) noteRegistry(req Request) {
	ref, err := name.ParseReference(req.Image)
	if err != nil || req.Stderr == nil {
		return
	}
	host := ref.Context().RegistryStr()
	if c, ok := DetectCloudRegistry(host); ok && c.Provider == ProviderGoogle {
		return
	}
	fmt.Fprintf(req.Stderr, "note: Cloud Build pushes with its service account, which Artifact Registry and GCR accept; "+ //nolint:errcheck // a progress note
		"%s is neither and may refuse the push\n", host)
}

// prepare writes the source archive and the build config of req into dir.
func (b CloudBuild) prepare(dir string, req Request) (archive, config string, err error) {
	archive = filepath.Join(dir, "context.tar.gz")
	if werr := writeContextArchive(archive, req.Dockerfile, req.ContextFiles, b.maxContext()); werr != nil {
		return "", "", werr
	}
	data, err := json.Marshal(b.config(req))
	if err != nil {
		return "", "", fmt.Errorf("encoding the Cloud Build config: %w", err)
	}
	config = filepath.Join(dir, "cloudbuild.json")
	if err := os.WriteFile(config, data, 0o600); err != nil {
		return "", "", fmt.Errorf("writing the Cloud Build config: %w", err)
	}
	return archive, config, nil
}

// orDiscard is w, or io.Discard when w is nil.
func orDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

// writeContextArchive writes the build context as a gzipped tar at path:
// the listed files, without any that would take the Dockerfile's name, and
// the Dockerfile under remoteDockerfile, with the fixed dates and normalized
// modes of every archive the CLI packs. It stops as soon as the archive
// passes limit bytes.
func writeContextArchive(path, dockerfile string, files []LayerFile, limit int64) error {
	entries := slices.DeleteFunc(slices.Clone(files), func(f LayerFile) bool { return f.Path == remoteDockerfile })
	entries = append(entries, LayerFile{Path: remoteDockerfile, Source: dockerfile})
	slices.SortFunc(entries, func(a, b LayerFile) int { return strings.Compare(a.Path, b.Path) })
	f, err := os.Create(path) //nolint:gosec // G304: a file in a directory this process made.
	if err != nil {
		return fmt.Errorf("making the build context archive: %w", err)
	}
	capped := &cappedWriter{w: f, left: limit}
	zw := gzip.NewWriter(capped)
	werr := writeTar(zw, kindContext, "", entries)
	if werr == nil {
		werr = zw.Close()
	}
	if cerr := f.Close(); werr == nil && cerr != nil {
		werr = cerr
	}
	if capped.over {
		return fmt.Errorf("the build context is over the %d MiB (compressed) the remote builder uploads; "+
			"leave large files out with .dockerignore or exclude_paths", limit>>20)
	}
	if werr != nil {
		return fmt.Errorf("writing the build context archive: %w", werr)
	}
	return nil
}

// defaultMaxContext is the largest compressed build context the remote
// builder uploads when CloudBuild.MaxContext is zero.
const defaultMaxContext int64 = 2 << 30

// errContextTooLarge stops the archive at its cap.
var errContextTooLarge = errors.New("the build context archive is over its cap")

// cappedWriter writes to w until left bytes are written, then refuses.
type cappedWriter struct {
	w    io.Writer
	left int64
	over bool
}

// Write implements io.Writer.
func (c *cappedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > c.left {
		c.over = true
		return 0, errContextTooLarge
	}
	c.left -= int64(len(p))
	n, err := c.w.Write(p)
	if err != nil {
		return n, fmt.Errorf("writing the build context archive: %w", err)
	}
	return n, nil
}

// CloudBuildSourceBucket is the bucket where gcloud builds submit stages the
// source archive of a build in project, as gcloud names it: the project id
// with ':' and '.' replaced by '_' and "google" by "elgoog", which bucket
// names may not contain.
func CloudBuildSourceBucket(project string) string {
	safe := strings.NewReplacer(":", "_", ".", "_").Replace(project)
	safe = strings.ReplaceAll(safe, "google", "elgoog")
	return "gs://" + safe + "_cloudbuild/source"
}
