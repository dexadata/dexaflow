package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/spf13/cobra"

	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

// newAdminDagsCommand groups the DAG pause/unpause operator commands.
func newAdminDagsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dags",
		Short: "Pause or unpause registered DAGs.",
	}
	cmd.AddCommand(newAdminSetPausedCommand(true), newAdminSetPausedCommand(false))
	return cmd
}

// newAdminSetPausedCommand builds either `pause` or `unpause`, depending on the
// target is_paused value, sharing one implementation. With --all it discovers
// every registered DAG and PATCHes each; otherwise it acts on a single dag_id.
func newAdminSetPausedCommand(paused bool) *cobra.Command {
	var f adminFlags
	var all bool
	verb, titled := "unpause", "Unpause"
	if paused {
		verb, titled = "pause", "Pause"
	}
	cmd := &cobra.Command{
		Use:   verb + " [dag_id]",
		Short: titled + " a DAG (PATCH is_paused), or every DAG with --all.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, err := adminClient(cmd, f)
			if err != nil {
				return err
			}
			ctx := cmdContext(cmd)
			ids, err := resolvePauseTargets(ctx, c, args, all, verb)
			if err != nil {
				return err
			}
			return setPausedForDAGs(ctx, cmd.OutOrStdout(), c, ids, paused)
		},
	}
	addAdminFlags(cmd, &f)
	cmd.Flags().BoolVar(&all, "all", false, verb+" every registered DAG")
	return cmd
}

// resolvePauseTargets turns the args and --all flag into the list of dag_ids to
// act on, erroring on the ambiguous "--all plus a dag_id" and "neither" cases.
func resolvePauseTargets(ctx context.Context, c *apiclient.ClientWithResponses, args []string, all bool, verb string) ([]string, error) {
	if all {
		if len(args) > 0 {
			return nil, fmt.Errorf("--all %ss every DAG; do not also pass a dag_id", verb)
		}
		return listAllDagIDs(ctx, c)
	}
	if len(args) != 1 {
		return nil, fmt.Errorf("exactly one dag_id is required (or use --all)")
	}
	return []string{args[0]}, nil
}

// setPausedForDAGs PATCHes is_paused for each dag_id via the typed client,
// printing a per-DAG result line and returning a non-zero error if any PATCH
// failed (so automation catches a partial apply).
func setPausedForDAGs(ctx context.Context, w io.Writer, c *apiclient.ClientWithResponses, ids []string, paused bool) error {
	if len(ids) == 0 {
		return writeLine(w, "No DAGs to update.")
	}
	verb := "unpaused"
	if paused {
		verb = "paused"
	}
	lines := make([]string, 0, len(ids)+1)
	var failed []string
	for _, id := range ids {
		if err := patchDagPaused(ctx, c, id, paused); err != nil {
			failed = append(failed, id)
			lines = append(lines, fmt.Sprintf("  %-30s FAILED: %v", id, err))
			continue
		}
		lines = append(lines, fmt.Sprintf("  %-30s %s", id, verb))
	}
	lines = append(lines, fmt.Sprintf("%s %d/%d DAG(s)", verb, len(ids)-len(failed), len(ids)))
	if werr := writeLine(w, strings.Join(lines, "\n")); werr != nil {
		return werr
	}
	if len(failed) > 0 {
		return fmt.Errorf("failed to %s %d DAG(s): %s", verb, len(failed), strings.Join(failed, ", "))
	}
	return nil
}

// patchDagPaused sends PATCH /api/v2/dags/{id} with the target is_paused value.
func patchDagPaused(ctx context.Context, c *apiclient.ClientWithResponses, id string, paused bool) error {
	resp, err := c.UpdateDagWithResponse(ctx, id, apiclient.DAGUpdate{IsPaused: &paused})
	if err != nil {
		return err
	}
	if resp.StatusCode() != http.StatusOK {
		return apiStatusError(resp.StatusCode(), resp.Body)
	}
	return nil
}
