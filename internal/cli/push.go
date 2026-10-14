package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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
			status, body, err := pushVersion(cmdContext(cmd), serverURL, token, spec.DagID, data)
			if err != nil {
				return err
			}
			if status >= http.StatusMultipleChoices {
				return versionConflictHint(apiStatusError(status, []byte(body)), spec.DagVersion, labelUnknown)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Registered %q with %s (HTTP %d)\n", spec.DagID, serverURL, status)
			return err
		},
	}
	cmd.Flags().StringVar(&serverURL, "server", "", "control plane base URL, over --target (default: the target in use, then server_url from the config file)")
	cmd.Flags().StringVar(&token, "token", os.Getenv("LEOFLOW_TOKEN"), "JWT bearer token (default: the session saved for the server)")
	return cmd
}

// pushVersion POSTs a dag.json to the control plane's versions endpoint.
func pushVersion(ctx context.Context, serverURL, token, dagID string, spec []byte) (status int, body string, err error) {
	return pushVersionPaused(ctx, serverURL, token, dagID, spec, nil)
}

// pushVersionPaused is pushVersion that, when paused is set, asks the server
// to leave the DAG in that state (the is_paused query parameter).
func pushVersionPaused(ctx context.Context, serverURL, token, dagID string, spec []byte, paused *bool) (status int, body string, err error) {
	status, _, raw, err := postVersion(ctx, versionPost{serverURL: serverURL, token: token, dagID: dagID, spec: spec, paused: paused})
	return status, string(raw), err
}

// versionPost is one POST of a spec to /api/v2/dags/{dag_id}/versions: a
// register, or with dryRun the server's dry run of one.
type versionPost struct {
	serverURL, token, dagID string
	spec                    []byte
	paused                  *bool
	dryRun                  bool
}

// postVersion sends p and returns the answer's status, headers and body.
func postVersion(ctx context.Context, p versionPost) (status int, header http.Header, body []byte, err error) {
	url := strings.TrimRight(p.serverURL, "/") + "/api/v2/dags/" + p.dagID + "/versions"
	var query []string
	if p.dryRun {
		query = append(query, "dry_run=true")
	}
	if p.paused != nil {
		query = append(query, "is_paused="+strconv.FormatBool(*p.paused))
	}
	if len(query) > 0 {
		url += "?" + strings.Join(query, "&")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(p.spec))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("posting to %s: %w", url, err)
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
