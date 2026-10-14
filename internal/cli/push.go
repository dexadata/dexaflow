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
	var serverURL, token, image string
	cmd := &cobra.Command{
		Use:   "push <dag.json>",
		Short: "Register a compiled dag.json with the control plane.",
		Long: "Register a compiled dag.json with the control plane.\n\n" +
			"The image is registered by digest: an image given as a tag, in dag.json or with --image, is resolved " +
			"in its registry first, with the credentials `dexaflow deploy` reads. When the registry cannot be read, " +
			"the tag is registered as written, with a warning that says why.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPush(cmd, args[0], serverURL, token, image)
		},
	}
	cmd.Flags().StringVar(&serverURL, "server", "", "control plane base URL, over --target (default: the target in use, then server_url from the config file)")
	cmd.Flags().StringVar(&token, "token", os.Getenv("LEOFLOW_TOKEN"), "JWT bearer token (default: the session saved for the server)")
	cmd.Flags().StringVar(&image, "image", "", "image to register instead of the one in dag.json; a tag is resolved to its digest, a <repository>@sha256:<digest> is used as given")
	return cmd
}

// pushTimeout bounds the registry read that pins a pushed spec's image.
const pushTimeout = 30 * time.Second

// runPush registers the dag.json at path, its image pinned by digest
// (pinPushImage).
func runPush(cmd *cobra.Command, path, serverURL, token, image string) error {
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
	data, unpinned, err := pinPushImage(cmd, filepath.Dir(path), data, &spec, image)
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

// pinPushImage settles the image a push registers (#1703): override when
// set, else the spec's own, resolved to its digest in the registry when it
// is a tag, so `compile --build --push` from an older CLI, or an image tagged
// by another tool, registers the bytes the tag names now. It returns the
// spec to send, rewritten when the image changed, and the reason a tag could
// not be pinned (unpinned), which is warned about here and not fatal: a
// server that does not require a digest takes the tag as before.
func pinPushImage(cmd *cobra.Command, dir string, data []byte, spec *domain.DAGSpec, override string) (out []byte, unpinned, err error) {
	image := spec.Image
	if override != "" {
		image = override
	}
	if !domain.IsDigestPinned(image) {
		ctx, cancel := context.WithTimeout(cmdContext(cmd), pushTimeout)
		defer cancel()
		pinned, rerr := pushRegistry(dir).Resolve(ctx, image)
		if rerr != nil {
			unpinned = rerr
			//nolint:errcheck // advisory
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not pin image %q by digest, so it is registered as a tag: %v\n", image, rerr)
		} else {
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

// pushRegistry is the registry client of a push: the credentials the
// dexaflow.yaml next to the dag.json asks for (registry.auth_method), or the
// default ones when there is none to read.
func pushRegistry(dir string) build.Registry {
	cfg, err := loadProjectConfigLenient(dir)
	if err != nil {
		cfg = nil
	}
	return registryFor(cfg)
}

// explainUnpinned adds to err, a register the server refused, why the image
// was not pinned and how to fix it, when the refusal is deploy.require_digest's
// and the push could not pin the tag. Any other refusal is returned as is.
func explainUnpinned(err error, body []byte, unpinned error) error {
	if unpinned == nil || !strings.Contains(string(body), "deploy.require_digest") {
		return err
	}
	return fmt.Errorf("%w\n  the push could not pin the image by digest: %v\n"+
		"  fix: make the registry readable from here (`docker login <registry>`, or the session of the cloud CLI that owns it), "+
		"or pass --image <repository>@sha256:<digest>", err, unpinned)
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
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
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
