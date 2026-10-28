package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/build"
	"github.com/dexadata/dexaflow/internal/domain"
)

func newPushCommand() *cobra.Command {
	var serverURL, token, image, project string
	cmd := &cobra.Command{
		Use:   "push <dag.json>",
		Short: "Register a compiled dag.json with the control plane.",
		Long: "Register a compiled dag.json with the control plane.\n\n" +
			"The image is registered by digest: an image given as a tag, in dag.json or with --image, is resolved " +
			"in its registry first (a read of up to 30s), with the credentials `dexaflow deploy` reads and the " +
			"registry.auth_method of the project (--project). A pinned image is used as given and reads nothing. When the " +
			"registry cannot be read, a tag from dag.json is registered as written, with a warning that says why; " +
			"a tag given with --image must resolve.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPush(cmd, args[0], serverURL, token, pushImage{ref: image, project: project})
		},
	}
	cmd.Flags().StringVar(&serverURL, "server", "", "control plane base URL, over --target (default: the target in use, then server_url from the config file)")
	cmd.Flags().StringVar(&token, "token", os.Getenv("LEOFLOW_TOKEN"), "JWT bearer token (default: the session saved for the server)")
	cmd.Flags().StringVar(&project, "project", "", "project directory whose dexaflow.yaml gives registry.auth_method (default: the dag.json's directory when it holds one, else the working directory)")
	cmd.Flags().StringVar(&image, "image", "", "image to register instead of the one in dag.json; a tag is resolved to its digest, a <repository>@sha256:<digest> is used as given")
	return cmd
}

// pushTimeout bounds the registry read that pins a pushed spec's image.
const pushTimeout = 30 * time.Second

// runPush registers the dag.json at path, its image pinned by digest
// (pinPushImage).
func runPush(cmd *cobra.Command, path, serverURL, token string, img pushImage) error {
	if img.ref != "" {
		if err := checkImageFlag(img.ref); err != nil {
			return err
		}
	}
	data, err := os.ReadFile(path) //nolint:gosec // path supplied by the operator on the CLI
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	var spec domain.DAGSpec
	if jerr := json.Unmarshal(data, &spec); jerr != nil {
		return fmt.Errorf("parsing %s: %w", path, jerr)
	}
	if verr := spec.Validate(); verr != nil {
		return fmt.Errorf("invalid dag.json: %w", verr)
	}
	serverURL, token, err = resolveServerToken(cmd, serverURL, token)
	if err != nil {
		return err
	}
	ctx := cmdContext(cmd)
	img.project = pushProjectDir(img.project, path)
	data, unpinned, err := pinPushImage(cmd, data, &spec, img)
	if err != nil {
		return err
	}
	info := collectBuildInfo(ctx, filepath.Dir(path), os.Getenv)
	status, _, raw, err := postVersion(ctx, versionPost{serverURL: serverURL, token: token, dagID: spec.DagID, spec: data, build: &info})
	if err != nil {
		return err
	}
	if status >= http.StatusMultipleChoices {
		return explainUnpinned(versionConflictHint(apiStatusError(status, raw), spec.DagVersion, labelUnknown), raw, unpinned)
	}
	printRegisterWarnings(cmd.ErrOrStderr(), raw)
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "Registered %q with %s (HTTP %d)\n", spec.DagID, serverURL, status)
	return err
}

// pushImage is what a push was told about its image: --image (ref, "" for
// the one in dag.json) and the project whose config reads the registry.
type pushImage struct {
	ref, project string
}

// checkImageFlag refuses an --image that is not an image reference: one the
// registry client cannot parse, an @ that is not a full sha256 pin, or a
// value an image reference cannot carry (domain.CheckImageReference).
func checkImageFlag(ref string) error {
	if err := domain.CheckImageReference("--image", ref); err != nil {
		return err
	}
	if strings.Contains(ref, "@") && !domain.IsDigestPinned(ref) {
		return fmt.Errorf("--image %q has a digest that is not a full sha256 pin; give <repository>@sha256:<64 hex>, or a tag to resolve", ref)
	}
	if err := build.CheckReference(ref); err != nil {
		return fmt.Errorf("--image: %w", err)
	}
	return nil
}

// pinPushImage settles the image a push registers (#1703): img.ref when
// set, else the spec's own, resolved to its digest in the registry when it
// is a tag, so a dag.json compiled by an older CLI, or an image tagged by
// another tool, registers the bytes the tag names now. It returns the spec
// to send, rewritten when the image changed, and the reason a tag from
// dag.json could not be pinned (unpinned), which is warned about here and is
// not fatal: a server that does not require a digest takes the tag as
// before. A tag given with --image must resolve: it is an explicit request
// to register that image by digest.
func pinPushImage(cmd *cobra.Command, data []byte, spec *domain.DAGSpec, img pushImage) (out []byte, unpinned, err error) {
	image := spec.Image
	if img.ref != "" {
		image = img.ref
	}
	if !domain.IsDigestPinned(image) {
		ctx, cancel := context.WithTimeout(cmdContext(cmd), pushTimeout)
		defer cancel()
		pinned, rerr := pushRegistry(ctx, cmd.ErrOrStderr(), img.project).Resolve(ctx, image)
		switch {
		case rerr != nil && img.ref != "":
			return nil, nil, fmt.Errorf("--image %q could not be pinned by digest: %w\n  %s", image, rerr, pinFix)
		case rerr != nil:
			unpinned = rerr
			//nolint:errcheck // advisory
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not pin image %q by digest, so it is registered as a tag: %v\n", image, rerr)
		default:
			//nolint:errcheck // informational
			fmt.Fprintf(cmd.OutOrStdout(), "image: %s (pinned from %s)\n", pinned.Ref, image)
			image = pinned.Ref
		}
	}
	if image == spec.Image {
		return data, unpinned, nil
	}
	out, err = repinImageInSpec(data, image)
	if err != nil {
		return nil, nil, err
	}
	spec.Image = image
	return out, unpinned, nil
}

// pinFix is how to get past an image a push could not pin.
const pinFix = "fix: make the registry readable from here (`dexaflow registry login <registry>` or `docker login <registry>`, or the session of the cloud CLI that owns it), " +
	"or pass --image <repository>@sha256:<digest>"

// pushProjectDir is the project whose config a push reads: flag (--project)
// when set, else the directory of the dag.json at path when it holds a
// project file, else the working directory, where `dexaflow compile <dir>
// -o dag.json` leaves the file.
func pushProjectDir(flag, path string) string {
	if flag != "" {
		return flag
	}
	if dir := filepath.Dir(path); fileExists(projectConfigPath(dir)) {
		return dir
	}
	return "."
}

// pushRegistry is the registry client of a push: the credentials the
// project config in dir asks for (registry.auth_method), or the default ones
// when dir holds none. A config that does not parse is warned about on w,
// and the defaults are used; a deprecated auth_method prints its note.
func pushRegistry(ctx context.Context, w io.Writer, dir string) build.Registry {
	if !fileExists(projectConfigPath(dir)) {
		return registryFor(ctx, nil)
	}
	cfg, err := loadProjectConfigLenient(dir)
	if err != nil {
		//nolint:errcheck // advisory
		fmt.Fprintf(w, "warning: reading registry.auth_method: %v; the registry is read with the default credentials\n", err)
		return registryFor(ctx, nil)
	}
	warnAuthMethod(w, cfg)
	return registryFor(ctx, cfg)
}

// explainUnpinned adds to err, a register the server refused, why the image
// was not pinned and how to fix it, when the refusal is deploy.require_digest's
// and the push could not pin the tag. Any other refusal is returned as is.
// The server's problem has no code of its own for this refusal, so its
// detail is matched.
func explainUnpinned(err error, body []byte, unpinned error) error {
	if unpinned == nil {
		return err
	}
	var problem struct {
		Detail string `json:"detail"`
	}
	if json.Unmarshal(body, &problem) != nil || !strings.Contains(problem.Detail, "deploy.require_digest=enforce") {
		return err
	}
	return fmt.Errorf("%w\n  the push could not pin the image by digest: %w\n  %s", err, unpinned, pinFix)
}

// printRegisterWarnings prints on w each warning of a register's answer,
// such as an image not pinned by digest under deploy.require_digest=warn
// (#1703). An answer without warnings, or one that is not JSON (an older
// server), prints nothing.
func printRegisterWarnings(w io.Writer, body []byte) {
	var answer struct {
		Warnings []string `json:"warnings"`
	}
	if json.Unmarshal(body, &answer) != nil {
		return
	}
	printNewWarnings(w, answer.Warnings, nil)
}

// pushVersion POSTs a dag.json to the control plane's versions endpoint.
func pushVersion(ctx context.Context, serverURL, token, dagID string, spec []byte) (status int, body string, err error) {
	status, _, raw, err := postVersion(ctx, versionPost{serverURL: serverURL, token: token, dagID: dagID, spec: spec})
	return status, string(raw), err
}

// versionPost is one POST of a spec to /api/v2/dags/{dag_id}/versions: a
// register, or with dryRun the server's dry run of one, on its
// /versions/dry_run route. promoted, set by `dexaflow promote`, names the
// control plane the version comes from, for the register's audit entry.
// build, when set, is where the version was built, sent as the build_*
// parameters (#1702).
type versionPost struct {
	serverURL, token, dagID string
	spec                    []byte
	paused                  *bool
	dryRun                  bool
	promoted                *domain.DagPromotion
	build                   *domain.BuildInfo
}

// query is the query string of p's POST, "" when it has none.
func (p versionPost) query() string {
	q := url.Values{}
	if p.paused != nil {
		q.Set("is_paused", strconv.FormatBool(*p.paused))
	}
	if p.promoted != nil {
		q.Set("promoted_from", p.promoted.Target)
		if p.promoted.Server != "" {
			q.Set("promoted_from_server", p.promoted.Server)
		}
	}
	addBuildInfo(q, p.build)
	if len(q) == 0 {
		return ""
	}
	return "?" + q.Encode()
}

// postVersion sends p and returns the answer's status, headers and body.
func postVersion(ctx context.Context, p versionPost) (status int, header http.Header, body []byte, err error) {
	u := strings.TrimRight(p.serverURL, "/") + "/api/v2/dags/" + p.dagID + "/versions"
	if p.dryRun {
		// A route of its own: an older server answers it 404, never
		// registering what a dry run sends.
		u += "/dry_run"
	}
	u += p.query()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(p.spec))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
	}
	resp, err := cliHTTPClient(30 * time.Second).Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("posting to %s: %w", u, err)
	}
	raw, readErr := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	if readErr != nil {
		return resp.StatusCode, resp.Header, nil, fmt.Errorf("reading response: %w", readErr)
	}
	if closeErr != nil {
		return resp.StatusCode, resp.Header, nil, fmt.Errorf("closing response: %w", closeErr)
	}
	return resp.StatusCode, resp.Header, raw, nil
}
