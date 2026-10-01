package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/config"
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
			if serverURL == "" || token == "" {
				cfg, cerr := config.Load(configFilePath(cmd), cmd.Flags())
				if cerr != nil {
					return cerr
				}
				if serverURL == "" {
					serverURL = cfg.ServerURL
				}
				if token == "" {
					token = cfg.Token
				}
			}
			status, body, err := pushVersion(cmdContext(cmd), serverURL, token, spec.DagID, data)
			if err != nil {
				return err
			}
			if status >= http.StatusMultipleChoices {
				return apiStatusError(status, []byte(body))
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Registered %q with %s (HTTP %d)\n", spec.DagID, serverURL, status)
			return err
		},
	}
	cmd.Flags().StringVar(&serverURL, "server", "", "control plane base URL (default: config server_url)")
	cmd.Flags().StringVar(&token, "token", os.Getenv("LEOFLOW_TOKEN"), "JWT bearer token")
	return cmd
}

// pushVersion POSTs a dag.json to the control plane's versions endpoint.
func pushVersion(ctx context.Context, serverURL, token, dagID string, spec []byte) (status int, body string, err error) {
	url := strings.TrimRight(serverURL, "/") + "/api/v2/dags/" + dagID + "/versions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(spec))
	if err != nil {
		return 0, "", fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("posting to %s: %w", url, err)
	}
	raw, readErr := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	if readErr != nil {
		return resp.StatusCode, "", fmt.Errorf("reading response: %w", readErr)
	}
	if closeErr != nil {
		return resp.StatusCode, "", fmt.Errorf("closing response: %w", closeErr)
	}
	return resp.StatusCode, string(raw), nil
}
