package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

// adminRun is a flattened DAG run, carrying its owning DAG id so runs collected
// across many DAGs stay attributable.
type adminRun struct {
	dagID string
	runID string
	state string
	start *time.Time
}

// newAdminRunsCommand groups the run-inspection operator commands.
func newAdminRunsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "runs",
		Short: "Inspect DAG runs across the control plane.",
	}
	cmd.AddCommand(newAdminRunsListCommand())
	return cmd
}

// newAdminRunsListCommand builds `leoflow admin runs list`: list runs, optionally
// narrowed by state (server-side), by DAG, and by age — the "what is stuck?"
// query. Because runs are exposed per DAG, an unfiltered listing walks every
// registered DAG.
func newAdminRunsListCommand() *cobra.Command {
	var f adminFlags
	var state, dagID string
	var olderThan time.Duration
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List DAG runs, filtered by --state, --older-than, and/or --dag.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := validateRunState(state); err != nil {
				return err
			}
			c, _, err := adminClient(cmd, f)
			if err != nil {
				return err
			}
			runs, err := collectRuns(cmdContext(cmd), c, dagID, state)
			if err != nil {
				return err
			}
			runs = filterRunsByAge(runs, olderThan, time.Now())
			return renderRuns(cmd.OutOrStdout(), runs)
		},
	}
	addAdminFlags(cmd, &f)
	cmd.Flags().StringVar(&state, "state", "", "filter by run state: queued, running, success, or failed")
	cmd.Flags().StringVar(&dagID, "dag", "", "limit to a single DAG id (default: all DAGs)")
	cmd.Flags().DurationVar(&olderThan, "older-than", 0, "only runs whose start time is older than this (e.g. 2h)")
	return cmd
}

// validateRunState rejects an unknown --state up front, so a typo is a loud
// error rather than a silently-empty listing.
func validateRunState(state string) error {
	if state == "" {
		return nil
	}
	if apiclient.ListDagRunsParamsState(state).Valid() {
		return nil
	}
	return fmt.Errorf("unknown --state %q (want queued, running, success, or failed)", state)
}

// collectRuns gathers runs for a single DAG (when dagID is set) or across every
// registered DAG, passing the state filter through to the server.
func collectRuns(ctx context.Context, c *apiclient.ClientWithResponses, dagID, state string) ([]adminRun, error) {
	ids := []string{dagID}
	if dagID == "" {
		all, err := listAllDagIDs(ctx, c)
		if err != nil {
			return nil, err
		}
		ids = all
	}
	var runs []adminRun
	for _, id := range ids {
		got, err := listRunsForDag(ctx, c, id, state)
		if err != nil {
			return nil, err
		}
		runs = append(runs, got...)
	}
	return runs, nil
}

// listRunsForDag lists one DAG's runs, applying the optional server-side state
// filter.
func listRunsForDag(ctx context.Context, c *apiclient.ClientWithResponses, dagID, state string) ([]adminRun, error) {
	var params *apiclient.ListDagRunsParams
	if state != "" {
		states := []apiclient.ListDagRunsParamsState{apiclient.ListDagRunsParamsState(state)}
		params = &apiclient.ListDagRunsParams{State: &states}
	}
	resp, err := c.ListDagRunsWithResponse(ctx, dagID, params)
	if err != nil {
		return nil, fmt.Errorf("listing runs for %q: %w", dagID, err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return nil, fmt.Errorf("listing runs for %q returned %d: %s", dagID, resp.StatusCode(), string(resp.Body))
	}
	if resp.JSON200.DagRuns == nil {
		return []adminRun{}, nil
	}
	out := make([]adminRun, 0, len(*resp.JSON200.DagRuns))
	for _, r := range *resp.JSON200.DagRuns {
		out = append(out, adminRun{
			dagID: fallbackDagID(deref(r.DagId), dagID),
			runID: deref(r.DagRunId),
			state: runStateString(r.State),
			start: r.StartDate,
		})
	}
	return out, nil
}

// fallbackDagID uses the run's own dag_id when present, else the DAG the run was
// listed under (some responses omit the redundant field).
func fallbackDagID(fromRun, listedUnder string) string {
	if fromRun != "" {
		return fromRun
	}
	return listedUnder
}

// runStateString renders a run state pointer, defaulting to "unknown".
func runStateString(s *apiclient.DAGRunState) string {
	if s == nil {
		return "unknown"
	}
	return string(*s)
}

// filterRunsByAge keeps only runs whose start time is at least olderThan in the
// past. A non-positive threshold disables the filter (every run is kept,
// including runs with no start time yet).
func filterRunsByAge(runs []adminRun, olderThan time.Duration, now time.Time) []adminRun {
	if olderThan <= 0 {
		return runs
	}
	out := make([]adminRun, 0, len(runs))
	for _, r := range runs {
		if r.start != nil && now.Sub(*r.start) >= olderThan {
			out = append(out, r)
		}
	}
	return out
}

// renderRuns prints the runs as an aligned table, or a friendly note when empty.
func renderRuns(w io.Writer, runs []adminRun) error {
	if len(runs) == 0 {
		return writeLine(w, "No runs found.")
	}
	lines := make([]string, 0, len(runs)+1)
	lines = append(lines, fmt.Sprintf("%-24s %-30s %-10s %s", "DAG", "RUN", "STATE", "AGE"))
	for _, r := range runs {
		lines = append(lines, fmt.Sprintf("%-24s %-30s %-10s %s", r.dagID, r.runID, r.state, ageString(r.start)))
	}
	return writeLine(w, strings.Join(lines, "\n"))
}

// ageString renders how long ago a run started, or "-" when it has no start.
func ageString(start *time.Time) string {
	if start == nil {
		return "-"
	}
	return time.Since(*start).Round(time.Second).String()
}
