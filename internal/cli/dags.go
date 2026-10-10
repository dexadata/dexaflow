package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

// newDagsCommand groups DAG-management subcommands.
func newDagsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dags",
		Short: "Manage registered DAGs.",
	}
	cmd.AddCommand(newDagsListCommand())
	cmd.AddCommand(newDagsDeleteCommand())
	return cmd
}

// newDagsListCommand lists registered DAGs — the read counterpart to delete, so
// verifying a registration no longer means hitting /api/v2/dags by hand.
func newDagsListCommand() *cobra.Command {
	var serverURL, token string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List registered DAGs.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			base, bearer, err := resolveServerToken(cmd, serverURL, token)
			if err != nil {
				return err
			}
			c, err := apiclient.New(base, bearer)
			if err != nil {
				return err
			}
			resp, err := c.ListDagsWithResponse(cmdContext(cmd), nil)
			if err != nil {
				return fmt.Errorf("listing DAGs: %w", err)
			}
			if resp.StatusCode() != http.StatusOK {
				return apiStatusError(resp.StatusCode(), resp.Body)
			}
			return printDagList(cmd.OutOrStdout(), resp.JSON200)
		},
	}
	cmd.Flags().StringVar(&serverURL, "server", "", "control plane base URL (default: config server_url)")
	cmd.Flags().StringVar(&token, "token", os.Getenv("LEOFLOW_TOKEN"), "JWT bearer token (default: config token)")
	return cmd
}

// printDagList renders the DAG collection as an aligned table (DAG_ID, PAUSED,
// OWNERS), or a friendly line when nothing is registered.
func printDagList(w io.Writer, coll *apiclient.DAGCollection) error {
	if coll == nil || coll.Dags == nil || len(*coll.Dags) == 0 {
		_, err := fmt.Fprintln(w, "No DAGs registered.")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "DAG_ID\tPAUSED\tOWNERS"); err != nil {
		return err
	}
	for _, d := range *coll.Dags {
		id := ""
		if d.DagId != nil {
			id = *d.DagId
		}
		paused := "no"
		if d.IsPaused != nil && *d.IsPaused {
			paused = "yes"
		}
		owners := ""
		if d.Owners != nil {
			owners = strings.Join(*d.Owners, ",")
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\n", id, paused, owners); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// newDagsDeleteCommand clears a DAG's history or deregisters it (ADR 0020).
func newDagsDeleteCommand() *cobra.Command {
	var serverURL, token string
	var deregister bool
	cmd := &cobra.Command{
		Use:   "delete <dag_id>",
		Short: "Clear a DAG's run history, or fully deregister it with --deregister.",
		Long: "By default this clears the DAG's run history but keeps the DAG and its " +
			"versions registered — the same as the UI trash button (ADR 0020). With " +
			"--deregister it removes the DAG artifact entirely.\n\n" +
			"GitOps note: deregister is not permanent while the DAG's source still exists. " +
			"In production the next deploy re-registers it as a new version; under " +
			"`dexaflow lite` the watcher re-registers it on the next reload — delete the " +
			"DAG's file to stop that.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dagID := args[0]
			base, bearer, err := resolveServerToken(cmd, serverURL, token)
			if err != nil {
				return err
			}
			status, body, err := deleteDag(cmdContext(cmd), base, bearer, dagID, deregister)
			if err != nil {
				return err
			}
			if status >= http.StatusMultipleChoices {
				return apiStatusError(status, []byte(body))
			}
			out := cmd.OutOrStdout()
			if deregister {
				if _, werr := fmt.Fprintf(out, "Deregistered %q (artifact removed).\n", dagID); werr != nil {
					return werr
				}
				_, werr := fmt.Fprintln(out, "Note: deregister is not permanent while the source exists — in prod the next "+
					"deploy re-registers it; under `dexaflow lite` the next reload does, unless you delete the file.")
				return werr
			}
			_, werr := fmt.Fprintf(out, "Cleared %q's run history (DAG still registered). Use --deregister to remove the artifact.\n", dagID)
			return werr
		},
	}
	cmd.Flags().StringVar(&serverURL, "server", "", "control plane base URL (default: config server_url)")
	cmd.Flags().StringVar(&token, "token", os.Getenv("LEOFLOW_TOKEN"), "JWT bearer token (default: config token)")
	cmd.Flags().BoolVar(&deregister, "deregister", false, "remove the DAG artifact entirely, not just its run history")
	return cmd
}

// deleteDag calls DELETE /api/v2/dags/{id}, adding ?deregister=true to remove the
// artifact rather than only clearing history (ADR 0020).
func deleteDag(ctx context.Context, serverURL, token, dagID string, deregister bool) (status int, body string, err error) {
	u := strings.TrimRight(serverURL, "/") + "/api/v2/dags/" + url.PathEscape(dagID)
	if deregister {
		u += "?deregister=true"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, http.NoBody)
	if err != nil {
		return 0, "", fmt.Errorf("building request: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("calling %s: %w", u, err)
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
