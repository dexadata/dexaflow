package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/spf13/cobra"

	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

// newAdminCommand groups the operator commands for a running control plane:
// health checks, DAG pausing, draining, and run inspection. Unlike the
// authoring loop (init → compile → push → deploy), these operate a deployed Pro
// install over the typed /api/v2 client — the "operate what is already running"
// verbs. The tree is deliberately extensible: tasks, connections, and variables
// slot in as further subcommands without reshaping it.
func newAdminCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "admin",
		Short: "Operate a running control plane (health, pause, drain, runs).",
		Long: "Operator commands for a running Leoflow control plane (Pro). These act " +
			"over the /api/v2 API — checking health, pausing DAGs, draining the control " +
			"plane before maintenance, and inspecting runs — and reuse the same " +
			"--server/--token/config precedence as `leoflow deploy`.",
	}
	cmd.AddCommand(
		newAdminHealthCommand(),
		newAdminDagsCommand(),
		newAdminDrainCommand(),
		newAdminRunsCommand(),
		newAdminUsersCommand(),
	)
	return cmd
}

// adminFlags carries the server/token resolution flags shared by every admin
// subcommand.
type adminFlags struct {
	serverURL string
	token     string
}

// addAdminFlags registers the --server/--token flags with the deploy-style
// precedence: the explicit flag, then the LEOFLOW_TOKEN env (as the token flag
// default), then the persisted config written by `leoflow auth login`.
func addAdminFlags(cmd *cobra.Command, f *adminFlags) {
	cmd.Flags().StringVar(&f.serverURL, "server", "", "control plane base URL (default: config server_url)")
	cmd.Flags().StringVar(&f.token, "token", os.Getenv("LEOFLOW_TOKEN"), "JWT bearer token (default: config token)")
}

// adminClient resolves the server/token precedence and builds a typed /api/v2
// client, returning it alongside the resolved base URL (for display).
func adminClient(cmd *cobra.Command, f adminFlags) (client *apiclient.ClientWithResponses, baseURL string, err error) {
	serverURL, token, rerr := resolveServerToken(cmd, f.serverURL, f.token)
	if rerr != nil {
		return nil, "", rerr
	}
	c, nerr := apiclient.New(serverURL, token)
	if nerr != nil {
		return nil, "", nerr
	}
	return c, serverURL, nil
}

// writeLine writes s followed by a newline, propagating any write error so the
// errcheck linter is satisfied at the call sites.
func writeLine(w io.Writer, s string) error {
	_, err := fmt.Fprintln(w, s)
	return err
}

// listAllDags pages through GET /api/v2/dags and returns every registered DAG.
// Draining and `pause --all` need the full set, so it follows total_entries
// rather than trusting a single page.
func listAllDags(ctx context.Context, c *apiclient.ClientWithResponses) ([]apiclient.DAG, error) {
	const page = 100
	var out []apiclient.DAG
	offset := 0
	for {
		limit := page
		off := offset
		resp, err := c.ListDagsWithResponse(ctx, &apiclient.ListDagsParams{Limit: &limit, Offset: &off})
		if err != nil {
			return nil, fmt.Errorf("listing DAGs: %w", err)
		}
		if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
			return nil, fmt.Errorf("listing DAGs returned %d: %s", resp.StatusCode(), string(resp.Body))
		}
		got := 0
		if resp.JSON200.Dags != nil {
			out = append(out, (*resp.JSON200.Dags)...)
			got = len(*resp.JSON200.Dags)
		}
		offset += got
		total := 0
		if resp.JSON200.TotalEntries != nil {
			total = *resp.JSON200.TotalEntries
		}
		if got == 0 || offset >= total {
			return out, nil
		}
	}
}

// listAllDagIDs returns the id of every registered DAG (skipping any DAG the
// server returns without one).
func listAllDagIDs(ctx context.Context, c *apiclient.ClientWithResponses) ([]string, error) {
	dags, err := listAllDags(ctx, c)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(dags))
	for _, d := range dags {
		if d.DagId != nil {
			ids = append(ids, *d.DagId)
		}
	}
	return ids, nil
}

// deref returns the pointed-to string, or "" for a nil pointer.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
