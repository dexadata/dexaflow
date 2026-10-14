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

	"github.com/dexadata/dexaflow/internal/domain"
)

func newPushCommand() *cobra.Command {
	var serverURL, token string
	cmd := &cobra.Command{
		Use:   "push <dag.json>",
		Short: "Register a compiled dag.json with the control plane.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := os.ReadFile(args[0]) //nolint:gosec // path supplied by the operator on the CLI
			if err != nil {
				return fmt.Errorf("reading %s: %w", args[0], err)
			}
			var spec domain.DAGSpec
			if jerr := json.Unmarshal(data, &spec); jerr != nil {
				return fmt.Errorf("parsing %s: %w", args[0], jerr)
			}
			if verr := spec.Validate(); verr != nil {
				return fmt.Errorf("invalid dag.json: %w", verr)
			}
			serverURL, token, err = resolveServerToken(cmd, serverURL, token)
			if err != nil {
				return err
			}
			ctx := cmdContext(cmd)
			build := collectBuildInfo(ctx, filepath.Dir(args[0]), os.Getenv)
			status, _, raw, err := postVersion(ctx, versionPost{serverURL: serverURL, token: token, dagID: spec.DagID, spec: data, build: &build})
			if err != nil {
				return err
			}
			if status >= http.StatusMultipleChoices {
				return versionConflictHint(apiStatusError(status, raw), spec.DagVersion, labelUnknown)
			}
			printRegisterWarnings(cmd.ErrOrStderr(), raw)
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Registered %q with %s (HTTP %d)\n", spec.DagID, serverURL, status)
			return err
		},
	}
	cmd.Flags().StringVar(&serverURL, "server", "", "control plane base URL, over --target (default: the target in use, then server_url from the config file)")
	cmd.Flags().StringVar(&token, "token", os.Getenv("LEOFLOW_TOKEN"), "JWT bearer token (default: the session saved for the server)")
	return cmd
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
