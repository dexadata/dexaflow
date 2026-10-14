package build

import (
	"bytes"
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
	// on PATH), or nil when it can.
	Ready() error
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
func (b CloudBuild) Ready() error {
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
// Build and waits for the build, which pushes the image, then reads the
// pushed image's digest from the registry when a pin is asked for.
func (b CloudBuild) Build(ctx context.Context, req Request) (Result, error) {
	if !req.Push {
		return Result{}, errors.New("the remote builder pushes the image it builds, so the build needs a push (--push)")
	}
	if req.ContextFiles == nil {
		return Result{}, errors.New("the remote builder sends the build context to Cloud Build, and this build listed none")
	}
	if err := b.Ready(); err != nil {
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
	if serr := b.submit(ctx, req, archive, config); serr != nil {
		return Result{}, serr
	}
	res := Result{Path: PathRemote, Image: req.Image, Platforms: req.Platforms, Pushed: true}
	if !req.Pin {
		return res, nil
	}
	pinned, err := b.registry().Resolve(ctx, req.Image)
	if err != nil {
		return Result{}, fmt.Errorf("reading the digest of the image %q Cloud Build pushed: %w", req.Image, err)
	}
	res.Ref = pinned.Ref
	return res, nil
}

// prepare writes the source archive and the build config of req into dir.
func (b CloudBuild) prepare(dir string, req Request) (archive, config string, err error) {
	archive = filepath.Join(dir, "context.tar.gz")
	if werr := writeContextArchive(archive, req.Dockerfile, req.ContextFiles); werr != nil {
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

// submit runs `gcloud builds submit` and waits for the build. Its output goes
// to the request's writers; a failure keeps the last line gcloud wrote to
// stderr and says what to check.
func (b CloudBuild) submit(ctx context.Context, req Request, archive, config string) error {
	args := []string{"builds", "submit", archive, "--config=" + config, "--project=" + b.Project}
	if b.Region != "" {
		args = append(args, "--region="+b.Region)
	}
	if b.WorkerPool != "" {
		args = append(args, "--worker-pool="+b.WorkerPool)
	}
	if b.ServiceAccount != "" {
		args = append(args, "--service-account="+b.ServiceAccount)
	}
	args = append(args, "--quiet")
	//nolint:gosec // G204: gcloud with fixed subcommands; every value is validated config passed as --flag=value.
	c := exec.CommandContext(ctx, b.tool(), args...)
	var tail bytes.Buffer
	c.Stdin = strings.NewReader("")
	c.Stdout = orDiscard(req.Stdout)
	c.Stderr = io.MultiWriter(orDiscard(req.Stderr), &tail)
	if err := c.Run(); err != nil {
		return fmt.Errorf("building %q on Cloud Build in project %s: gcloud builds submit: %w%s\n"+
			"  check that gcloud is signed in (`gcloud auth login`, or a service account with `gcloud auth activate-service-account`), "+
			"that the account has roles/cloudbuild.builds.editor on the project, "+
			"and that the project's Cloud Build service account may push to the repository (roles/artifactregistry.writer)",
			req.Image, b.Project, err, lastLine(tail.String()))
	}
	return nil
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
// modes of every archive the CLI packs.
func writeContextArchive(path, dockerfile string, files []LayerFile) error {
	entries := slices.DeleteFunc(slices.Clone(files), func(f LayerFile) bool { return f.Path == remoteDockerfile })
	entries = append(entries, LayerFile{Path: remoteDockerfile, Source: dockerfile})
	slices.SortFunc(entries, func(a, b LayerFile) int { return strings.Compare(a.Path, b.Path) })
	f, err := os.Create(path) //nolint:gosec // G304: a file in a directory this process made.
	if err != nil {
		return fmt.Errorf("making the build context archive: %w", err)
	}
	zw := gzip.NewWriter(f)
	werr := writeTar(zw, kindContext, "", entries)
	if werr == nil {
		werr = zw.Close()
	}
	if cerr := f.Close(); werr == nil && cerr != nil {
		werr = cerr
	}
	if werr != nil {
		return fmt.Errorf("writing the build context archive: %w", werr)
	}
	return nil
}
