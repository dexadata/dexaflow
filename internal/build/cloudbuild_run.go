package build

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// How a Cloud Build build is run (ADR 0083): submitted with --async, so
// gcloud returns the build id at once; its log streamed with `gcloud builds
// log --stream` when it logs to Cloud Storage (a build with a service
// account of its own logs to Cloud Logging only, which that command cannot
// read, so the build is described until it ends and its log is named);
// then described for its status and the digest of the image it pushed. A
// stopped command interrupts gcloud and cancels the build, which would
// otherwise go on and push.

// cloudBuildPoll is how often a build whose log is not streamed is
// described.
const cloudBuildPoll = 5 * time.Second

// cloudBuildWaitDelay bounds how long gcloud has to exit after SIGINT
// before it is killed.
const cloudBuildWaitDelay = 10 * time.Second

// cloudBuildCancelTimeout bounds `gcloud builds cancel` after a stop.
const cloudBuildCancelTimeout = 30 * time.Second

// buildID is the form of a Cloud Build build id, a UUID. An id is checked
// against it before it reaches the argv of gcloud.
var buildID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// cloudBuildRun is the build resource gcloud prints with --format=json,
// the fields the CLI reads.
type cloudBuildRun struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	StatusDetail string `json:"statusDetail"` //nolint:tagliatelle // Cloud Build's field name
	LogURL       string `json:"logUrl"`       //nolint:tagliatelle // Cloud Build's field name
	FailureInfo  struct {
		Detail string `json:"detail"`
	} `json:"failureInfo"` //nolint:tagliatelle // Cloud Build's field name
	Results struct {
		Images []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
		} `json:"images"`
	} `json:"results"`
}

// done reports whether the build has ended, in success or not.
func (r cloudBuildRun) done() bool {
	switch r.Status {
	case "SUCCESS", "FAILURE", "INTERNAL_ERROR", "TIMEOUT", "CANCELLED", "EXPIRED": //nolint:misspell // Cloud Build's status names
		return true
	}
	return false
}

// digestOf is the digest the build reports for image, "" when it reports
// none.
func (r cloudBuildRun) digestOf(image string) string {
	for _, i := range r.Results.Images {
		if i.Name == image {
			return i.Digest
		}
	}
	if len(r.Results.Images) == 1 {
		return r.Results.Images[0].Digest
	}
	return ""
}

// checkDigest refuses a pin that is not the image the build pushed: the
// tag was read after the build, and a concurrent push of the same tag would
// otherwise be registered.
func (r cloudBuildRun) checkDigest(image string, pinned Pinned) error {
	want := r.digestOf(image)
	if want == "" {
		return fmt.Errorf("the Cloud Build build %s reports no digest for %s, so the image it pushed cannot be told apart from another push of the tag", r.ID, image)
	}
	got := ""
	if i := strings.LastIndex(pinned.Ref, "@"); i >= 0 {
		got = pinned.Ref[i+1:]
	}
	if got != want {
		return fmt.Errorf("the tag %s names %s in the registry, not %s, which Cloud Build build %s pushed: "+
			"another push of the same tag happened meanwhile; deploy again", image, got, want, r.ID)
	}
	return nil
}

// poll returns how often a build is described.
func (b CloudBuild) poll() time.Duration {
	if b.Poll > 0 {
		return b.Poll
	}
	return cloudBuildPoll
}

// where is the --project and --region of every gcloud builds command.
func (b CloudBuild) where() []string {
	args := []string{"--project=" + b.Project}
	if b.Region != "" {
		args = append(args, "--region="+b.Region)
	}
	return args
}

// run submits the build and follows it until it ends in success.
func (b CloudBuild) run(ctx context.Context, req Request, archive, config string) (cloudBuildRun, error) {
	submitted, err := b.submit(ctx, req, archive, config)
	if err != nil {
		return cloudBuildRun{}, err
	}
	b.note(req, "cloud-build: build %s submitted to project %s; log: %s\n", submitted.ID, b.Project, submitted.LogURL)
	if b.ServiceAccount != "" {
		b.note(req, "cloud-build: a build with its own service account logs to Cloud Logging only, which gcloud does not stream; "+
			"read it with  %s\n", b.loggingCommand(submitted.ID))
	}
	final, err := b.follow(ctx, req, submitted.ID)
	if err != nil {
		return cloudBuildRun{}, err
	}
	if final.Status != "SUCCESS" {
		detail := final.FailureInfo.Detail
		if detail == "" {
			detail = final.StatusDetail
		}
		return cloudBuildRun{}, fmt.Errorf("building %q on Cloud Build: build %s ended %s: %s\n  read its log: %s%s",
			req.Image, final.ID, final.Status, detail, b.logCommand(final.ID), orURL(final.LogURL))
	}
	return final, nil
}

// orURL is " (url)", or "" for no url.
func orURL(url string) string {
	if url == "" {
		return ""
	}
	return " (" + url + ")"
}

// logCommand is how to read the log of build id.
func (b CloudBuild) logCommand(id string) string {
	if b.ServiceAccount != "" {
		return b.loggingCommand(id)
	}
	return strings.Join(append([]string{"gcloud", "builds", "log", id}, b.where()...), " ")
}

// loggingCommand reads the Cloud Logging log of build id.
func (b CloudBuild) loggingCommand(id string) string {
	return fmt.Sprintf("gcloud logging read 'resource.type=\"build\" AND resource.labels.build_id=%q' --project=%s", id, b.Project)
}

// note writes a progress note to the build's standard error, if it has one.
func (CloudBuild) note(req Request, format string, args ...any) {
	if req.Stderr != nil {
		fmt.Fprintf(req.Stderr, format, args...) //nolint:errcheck // a progress note
	}
}

// submit runs `gcloud builds submit --async` and returns the build it
// created. A failure keeps the last line gcloud wrote to stderr and says
// what to check.
func (b CloudBuild) submit(ctx context.Context, req Request, archive, config string) (cloudBuildRun, error) {
	args := append([]string{"builds", "submit", archive, "--config=" + config}, b.where()...)
	if b.WorkerPool != "" {
		args = append(args, "--worker-pool="+b.WorkerPool)
	}
	if b.ServiceAccount != "" {
		args = append(args, "--service-account="+b.ServiceAccount)
	}
	args = append(args, "--async", "--format=json", "--quiet")
	var out bytes.Buffer
	tail, err := b.gcloud(ctx, &out, req.Stderr, args...)
	if err != nil && ctx.Err() != nil {
		// gcloud may have created the build before it was stopped, and the
		// id it would have printed is lost.
		return cloudBuildRun{}, fmt.Errorf("the build was stopped while gcloud submitted it (%w): a Cloud Build build may have started; "+
			"find it with  gcloud builds list --filter=tags=dexaflow --ongoing %s  and stop it with  gcloud builds cancel <id> %s",
			ctx.Err(), strings.Join(b.where(), " "), strings.Join(b.where(), " "))
	}
	if err != nil {
		return cloudBuildRun{}, fmt.Errorf("building %q on Cloud Build in project %s: gcloud builds submit: %w%s\n"+
			"  check that gcloud is signed in (`gcloud auth login`, or a service account with `gcloud auth activate-service-account`), "+
			"that the account has roles/cloudbuild.builds.editor on the project, "+
			"and that the project's Cloud Build service account may push to the repository (roles/artifactregistry.writer)",
			req.Image, b.Project, err, tail)
	}
	var run cloudBuildRun
	if jerr := json.Unmarshal(out.Bytes(), &run); jerr != nil {
		return cloudBuildRun{}, fmt.Errorf("building %q on Cloud Build: reading the build gcloud builds submit printed: %w", req.Image, jerr)
	}
	if !buildID.MatchString(run.ID) {
		return cloudBuildRun{}, fmt.Errorf("building %q on Cloud Build: gcloud builds submit printed %q, which is not a build id; "+
			"find the build with  gcloud builds list --filter=tags=dexaflow --ongoing %s", req.Image, run.ID, strings.Join(b.where(), " "))
	}
	return run, nil
}

// follow waits for build id to end: it streams the log when gcloud can,
// then describes the build until it has ended. A stop cancels the build.
func (b CloudBuild) follow(ctx context.Context, req Request, id string) (cloudBuildRun, error) {
	if b.ServiceAccount == "" {
		// The stream ends with the build. A stream that fails says nothing
		// about the build: the describe below does.
		_, _ = b.gcloud(ctx, req.Stdout, req.Stderr, append([]string{"builds", "log", id, "--stream"}, b.where()...)...) //nolint:errcheck // see above
	}
	for {
		if ctx.Err() != nil {
			return cloudBuildRun{}, b.cancel(ctx, id, fmt.Errorf("the build was stopped: %w", ctx.Err()))
		}
		run, err := b.describeRetrying(ctx, id)
		if ctx.Err() != nil {
			return cloudBuildRun{}, b.cancel(ctx, id, fmt.Errorf("the build was stopped: %w", ctx.Err()))
		}
		if err != nil {
			return cloudBuildRun{}, b.cancel(ctx, id, err)
		}
		if run.done() {
			return run, nil
		}
		select {
		case <-ctx.Done():
		case <-time.After(b.poll()):
		}
	}
}

// describeTries is how many times a build is described before a failure
// to read it ends the follow.
const describeTries = 4

// describeRetrying describes build id, trying again after a failure (an
// expired token, a network blip) up to describeTries times, waiting Poll,
// then twice as long each time.
func (b CloudBuild) describeRetrying(ctx context.Context, id string) (cloudBuildRun, error) {
	wait := b.poll()
	var err error
	for try := 1; ; try++ {
		var run cloudBuildRun
		if run, err = b.describe(ctx, id); err == nil || try == describeTries || ctx.Err() != nil {
			return run, err
		}
		select {
		case <-ctx.Done():
			return cloudBuildRun{}, err
		case <-time.After(wait):
		}
		wait *= 2
	}
}

// describe reads build id.
func (b CloudBuild) describe(ctx context.Context, id string) (cloudBuildRun, error) {
	var out bytes.Buffer
	tail, err := b.gcloud(ctx, &out, nil, append([]string{"builds", "describe", id, "--format=json"}, b.where()...)...)
	if err != nil {
		return cloudBuildRun{}, fmt.Errorf("reading Cloud Build build %s: %w%s", id, err, tail)
	}
	var run cloudBuildRun
	if jerr := json.Unmarshal(out.Bytes(), &run); jerr != nil {
		return cloudBuildRun{}, fmt.Errorf("reading Cloud Build build %s: %w", id, jerr)
	}
	return run, nil
}

// cancel cancels build id once following it ended with why (a stop, or a
// build that could not be read), so the build does not go on and push
// unwatched, and says so. It runs even when ctx is canceled, for at most
// cloudBuildCancelTimeout.
func (b CloudBuild) cancel(ctx context.Context, id string, why error) error {
	ctx, stop := context.WithTimeout(context.WithoutCancel(ctx), cloudBuildCancelTimeout)
	defer stop()
	command := strings.Join(append([]string{"gcloud", "builds", "cancel", id}, b.where()...), " ")
	if _, err := b.gcloud(ctx, nil, nil, append([]string{"builds", "cancel", id}, b.where()...)...); err != nil {
		return fmt.Errorf("%w; canceling Cloud Build build %s failed (%w): cancel it with  %s", why, id, err, command)
	}
	return fmt.Errorf("%w; Cloud Build build %s was canceled (%s)", why, id, command)
}

// gcloud runs gcloud with args and no input, writing its stdout to stdout
// and its stderr to stderr (either may be nil), and returns ": " and the
// last line of its stderr, for an error. A stop sends SIGINT, which gcloud
// handles, rather than killing it, and kills it after cloudBuildWaitDelay.
func (b CloudBuild) gcloud(ctx context.Context, stdout, stderr io.Writer, args ...string) (tail string, err error) {
	//nolint:gosec // G204: gcloud with fixed subcommands; every value is validated config passed as --flag=value.
	c := exec.CommandContext(ctx, b.tool(), args...)
	c.Cancel = func() error { return c.Process.Signal(os.Interrupt) }
	c.WaitDelay = cloudBuildWaitDelay
	var errBuf bytes.Buffer
	c.Stdin = strings.NewReader("")
	c.Stdout = orDiscard(stdout)
	c.Stderr = io.MultiWriter(orDiscard(stderr), &errBuf)
	if rerr := c.Run(); rerr != nil {
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return lastLine(errBuf.String()), fmt.Errorf("gcloud %s was stopped: %w", args[1], ctx.Err())
		}
		return lastLine(errBuf.String()), fmt.Errorf("gcloud %s %s: %w", args[0], args[1], rerr)
	}
	return "", nil
}
